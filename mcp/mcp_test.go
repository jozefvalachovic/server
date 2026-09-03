package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

type echoInput struct {
	Text string `json:"text" jsonschema:"text to echo"`
}

type echoOutput struct {
	Echo string `json:"echo"`
}

type rpcResponse struct {
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Code    int64  `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func testConfig() Config {
	return Config{Name: "test-server", Version: "1.0.0"}
}

func newTestServer(t *testing.T, cfg Config) *Server {
	t.Helper()
	server, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return server
}

func modernParams(fields map[string]any) map[string]any {
	params := make(map[string]any, len(fields)+1)
	maps.Copy(params, fields)
	params["_meta"] = map[string]any{
		sdk.MetaKeyProtocolVersion:    ProtocolVersion,
		sdk.MetaKeyClientCapabilities: map[string]any{},
	}
	return params
}

func postRPC(t *testing.T, handler http.Handler, method, protocol string, params any) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  method,
		"params":  params,
	})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Mcp-Method", method)
	if protocol != "" {
		req.Header.Set("Mcp-Protocol-Version", protocol)
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)
	return recorder
}

func decodeRPC(t *testing.T, recorder *httptest.ResponseRecorder) rpcResponse {
	t.Helper()
	var response rpcResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode RPC response: %v; body=%q", err, recorder.Body.String())
	}
	return response
}

func connectClient(t *testing.T, handler http.Handler, httpClient *http.Client) *sdk.ClientSession {
	t.Helper()
	httpServer := httptest.NewServer(handler)
	t.Cleanup(httpServer.Close)

	client := sdk.NewClient(
		&sdk.Implementation{Name: "test-client", Version: "1.0.0"},
		&sdk.ClientOptions{Capabilities: &sdk.ClientCapabilities{}},
	)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	session, err := client.Connect(ctx, &sdk.StreamableClientTransport{
		Endpoint:   httpServer.URL,
		HTTPClient: httpClient,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session
}

func TestDiscoverIsLatestOnlyAndStateless(t *testing.T) {
	server := newTestServer(t, testConfig())
	recorder := postRPC(t, server, "server/discover", ProtocolVersion, modernParams(nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", recorder.Code, recorder.Body.String())
	}
	if sessionID := recorder.Header().Get("Mcp-Session-Id"); sessionID != "" {
		t.Fatalf("stateless server returned session ID %q", sessionID)
	}

	response := decodeRPC(t, recorder)
	if response.Error != nil {
		t.Fatalf("unexpected error: %+v", response.Error)
	}
	var result struct {
		SupportedVersions []string `json:"supportedVersions"`
	}
	if err := json.Unmarshal(response.Result, &result); err != nil {
		t.Fatal(err)
	}
	if len(result.SupportedVersions) != 1 || result.SupportedVersions[0] != ProtocolVersion {
		t.Fatalf("unexpected supported versions: %v", result.SupportedVersions)
	}
}

func TestOfficialClientTypedTools(t *testing.T) {
	server := newTestServer(t, testConfig())
	var calls atomic.Int32
	AddTool(server, &Tool{
		Name:        "echo",
		Description: "Echo text.",
	}, func(_ context.Context, _ *CallToolRequest, input echoInput) (*CallToolResult, echoOutput, error) {
		calls.Add(1)
		return nil, echoOutput{Echo: input.Text}, nil
	})

	session := connectClient(t, server, nil)
	if got := session.InitializeResult().ProtocolVersion; got != ProtocolVersion {
		t.Fatalf("want protocol %s, got %s", ProtocolVersion, got)
	}
	if id := session.ID(); id != "" {
		t.Fatalf("stateless client session has ID %q", id)
	}

	tools, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(tools.Tools) != 1 || tools.Tools[0].Name != "echo" {
		t.Fatalf("unexpected tools: %+v", tools.Tools)
	}
	if tools.Tools[0].InputSchema == nil || tools.Tools[0].OutputSchema == nil {
		t.Fatal("typed tool must advertise inferred input and output schemas")
	}

	result, err := session.CallTool(context.Background(), &sdk.CallToolParams{
		Name:      "echo",
		Arguments: map[string]any{"text": "hello"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatalf("unexpected tool error: %+v", result.Content)
	}
	encoded, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var output echoOutput
	if err := json.Unmarshal(encoded, &output); err != nil {
		t.Fatal(err)
	}
	if output.Echo != "hello" {
		t.Fatalf("want hello, got %q", output.Echo)
	}

	invalid, err := session.CallTool(context.Background(), &sdk.CallToolParams{
		Name:      "echo",
		Arguments: map[string]any{"text": 42},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !invalid.IsError {
		t.Fatal("invalid typed input must return a tool error")
	}
	if calls.Load() != 1 {
		t.Fatalf("handler called %d times; invalid input reached handler", calls.Load())
	}
}

func TestLegacyProtocolRejected(t *testing.T) {
	server := newTestServer(t, testConfig())
	recorder := postRPC(t, server, "initialize", "2025-11-25", map[string]any{
		"protocolVersion": "2025-11-25",
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "legacy", "version": "1"},
	})
	response := decodeRPC(t, recorder)
	if response.Error == nil || response.Error.Code != sdk.CodeUnsupportedProtocolVersion {
		t.Fatalf("want unsupported protocol error, got %+v", response.Error)
	}
}

func TestRemovedMethodRejected(t *testing.T) {
	server := newTestServer(t, testConfig())
	recorder := postRPC(t, server, "ping", ProtocolVersion, modernParams(nil))
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("want 404, got %d: %s", recorder.Code, recorder.Body.String())
	}
	response := decodeRPC(t, recorder)
	if response.Error == nil || response.Error.Code != jsonrpc.CodeMethodNotFound {
		t.Fatalf("want method-not-found error, got %+v", response.Error)
	}
}

func TestStatelessHTTPRejectsGETAndDELETE(t *testing.T) {
	server := newTestServer(t, testConfig())
	for _, method := range []string{http.MethodGet, http.MethodDelete} {
		t.Run(method, func(t *testing.T) {
			req := httptest.NewRequest(method, "/mcp", nil)
			recorder := httptest.NewRecorder()
			server.ServeHTTP(recorder, req)
			if recorder.Code != http.StatusMethodNotAllowed {
				t.Fatalf("want 405, got %d", recorder.Code)
			}
			if allow := recorder.Header().Get("Allow"); allow != "POST" {
				t.Fatalf("want Allow: POST, got %q", allow)
			}
		})
	}
}

func TestRequestCancellationReachesTool(t *testing.T) {
	server := newTestServer(t, testConfig())
	started := make(chan struct{})
	cancelled := make(chan struct{})
	AddTool(server, &Tool{Name: "wait"}, func(ctx context.Context, _ *CallToolRequest, _ any) (*CallToolResult, any, error) {
		close(started)
		<-ctx.Done()
		close(cancelled)
		return nil, nil, ctx.Err()
	})
	session := connectClient(t, server, nil)

	ctx, cancel := context.WithCancel(context.Background())
	callDone := make(chan error, 1)
	go func() {
		_, err := session.CallTool(ctx, &sdk.CallToolParams{Name: "wait"})
		callDone <- err
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("tool did not start")
	}
	cancel()

	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("HTTP cancellation did not reach tool context")
	}
	select {
	case err := <-callDone:
		if err == nil {
			t.Fatal("cancelled call returned no error")
		}
	case <-time.After(time.Second):
		t.Fatal("cancelled client call did not return")
	}
}

func TestConfiguredTimeoutReachesTool(t *testing.T) {
	cfg := testConfig()
	cfg.RequestTimeout = 20 * time.Millisecond
	server := newTestServer(t, cfg)
	deadline := make(chan error, 1)
	AddTool(server, &Tool{Name: "wait_for_timeout"}, func(ctx context.Context, _ *CallToolRequest, _ any) (*CallToolResult, any, error) {
		<-ctx.Done()
		deadline <- ctx.Err()
		return nil, nil, ctx.Err()
	})
	session := connectClient(t, server, nil)

	_, _ = session.CallTool(context.Background(), &sdk.CallToolParams{Name: "wait_for_timeout"})
	select {
	case err := <-deadline:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("want deadline exceeded, got %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("configured request timeout did not reach tool context")
	}
}

type contextKey string

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return fn(req)
}

func TestAuthenticationMiddlewareAndHooks(t *testing.T) {
	const (
		principalKey  contextKey = "principal"
		traceKey      contextKey = "trace"
		middlewareKey contextKey = "middleware"
	)
	finished := make(chan RequestEvent, 8)
	cfg := testConfig()
	cfg.Authenticate = func(ctx context.Context, req *http.Request) (context.Context, error) {
		if req.Header.Get("Authorization") != "Bearer test" {
			return nil, errors.New("invalid token")
		}
		return context.WithValue(ctx, principalKey, "alice"), nil
	}
	cfg.Hooks = RequestHooks{
		Start: func(ctx context.Context, _ string, _ Request) context.Context {
			return context.WithValue(ctx, traceKey, "trace-1")
		},
		Finish: func(_ context.Context, event RequestEvent) {
			finished <- event
		},
	}
	cfg.Middleware = []Middleware{func(next MethodHandler) MethodHandler {
		return func(ctx context.Context, method string, req Request) (Result, error) {
			return next(context.WithValue(ctx, middlewareKey, true), method, req)
		}
	}}
	server := newTestServer(t, cfg)
	AddTool(server, &Tool{Name: "identity"}, func(ctx context.Context, _ *CallToolRequest, _ any) (*CallToolResult, echoOutput, error) {
		if ctx.Value(principalKey) != "alice" || ctx.Value(traceKey) != "trace-1" || ctx.Value(middlewareKey) != true {
			return nil, echoOutput{}, errors.New("request context was not propagated")
		}
		return nil, echoOutput{Echo: "alice"}, nil
	})

	unauthorized := postRPC(t, server, "server/discover", ProtocolVersion, modernParams(nil))
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d", unauthorized.Code)
	}

	transport := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		req.Header.Set("Authorization", "Bearer test")
		return http.DefaultTransport.RoundTrip(req)
	})
	session := connectClient(t, server, &http.Client{Transport: transport})
	result, err := session.CallTool(context.Background(), &sdk.CallToolParams{Name: "identity"})
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatalf("context propagation failed: %+v", result.Content)
	}

	foundCall := false
	for len(finished) > 0 {
		if event := <-finished; event.Method == "tools/call" {
			foundCall = true
			if event.Duration <= 0 || event.StartedAt.IsZero() {
				t.Fatalf("incomplete request event: %+v", event)
			}
		}
	}
	if !foundCall {
		t.Fatal("finish hook did not observe tools/call")
	}
}

func TestOriginPolicy(t *testing.T) {
	cfg := testConfig()
	cfg.AllowedOrigins = []string{"https://app.example.com"}
	server := newTestServer(t, cfg)

	rejected := httptest.NewRequest(http.MethodOptions, "/mcp", nil)
	rejected.Header.Set("Origin", "https://evil.example")
	rejectedRecorder := httptest.NewRecorder()
	server.ServeHTTP(rejectedRecorder, rejected)
	if rejectedRecorder.Code != http.StatusForbidden {
		t.Fatalf("want 403, got %d", rejectedRecorder.Code)
	}

	allowed := httptest.NewRequest(http.MethodOptions, "/mcp", nil)
	allowed.Header.Set("Origin", "https://app.example.com")
	allowedRecorder := httptest.NewRecorder()
	server.ServeHTTP(allowedRecorder, allowed)
	if allowedRecorder.Code != http.StatusNoContent {
		t.Fatalf("want 204, got %d", allowedRecorder.Code)
	}
	if got := allowedRecorder.Header().Get("Access-Control-Allow-Origin"); got != "https://app.example.com" {
		t.Fatalf("unexpected allowed origin %q", got)
	}
}

func TestRequestBodyLimit(t *testing.T) {
	cfg := testConfig()
	cfg.MaxRequestBodyBytes = 64
	server := newTestServer(t, cfg)

	recorder := postRPC(t, server, "server/discover", ProtocolVersion, modernParams(map[string]any{
		"padding": string(bytes.Repeat([]byte("x"), 128)),
	}))
	if recorder.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("want 413, got %d: %s", recorder.Code, recorder.Body.String())
	}
}

func TestConcurrencyLimit(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	blocking := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		close(started)
		<-release
		w.WriteHeader(http.StatusNoContent)
	})
	handler := withConcurrencyLimit(1, blocking)

	firstDone := make(chan struct{})
	go func() {
		handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/", nil))
		close(firstDone)
	}()
	<-started

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/", nil))
	if recorder.Code != http.StatusTooManyRequests {
		t.Fatalf("want 429, got %d", recorder.Code)
	}
	close(release)
	<-firstDone
}

func TestRateLimit(t *testing.T) {
	limiter, err := newTokenBucket(&RateLimitConfig{RequestsPerSecond: 1, Burst: 1})
	if err != nil {
		t.Fatal(err)
	}
	handler := withRateLimit(limiter, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	first := httptest.NewRecorder()
	handler.ServeHTTP(first, httptest.NewRequest(http.MethodPost, "/", nil))
	if first.Code != http.StatusNoContent {
		t.Fatalf("first request: want 204, got %d", first.Code)
	}
	second := httptest.NewRecorder()
	handler.ServeHTTP(second, httptest.NewRequest(http.MethodPost, "/", nil))
	if second.Code != http.StatusTooManyRequests {
		t.Fatalf("second request: want 429, got %d", second.Code)
	}
}

func TestRequestTimeout(t *testing.T) {
	cancelled := make(chan error, 1)
	handler := withRequestTimeout(10*time.Millisecond, http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		<-req.Context().Done()
		cancelled <- req.Context().Err()
		w.WriteHeader(http.StatusNoContent)
	}))
	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/", nil))
	if err := <-cancelled; !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("want deadline exceeded, got %v", err)
	}
}

func TestAuditIncludesPolicyRejections(t *testing.T) {
	events := make(chan HTTPAuditEvent, 1)
	cfg := testConfig()
	cfg.AllowedOrigins = []string{"https://app.example.com"}
	cfg.Audit = func(_ context.Context, event HTTPAuditEvent) { events <- event }
	server := newTestServer(t, cfg)

	req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	req.Header.Set("Origin", "https://evil.example")
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, req)

	event := <-events
	if event.Status != http.StatusForbidden || event.Method != http.MethodPost || event.Path != "/mcp" {
		t.Fatalf("unexpected audit event: %+v", event)
	}
	if event.Duration <= 0 || event.StartedAt.IsZero() {
		t.Fatalf("incomplete audit event: %+v", event)
	}
}

func TestConfigValidation(t *testing.T) {
	tests := []struct {
		name string
		cfg  Config
	}{
		{name: "missing name", cfg: Config{Version: "1"}},
		{name: "missing version", cfg: Config{Name: "test"}},
		{name: "bad origin", cfg: Config{Name: "test", Version: "1", AllowedOrigins: []string{"https://example.com/path"}}},
		{name: "negative body", cfg: Config{Name: "test", Version: "1", MaxRequestBodyBytes: -1}},
		{name: "negative timeout", cfg: Config{Name: "test", Version: "1", RequestTimeout: -1}},
		{name: "negative concurrency", cfg: Config{Name: "test", Version: "1", MaxConcurrent: -1}},
		{name: "bad rate", cfg: Config{Name: "test", Version: "1", RateLimit: &RateLimitConfig{Burst: 1}}},
		{name: "bad burst", cfg: Config{Name: "test", Version: "1", RateLimit: &RateLimitConfig{RequestsPerSecond: 1}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := New(test.cfg); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

func TestStatusRecorderPreservesResponseControllerUnwrap(t *testing.T) {
	base := httptest.NewRecorder()
	recorder := &statusRecorder{ResponseWriter: base}
	if recorder.Unwrap() != base {
		t.Fatal("status recorder must expose its underlying writer")
	}
	if _, err := io.WriteString(recorder, "ok"); err != nil {
		t.Fatal(err)
	}
	if recorder.status != http.StatusOK {
		t.Fatalf("want status 200, got %d", recorder.status)
	}
}

func TestRunRejectsNilTransport(t *testing.T) {
	server := newTestServer(t, testConfig())
	if err := server.Run(context.Background(), nil); err == nil {
		t.Fatal("expected nil transport error")
	}
}

func TestTokenBucketRefills(t *testing.T) {
	limiter, err := newTokenBucket(&RateLimitConfig{RequestsPerSecond: 10, Burst: 1})
	if err != nil {
		t.Fatal(err)
	}
	now := limiter.last
	if !limiter.allow(now) || limiter.allow(now) {
		t.Fatal("unexpected initial token behavior")
	}
	if !limiter.allow(now.Add(100 * time.Millisecond)) {
		t.Fatal("token bucket did not refill")
	}
}

func TestConcurrentAuditCalls(t *testing.T) {
	var calls atomic.Int32
	handler := withAudit(func(context.Context, HTTPAuditEvent) {
		calls.Add(1)
	}, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	var wait sync.WaitGroup
	for range 8 {
		wait.Go(func() {
			handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/", nil))
		})
	}
	wait.Wait()
	if calls.Load() != 8 {
		t.Fatalf("want 8 audit calls, got %d", calls.Load())
	}
}

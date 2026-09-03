// Package mcp provides a production wrapper around the official Model Context
// Protocol Go SDK. It supports only protocol version 2026-07-28 and exposes
// stateless Streamable HTTP and stdio transports.
package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	// ProtocolVersion is the only MCP protocol version accepted by this package.
	ProtocolVersion = "2026-07-28"

	DefaultMaxRequestBodyBytes = 1 << 20
	DefaultRequestTimeout      = 30 * time.Second
	DefaultMaxConcurrent       = 64
)

type (
	Tool            = sdk.Tool
	CallToolRequest = sdk.CallToolRequest
	CallToolResult  = sdk.CallToolResult
	Middleware      = sdk.Middleware
	MethodHandler   = sdk.MethodHandler
	Request         = sdk.Request
	Result          = sdk.Result
	Transport       = sdk.Transport
)

// ToolHandler is a typed tool handler. Input and output schemas are inferred
// and validated by the official SDK when registered with AddTool.
type ToolHandler[In, Out any] func(context.Context, *CallToolRequest, In) (*CallToolResult, Out, error)

// Authenticator authenticates an HTTP request. It may return a derived context
// containing the authenticated principal or other request-scoped values.
type Authenticator func(context.Context, *http.Request) (context.Context, error)

// RateLimitConfig configures a process-local token bucket shared by all HTTP
// requests handled by a Server.
type RateLimitConfig struct {
	RequestsPerSecond float64
	Burst             int
}

// RequestEvent describes one MCP method invocation. Hooks may use it for
// tracing, metrics, or application audit records.
type RequestEvent struct {
	Method    string
	StartedAt time.Time
	Duration  time.Duration
	Err       error
}

// RequestHooks observe MCP method handling. Start may return a derived context
// that is propagated to the method handler. Finish runs synchronously.
type RequestHooks struct {
	Start  func(context.Context, string, Request) context.Context
	Finish func(context.Context, RequestEvent)
}

// HTTPAuditEvent describes one HTTP request, including policy rejections that
// do not reach MCP method dispatch.
type HTTPAuditEvent struct {
	StartedAt time.Time
	Duration  time.Duration
	Method    string
	Path      string
	Remote    string
	Status    int
}

type Config struct {
	Name         string
	Version      string
	Instructions string
	Logger       *slog.Logger

	AllowedOrigins      []string
	Authenticate        Authenticator
	MaxRequestBodyBytes int64
	RequestTimeout      time.Duration
	MaxConcurrent       int
	RateLimit           *RateLimitConfig

	Middleware []Middleware
	Hooks      RequestHooks
	Audit      func(context.Context, HTTPAuditEvent)
}

// Server is a latest-only MCP server backed by the official Go SDK.
type Server struct {
	sdk     *sdk.Server
	handler http.Handler
}

// New constructs a latest-only MCP server. Tools may be registered after New
// and before the server begins handling requests.
func New(cfg Config) (*Server, error) {
	if cfg.Name == "" {
		return nil, errors.New("mcp: Name is required")
	}
	if cfg.Version == "" {
		return nil, errors.New("mcp: Version is required")
	}

	allowedOrigins, allowAnyOrigin, err := parseAllowedOrigins(cfg.AllowedOrigins)
	if err != nil {
		return nil, err
	}

	maxBodyBytes := cfg.MaxRequestBodyBytes
	if maxBodyBytes == 0 {
		maxBodyBytes = DefaultMaxRequestBodyBytes
	}
	if maxBodyBytes < 0 {
		return nil, fmt.Errorf("mcp: MaxRequestBodyBytes must be >= 0, got %d", cfg.MaxRequestBodyBytes)
	}

	requestTimeout := cfg.RequestTimeout
	if requestTimeout == 0 {
		requestTimeout = DefaultRequestTimeout
	}
	if requestTimeout < 0 {
		return nil, fmt.Errorf("mcp: RequestTimeout must be >= 0, got %s", cfg.RequestTimeout)
	}

	maxConcurrent := cfg.MaxConcurrent
	if maxConcurrent == 0 {
		maxConcurrent = DefaultMaxConcurrent
	}
	if maxConcurrent < 0 {
		return nil, fmt.Errorf("mcp: MaxConcurrent must be >= 0, got %d", cfg.MaxConcurrent)
	}

	limiter, err := newTokenBucket(cfg.RateLimit)
	if err != nil {
		return nil, err
	}

	sdkServer := sdk.NewServer(
		&sdk.Implementation{Name: cfg.Name, Version: cfg.Version},
		&sdk.ServerOptions{
			Instructions: cfg.Instructions,
			Logger:       cfg.Logger,
			Capabilities: &sdk.ServerCapabilities{},
		},
	)

	middlewares := make([]sdk.Middleware, 0, len(cfg.Middleware)+2)
	if cfg.Hooks.Start != nil || cfg.Hooks.Finish != nil {
		middlewares = append(middlewares, requestHookMiddleware(cfg.Hooks))
	}
	middlewares = append(middlewares, latestOnlyMiddleware())
	middlewares = append(middlewares, cfg.Middleware...)
	sdkServer.AddReceivingMiddleware(middlewares...)

	base := sdk.NewStreamableHTTPHandler(
		func(*http.Request) *sdk.Server { return sdkServer },
		&sdk.StreamableHTTPOptions{
			Stateless:                    true,
			JSONResponse:                 true,
			Logger:                       cfg.Logger,
			MaxRequestBodyBytes:          maxBodyBytes,
			PropagateRequestCancellation: true,
		},
	)

	handler := http.Handler(base)
	handler = withRequestTimeout(requestTimeout, handler)
	handler = withConcurrencyLimit(maxConcurrent, handler)
	handler = withRateLimit(limiter, handler)
	handler = withAuthentication(cfg.Authenticate, handler)
	handler = withOriginPolicy(allowedOrigins, allowAnyOrigin, handler)
	if cfg.Audit != nil {
		handler = withAudit(cfg.Audit, handler)
	}

	return &Server{sdk: sdkServer, handler: handler}, nil
}

// AddTool registers a typed tool. The official SDK infers and validates the
// input and output schemas and populates structured results.
func AddTool[In, Out any](server *Server, tool *Tool, handler ToolHandler[In, Out]) {
	sdk.AddTool(server.sdk, tool, sdk.ToolHandlerFor[In, Out](handler))
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.handler.ServeHTTP(w, r)
}

// RunStdio serves MCP over stdin and stdout until the context is cancelled or
// the peer closes the connection.
func (s *Server) RunStdio(ctx context.Context) error {
	return s.sdk.Run(ctx, &sdk.StdioTransport{})
}

// Run serves MCP over a persistent SDK transport.
func (s *Server) Run(ctx context.Context, transport Transport) error {
	if transport == nil {
		return errors.New("mcp: transport is required")
	}
	return s.sdk.Run(ctx, transport)
}

// SDK returns the underlying official SDK server for advanced features such
// as prompts, resources, and custom methods.
func (s *Server) SDK() *sdk.Server {
	return s.sdk
}

// AddReceivingMiddleware adds official SDK middleware to the server.
func (s *Server) AddReceivingMiddleware(middleware ...Middleware) {
	s.sdk.AddReceivingMiddleware(middleware...)
}

func latestOnlyMiddleware() sdk.Middleware {
	return func(next sdk.MethodHandler) sdk.MethodHandler {
		return func(ctx context.Context, method string, req sdk.Request) (sdk.Result, error) {
			requested := ""
			if params := req.GetParams(); params != nil {
				requested, _ = params.GetMeta()[sdk.MetaKeyProtocolVersion].(string)
			}
			if requested != ProtocolVersion {
				data, _ := json.Marshal(sdk.UnsupportedProtocolVersionData{
					Supported: []string{ProtocolVersion},
					Requested: requested,
				})
				return nil, &jsonrpc.Error{
					Code:    sdk.CodeUnsupportedProtocolVersion,
					Message: "unsupported protocol version",
					Data:    data,
				}
			}

			result, err := next(ctx, method, req)
			if discover, ok := result.(*sdk.DiscoverResult); ok {
				discover.SupportedVersions = []string{ProtocolVersion}
			}
			return result, err
		}
	}
}

func requestHookMiddleware(hooks RequestHooks) sdk.Middleware {
	return func(next sdk.MethodHandler) sdk.MethodHandler {
		return func(ctx context.Context, method string, req sdk.Request) (result sdk.Result, err error) {
			startedAt := time.Now()
			if hooks.Start != nil {
				if derived := hooks.Start(ctx, method, req); derived != nil {
					ctx = derived
				}
			}
			defer func() {
				if hooks.Finish != nil {
					hooks.Finish(ctx, RequestEvent{
						Method:    method,
						StartedAt: startedAt,
						Duration:  time.Since(startedAt),
						Err:       err,
					})
				}
			}()
			return next(ctx, method, req)
		}
	}
}

func parseAllowedOrigins(origins []string) (map[string]struct{}, bool, error) {
	allowed := make(map[string]struct{}, len(origins))
	for _, origin := range origins {
		if origin == "*" {
			return allowed, true, nil
		}
		normalized, err := normalizeOrigin(origin)
		if err != nil {
			return nil, false, fmt.Errorf("mcp: invalid allowed origin %q: %w", origin, err)
		}
		allowed[normalized] = struct{}{}
	}
	return allowed, false, nil
}

func normalizeOrigin(origin string) (string, error) {
	parsed, err := url.Parse(origin)
	if err != nil {
		return "", err
	}
	if (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" ||
		(parsed.Path != "" && parsed.Path != "/") || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.User != nil {
		return "", errors.New("origin must contain only an http(s) scheme and host")
	}
	return strings.ToLower(parsed.Scheme + "://" + parsed.Host), nil
}

func withOriginPolicy(allowed map[string]struct{}, allowAny bool, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin == "" {
			next.ServeHTTP(w, r)
			return
		}

		normalized, err := normalizeOrigin(origin)
		_, permitted := allowed[normalized]
		if err != nil || (!allowAny && !permitted) {
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}

		w.Header().Add("Vary", "Origin")
		w.Header().Set("Access-Control-Allow-Origin", origin)
		if r.Method == http.MethodOptions {
			w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, Mcp-Method, Mcp-Name, Mcp-Protocol-Version")
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func withAuthentication(authenticate Authenticator, next http.Handler) http.Handler {
	if authenticate == nil {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, err := authenticate(r.Context(), r)
		if err != nil {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		if ctx != nil {
			r = r.WithContext(ctx)
		}
		next.ServeHTTP(w, r)
	})
}

func withRequestTimeout(timeout time.Duration, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), timeout)
		defer cancel()
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func withConcurrencyLimit(limit int, next http.Handler) http.Handler {
	semaphore := make(chan struct{}, limit)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case semaphore <- struct{}{}:
			defer func() { <-semaphore }()
			next.ServeHTTP(w, r)
		default:
			w.Header().Set("Retry-After", "1")
			http.Error(w, "Too Many Requests", http.StatusTooManyRequests)
		}
	})
}

type tokenBucket struct {
	mu                sync.Mutex
	requestsPerSecond float64
	burst             float64
	tokens            float64
	last              time.Time
}

func newTokenBucket(cfg *RateLimitConfig) (*tokenBucket, error) {
	if cfg == nil {
		return nil, nil
	}
	if cfg.RequestsPerSecond <= 0 {
		return nil, fmt.Errorf("mcp: RateLimit.RequestsPerSecond must be > 0, got %g", cfg.RequestsPerSecond)
	}
	if cfg.Burst <= 0 {
		return nil, fmt.Errorf("mcp: RateLimit.Burst must be > 0, got %d", cfg.Burst)
	}
	now := time.Now()
	return &tokenBucket{
		requestsPerSecond: cfg.RequestsPerSecond,
		burst:             float64(cfg.Burst),
		tokens:            float64(cfg.Burst),
		last:              now,
	}, nil
}

func (limiter *tokenBucket) allow(now time.Time) bool {
	limiter.mu.Lock()
	defer limiter.mu.Unlock()

	elapsed := now.Sub(limiter.last).Seconds()
	limiter.tokens = min(limiter.burst, limiter.tokens+elapsed*limiter.requestsPerSecond)
	limiter.last = now
	if limiter.tokens < 1 {
		return false
	}
	limiter.tokens--
	return true
}

func withRateLimit(limiter *tokenBucket, next http.Handler) http.Handler {
	if limiter == nil {
		return next
	}
	retryAfter := strconv.Itoa(max(1, int(math.Ceil(1/limiter.requestsPerSecond))))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !limiter.allow(time.Now()) {
			w.Header().Set("Retry-After", retryAfter)
			http.Error(w, "Too Many Requests", http.StatusTooManyRequests)
			return
		}
		next.ServeHTTP(w, r)
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (w *statusRecorder) WriteHeader(status int) {
	if w.status != 0 {
		return
	}
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

func (w *statusRecorder) Write(data []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.ResponseWriter.Write(data)
}

func (w *statusRecorder) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

func withAudit(audit func(context.Context, HTTPAuditEvent), next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		startedAt := time.Now()
		recorder := &statusRecorder{ResponseWriter: w}
		next.ServeHTTP(recorder, r)
		status := recorder.status
		if status == 0 {
			status = http.StatusOK
		}
		audit(r.Context(), HTTPAuditEvent{
			StartedAt: startedAt,
			Duration:  time.Since(startedAt),
			Method:    r.Method,
			Path:      r.URL.Path,
			Remote:    r.RemoteAddr,
			Status:    status,
		})
	})
}

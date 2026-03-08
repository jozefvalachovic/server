package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func newTestHandler() http.Handler {
	return Handler(Config{
		Name:    "test-server",
		Version: "0.1.0",
		Tools: []Tool{
			{
				Name:        "echo",
				Description: "Returns input as-is",
				Input:       (*struct{ Msg string })(nil),
				Handler: func(ctx context.Context, raw json.RawMessage) (any, error) {
					var in struct{ Msg string }
					_ = json.Unmarshal(raw, &in)
					return in.Msg, nil
				},
			},
		},
	})
}

func rpcCall(t *testing.T, h http.Handler, method string, id any, params any) *httptest.ResponseRecorder {
	t.Helper()
	body := map[string]any{"jsonrpc": "2.0", "method": method}
	if id != nil {
		body["id"] = id
	}
	if params != nil {
		body["params"] = params
	}
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestInitialize(t *testing.T) {
	h := newTestHandler()
	rec := rpcCall(t, h, "initialize", 1, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	var resp rpcResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	if resp.Error != nil {
		t.Fatalf("unexpected error: %s", resp.Error.Message)
	}
}

func TestToolsList(t *testing.T) {
	h := newTestHandler()
	rec := rpcCall(t, h, "tools/list", 1, nil)
	var resp rpcResponse
	_ = json.NewDecoder(rec.Body).Decode(&resp)
	if resp.Error != nil {
		t.Fatalf("unexpected error: %s", resp.Error.Message)
	}
	// Result should contain tools array
	b, _ := json.Marshal(resp.Result)
	var result struct {
		Tools []struct{ Name string } `json:"tools"`
	}
	_ = json.Unmarshal(b, &result)
	if len(result.Tools) != 1 || result.Tools[0].Name != "echo" {
		t.Fatalf("unexpected tools list: %s", string(b))
	}
}

func TestToolsCall(t *testing.T) {
	h := newTestHandler()
	rec := rpcCall(t, h, "tools/call", 1, map[string]any{
		"name":      "echo",
		"arguments": map[string]any{"Msg": "hello"},
	})
	var resp rpcResponse
	_ = json.NewDecoder(rec.Body).Decode(&resp)
	if resp.Error != nil {
		t.Fatalf("unexpected error: %s", resp.Error.Message)
	}
}

func TestToolNotFound(t *testing.T) {
	h := newTestHandler()
	rec := rpcCall(t, h, "tools/call", 1, map[string]any{"name": "nope"})
	var resp rpcResponse
	_ = json.NewDecoder(rec.Body).Decode(&resp)
	if resp.Error == nil {
		t.Fatal("expected error for unknown tool")
	}
	if resp.Error.Code != codeMethodNotFound {
		t.Fatalf("want code %d, got %d", codeMethodNotFound, resp.Error.Code)
	}
}

func TestMethodNotFound(t *testing.T) {
	h := newTestHandler()
	rec := rpcCall(t, h, "nonexistent/method", 1, nil)
	var resp rpcResponse
	_ = json.NewDecoder(rec.Body).Decode(&resp)
	if resp.Error == nil || resp.Error.Code != codeMethodNotFound {
		t.Fatal("expected method not found error")
	}
}

func TestNotification_NoBody(t *testing.T) {
	h := newTestHandler()
	// Notification: no id field
	rec := rpcCall(t, h, "initialized", nil, nil)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("want 204 for notification, got %d", rec.Code)
	}
}

func TestCORSHeaders(t *testing.T) {
	h := Handler(Config{
		Name:           "cors-test",
		AllowedOrigins: []string{"https://example.com"},
	})
	req := httptest.NewRequest(http.MethodOptions, "/mcp", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("want 204, got %d", rec.Code)
	}
	origin := rec.Header().Get("Access-Control-Allow-Origin")
	if origin != "https://example.com" {
		t.Fatalf("want https://example.com, got %s", origin)
	}
}

func TestCORSHeaders_DefaultWildcard(t *testing.T) {
	h := Handler(Config{Name: "cors-default"})
	req := httptest.NewRequest(http.MethodOptions, "/mcp", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	origin := rec.Header().Get("Access-Control-Allow-Origin")
	if origin != "*" {
		t.Fatalf("want *, got %s", origin)
	}
}

func TestGetCapability(t *testing.T) {
	h := newTestHandler()
	req := httptest.NewRequest(http.MethodGet, "/mcp", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	var doc capabilityDoc
	if err := json.NewDecoder(rec.Body).Decode(&doc); err != nil {
		t.Fatal(err)
	}
	if doc.Name != "test-server" {
		t.Fatalf("want test-server, got %s", doc.Name)
	}
}

func TestInvalidMethod(t *testing.T) {
	h := newTestHandler()
	req := httptest.NewRequest(http.MethodPut, "/mcp", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("want 405, got %d", rec.Code)
	}
}

func TestInvalidJSON(t *testing.T) {
	h := newTestHandler()
	req := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewReader([]byte("not json")))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var resp rpcResponse
	_ = json.NewDecoder(rec.Body).Decode(&resp)
	if resp.Error == nil || resp.Error.Code != codeParseError {
		t.Fatal("expected parse error")
	}
}

func TestInvalidJSONRPCVersion(t *testing.T) {
	h := newTestHandler()
	b, _ := json.Marshal(map[string]any{
		"jsonrpc": "1.0",
		"id":      1,
		"method":  "initialize",
	})
	req := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewReader(b))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var resp rpcResponse
	_ = json.NewDecoder(rec.Body).Decode(&resp)
	if resp.Error == nil || resp.Error.Code != codeInvalidRequest {
		t.Fatal("expected invalid request error for wrong version")
	}
}

package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// ── shared fixtures ───────────────────────────────────────────────────────────

type benchProductInput struct {
	ID int `json:"id" description:"Numeric product ID."`
}

var benchHandler = Handler(Config{
	Name:    "bench-server",
	Version: "1.0.0",
	Tools: []Tool{
		{
			Name:        "echo",
			Description: "Echoes its input back.",
			Input:       (*benchProductInput)(nil),
			Handler: func(_ context.Context, raw json.RawMessage) (any, error) {
				var in benchProductInput
				_ = json.Unmarshal(raw, &in)
				return in, nil
			},
		},
		{
			Name:        "noop",
			Description: "No-op tool that always returns an empty object.",
			Handler: func(_ context.Context, _ json.RawMessage) (any, error) {
				return map[string]any{}, nil
			},
		},
	},
})

// postJSON fires a JSON-RPC POST at benchHandler and discards the response.
func postJSON(b *testing.B, body string) {
	b.Helper()
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	benchHandler.ServeHTTP(rec, req)
}

// ── initialize ────────────────────────────────────────────────────────────────

func BenchmarkMCP_Initialize(b *testing.B) {
	body := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`
	b.ReportAllocs()
	for b.Loop() {
		postJSON(b, body)
	}
}

// ── initialized (notification — 204 path) ────────────────────────────────────

func BenchmarkMCP_Initialized(b *testing.B) {
	// No id → notification → 204, no encoding.
	body := `{"jsonrpc":"2.0","method":"initialized","params":{}}`
	b.ReportAllocs()
	for b.Loop() {
		postJSON(b, body)
	}
}

// ── tools/list ────────────────────────────────────────────────────────────────

func BenchmarkMCP_ToolsList(b *testing.B) {
	body := `{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}`
	b.ReportAllocs()
	for b.Loop() {
		postJSON(b, body)
	}
}

// ── tools/call ────────────────────────────────────────────────────────────────

func BenchmarkMCP_ToolsCall_Echo(b *testing.B) {
	body := `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"echo","arguments":{"id":42}}}`
	b.ReportAllocs()
	for b.Loop() {
		postJSON(b, body)
	}
}

func BenchmarkMCP_ToolsCall_Noop(b *testing.B) {
	body := `{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"noop","arguments":{}}}`
	b.ReportAllocs()
	for b.Loop() {
		postJSON(b, body)
	}
}

func BenchmarkMCP_ToolsCall_NotFound(b *testing.B) {
	body := `{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"does_not_exist","arguments":{}}}`
	b.ReportAllocs()
	for b.Loop() {
		postJSON(b, body)
	}
}

// ── Handler construction (schema reflection cost) ─────────────────────────────

func BenchmarkMCP_HandlerConstruction(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		_ = Handler(Config{
			Name:    "bench",
			Version: "1.0.0",
			Tools: []Tool{
				{Name: "t1", Input: (*benchProductInput)(nil), Handler: func(_ context.Context, _ json.RawMessage) (any, error) { return nil, nil }},
				{Name: "t2", Input: (*benchProductInput)(nil), Handler: func(_ context.Context, _ json.RawMessage) (any, error) { return nil, nil }},
			},
		})
	}
}

// ── GET (capability document) ─────────────────────────────────────────────────

func BenchmarkMCP_GetCapabilities(b *testing.B) {
	req := httptest.NewRequest(http.MethodGet, "/mcp", nil)
	b.ReportAllocs()
	for b.Loop() {
		rec := httptest.NewRecorder()
		benchHandler.ServeHTTP(rec, req)
	}
}

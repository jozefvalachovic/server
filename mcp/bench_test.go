package mcp

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

type benchProductInput struct {
	ID int `json:"id" jsonschema:"numeric product ID"`
}

type benchProductOutput struct {
	ID int `json:"id"`
}

type benchEmpty struct{}

var benchHandler = newBenchHandler()

func newBenchHandler() *Server {
	server, err := New(Config{Name: "bench-server", Version: "1.0.0"})
	if err != nil {
		panic(err)
	}
	AddTool(server, &Tool{Name: "echo", Description: "Echoes its input back."},
		func(_ context.Context, _ *CallToolRequest, input benchProductInput) (*CallToolResult, benchProductOutput, error) {
			return nil, benchProductOutput(input), nil
		})
	AddTool(server, &Tool{Name: "noop", Description: "Returns an empty object."},
		func(context.Context, *CallToolRequest, benchEmpty) (*CallToolResult, benchEmpty, error) {
			return nil, benchEmpty{}, nil
		})
	return server
}

func postJSON(b *testing.B, method string, body []byte) {
	b.Helper()
	req := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Mcp-Method", method)
	recorder := httptest.NewRecorder()
	benchHandler.ServeHTTP(recorder, req)
}

func BenchmarkMCPDiscover(b *testing.B) {
	body := []byte(`{"jsonrpc":"2.0","id":1,"method":"server/discover","params":{"_meta":{"io.modelcontextprotocol/protocol-version":"2026-07-28","io.modelcontextprotocol/client-capabilities":{}}}}`)
	b.ReportAllocs()
	for b.Loop() {
		postJSON(b, "server/discover", body)
	}
}

func BenchmarkMCPToolsList(b *testing.B) {
	body := []byte(`{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{"_meta":{"io.modelcontextprotocol/protocol-version":"2026-07-28","io.modelcontextprotocol/client-capabilities":{}}}}`)
	b.ReportAllocs()
	for b.Loop() {
		postJSON(b, "tools/list", body)
	}
}

func BenchmarkMCPToolsCallEcho(b *testing.B) {
	body := []byte(`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"echo","arguments":{"id":42},"_meta":{"io.modelcontextprotocol/protocol-version":"2026-07-28","io.modelcontextprotocol/client-capabilities":{}}}}`)
	b.ReportAllocs()
	for b.Loop() {
		postJSON(b, "tools/call", body)
	}
}

func BenchmarkMCPToolsCallNoop(b *testing.B) {
	body := []byte(`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"noop","arguments":{},"_meta":{"io.modelcontextprotocol/protocol-version":"2026-07-28","io.modelcontextprotocol/client-capabilities":{}}}}`)
	b.ReportAllocs()
	for b.Loop() {
		postJSON(b, "tools/call", body)
	}
}

func BenchmarkMCPToolsCallNotFound(b *testing.B) {
	body := []byte(`{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"does_not_exist","arguments":{},"_meta":{"io.modelcontextprotocol/protocol-version":"2026-07-28","io.modelcontextprotocol/client-capabilities":{}}}}`)
	b.ReportAllocs()
	for b.Loop() {
		postJSON(b, "tools/call", body)
	}
}

func BenchmarkMCPServerConstruction(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		server, err := New(Config{Name: "bench", Version: "1.0.0"})
		if err != nil {
			b.Fatal(err)
		}
		AddTool(server, &Tool{Name: "echo"},
			func(_ context.Context, _ *CallToolRequest, input benchProductInput) (*CallToolResult, benchProductOutput, error) {
				return nil, benchProductOutput(input), nil
			})
	}
}

func BenchmarkMCPStatelessGETRejection(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		recorder := httptest.NewRecorder()
		benchHandler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/mcp", nil))
	}
}

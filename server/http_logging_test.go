package server

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jozefvalachovic/logger/v4"
)

func TestHTTPServerRequestBodyLoggingDefaultOff(t *testing.T) {
	const body = `{"content":"private model prompt"}`
	server, _ := newHTTPLoggingTestServer(t, false, nil)
	logs := captureHTTPLogs(t)

	serveFailedRequest(server, body)

	if strings.Contains(logs.String(), "private model prompt") {
		t.Fatal("failed request body was logged without explicit opt-in")
	}
}

func TestHTTPServerRequestBodyLoggingOptIn(t *testing.T) {
	const body = `{"content":"diagnostic payload"}`
	server, _ := newHTTPLoggingTestServer(t, true, nil)
	logs := captureHTTPLogs(t)

	serveFailedRequest(server, body)

	if !strings.Contains(logs.String(), "diagnostic payload") {
		t.Fatal("failed request body was not logged after explicit opt-in")
	}
}

func TestHTTPServerRequestBodyLoggingRedactsSensitiveFields(t *testing.T) {
	// logger v4.3.0 redacts namespaced keys: JSON body fields are logged as
	// "body.<field>", and RedactKeys match on the final dotted segment, so
	// body.password is masked even though the key is not an exact match.
	const body = `{"username":"alice","password":"hunter2-secret"}`
	server, _ := newHTTPLoggingTestServer(t, true, nil)
	logs := captureHTTPLogs(t)

	serveFailedRequest(server, body)

	output := logs.String()
	if strings.Contains(output, "hunter2-secret") {
		t.Fatal("sensitive body field was logged in the clear; expected namespaced redaction")
	}
	if !strings.Contains(output, "alice") {
		t.Fatal("non-sensitive body field was not logged; redaction too broad")
	}
}

func TestHTTPServerRequestBodyLoggingDisabledPreservesBody(t *testing.T) {
	const body = `{"content":"complete downstream payload"}`
	server, received := newHTTPLoggingTestServer(t, false, new(string))
	captureHTTPLogs(t)

	serveFailedRequest(server, body)

	if *received != body {
		t.Fatalf("downstream body = %q, want %q", *received, body)
	}
}

func TestHTTPServerDefaultSkipPathsSuppressProbeLogs(t *testing.T) {
	server, _ := newHTTPLoggingTestServer(t, false, nil)
	logs := captureHTTPLogs(t)

	// All default probe paths must be suppressed from access logs,
	// including the ones previously dropped by the duplicate
	// WithSkipPaths call (/health, /healthcheck, /ready, /livez, /live).
	for _, path := range []string{"/health", "/healthcheck", "/healthz", "/readyz", "/ready", "/livez", "/live"} {
		request := httptest.NewRequest(http.MethodGet, path, nil)
		server.server.Handler.ServeHTTP(httptest.NewRecorder(), request)
	}
	// A regular path must still be logged.
	request := httptest.NewRequest(http.MethodGet, "/api/items", nil)
	server.server.Handler.ServeHTTP(httptest.NewRecorder(), request)

	output := logs.String()
	if !strings.Contains(output, `"__path":"/api/items"`) {
		t.Fatal("regular path /api/items was not access-logged")
	}
	for _, path := range []string{"/health", "/healthcheck", "/healthz", "/readyz", "/ready", "/livez", "/live"} {
		if strings.Contains(output, `"__path":"`+path+`"`) {
			t.Fatalf("probe path %s was access-logged despite default skip list", path)
		}
	}
}

func TestHTTPServerAuditSkipPathsMergeWithDefaults(t *testing.T) {
	t.Setenv("HTTP_HOST", "127.0.0.1")
	t.Setenv("HTTP_PORT", "8080")

	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	mux.HandleFunc("GET /probe", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	mux.HandleFunc("GET /api/items", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })

	server, err := NewHTTPServer(mux, "logging-test", "1.0.0", HTTPServerConfig{
		AuditConfig: &HTTPAuditConfig{
			Enabled:   true,
			SkipPaths: []string{"/probe", "/healthz"}, // overlaps a default; must dedupe
		},
	})
	if err != nil {
		t.Fatalf("NewHTTPServer: %v", err)
	}
	logs := captureHTTPLogs(t)

	for _, path := range []string{"/health", "/probe", "/api/items"} {
		request := httptest.NewRequest(http.MethodGet, path, nil)
		server.server.Handler.ServeHTTP(httptest.NewRecorder(), request)
	}

	output := logs.String()
	if !strings.Contains(output, `"__path":"/api/items"`) {
		t.Fatal("regular path /api/items was not access-logged")
	}
	for _, path := range []string{"/health", "/probe"} {
		if strings.Contains(output, `"__path":"`+path+`"`) {
			t.Fatalf("path %s was access-logged despite merged skip list", path)
		}
	}
}

func newHTTPLoggingTestServer(t *testing.T, enabled bool, received *string) (*HTTPServer, *string) {
	t.Helper()
	t.Setenv("HTTP_HOST", "127.0.0.1")
	t.Setenv("HTTP_PORT", "8080")
	if received == nil {
		received = new(string)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("POST /failed", func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read request body: %v", err)
		}
		*received = string(body)
		w.WriteHeader(http.StatusBadRequest)
	})

	server, err := NewHTTPServer(mux, "logging-test", "1.0.0", HTTPServerConfig{
		LogRequestBodyOnErrors: enabled,
	})
	if err != nil {
		t.Fatalf("NewHTTPServer: %v", err)
	}
	return server, received
}

func captureHTTPLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	previous := logger.GetConfig()
	var output bytes.Buffer
	configured := previous
	configured.Output = &output
	configured.AsyncMode = false
	configured.EnableColor = false
	configured.CompactJSON = true
	configured.EnableDedup = false
	configured.SampleRate = 1
	configured.SampleRateSet = true
	logger.SetConfig(configured)
	t.Cleanup(func() { logger.SetConfig(previous) })
	return &output
}

func serveFailedRequest(server *HTTPServer, body string) {
	request := httptest.NewRequest(http.MethodPost, "/failed", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	server.server.Handler.ServeHTTP(httptest.NewRecorder(), request)
}

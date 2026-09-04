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

func TestHTTPServerRequestBodyLoggingDisabledPreservesBody(t *testing.T) {
	const body = `{"content":"complete downstream payload"}`
	server, received := newHTTPLoggingTestServer(t, false, new(string))
	captureHTTPLogs(t)

	serveFailedRequest(server, body)

	if *received != body {
		t.Fatalf("downstream body = %q, want %q", *received, body)
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

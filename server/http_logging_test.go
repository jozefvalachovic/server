package server

import (
	"bytes"
	"io"
	"log"
	"log/slog"
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

func TestHTTPServerLogLevelsExactOverride(t *testing.T) {
	// Consumer intent: routine 401 probes on an auth service log at Info
	// instead of the 4xx Warn default.
	server := newHTTPLogLevelsTestServer(t, map[int]logger.LogLevel{
		http.StatusUnauthorized: logger.Info,
	})
	logs := captureHTTPLogs(t)

	serveLogLevelsRequest(server, "/unauthorized")

	line := findAccessLogLine(logs.String(), "/unauthorized")
	if line == "" {
		t.Fatal("401 request was not access-logged")
	}
	if !strings.Contains(line, " INFO ") {
		t.Fatalf("401 logged at wrong level; want INFO, got line: %s", line)
	}
}

func TestHTTPServerLogLevelsDefaultWarnFor401(t *testing.T) {
	// Without LogLevels the logger default applies: 4xx → Warn.
	server := newHTTPLogLevelsTestServer(t, nil)
	logs := captureHTTPLogs(t)

	serveLogLevelsRequest(server, "/unauthorized")

	line := findAccessLogLine(logs.String(), "/unauthorized")
	if line == "" {
		t.Fatal("401 request was not access-logged")
	}
	if !strings.Contains(line, " WARN ") {
		t.Fatalf("401 default level changed; want WARN, got line: %s", line)
	}
}

func TestHTTPServerLogLevelsClassKeyAppliesToClass(t *testing.T) {
	// Class key 400 covers every 4xx, so a 404 logs at the class level.
	server := newHTTPLogLevelsTestServer(t, map[int]logger.LogLevel{
		400: logger.Info,
	})
	logs := captureHTTPLogs(t)

	serveLogLevelsRequest(server, "/missing")

	line := findAccessLogLine(logs.String(), "/missing")
	if line == "" {
		t.Fatal("404 request was not access-logged")
	}
	if !strings.Contains(line, " INFO ") {
		t.Fatalf("class key 400 did not apply to 404; want INFO, got line: %s", line)
	}
}

func TestHTTPServerLogLevelsExactBeatsClass(t *testing.T) {
	// Exact key 404 must win over class key 400.
	server := newHTTPLogLevelsTestServer(t, map[int]logger.LogLevel{
		400:                 logger.Info,
		http.StatusNotFound: logger.Error,
	})
	logs := captureHTTPLogs(t)

	serveLogLevelsRequest(server, "/missing")
	serveLogLevelsRequest(server, "/unauthorized")

	notFound := findAccessLogLine(logs.String(), "/missing")
	if !strings.Contains(notFound, " ERROR ") {
		t.Fatalf("exact key 404 did not beat class key 400; want ERROR, got line: %s", notFound)
	}
	// 401 has no exact key, so the class key still applies.
	unauthorized := findAccessLogLine(logs.String(), "/unauthorized")
	if !strings.Contains(unauthorized, " INFO ") {
		t.Fatalf("class key 400 did not apply to 401; want INFO, got line: %s", unauthorized)
	}
}

func TestHTTPServerLogLevelsMapCopiedAtConstruction(t *testing.T) {
	// WithLogLevels assigns the map by reference; NewHTTPServer must clone it
	// so later caller mutation cannot change logging at runtime.
	levels := map[int]logger.LogLevel{http.StatusUnauthorized: logger.Info}
	server := newHTTPLogLevelsTestServer(t, levels)
	levels[http.StatusUnauthorized] = logger.Error // mutate after construction

	logs := captureHTTPLogs(t)
	serveLogLevelsRequest(server, "/unauthorized")

	line := findAccessLogLine(logs.String(), "/unauthorized")
	if !strings.Contains(line, " INFO ") {
		t.Fatalf("caller map mutation leaked into logging; want INFO, got line: %s", line)
	}
}

func TestHTTPServerConfigValidateRejectsInvalidLogLevelKeys(t *testing.T) {
	cfg := HTTPServerConfig{LogLevels: map[int]logger.LogLevel{99: logger.Info}}
	if err := cfg.Validate(); err == nil {
		t.Fatal("Validate accepted LogLevels key 99; want error")
	}
	cfg = HTTPServerConfig{LogLevels: map[int]logger.LogLevel{600: logger.Info}}
	if err := cfg.Validate(); err == nil {
		t.Fatal("Validate accepted LogLevels key 600; want error")
	}
	cfg = HTTPServerConfig{LogLevels: map[int]logger.LogLevel{
		http.StatusUnauthorized: logger.Info,
		400:                     logger.Warn,
	}}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate rejected valid LogLevels: %v", err)
	}
}

func newHTTPLogLevelsTestServer(t *testing.T, levels map[int]logger.LogLevel) *HTTPServer {
	t.Helper()
	t.Setenv("HTTP_HOST", "127.0.0.1")
	t.Setenv("HTTP_PORT", "8080")

	mux := http.NewServeMux()
	mux.HandleFunc("GET /unauthorized", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	})
	mux.HandleFunc("GET /missing", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})

	server, err := NewHTTPServer(mux, "logging-test", "1.0.0", HTTPServerConfig{
		LogLevels: levels,
	})
	if err != nil {
		t.Fatalf("NewHTTPServer: %v", err)
	}
	return server
}

func serveLogLevelsRequest(server *HTTPServer, path string) {
	request := httptest.NewRequest(http.MethodGet, path, nil)
	server.server.Handler.ServeHTTP(httptest.NewRecorder(), request)
}

// findAccessLogLine returns the log line containing the access-log entry for
// path, or "" when absent. The pretty format is "time LEVEL message json".
func findAccessLogLine(output, path string) string {
	for line := range strings.SplitSeq(output, "\n") {
		if strings.Contains(line, `"__path":"`+path+`"`) {
			return line
		}
	}
	return ""
}

func TestHTTPServerErrorLogDefaultNil(t *testing.T) {
	server := newErrorLogTestServer(t, nil)

	if server.server.ErrorLog != nil {
		t.Fatal("ErrorLog set without configuration; want nil (net/http default)")
	}
}

func TestHTTPServerErrorLogPassedThrough(t *testing.T) {
	errorLog := log.New(io.Discard, "", 0)
	server := newErrorLogTestServer(t, errorLog)

	if server.server.ErrorLog != errorLog {
		t.Fatal("HTTPServerConfig.ErrorLog was not passed to http.Server.ErrorLog")
	}
}

func TestHTTPServerErrorLogRoutesThroughLoggerPipeline(t *testing.T) {
	server := newErrorLogTestServer(t, logger.StdLogger(slog.LevelError))
	logs := captureHTTPLogs(t)

	server.server.ErrorLog.Print("http: TLS handshake error from 10.0.0.1:1234: EOF")

	output := logs.String()
	if !strings.Contains(output, "TLS handshake error") {
		t.Fatalf("net/http error line did not reach the logger pipeline:\n%s", output)
	}
	if !strings.Contains(output, " ERROR ") {
		t.Fatalf("net/http error line not logged at ERROR:\n%s", output)
	}
}

func newErrorLogTestServer(t *testing.T, errorLog *log.Logger) *HTTPServer {
	t.Helper()
	t.Setenv("HTTP_HOST", "127.0.0.1")
	t.Setenv("HTTP_PORT", "8080")

	server, err := NewHTTPServer(http.NewServeMux(), "logging-test", "1.0.0", HTTPServerConfig{
		ErrorLog: errorLog,
	})
	if err != nil {
		t.Fatalf("NewHTTPServer: %v", err)
	}
	return server
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
	configured.Level = slog.LevelDebug
	configured.LevelSet = true
	logger.SetConfig(configured)
	t.Cleanup(func() { logger.SetConfig(previous) })
	return &output
}

func serveFailedRequest(server *HTTPServer, body string) {
	request := httptest.NewRequest(http.MethodPost, "/failed", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	server.server.Handler.ServeHTTP(httptest.NewRecorder(), request)
}

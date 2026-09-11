package server

import (
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/jozefvalachovic/logger/v4"
)

// TestBuildLoggerConfigAppliesRedactPaths covers the core fix: RedactPaths must
// survive the ConfigFromEnv + SetConfig replacement inside logger init.
func TestBuildLoggerConfigAppliesRedactPaths(t *testing.T) {
	paths := []string{"/oauth/google/callback", "/oauth/microsoft/callback"}

	cfg := buildLoggerConfig(nil, paths)

	if !slices.Contains(cfg.RedactPaths, "/oauth/google/callback") {
		t.Fatalf("RedactPaths = %v, want it to contain /oauth/google/callback", cfg.RedactPaths)
	}
	if !slices.Contains(cfg.RedactPaths, "/oauth/microsoft/callback") {
		t.Fatalf("RedactPaths = %v, want it to contain /oauth/microsoft/callback", cfg.RedactPaths)
	}
	if !cfg.EnableDedup || !cfg.EnableMetrics {
		t.Fatal("buildLoggerConfig dropped the dedup/metrics defaults")
	}
}

func TestBuildLoggerConfigNilRedactPathsKeepsDefaults(t *testing.T) {
	cfg := buildLoggerConfig(nil, nil)

	if len(cfg.RedactPaths) != 0 {
		t.Fatalf("RedactPaths = %v, want empty when none supplied", cfg.RedactPaths)
	}
}

func TestBuildLoggerConfigClonesCallerSlice(t *testing.T) {
	// Mutating the caller's slice after construction must not change redaction.
	paths := []string{"/oauth/google/callback"}
	cfg := buildLoggerConfig(nil, paths)
	paths[0] = "/mutated"

	if !slices.Contains(cfg.RedactPaths, "/oauth/google/callback") {
		t.Fatalf("RedactPaths = %v, want the original value; caller slice was aliased", cfg.RedactPaths)
	}
	if slices.Contains(cfg.RedactPaths, "/mutated") {
		t.Fatal("caller slice mutation leaked into the logger config")
	}
}

func TestBuildLoggerConfigPreservesOTelBridge(t *testing.T) {
	// RedactPaths must not displace the OTel additional handler.
	cfg := buildLoggerConfig(&OTelBridgeConfig{
		ServiceName:    "svc",
		ServiceVersion: "1.0.0",
	}, []string{"/oauth/google/callback"})

	if len(cfg.AdditionalHandlers) != 1 {
		t.Fatalf("AdditionalHandlers = %d, want 1 OTel bridge handler", len(cfg.AdditionalHandlers))
	}
	if !slices.Contains(cfg.RedactPaths, "/oauth/google/callback") {
		t.Fatalf("RedactPaths = %v, want it to contain /oauth/google/callback", cfg.RedactPaths)
	}
}

func TestMissingRedactPaths(t *testing.T) {
	tests := []struct {
		name string
		want []string
		have []string
		exp  []string
	}{
		{"all covered", []string{"/a"}, []string{"/a", "/b"}, nil},
		{"one missing", []string{"/a", "/c"}, []string{"/a", "/b"}, []string{"/c"}},
		{"none covered", []string{"/a"}, nil, []string{"/a"}},
		{"nothing wanted", nil, []string{"/a"}, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := missingRedactPaths(tc.want, tc.have)
			if !slices.Equal(got, tc.exp) {
				t.Fatalf("missingRedactPaths(%v, %v) = %v, want %v", tc.want, tc.have, got, tc.exp)
			}
		})
	}
}

// TestHTTPServerRedactPathsMaskOAuthCode is the end-to-end regression for the
// reported leak: an OAuth callback query string must not reach the access log.
func TestHTTPServerRedactPathsMaskOAuthCode(t *testing.T) {
	t.Setenv("HTTP_HOST", "127.0.0.1")
	t.Setenv("HTTP_PORT", "8080")

	mux := http.NewServeMux()
	mux.HandleFunc("GET /oauth/microsoft/callback", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("GET /api/items", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	// The middleware snapshots the logger config during NewHTTPServer, so the
	// paths have to be applied by then. initLogger is once-guarded per process
	// and an earlier test may have already run it, so this test drives the same
	// code path directly: build the config with the paths and install it before
	// constructing the server.
	previous := logger.GetConfig()
	logger.SetConfig(buildLoggerConfig(nil, []string{"/oauth/microsoft/callback"}))
	t.Cleanup(func() { logger.SetConfig(previous) })

	srv, err := NewHTTPServer(mux, "redact-test", "1.0.0", HTTPServerConfig{
		RedactPaths: []string{"/oauth/microsoft/callback"},
	})
	if err != nil {
		t.Fatalf("NewHTTPServer: %v", err)
	}

	logs := captureHTTPLogs(t)
	// captureHTTPLogs reinstalled a config derived from the current one, so the
	// redact paths configured above are preserved.
	const code = "SUPER-SECRET-AUTH-CODE"
	for _, path := range []string{"/oauth/microsoft/callback?code=" + code, "/api/items"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		srv.server.Handler.ServeHTTP(httptest.NewRecorder(), req)
	}

	output := logs.String()
	if strings.Contains(output, code) {
		t.Fatalf("authorization code leaked into the access log:\n%s", output)
	}
	if !strings.Contains(output, `"__path":"/api/items"`) {
		t.Fatalf("unrelated path was not logged normally:\n%s", output)
	}
	if !strings.Contains(output, logger.GetConfig().RedactMask) {
		t.Fatalf("expected the redaction mask in the access log:\n%s", output)
	}
}

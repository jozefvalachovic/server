package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jozefvalachovic/server/middleware"
	"github.com/jozefvalachovic/server/response"
	"github.com/jozefvalachovic/server/routes"
	"github.com/jozefvalachovic/server/server"
	"github.com/jozefvalachovic/server/swagger"
)

// ── helpers ────────────────────────────────────────────────────────────────

// setupIntegrationServer creates the full mux with routes, caching, and
// middleware (via httptest.Server) — no env vars, no real ports.
func setupIntegrationServer(t *testing.T) *httptest.Server {
	t.Helper()
	resetProducts()

	mux := http.NewServeMux()

	cacheConfig := &routes.CacheConfig{
		DefaultTTL:      300 * time.Second,
		CleanupInterval: 30 * time.Second,
		MaxSize:         100,
	}

	store, err := routes.RegisterRoutes(mux, cacheConfig,
		productRegistrar,
		utilRegistrar,
	)
	if err != nil {
		t.Fatalf("route registration: %v", err)
	}

	// Health checks.
	hc := server.NewHealthChecker("1.0.0-test", 5*time.Second)
	hc.Register("cache", func(ctx context.Context) error {
		if store == nil {
			return errors.New("cache store not initialised")
		}
		return nil
	})
	mux.HandleFunc("GET /healthz", hc.LivenessHandler())
	mux.HandleFunc("GET /readyz", hc.ReadinessHandler())

	// Swagger.
	routes.RegisterSwagger(mux, "/docs", swagger.Config{
		Title:   "Test API",
		Version: "1.0.0-test",
	})

	// Wrap with a minimal middleware stack (no rate-limit for tests).
	var handler http.Handler = mux
	stack := []server.HTTPMiddleware{
		middleware.Recovery,
		middleware.Security,
		middleware.RequestSize,
		middleware.CORS(middleware.CORSConfig{
			AllowedOrigins:   []string{"http://localhost:3000"},
			AllowedHeaders:   []string{"Content-Type", "Authorization", "X-Request-ID"},
			AllowCredentials: true,
		}),
		middleware.RequestID(middleware.RequestIDConfig{}),
	}
	for i := len(stack) - 1; i >= 0; i-- {
		handler = stack[i](handler)
	}

	ts := httptest.NewServer(handler)
	t.Cleanup(func() {
		ts.Close()
		store.Stop()
	})
	return ts
}

// ── Middleware stack ───────────────────────────────────────────────────────

func TestIntegration_SecurityHeaders(t *testing.T) {
	ts := setupIntegrationServer(t)

	resp, err := http.Get(ts.URL + "/products")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()

	checks := map[string]string{
		"X-Content-Type-Options": "nosniff",
		"X-Frame-Options":        "DENY",
		"Server":                 "server",
	}
	for header, expected := range checks {
		if got := resp.Header.Get(header); got != expected {
			t.Errorf("header %s: expected %q, got %q", header, expected, got)
		}
	}
}

func TestIntegration_RequestIDInjected(t *testing.T) {
	ts := setupIntegrationServer(t)

	resp, err := http.Get(ts.URL + "/products")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()

	rid := resp.Header.Get("X-Request-ID")
	if rid == "" {
		t.Fatal("expected X-Request-ID header to be set")
	}
}

func TestIntegration_CORSPreflight(t *testing.T) {
	ts := setupIntegrationServer(t)

	req, _ := http.NewRequest(http.MethodOptions, ts.URL+"/products", nil)
	req.Header.Set("Origin", "http://localhost:3000")
	req.Header.Set("Access-Control-Request-Method", "POST")
	req.Header.Set("Access-Control-Request-Headers", "Content-Type")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()

	if got := resp.Header.Get("Access-Control-Allow-Origin"); got != "http://localhost:3000" {
		t.Fatalf("expected CORS origin 'http://localhost:3000', got %q", got)
	}
	if got := resp.Header.Get("Access-Control-Allow-Credentials"); got != "true" {
		t.Fatalf("expected credentials 'true', got %q", got)
	}
}

// ── Auth flow ─────────────────────────────────────────────────────────────

func TestIntegration_Auth_MeWithoutToken(t *testing.T) {
	ts := setupIntegrationServer(t)

	resp, err := http.Get(ts.URL + "/me")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", resp.StatusCode)
	}
}

func TestIntegration_Auth_MeWithToken(t *testing.T) {
	ts := setupIntegrationServer(t)

	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/me", nil)
	req.Header.Set("Authorization", "Bearer test-token-123")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, body)
	}

	var raw map[string]json.RawMessage
	_ = json.NewDecoder(resp.Body).Decode(&raw)
}

// ── Cache behavior ────────────────────────────────────────────────────────

func TestIntegration_Cache_HitAndInvalidation(t *testing.T) {
	ts := setupIntegrationServer(t)

	// First GET — populates cache.
	resp1, err := http.Get(ts.URL + "/products")
	if err != nil {
		t.Fatal(err)
	}
	body1, _ := io.ReadAll(resp1.Body)
	_ = resp1.Body.Close()

	// Second GET — should return cached data.
	resp2, err := http.Get(ts.URL + "/products")
	if err != nil {
		t.Fatal(err)
	}
	body2, _ := io.ReadAll(resp2.Body)
	_ = resp2.Body.Close()

	if !bytes.Equal(body1, body2) {
		t.Fatal("expected identical cached response on second GET")
	}

	// POST — should invalidate cache.
	postBody := `{"name":"New Item","description":"invalidates cache","price":5.00}`
	resp3, err := http.Post(ts.URL+"/products", "application/json", bytes.NewBufferString(postBody))
	if err != nil {
		t.Fatal(err)
	}
	_ = resp3.Body.Close()
	if resp3.StatusCode != http.StatusCreated {
		t.Fatalf("POST expected 201, got %d", resp3.StatusCode)
	}

	// Third GET — should be different (cache invalidated).
	resp4, err := http.Get(ts.URL + "/products")
	if err != nil {
		t.Fatal(err)
	}
	body4, _ := io.ReadAll(resp4.Body)
	_ = resp4.Body.Close()

	if bytes.Equal(body1, body4) {
		t.Fatal("expected different response after POST invalidation")
	}
}

// ── Health probes ─────────────────────────────────────────────────────────

func TestIntegration_Healthz(t *testing.T) {
	ts := setupIntegrationServer(t)

	resp, err := http.Get(ts.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var result server.HealthCheckResult
	_ = json.NewDecoder(resp.Body).Decode(&result)
	if result.Status != server.HealthStatusOK {
		t.Fatalf("expected status 'ok', got %q", result.Status)
	}
	if result.Version != "1.0.0-test" {
		t.Fatalf("expected version '1.0.0-test', got %q", result.Version)
	}
}

func TestIntegration_Readyz(t *testing.T) {
	ts := setupIntegrationServer(t)

	resp, err := http.Get(ts.URL + "/readyz")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var result server.HealthCheckResult
	_ = json.NewDecoder(resp.Body).Decode(&result)
	if result.Status != server.HealthStatusOK {
		t.Fatalf("expected status 'ok', got %q", result.Status)
	}
	if _, ok := result.Checks["cache"]; !ok {
		t.Fatal("expected 'cache' check in readiness response")
	}
}

func TestIntegration_Readyz_DegradedCheck(t *testing.T) {
	resetProducts()

	mux := http.NewServeMux()
	hc := server.NewHealthChecker("1.0.0-test", 5*time.Second)
	hc.Register("broken", func(ctx context.Context) error {
		return errors.New("connection refused")
	})
	mux.HandleFunc("GET /readyz", hc.ReadinessHandler())

	ts := httptest.NewServer(mux)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/readyz")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()

	// A single broken check → down → 503.
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", resp.StatusCode)
	}
}

// ── Method routing ────────────────────────────────────────────────────────

func TestIntegration_MethodNotAllowed(t *testing.T) {
	ts := setupIntegrationServer(t)

	req, _ := http.NewRequest(http.MethodPatch, ts.URL+"/products", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()

	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d", resp.StatusCode)
	}
	if allow := resp.Header.Get("Allow"); allow == "" {
		t.Fatal("expected Allow header on 405 response")
	}
}

// ── Content-Type ──────────────────────────────────────────────────────────

func TestIntegration_ContentTypeJSON(t *testing.T) {
	ts := setupIntegrationServer(t)

	endpoints := []string{"/products", "/products/1", "/admin", "/ip", "/healthz", "/readyz"}
	for _, ep := range endpoints {
		t.Run(ep, func(t *testing.T) {
			req, _ := http.NewRequest(http.MethodGet, ts.URL+ep, nil)
			// /admin needs X-Admin header to get 200.
			if ep == "/admin" {
				req.Header.Set("X-Admin", "1")
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			_ = resp.Body.Close()

			ct := resp.Header.Get("Content-Type")
			if ct == "" {
				t.Fatal("expected Content-Type header")
			}
			// Allow both "application/json" and "application/json; charset=utf-8".
			if ct != "application/json" && ct != "application/json; charset=utf-8" {
				t.Fatalf("expected application/json, got %q", ct)
			}
		})
	}
}

// ── Pagination contract ───────────────────────────────────────────────────

func TestIntegration_PaginationEnvelope(t *testing.T) {
	ts := setupIntegrationServer(t)

	resp, err := http.Get(ts.URL + "/products?limit=2&offset=0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()

	var envelope struct {
		Code       int `json:"code"`
		Pagination *struct {
			Limit       int  `json:"limit"`
			Offset      int  `json:"offset"`
			TotalCount  int  `json:"totalCount"`
			TotalPages  int  `json:"totalPages"`
			CurrentPage int  `json:"currentPage"`
			HasMore     bool `json:"hasMore"`
		} `json:"pagination"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&envelope)

	if envelope.Pagination == nil {
		t.Fatal("expected pagination in response")
	}
	if envelope.Pagination.TotalCount != 3 {
		t.Fatalf("expected totalCount=3, got %d", envelope.Pagination.TotalCount)
	}
	if envelope.Pagination.TotalPages != 2 {
		t.Fatalf("expected totalPages=2, got %d", envelope.Pagination.TotalPages)
	}
	if envelope.Pagination.CurrentPage != 1 {
		t.Fatalf("expected currentPage=1, got %d", envelope.Pagination.CurrentPage)
	}
	if !envelope.Pagination.HasMore {
		t.Fatal("expected hasMore=true with limit=2, total=3")
	}
}

// ── Error envelope schema ─────────────────────────────────────────────────

func TestIntegration_ErrorEnvelopeSchema(t *testing.T) {
	ts := setupIntegrationServer(t)

	// Hit a 404.
	resp, err := http.Get(ts.URL + "/products/999")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()

	var env response.APIError[any]
	_ = json.NewDecoder(resp.Body).Decode(&env)

	if env.Code != http.StatusNotFound {
		t.Fatalf("expected code 404, got %d", env.Code)
	}
	if env.Message == "" {
		t.Fatal("expected non-empty message in error envelope")
	}
}

// ── Swagger UI ────────────────────────────────────────────────────────────

func TestIntegration_SwaggerUI(t *testing.T) {
	ts := setupIntegrationServer(t)

	// /docs should redirect to /docs/.
	client := &http.Client{CheckRedirect: func(req *http.Request, via []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	resp, err := client.Get(ts.URL + "/docs")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusMovedPermanently {
		t.Fatalf("expected 301 redirect, got %d", resp.StatusCode)
	}

	// /docs/ should return HTML.
	resp2, err := http.Get(ts.URL + "/docs/")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp2.Body.Close() }()

	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp2.StatusCode)
	}
	body, _ := io.ReadAll(resp2.Body)
	if !bytes.Contains(body, []byte("<html")) && !bytes.Contains(body, []byte("<!DOCTYPE")) {
		t.Fatal("expected HTML content from swagger endpoint")
	}
}

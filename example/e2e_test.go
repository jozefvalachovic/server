package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jozefvalachovic/server/middleware"
	"github.com/jozefvalachovic/server/routes"
	"github.com/jozefvalachovic/server/server"
	"github.com/jozefvalachovic/server/swagger"
)

// ── helpers ────────────────────────────────────────────────────────────────

// e2eServer spins up the real HTTPServer (same construction as main()) on
// an ephemeral port. Returns the base URL and a cleanup function.
func e2eServer(t *testing.T) (baseURL string, cleanup func()) {
	t.Helper()
	resetProducts()

	// Find a free port.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("find free port: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close() // release so the server can bind

	_ = os.Setenv("HTTP_HOST", "127.0.0.1")
	_ = os.Setenv("HTTP_PORT", fmt.Sprintf("%d", port))
	t.Cleanup(func() {
		_ = os.Unsetenv("HTTP_HOST")
		_ = os.Unsetenv("HTTP_PORT")
	})

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

	hc := server.NewHealthChecker("1.0.0-e2e", 5*time.Second)
	hc.Register("cache", func(ctx context.Context) error {
		if store == nil {
			return errors.New("cache store not initialised")
		}
		return nil
	})
	mux.HandleFunc("GET /healthz", hc.LivenessHandler())
	mux.HandleFunc("GET /readyz", hc.ReadinessHandler())

	routes.RegisterSwagger(mux, "/docs", swagger.Config{
		Title:   "E2E Test API",
		Version: "1.0.0-e2e",
	})

	srv, err := server.NewHTTPServer(mux, "e2e-test", "1.0.0-e2e", server.HTTPServerConfig{
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 30 * time.Second,
		CORS: &server.CORSConfig{
			AllowedOrigins:   []string{"http://localhost:3000"},
			AllowedHeaders:   []string{"Content-Type", "Authorization", "X-Request-ID"},
			AllowCredentials: true,
		},
		RequestID: &server.RequestIDConfig{},
		Timeout:   &server.TimeoutConfig{Timeout: 10 * time.Second},
		Compress:  &server.CompressConfig{Enabled: true},
		Middlewares: []server.HTTPMiddleware{
			middleware.IPFilter(middleware.IPFilterConfig{}),
		},
	})
	if err != nil {
		t.Fatalf("server create: %v", err)
	}

	if err := srv.Start(); err != nil {
		t.Fatalf("server start: %v", err)
	}

	base := fmt.Sprintf("http://127.0.0.1:%d", port)

	// Wait for server to be ready (up to 2s).
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get(base + "/healthz")
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				break
			}
		}
		time.Sleep(25 * time.Millisecond)
	}

	return base, func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.GracefulShutdown(ctx)
		store.Stop()
	}
}

// ── Full CRUD lifecycle ───────────────────────────────────────────────────

func TestE2E_CRUDLifecycle(t *testing.T) {
	base, cleanup := e2eServer(t)
	defer cleanup()

	client := &http.Client{Timeout: 5 * time.Second}

	// 1. List initial products (expect 3 seeds).
	resp, err := client.Get(base + "/products")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list expected 200, got %d", resp.StatusCode)
	}
	var listResp struct {
		Data       []Product `json:"data"`
		Pagination *struct {
			TotalCount int `json:"totalCount"`
		} `json:"pagination"`
	}
	_ = json.Unmarshal(body, &listResp)
	if listResp.Pagination.TotalCount != 3 {
		t.Fatalf("expected 3 initial products, got %d", listResp.Pagination.TotalCount)
	}

	// 2. Create a new product.
	createBody := `{"name":"E2E Product","description":"Created by e2e test","price":42.00}`
	resp, err = client.Post(base+"/products", "application/json", bytes.NewBufferString(createBody))
	if err != nil {
		t.Fatal(err)
	}
	body, _ = io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create expected 201, got %d: %s", resp.StatusCode, body)
	}
	var createResp struct {
		Data Product `json:"data"`
	}
	_ = json.Unmarshal(body, &createResp)
	newID := createResp.Data.ID
	if newID < 4 {
		t.Fatalf("expected new ID >= 4, got %d", newID)
	}

	// 3. Get by ID.
	resp, err = client.Get(fmt.Sprintf("%s/products/%d", base, newID))
	if err != nil {
		t.Fatal(err)
	}
	body, _ = io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get expected 200, got %d", resp.StatusCode)
	}
	var getResp struct {
		Data Product `json:"data"`
	}
	_ = json.Unmarshal(body, &getResp)
	if getResp.Data.Name != "E2E Product" {
		t.Fatalf("expected name 'E2E Product', got %q", getResp.Data.Name)
	}

	// 4. Update the product.
	updateBody := `{"name":"E2E Updated","price":99.99}`
	req, _ := http.NewRequest(http.MethodPut, fmt.Sprintf("%s/products/%d", base, newID), bytes.NewBufferString(updateBody))
	req.Header.Set("Content-Type", "application/json")
	resp, err = client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ = io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("update expected 200, got %d: %s", resp.StatusCode, body)
	}
	var updateResp struct {
		Data Product `json:"data"`
	}
	_ = json.Unmarshal(body, &updateResp)
	if updateResp.Data.Name != "E2E Updated" {
		t.Fatalf("expected 'E2E Updated', got %q", updateResp.Data.Name)
	}
	if updateResp.Data.Price != 99.99 {
		t.Fatalf("expected price 99.99, got %f", updateResp.Data.Price)
	}

	// 5. Delete the product.
	req, _ = http.NewRequest(http.MethodDelete, fmt.Sprintf("%s/products/%d", base, newID), nil)
	resp, err = client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete expected 204, got %d", resp.StatusCode)
	}

	// 6. Confirm 404.
	resp, err = client.Get(fmt.Sprintf("%s/products/%d", base, newID))
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404 after delete, got %d", resp.StatusCode)
	}
}

// ── Concurrent writes ─────────────────────────────────────────────────────

func TestE2E_ConcurrentCreates(t *testing.T) {
	base, cleanup := e2eServer(t)
	defer cleanup()

	client := &http.Client{Timeout: 5 * time.Second}
	const n = 10
	var wg sync.WaitGroup
	errs := make(chan error, n)
	ids := make(chan int, n)

	for i := range n {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			body := fmt.Sprintf(`{"name":"Concurrent-%d","description":"test","price":%d.99}`, i, i+1)
			resp, err := client.Post(base+"/products", "application/json", bytes.NewBufferString(body))
			if err != nil {
				errs <- err
				return
			}
			defer func() { _ = resp.Body.Close() }()
			if resp.StatusCode != http.StatusCreated {
				b, _ := io.ReadAll(resp.Body)
				errs <- fmt.Errorf("goroutine %d: expected 201, got %d: %s", i, resp.StatusCode, b)
				return
			}
			var cr struct {
				Data Product `json:"data"`
			}
			_ = json.NewDecoder(resp.Body).Decode(&cr)
			ids <- cr.Data.ID
		}(i)
	}
	wg.Wait()
	close(errs)
	close(ids)

	for err := range errs {
		t.Fatal(err)
	}

	// Check for duplicate IDs.
	seen := map[int]bool{}
	for id := range ids {
		if seen[id] {
			t.Fatalf("duplicate product ID: %d", id)
		}
		seen[id] = true
	}
	if len(seen) != n {
		t.Fatalf("expected %d unique IDs, got %d", n, len(seen))
	}
}

// ── Error envelope schema ─────────────────────────────────────────────────

func TestE2E_ErrorEnvelopeConsistency(t *testing.T) {
	base, cleanup := e2eServer(t)
	defer cleanup()

	client := &http.Client{Timeout: 5 * time.Second}

	// Each sub-test triggers a different error status.
	cases := []struct {
		name     string
		method   string
		path     string
		body     string
		wantCode int
	}{
		{"not found", http.MethodGet, "/products/999", "", http.StatusNotFound},
		{"bad request", http.MethodGet, "/products/abc", "", http.StatusBadRequest},
		{"forbidden", http.MethodGet, "/admin", "", http.StatusForbidden},
		{"malformed json", http.MethodPost, "/products", `{bad`, http.StatusBadRequest},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var bodyReader io.Reader
			if tc.body != "" {
				bodyReader = bytes.NewBufferString(tc.body)
			}
			req, _ := http.NewRequest(tc.method, base+tc.path, bodyReader)
			if tc.body != "" {
				req.Header.Set("Content-Type", "application/json")
			}
			resp, err := client.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = resp.Body.Close() }()

			if resp.StatusCode != tc.wantCode {
				t.Fatalf("expected %d, got %d", tc.wantCode, resp.StatusCode)
			}

			var env struct {
				Code    int    `json:"code"`
				Message string `json:"message"`
			}
			_ = json.NewDecoder(resp.Body).Decode(&env)

			if env.Code != tc.wantCode {
				t.Fatalf("envelope code expected %d, got %d", tc.wantCode, env.Code)
			}
			if env.Message == "" {
				t.Fatal("expected non-empty message in error envelope")
			}
		})
	}
}

// ── Health contract under full server ─────────────────────────────────────

func TestE2E_HealthzAlwaysOK(t *testing.T) {
	base, cleanup := e2eServer(t)
	defer cleanup()

	client := &http.Client{Timeout: 5 * time.Second}

	// Hit healthz multiple times to verify it never fails.
	for i := range 5 {
		resp, err := client.Get(base + "/healthz")
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("healthz request %d: expected 200, got %d", i, resp.StatusCode)
		}
	}
}

func TestE2E_ReadyzIncludesVersion(t *testing.T) {
	base, cleanup := e2eServer(t)
	defer cleanup()

	client := &http.Client{Timeout: 5 * time.Second}

	resp, err := client.Get(base + "/readyz")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()

	var result server.HealthCheckResult
	_ = json.NewDecoder(resp.Body).Decode(&result)
	if result.Version != "1.0.0-e2e" {
		t.Fatalf("expected version '1.0.0-e2e', got %q", result.Version)
	}
	if result.Status != server.HealthStatusOK {
		t.Fatalf("expected status 'ok', got %q", result.Status)
	}
}

// ── Swagger endpoint ──────────────────────────────────────────────────────

func TestE2E_SwaggerReturnsHTML(t *testing.T) {
	base, cleanup := e2eServer(t)
	defer cleanup()

	client := &http.Client{Timeout: 5 * time.Second}

	resp, err := client.Get(base + "/docs/")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !bytes.Contains(body, []byte("<")) {
		t.Fatal("expected HTML content")
	}
}

// ── Graceful shutdown ─────────────────────────────────────────────────────

func TestE2E_GracefulShutdown(t *testing.T) {
	base, cleanup := e2eServer(t)

	client := &http.Client{Timeout: 5 * time.Second}

	// Verify server is alive.
	resp, err := client.Get(base + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("pre-shutdown: expected 200, got %d", resp.StatusCode)
	}

	// Trigger graceful shutdown.
	cleanup()

	// After shutdown, connections should be refused (eventually).
	// Give the OS a moment to close the socket.
	time.Sleep(100 * time.Millisecond)
	_, err = client.Get(base + "/healthz")
	if err == nil {
		t.Fatal("expected connection error after shutdown")
	}
}

// ── Full middleware chain verification ─────────────────────────────────────

func TestE2E_MiddlewareChainHeaders(t *testing.T) {
	base, cleanup := e2eServer(t)
	defer cleanup()

	client := &http.Client{Timeout: 5 * time.Second}

	resp, err := client.Get(base + "/products")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()

	// Security headers.
	if got := resp.Header.Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("X-Content-Type-Options: expected 'nosniff', got %q", got)
	}
	if got := resp.Header.Get("X-Frame-Options"); got != "DENY" {
		t.Errorf("X-Frame-Options: expected 'DENY', got %q", got)
	}
	if got := resp.Header.Get("Server"); got != "server" {
		t.Errorf("Server: expected 'server', got %q", got)
	}

	// Request ID.
	if rid := resp.Header.Get("X-Request-ID"); rid == "" {
		t.Error("expected X-Request-ID header")
	}
}

// ── Auth via full server ──────────────────────────────────────────────────

func TestE2E_AuthFlow(t *testing.T) {
	base, cleanup := e2eServer(t)
	defer cleanup()

	client := &http.Client{Timeout: 5 * time.Second}

	// Without token → 401.
	resp, err := client.Get(base + "/me")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401 without token, got %d", resp.StatusCode)
	}

	// With valid Bearer token → 200.
	req, _ := http.NewRequest(http.MethodGet, base+"/me", nil)
	req.Header.Set("Authorization", "Bearer my-secret-token")
	resp, err = client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200 with token, got %d: %s", resp.StatusCode, body)
	}

	var result struct {
		Data struct {
			Identity string `json:"identity"`
		} `json:"data"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&result)
	if result.Data.Identity != "user:my-secret-token" {
		t.Fatalf("expected identity 'user:my-secret-token', got %q", result.Data.Identity)
	}
}

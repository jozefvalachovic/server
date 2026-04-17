// Command example demonstrates the github.com/jozefvalachovic/server library
// by running a small HTTP server with mock product data.
//
// Run via the helper script (sets HTTP_HOST / HTTP_PORT defaults automatically):
//
//	./example.sh
//
// Or set the variables yourself:
//
//	HTTP_HOST=127.0.0.1 HTTP_PORT=8080 go run ./example
//
// Open http://127.0.0.1:8080/docs for the Swagger UI.
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jozefvalachovic/server/client"
	"github.com/jozefvalachovic/server/mcp"
	"github.com/jozefvalachovic/server/middleware"
	"github.com/jozefvalachovic/server/routes"
	"github.com/jozefvalachovic/server/server"
	"github.com/jozefvalachovic/server/swagger"
	"github.com/jozefvalachovic/server/watch"
)

// httpClient is the shared resilient outbound HTTP client used across handlers.
var httpClient *client.Client

func main() {
	watch.Init() // no-op unless DEV=1; becomes the hot-reload watcher in dev

	// ── resilient outbound HTTP client ─────────────────────────────────────
	// Shared across all handlers via the package-level httpClient var.
	// Retries transient errors up to 2 times with exponential backoff;
	// opens the circuit after 5 consecutive failures.
	httpClient = client.New(client.Config{
		Timeout: 10 * time.Second,
		Retry: &client.RetryConfig{
			MaxRetries:     2,
			InitialBackoff: 200 * time.Millisecond,
			MaxBackoff:     5 * time.Second,
		},
		CircuitBreaker: &client.CircuitBreakerConfig{
			Threshold:    5,
			OpenDuration: 30 * time.Second,
		},
	})

	// ── mux & route registration ───────────────────────────────────────────
	mux := http.NewServeMux()

	cacheConfig := &routes.CacheConfig{
		DefaultTTL:      300 * time.Second,
		CleanupInterval: 30 * time.Second,
		MaxSize:         300,
		MaxMemoryMB:     128,
	}

	store, err := routes.RegisterRoutes(mux, cacheConfig,
		productRegistrar, // /products  + /products/{id}
		utilRegistrar,    // /me, /admin, /ip, /validate-email, /cache-demo
	)
	if err != nil {
		log.Fatalf("route registration: %v", err)
	}
	defer store.Stop()

	// ── structured health checks ──────────────────────────────────────────
	// LivenessHandler (/healthz) always returns 200 — it only proves the
	// process is alive and intentionally skips dep checks to avoid k8s
	// restart cascades. ReadinessHandler (/readyz) runs all registered
	// checks concurrently and returns 503 when any critical dependency is
	// down. Non-critical failures result in "degraded" (200); critical
	// failures force "down" (503).
	hc := server.NewHealthChecker("1.0.0", 5*time.Second)
	hc.RegisterLoggerHealthCheck()
	hc.RegisterCritical("cache", func(ctx context.Context) error {
		_ = ctx
		if store == nil {
			return errors.New("cache store not initialised")
		}
		return nil
	})

	mux.HandleFunc("GET /healthz", hc.LivenessHandler())
	mux.HandleFunc("GET /readyz", hc.ReadinessHandler())

	// ── swagger UI at /docs ────────────────────────────────────────────────
	routes.RegisterSwagger(mux, "/docs", swagger.Config{
		Title:       "Example API",
		Version:     "1.0.0",
		Description: "Mock API that exercises every exported package in the github.com/jozefvalachovic/server library.",
		Endpoints: []swagger.Endpoint{
			{
				Method:      swagger.GET,
				Path:        "/products",
				Summary:     "List products (paginated, cached)",
				Description: "Query ?limit=N&offset=N. Response is cached; POST invalidates.",
				Response:    (*Product)(nil),
			},
			{
				Method:      swagger.POST,
				Path:        "/products",
				Summary:     "Create product",
				Description: "Decodes the JSON body with ValidateAndDecode, appends to the in-memory list.",
				Tags:        []string{"write"},
				Request:     (*CreateProductRequest)(nil),
				Response:    (*Product)(nil),
			},
			{
				Method:   swagger.GET,
				Path:     "/products/{id}",
				Summary:  "Get single product",
				Response: (*Product)(nil),
			},
			{
				Method:      swagger.PUT,
				Path:        "/products/{id}",
				Summary:     "Update product",
				Description: "Partially updates a product. Only non-zero fields are applied. Invalidates the products cache.",
				Tags:        []string{"write"},
				Request:     (*UpdateProductRequest)(nil),
				Response:    (*Product)(nil),
			},
			{
				Method:      swagger.DELETE,
				Path:        "/products/{id}",
				Summary:     "Delete product",
				Description: "Removes the product from the in-memory list. Returns 204 No Content on success.",
				Tags:        []string{"write"},
			},
			{
				Method:      swagger.GET,
				Path:        "/me",
				Summary:     "Authenticated user",
				Description: "Returns 401 when the Authorization header is absent.",
				Tags:        []string{"auth"},
			},
			{
				Method:      swagger.GET,
				Path:        "/admin",
				Summary:     "Admin stats",
				Description: "Returns 403 when the X-Admin header is absent.",
				Tags:        []string{"auth"},
			},
			{
				Method:  swagger.GET,
				Path:    "/ip",
				Summary: "Client IP address",
				Tags:    []string{"util"},
			},
			{
				Method:      swagger.POST,
				Path:        "/validate-email",
				Summary:     "Validate & sanitize an e-mail address",
				Description: "Uses request.SanitizeEmail + request.ValidateEmail.",
				Tags:        []string{"util"},
				Request:     (*EmailRequest)(nil),
			},
			{
				Method:      swagger.GET,
				Path:        "/cache-demo",
				Summary:     "Direct cache usage demo",
				Description: "Creates a temporary CacheStore, stores a value, reads it back and returns the stats.",
				Tags:        []string{"util"},
			},
			{
				Method:      swagger.GET,
				Path:        "/fetch",
				Summary:     "Resilient outbound fetch demo",
				Description: "Uses the shared client.Client (circuit breaker + retry) to call /healthz on this server and return the result.",
				Tags:        []string{"util"},
			},
			{
				Method:  swagger.GET,
				Path:    "/healthz",
				Summary: "Liveness probe (no dep checks)",
				Tags:    []string{"ops"},
			},
			{
				Method:      swagger.GET,
				Path:        "/readyz",
				Summary:     "Readiness probe (runs all dep checks)",
				Description: "Returns 503 when any critical dependency check fails. Non-critical failures return 200 with degraded status.",
				Tags:        []string{"ops"},
			},
			{
				Method:      swagger.GET,
				Path:        "/metrics/",
				Summary:     "Admin metrics UI",
				Description: "Per-route request counts, latency, and error rates. Protected by ADMIN_NAME / ADMIN_SECRET / ADMIN_SIGNING_KEY session cookie.",
				Tags:        []string{"admin"},
			},
			{
				Method:      swagger.GET,
				Path:        "/cache/",
				Summary:     "Admin cache UI",
				Description: "Cache statistics and live data explorer with delete/flush actions. Protected by ADMIN_NAME / ADMIN_SECRET / ADMIN_SIGNING_KEY session cookie.",
				Tags:        []string{"admin"},
			},
		},
	})

	// ── MCP tool server at /mcp ───────────────────────────────────────────
	routes.RegisterMCP(mux, "/mcp", mcp.Config{
		Name:           "example-server",
		Version:        "1.0.0",
		Tools:          mcpTools(),
		AllowedOrigins: []string{"http://localhost:3000", "https://example.com"},
	})

	// ── HTTP server ────────────────────────────────────────────────────────
	// Build admin config separately so we don't store a typed-nil interface
	// (which would compare != nil and panic inside the admin package).
	adminCfg := &server.AdminConfig{
		AppName:    "example-server",
		AppVersion: "1.0.0",
		Store:      store,
	}
	httpCfg := server.HTTPServerConfig{
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 30 * time.Second,

		// TLS with auto cert rotation (uncomment to enable):
		// TLSConfig:          server.DefaultTLSConfig(),
		// AutoCertReload:     true,                // polls cert/key files for changes
		// CertReloadInterval: 30 * time.Second,    // default; set 0 for 30s

		// OTelBridge: duplicate log output to an OTel-compatible JSON handler
		// on stderr so a sidecar collector (e.g. Alloy, OTel Collector) can
		// ingest structured logs with service.name/version and severity mapping.
		// Remove or set nil when no collector is deployed.
		OTelBridge: &server.OTelBridgeConfig{
			ServiceName:    "example-server",
			ServiceVersion: "1.0.0",
		},

		// CORS: allow the local dev front-end and production origin.
		// Remove or set Disabled:true for fully public / same-origin APIs.
		CORS: &server.CORSConfig{
			AllowedOrigins:   []string{"http://localhost:3000", "https://example.com"},
			AllowedHeaders:   []string{"Content-Type", "Authorization", "X-Request-ID", "X-API-Key"},
			AllowCredentials: true,
		},

		// RequestID: default config — X-Request-ID header, 16-byte random hex.
		RequestID: &server.RequestIDConfig{},

		// Timeout: 30 s per-handler deadline (writes 504 on breach).
		Timeout: &server.TimeoutConfig{Timeout: 30 * time.Second},

		// Compress: gzip for JSON / text responses when client accepts it.
		Compress: &server.CompressConfig{Enabled: true},

		// RateLimit: 50 req/s sustained, burst of 100 per client IP.
		RateLimitConfig: &server.HTTPRateLimitConfig{
			RequestsPerSecond: 50,
			Burst:             100,
		},

		// AuditConfig: emit structured audit log for every state-changing
		// request. Health probe paths (/healthz, /readyz) are always
		// skipped by the server; add app-specific paths here.
		AuditConfig: &server.HTTPAuditConfig{
			Enabled:   true,
			Methods:   []string{"POST", "PUT", "PATCH", "DELETE"},
			SkipPaths: []string{"/.well-known/appspecific/com.chrome.devtools.json"},
		},

		// Admin: metrics + cache UI protected by ADMIN_NAME / ADMIN_SECRET / ADMIN_SIGNING_KEY.
		// Routes are registered automatically; set both env vars to enable.
		Admin: adminCfg,

		// Middlewares: extra app-level middleware applied after the built-in
		// stack. Auth is shown here as a per-route wrapper in utilRegistrar
		// instead of globally so public endpoints remain open.
		Middlewares: []server.HTTPMiddleware{
			middleware.IPFilter(middleware.IPFilterConfig{
				// Example: uncomment to restrict to loopback + RFC-1918.
				// Allowlist: []string{"127.0.0.0/8", "10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16"},
			}),
		},
	}
	if err := httpCfg.Validate(); err != nil {
		log.Fatalf("invalid HTTP server config: %v", err)
	}
	srv, err := server.NewHTTPServer(mux, "example-server", "1.0.0", httpCfg)
	if err != nil {
		log.Fatalf("http server: %v", err)
	}

	// Register signal handler before starting so no signal is missed.
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	host, port := os.Getenv("HTTP_HOST"), os.Getenv("HTTP_PORT")
	fmt.Printf("\nExample server started\n")
	fmt.Printf("  GET    http://%s:%s/products       paginated list (cached)\n", host, port)
	fmt.Printf("  POST   http://%s:%s/products       create product\n", host, port)
	fmt.Printf("  GET    http://%s:%s/products/1     single product\n", host, port)
	fmt.Printf("  PUT    http://%s:%s/products/1     update product\n", host, port)
	fmt.Printf("  DELETE http://%s:%s/products/1     delete product\n", host, port)
	fmt.Printf("  GET    http://%s:%s/me             auth demo (Bearer <token> required)\n", host, port)
	fmt.Printf("  GET    http://%s:%s/admin          403 demo (add -H 'X-Admin: 1')\n", host, port)
	fmt.Printf("  GET    http://%s:%s/ip             client IP via request.GetIPAddress\n", host, port)
	fmt.Printf("  POST   http://%s:%s/validate-email email validation demo\n", host, port)
	fmt.Printf("  GET    http://%s:%s/cache-demo     direct CacheStore demo\n", host, port)
	fmt.Printf("  GET    http://%s:%s/fetch          resilient client + circuit breaker demo\n", host, port)
	fmt.Printf("  GET    http://%s:%s/healthz        liveness probe\n", host, port)
	fmt.Printf("  GET    http://%s:%s/readyz         readiness probe (dep checks)\n", host, port)
	fmt.Printf("  GET    http://%s:%s/docs           swagger UI\n", host, port)
	fmt.Printf("  POST   http://%s:%s/mcp            MCP tool server (JSON-RPC 2.0)\n", host, port)
	fmt.Printf("  GET    http://%s:%s/metrics/       admin metrics UI (ADMIN_NAME + ADMIN_SECRET + ADMIN_SIGNING_KEY)\n", host, port)
	fmt.Printf("  GET    http://%s:%s/cache/         admin cache UI  (ADMIN_NAME + ADMIN_SECRET + ADMIN_SIGNING_KEY)\n\n", host, port)

	if err := srv.Start(); err != nil {
		log.Fatalf("server exited: %v", err)
	}

	// Warm up the products cache and seed the metrics collector with a handful
	// of requests so the admin dashboards show real data from the first visit.
	go func() {
		base := fmt.Sprintf("http://%s:%s", host, port)
		// Give the listener a moment to be ready.
		time.Sleep(150 * time.Millisecond)
		paths := []string{
			"/products",
			"/products?limit=2&offset=0",
			"/products?limit=1&offset=1",
			"/products/1",
			"/products/2",
			"/healthz",
			"/readyz",
		}
		for _, p := range paths {
			resp, err := http.Get(base + p) //nolint:gosec
			if err == nil {
				_ = resp.Body.Close()
			}
		}
	}()

	// Block main until SIGINT / SIGTERM, then shut down gracefully.
	<-quit
	log.Println("signal received – delaying shutdown 5s for Kubernetes LB propagation …")
	time.Sleep(5 * time.Second)

	log.Println("shutting down …")
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer shutdownCancel()
	if err := srv.GracefulShutdown(shutdownCtx); err != nil {
		log.Printf("shutdown error: %v", err)
	}
}

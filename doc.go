// Package server provides reusable building blocks for Go HTTP and TCP servers.
//
// Import each sub-package directly by its functional area:
//
//	import "github.com/jozefvalachovic/server/server"     // HTTP, TCP and metrics servers
//	import "github.com/jozefvalachovic/server/routes"     // route registration and RouteHandler
//	import "github.com/jozefvalachovic/server/response"   // JSON response writers and models
//	import "github.com/jozefvalachovic/server/middleware"  // HTTPCache for CachedRouteHandler; per-route middleware
//	import "github.com/jozefvalachovic/server/client"     // optional — Client, ClientConfig etc. re-exported from server
//	import "github.com/jozefvalachovic/server/request"    // GetIPAddress, ValidateEmail, SanitizeEmail …
//	import "github.com/jozefvalachovic/server/swagger"    // embedded Swagger UI
//	import "github.com/jozefvalachovic/server/mcp"        // Model Context Protocol tool server
//	import "github.com/jozefvalachovic/server/watch"      // hot-reload (no-op unless DEV=1)
//
// Several types are re-exported so callers need fewer imports:
//
// From the server package (only "server" import needed):
//
//	server.HTTPRateLimitConfig        // used with server.NewHTTPServer
//	server.TCPRateLimitConfig         // used with server.NewTCPServer
//	server.Client                     // resilient HTTP client
//	server.ClientConfig               // client configuration
//	server.ClientRetryConfig          // retry policy
//	server.ClientCircuitBreakerConfig // circuit-breaker policy
//	server.NewClient                  // constructor (wraps client.New)
//
// From the routes package (only "routes" import needed):
//
//	routes.CacheConfig // cache configuration, re-exported from cache.CacheConfig
//
// Typical usage:
//
//	mux := http.NewServeMux()
//
//	store, err := routes.RegisterRoutes(mux, nil,
//	    func(mux *http.ServeMux) {
//	        routes.RegisterRouteList(mux, []routes.Route{
//	            {Method: http.MethodGet,  Path: "/organisations", Handler: listOrgs},
//	            {Method: http.MethodPost, Path: "/organisations", Handler: createOrg},
//	        })
//	    },
//	)
//
//	srv, err := server.NewHTTPServer(mux, "my-app", "1.0.0", server.HTTPServerConfig{})
//	srv.Start()
//
// # Development hot-reload
//
// Import the watch sub-package and call watch.Init() as the very first
// statement in main(). It is a complete no-op unless DEV=1 is set.
//
//	import "github.com/jozefvalachovic/server/watch"
//
//	func main() {
//	    watch.Init() // hot-reloads on .go file changes when DEV=1
//	    // ... normal server setup
//	}
//
// Run with hot-reload:
//
//	DEV=1 go run .          # watches the package directory automatically
//	./example.sh            # convenience wrapper (sets HTTP_HOST, HTTP_PORT, DEV=1)
//
// Watch additional directories:
//
//	watch.Init(watch.Config{ExtraDirs: []string{"../shared"}})
//
// # Middleware ordering
//
// NewHTTPServer assembles a fixed middleware stack. Built-in middleware always
// executes in this order (outermost first):
//
//  1. Logger         — access logging + optional audit (true outermost layer)
//  2. Recovery       — panic → 500 + stack trace
//  3. Security       — security response headers
//  4. IPFilter       — IP allowlist / blocklist         (if configured)
//  5. RequestSize    — max body size enforcement
//  6. RateLimit      — per-client token-bucket limiting (if configured)
//  7. CORS           — cross-origin headers             (if configured)
//  8. RequestID      — inject / propagate request IDs
//  9. TraceContext   — W3C traceparent / tracestate
//  10. Timeout       — per-request handler deadline
//  11. Compress      — gzip content encoding            (if configured)
//  12. Admin         — admin metrics collector           (if configured)
//  13. Custom        — HTTPServerConfig.Middlewares (index 0 executes first)
//     → Handler mux
//
// Custom middleware passed via Middlewares runs after all built-in layers and
// before the handler. Index 0 executes first.
//
// # HTTPCache placement
//
// The HTTPCache middleware is NOT part of the built-in stack — it is applied
// per-route via CachedRouteHandler. When combined with Compress, HTTPCache must
// execute BEFORE Compress so it stores uncompressed bodies and Compress re-encodes
// them per request. This keeps one cache entry per Accept-Encoding variant and
// avoids storing pre-gzipped bodies that would be served to clients that do not
// accept gzip. The middleware rejects any response already carrying a
// Content-Encoding header to enforce this ordering at runtime.
//
// HTTPCache intentionally does NOT honour Cache-Control: no-cache, no-store or
// private on responses; eligibility is decided by status code, method and the
// caller's key-prefix. Callers who need RFC 9111 semantics should wrap their
// handlers accordingly.
//
// # Graceful shutdown
//
// Bind the server lifetime to OS signals so Kubernetes can drain the pod cleanly
// on rollout:
//
//	srv, _ := server.NewHTTPServer(mux, "my-app", "1.0.0", server.HTTPServerConfig{})
//	if err := srv.Start(); err != nil {
//	    log.Fatal(err)
//	}
//
//	sig := make(chan os.Signal, 1)
//	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
//	<-sig
//
//	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
//	defer cancel()
//	if err := srv.GracefulShutdown(ctx); err != nil {
//	    srv.ForceShutdown() // deadline exceeded — close active connections
//	}
//
// Alternatively, StartWithContext wires cancellation for you:
//
//	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
//	defer cancel()
//	_ = srv.StartWithContext(ctx) // GracefulShutdown runs when ctx is cancelled
//
// # Health and readiness
//
// Register /health (liveness) and /readiness (dependencies) so Kubernetes can
// distinguish process-is-alive from ready-to-serve-traffic. Mark dependencies
// whose failure should drain the pod with RegisterCritical:
//
//	hc := server.NewHealthChecker("1.0.0", 5*time.Second)
//	hc.RegisterCritical("postgres", func(ctx context.Context) error {
//	    return db.PingContext(ctx)
//	})
//	hc.Register("redis", func(ctx context.Context) error {
//	    return rdb.Ping(ctx).Err()
//	})
//	mux.HandleFunc("GET /health",    hc.LivenessHandler())
//	mux.HandleFunc("GET /readiness", hc.ReadinessHandler())
//
// Liveness returns 200 unconditionally (process-is-alive). Readiness returns
// 503 when any critical check fails or when every registered check is down;
// a degraded state returns 200 so the pod remains in rotation.
package server

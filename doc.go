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
//	    func(mux *http.ServeMux, store *cache.CacheStore) {
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
package server

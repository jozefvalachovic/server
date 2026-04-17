package routes

import (
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strings"
	"sync"

	"github.com/jozefvalachovic/server/cache"
	"github.com/jozefvalachovic/server/mcp"
	"github.com/jozefvalachovic/server/middleware"
	"github.com/jozefvalachovic/server/response"
	"github.com/jozefvalachovic/server/swagger"

	"github.com/jozefvalachovic/logger/v4"
)

// CacheConfig is a re-export of cache.CacheConfig so callers registering
// routes with caching only need to import "routes", not "cache".
type CacheConfig = cache.CacheConfig

// activeStore is set by RegisterRoutes and used by CachedRouteHandler so
// registrars never need to receive or forward the *cache.CacheStore pointer.
// Protected by activeStoreMu against concurrent access (e.g. parallel tests).
//
// This is a process-scoped singleton by design. It works correctly for the
// standard Kubernetes deployment model (one mux per pod) and keeps the route
// registration API simple: registrars receive only an *http.ServeMux and call
// CachedRouteHandler without threading a store through every layer.
//
// Limitations:
//   - Only one active cache store exists at a time. Calling RegisterRoutes a
//     second time replaces the store; earlier registrars that captured the old
//     store via CachedRouteHandler continue to reference it.
//   - Two independent mux trees with different cache stores in the same process
//     (e.g. parallel test cases) are not supported. In tests, run such cases
//     sequentially or pass the store explicitly via middleware.HTTPCacheConfig.
//   - CachedRouteHandler must only be called synchronously inside a
//     RegisterRouteRegistrar callback (while RegisterRoutes holds the lock).
//     Calling it from a goroutine spawned by a registrar may read a stale store.
var (
	activeStore   *cache.CacheStore
	activeStoreMu sync.RWMutex
)

// RegisterRouteRegistrar is a function that registers routes onto the mux.
// The cache store is managed internally by the routes package; use
// CachedRouteHandler inside a registrar to add caching without handling stores.
type RegisterRouteRegistrar func(mux *http.ServeMux)

// RegisterRoutes registers route groups to the main router.
// When cacheConfig is non-nil a dedicated CacheStore is created and managed
// internally; call store.Stop() on the returned value during graceful shutdown.
// Returns nil when cacheConfig is nil.
// Returns an error if cacheConfig is non-nil but invalid (e.g. zero DefaultTTL).
func RegisterRoutes(mux *http.ServeMux, cacheConfig *cache.CacheConfig,
	registrars ...RegisterRouteRegistrar,
) (*cache.CacheStore, error) {
	activeStoreMu.Lock()
	defer activeStoreMu.Unlock()

	activeStore = nil
	if cacheConfig != nil {
		s, err := cache.NewCacheStore(*cacheConfig)
		if err != nil {
			return nil, err
		}
		activeStore = s
	}

	// Register all provided route registrars while the lock is held so that
	// CachedRouteHandler calls inside registrars see a consistent store.
	for _, register := range registrars {
		register(mux)
	}

	return activeStore, nil
}

// CachedRouteHandler wraps RouteHandler(routes) with the HTTPCache middleware.
// The store configured in RegisterRoutes is injected automatically — no need
// to pass *cache.CacheStore through your registrar.
func CachedRouteHandler(routes Routes, cfg middleware.HTTPCacheConfig) http.HandlerFunc {
	// The caller is expected to invoke CachedRouteHandler inside a registrar
	// passed to RegisterRoutes, which already holds activeStoreMu.
	cfg.Store = activeStore
	return middleware.HTTPCache(cfg)(RouteHandler(routes)).ServeHTTP
}

// RegisterReadinessEndpoint registers a readiness probe at /readiness.
// The readyFunc should return true if the app is ready, false otherwise.
func RegisterReadinessEndpoint(mux *http.ServeMux, readyFunc func() bool) {
	mux.HandleFunc("/readiness", RouteHandler(Routes{
		http.MethodGet: func(w http.ResponseWriter, r *http.Request) {
			if readyFunc != nil && readyFunc() {
				w.WriteHeader(http.StatusOK)
				if _, err := w.Write([]byte("READY")); err != nil {
					logger.LogWarn("Failed to write readiness response", "error", err.Error())
				}
			} else {
				w.WriteHeader(http.StatusServiceUnavailable)
				if _, err := w.Write([]byte("NOT READY")); err != nil {
					logger.LogWarn("Failed to write readiness response", "error", err.Error())
				}
			}
		},
	}))
}

// RegisterSwagger mounts the swagger UI at the given path prefix (e.g. "/docs").
// Trailing-slash redirect and StripPrefix plumbing are handled internally.
func RegisterSwagger(mux *http.ServeMux, path string, cfg swagger.Config) {
	path = strings.TrimRight(path, "/")
	mux.Handle(path+"/", http.StripPrefix(path, swagger.Handler(cfg)))
	// Redirect bare path (e.g. /docs) → /docs/ so the relative asset URLs resolve.
	mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, path+"/", http.StatusMovedPermanently)
	})
}

// RegisterMCP mounts an MCP (Model Context Protocol) tool server at the given
// path (e.g. "/mcp"). Agents discover and call tools via JSON-RPC 2.0 POST
// requests to that endpoint following the MCP 2024-11-05 specification.
func RegisterMCP(mux *http.ServeMux, path string, cfg mcp.Config) {
	path = strings.TrimRight(path, "/")
	mux.Handle(path, mcp.Handler(cfg))
}

type Routes map[string]http.HandlerFunc

// RouteHandler provides automatic 405 Method Not Allowed responses.
// The Allow header value is computed once at construction time from the
// provided routes map so that 405 paths incur no per-request allocations.
func RouteHandler(routes Routes) http.HandlerFunc {
	// Pre-build sorted Allow string once (RFC 7231 §6.5.5).
	allow := strings.Join(slices.Sorted(maps.Keys(routes)), ", ")

	return func(w http.ResponseWriter, r *http.Request) {
		if handler, exists := routes[r.Method]; exists {
			handler(w, r)
		} else {
			w.Header().Set("Allow", allow)
			response.APIErrorWriter(w, response.APIError[any]{
				Code:    http.StatusMethodNotAllowed,
				Error:   response.ErrMethodNotAllowed,
				Message: "Method not allowed for this endpoint",
			})
		}
	}
}

type Route struct {
	Method      string
	Path        string
	Handler     http.HandlerFunc
	Middlewares []func(http.Handler) http.Handler
}

// RegisterRoute registers a single route.
// It delegates to RegisterRouteList so that wrong-method requests always
// receive a proper JSON 405 with an Allow header via RouteHandler.
func RegisterRoute(mux *http.ServeMux, route Route) {
	RegisterRouteList(mux, []Route{route})
}

// RegisterRouteList registers multiple routes, grouping those that share a
// path into a single RouteHandler for consistent 405 Method Not Allowed handling.
// Per-route middleware is applied to each handler before grouping.
//
// Panics when the same (path, method) pair is registered more than once. This
// is a programming error — silently overwriting the earlier handler would make
// subtle routing bugs nearly impossible to diagnose in production. Fail fast
// at startup instead.
func RegisterRouteList(mux *http.ServeMux, routes []Route) {
	grouped := make(map[string]Routes)
	for _, route := range routes {
		handler := http.Handler(route.Handler)
		// Apply middlewares: index 0 is outermost (first to execute per request).
		for i := len(route.Middlewares) - 1; i >= 0; i-- {
			handler = route.Middlewares[i](handler)
		}
		if grouped[route.Path] == nil {
			grouped[route.Path] = make(Routes)
		}
		if _, exists := grouped[route.Path][route.Method]; exists {
			panic(fmt.Sprintf("routes: duplicate handler for %s %s", route.Method, route.Path))
		}
		grouped[route.Path][route.Method] = handler.ServeHTTP
	}
	for path, r := range grouped {
		mux.Handle(path, RouteHandler(r))
	}
}

// RegisterGroup registers routes that share a common set of middleware.
// This avoids repeating the same middleware list on every Route and makes
// intent explicit when a block of endpoints share cross-cutting behaviour
// (e.g. authentication, auditing).
//
// Example:
//
//	routes.RegisterGroup(mux, []func(http.Handler) http.Handler{authMiddleware}, []routes.Route{
//	    {Method: http.MethodGet, Path: "/users", Handler: listUsers},
//	    {Method: http.MethodPost, Path: "/users", Handler: createUser},
//	})
func RegisterGroup(mux *http.ServeMux, groupMiddlewares []func(http.Handler) http.Handler, routes []Route) {
	for i := range routes {
		// Prepend group middleware before any per-route middleware so that the
		// group layer is always outermost.
		routes[i].Middlewares = append(groupMiddlewares, routes[i].Middlewares...)
	}
	RegisterRouteList(mux, routes)
}

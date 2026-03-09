package routes

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jozefvalachovic/server/cache"
	"github.com/jozefvalachovic/server/middleware"
	"github.com/jozefvalachovic/server/swagger"
)

// minimalSwaggerCfg returns a zero-overhead Config suitable for use in tests.
func minimalSwaggerCfg() swagger.Config {
	return swagger.Config{Title: "Test API"}
}

// ── RouteHandler ──────────────────────────────────────────────────────────────

func TestRouteHandler_DispatchesCorrectMethod(t *testing.T) {
	called := false
	h := RouteHandler(Routes{
		http.MethodGet: func(w http.ResponseWriter, r *http.Request) {
			called = true
			w.WriteHeader(http.StatusOK)
		},
	})

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	if !called {
		t.Fatal("GET handler was not called")
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
}

func TestRouteHandler_Returns405ForUnknownMethod(t *testing.T) {
	h := RouteHandler(Routes{
		http.MethodGet: func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		},
	})

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/", nil))

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("want 405, got %d", rec.Code)
	}
}

func TestRouteHandler_AllowHeaderOn405(t *testing.T) {
	h := RouteHandler(Routes{
		http.MethodGet:  func(w http.ResponseWriter, r *http.Request) {},
		http.MethodPost: func(w http.ResponseWriter, r *http.Request) {},
	})

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/", nil))

	allow := rec.Header().Get("Allow")
	if allow == "" {
		t.Fatal("Allow header must be set on 405 responses")
	}
	if !strings.Contains(allow, http.MethodGet) {
		t.Fatalf("Allow header %q missing GET", allow)
	}
	if !strings.Contains(allow, http.MethodPost) {
		t.Fatalf("Allow header %q missing POST", allow)
	}
}

func TestRouteHandler_MultipleMethodsDispatched(t *testing.T) {
	getOK, postOK := false, false
	h := RouteHandler(Routes{
		http.MethodGet: func(w http.ResponseWriter, r *http.Request) {
			getOK = true
			w.WriteHeader(http.StatusOK)
		},
		http.MethodPost: func(w http.ResponseWriter, r *http.Request) {
			postOK = true
			w.WriteHeader(http.StatusCreated)
		},
	})

	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/", nil))

	if !getOK {
		t.Fatal("GET handler was not called")
	}
	if !postOK {
		t.Fatal("POST handler was not called")
	}
}

// ── RegisterReadinessEndpoint ─────────────────────────────────────────────────

func TestRegisterReadinessEndpoint_Ready(t *testing.T) {
	mux := http.NewServeMux()
	RegisterReadinessEndpoint(mux, func() bool { return true })

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readiness", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	if rec.Body.String() != "READY" {
		t.Fatalf("want body READY, got %q", rec.Body.String())
	}
}

func TestRegisterReadinessEndpoint_NotReady(t *testing.T) {
	mux := http.NewServeMux()
	RegisterReadinessEndpoint(mux, func() bool { return false })

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readiness", nil))

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("want 503, got %d", rec.Code)
	}
	if rec.Body.String() != "NOT READY" {
		t.Fatalf("want body NOT READY, got %q", rec.Body.String())
	}
}

func TestRegisterReadinessEndpoint_NilFunc_NotReady(t *testing.T) {
	mux := http.NewServeMux()
	RegisterReadinessEndpoint(mux, nil)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readiness", nil))

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("nil readyFunc: want 503, got %d", rec.Code)
	}
}

// ── RegisterRoute & RegisterRouteList ─────────────────────────────────────────

func TestRegisterRoute_BasicDispatch(t *testing.T) {
	mux := http.NewServeMux()
	called := false

	RegisterRoute(mux, Route{
		Method:  http.MethodGet,
		Path:    "/items",
		Handler: func(w http.ResponseWriter, r *http.Request) { called = true; w.WriteHeader(http.StatusOK) },
	})

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/items", nil))

	if !called {
		t.Fatal("handler was not called")
	}
}

func TestRegisterRoute_UsesRouteHandler405(t *testing.T) {
	mux := http.NewServeMux()

	RegisterRoute(mux, Route{
		Method:  http.MethodGet,
		Path:    "/only-get",
		Handler: func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) },
	})

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/only-get", nil))

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("want 405, got %d", rec.Code)
	}
}

func TestRegisterRouteList_Groups405(t *testing.T) {
	mux := http.NewServeMux()

	RegisterRouteList(mux, []Route{
		{Method: http.MethodGet, Path: "/res", Handler: func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }},
		{Method: http.MethodPost, Path: "/res", Handler: func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusCreated) }},
	})

	// DELETE → 405 with both GET and POST listed in Allow header.
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/res", nil))

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("want 405, got %d", rec.Code)
	}
	allow := rec.Header().Get("Allow")
	if !strings.Contains(allow, http.MethodGet) || !strings.Contains(allow, http.MethodPost) {
		t.Fatalf("Allow header %q missing expected methods", allow)
	}
}

func TestRegisterRouteList_MiddlewareOrder(t *testing.T) {
	// Index 0 is outermost (first to execute). Verify outer → inner → handler order.
	var order []string

	makeMiddleware := func(name string) func(http.Handler) http.Handler {
		return func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				order = append(order, name)
				next.ServeHTTP(w, r)
			})
		}
	}

	mux := http.NewServeMux()
	RegisterRouteList(mux, []Route{{
		Method:  http.MethodGet,
		Path:    "/chain",
		Handler: func(w http.ResponseWriter, r *http.Request) { order = append(order, "handler") },
		Middlewares: []func(http.Handler) http.Handler{
			makeMiddleware("outer"),
			makeMiddleware("inner"),
		},
	}})

	mux.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/chain", nil))

	if len(order) != 3 {
		t.Fatalf("expected 3 calls, got %v", order)
	}
	if order[0] != "outer" || order[1] != "inner" || order[2] != "handler" {
		t.Fatalf("unexpected call order: %v", order)
	}
}

// ── RegisterSwagger ───────────────────────────────────────────────────────────

func TestRegisterSwagger_BarePathRedirects(t *testing.T) {
	mux := http.NewServeMux()
	RegisterSwagger(mux, "/docs", minimalSwaggerCfg())

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/docs", nil))

	if rec.Code != http.StatusMovedPermanently {
		t.Fatalf("want 301, got %d", rec.Code)
	}
	if got := rec.Header().Get("Location"); got != "/docs/" {
		t.Fatalf("want Location: /docs/, got %q", got)
	}
}

func TestRegisterSwagger_TrailingSlashTrimmed(t *testing.T) {
	mux := http.NewServeMux()
	// RegisterSwagger should trim the trailing slash before mounting.
	RegisterSwagger(mux, "/docs/", minimalSwaggerCfg())

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/docs", nil))

	if rec.Code != http.StatusMovedPermanently {
		t.Fatalf("want 301 after trimming trailing slash, got %d", rec.Code)
	}
}

func TestRegisterSwagger_UIServes200(t *testing.T) {
	mux := http.NewServeMux()
	RegisterSwagger(mux, "/docs", minimalSwaggerCfg())

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/docs/", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("want 200 for /docs/, got %d", rec.Code)
	}
}

// ── RegisterRoutes ────────────────────────────────────────────────────────────

func TestRegisterRoutes_HealthEndpoint(t *testing.T) {
	mux := http.NewServeMux()
	store, err := RegisterRoutes(mux, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if store != nil {
		t.Fatal("store should be nil when cacheConfig is nil")
	}

	// /health is intentionally NOT registered by RegisterRoutes; the application
	// is expected to wire HealthChecker (or a custom handler) separately so that
	// real dependency checks are performed rather than an always-200 stub.
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))

	if rec.Code != http.StatusNotFound {
		t.Fatalf("want 404 for /health (route not auto-registered), got %d", rec.Code)
	}
}

func TestRegisterRoutes_CallsRegistrar(t *testing.T) {
	mux := http.NewServeMux()
	registered := false

	_, err := RegisterRoutes(mux, nil, func(m *http.ServeMux) {
		registered = true
		m.HandleFunc("/custom", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusTeapot)
		})
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !registered {
		t.Fatal("registrar was not called")
	}

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/custom", nil))
	if rec.Code != http.StatusTeapot {
		t.Fatalf("want 418, got %d", rec.Code)
	}
}

func TestRegisterRoutes_WithValidCacheConfig_StoreIsNonNil(t *testing.T) {
	mux := http.NewServeMux()
	cfg := &cache.CacheConfig{
		DefaultTTL:      10 * time.Second,
		CleanupInterval: 5 * time.Second,
		MaxSize:         10,
		MaxMemoryMB:     16,
	}

	store, err := RegisterRoutes(mux, cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if store == nil {
		t.Fatal("expected non-nil store when cacheConfig is valid")
	}
	store.Stop()
}

func TestRegisterRoutes_WithInvalidCacheConfig_ReturnsError(t *testing.T) {
	mux := http.NewServeMux()
	// DefaultTTL == 0 is invalid according to cache.NewCacheStore.
	cfg := &cache.CacheConfig{
		DefaultTTL:      0,
		CleanupInterval: 5 * time.Second,
		MaxSize:         10,
		MaxMemoryMB:     16,
	}

	store, err := RegisterRoutes(mux, cfg)
	if err == nil {
		if store != nil {
			store.Stop()
		}
		t.Fatal("expected an error for invalid cache config (zero DefaultTTL), got nil")
	}
}

// ── CachedRouteHandler ────────────────────────────────────────────────────────

// newRoutesTestStore returns a *cache.CacheStore for use in routes tests.
func newRoutesTestStore(t *testing.T) *cache.CacheStore {
	t.Helper()
	s, err := cache.NewCacheStore(cache.CacheConfig{
		DefaultTTL:      10 * time.Second,
		CleanupInterval: 5 * time.Second,
		MaxSize:         50,
		MaxMemoryMB:     32,
	})
	if err != nil {
		t.Fatalf("failed to create test store: %v", err)
	}
	t.Cleanup(s.Stop)
	return s
}

func TestCachedRouteHandler_DispatchesGETMethod(t *testing.T) {
	activeStore = newRoutesTestStore(t)
	called := false

	h := CachedRouteHandler(Routes{
		http.MethodGet: func(w http.ResponseWriter, r *http.Request) {
			called = true
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("ok"))
		},
	}, middleware.HTTPCacheConfig{
		KeyPrefix: func(r *http.Request) string { return "routes_test_dispatch" },
	})

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	if !called {
		t.Fatal("GET handler was not called through CachedRouteHandler")
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
}

func TestCachedRouteHandler_CachesGETResponse(t *testing.T) {
	activeStore = newRoutesTestStore(t)
	handlerCalls := 0

	h := CachedRouteHandler(Routes{
		http.MethodGet: func(w http.ResponseWriter, r *http.Request) {
			handlerCalls++
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("cached-body"))
		},
	}, middleware.HTTPCacheConfig{
		KeyPrefix: func(r *http.Request) string { return "routes_test_cache" },
	})

	// First request — MISS, handler called.
	rec1 := httptest.NewRecorder()
	h.ServeHTTP(rec1, httptest.NewRequest(http.MethodGet, "/items", nil))
	if rec1.Header().Get("X-Cache") != "MISS" {
		t.Fatalf("first request: want MISS, got %q", rec1.Header().Get("X-Cache"))
	}

	// Second request — HIT, handler must NOT be called again.
	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, httptest.NewRequest(http.MethodGet, "/items", nil))
	if rec2.Header().Get("X-Cache") != "HIT" {
		t.Fatalf("second request: want HIT, got %q", rec2.Header().Get("X-Cache"))
	}
	if handlerCalls != 1 {
		t.Fatalf("handler must be called exactly once; got %d calls", handlerCalls)
	}
}

func TestCachedRouteHandler_Returns405ForUnregisteredMethod(t *testing.T) {
	activeStore = newRoutesTestStore(t)

	h := CachedRouteHandler(Routes{
		http.MethodGet: func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		},
	}, middleware.HTTPCacheConfig{
		KeyPrefix: func(r *http.Request) string { return "routes_test_405" },
	})

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/items", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("want 405, got %d", rec.Code)
	}
}

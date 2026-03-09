package routes

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jozefvalachovic/server/cache"
	"github.com/jozefvalachovic/server/middleware"
)

// ── shared helpers ────────────────────────────────────────────────────────────

var benchOKHandler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"ok":true}`))
})

func newBenchStore(b *testing.B) *cache.CacheStore {
	b.Helper()
	s, err := cache.NewCacheStore(cache.CacheConfig{
		MaxSize:         1000,
		DefaultTTL:      30 * time.Second,
		CleanupInterval: 15 * time.Second,
		MaxMemoryMB:     64,
	})
	if err != nil {
		b.Fatalf("store: %v", err)
	}
	b.Cleanup(s.Stop)
	return s
}

// ── RouteHandler ──────────────────────────────────────────────────────────────

func BenchmarkRouteHandler_Hit(b *testing.B) {
	h := RouteHandler(Routes{
		http.MethodGet: benchOKHandler.ServeHTTP,
	})
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	b.ReportAllocs()
	for b.Loop() {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
	}
}

func BenchmarkRouteHandler_Miss405(b *testing.B) {
	h := RouteHandler(Routes{
		http.MethodGet: benchOKHandler.ServeHTTP,
	})
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	b.ReportAllocs()
	for b.Loop() {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
	}
}

func BenchmarkRouteHandler_MultiMethod(b *testing.B) {
	h := RouteHandler(Routes{
		http.MethodGet:    benchOKHandler.ServeHTTP,
		http.MethodPost:   benchOKHandler.ServeHTTP,
		http.MethodPut:    benchOKHandler.ServeHTTP,
		http.MethodDelete: benchOKHandler.ServeHTTP,
	})
	req := httptest.NewRequest(http.MethodPut, "/", nil)
	b.ReportAllocs()
	for b.Loop() {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
	}
}

// ── RegisterRouteList ─────────────────────────────────────────────────────────

func BenchmarkRegisterRouteList(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		mux := http.NewServeMux()
		RegisterRouteList(mux, []Route{
			{Method: http.MethodGet, Path: "/a", Handler: benchOKHandler.ServeHTTP},
			{Method: http.MethodPost, Path: "/a", Handler: benchOKHandler.ServeHTTP},
			{Method: http.MethodGet, Path: "/b", Handler: benchOKHandler.ServeHTTP},
			{Method: http.MethodGet, Path: "/c", Handler: benchOKHandler.ServeHTTP},
		})
	}
}

// ── CachedRouteHandler ────────────────────────────────────────────────────────

func BenchmarkCachedRouteHandler_Hit(b *testing.B) {
	store := newBenchStore(b)
	h := CachedRouteHandler(
		Routes{http.MethodGet: benchOKHandler.ServeHTTP},
		middleware.HTTPCacheConfig{
			Store:     store,
			KeyPrefix: func(_ *http.Request) string { return "bench-routes-hit" },
		},
	)

	// Warm cache.
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	b.ReportAllocs()
	for b.Loop() {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
	}
}

func BenchmarkCachedRouteHandler_Miss(b *testing.B) {
	store := newBenchStore(b)
	h := CachedRouteHandler(
		Routes{http.MethodGet: benchOKHandler.ServeHTTP},
		middleware.HTTPCacheConfig{
			Store:     store,
			KeyPrefix: func(r *http.Request) string { return r.URL.Path },
		},
	)
	b.ReportAllocs()
	for b.Loop() {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/bench", nil)
		h.ServeHTTP(rec, req)
	}
}

// ── RegisterSwagger (construction cost) ──────────────────────────────────────

func BenchmarkRegisterSwagger(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		mux := http.NewServeMux()
		RegisterSwagger(mux, "/docs", minimalSwaggerCfg())
	}
}

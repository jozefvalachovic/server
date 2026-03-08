package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jozefvalachovic/server/cache"
)

// ── shared helpers ────────────────────────────────────────────────────────────

var okHandler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"ok":true}`))
})

func newBenchStore(b *testing.B) *cache.CacheStore {
	b.Helper()
	s, err := cache.NewCacheStore(cache.CacheConfig{
		MaxSize:         1000,
		DefaultTTL:      30 * time.Second,
		CleanupInterval: 15 * time.Second,
	})
	if err != nil {
		b.Fatalf("store: %v", err)
	}
	b.Cleanup(s.Stop)
	return s
}

// ── Security ──────────────────────────────────────────────────────────────────

func BenchmarkSecurity(b *testing.B) {
	h := Security(okHandler)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	b.ReportAllocs()
	for b.Loop() {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
	}
}

// ── Recovery ─────────────────────────────────────────────────────────────────

func BenchmarkRecovery_NoPanic(b *testing.B) {
	h := Recovery(okHandler)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	b.ReportAllocs()
	for b.Loop() {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
	}
}

func BenchmarkRecovery_WithPanic(b *testing.B) {
	panicH := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		panic("boom")
	})
	h := Recovery(panicH)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	b.ReportAllocs()
	for b.Loop() {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
	}
}

// ── RequestSize ───────────────────────────────────────────────────────────────

func BenchmarkRequestSize_UnderLimit(b *testing.B) {
	h := RequestSize(okHandler)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	b.ReportAllocs()
	for b.Loop() {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
	}
}

// ── HTTPCache ─────────────────────────────────────────────────────────────────

func BenchmarkHTTPCache_CacheMiss(b *testing.B) {
	store := newBenchStore(b)
	h := HTTPCache(HTTPCacheConfig{
		Store:     store,
		KeyPrefix: func(r *http.Request) string { return r.URL.Path },
	})(okHandler)
	b.ReportAllocs()
	for b.Loop() {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/bench", nil)
		h.ServeHTTP(rec, req)
	}
}

func BenchmarkHTTPCache_CacheHit(b *testing.B) {
	store := newBenchStore(b)
	h := HTTPCache(HTTPCacheConfig{
		Store:     store,
		KeyPrefix: staticPrefix("bench-hit"),
	})(okHandler)

	// Warm the cache with one real request.
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/bench", nil))

	req := httptest.NewRequest(http.MethodGet, "/bench", nil)
	b.ReportAllocs()
	for b.Loop() {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
	}
}

func BenchmarkHTTPCache_InvalidatingPost(b *testing.B) {
	store := newBenchStore(b)
	h := HTTPCache(HTTPCacheConfig{
		Store:             store,
		KeyPrefix:         staticPrefix("bench-post"),
		InvalidateMethods: []string{http.MethodPost},
	})(okHandler)

	req := httptest.NewRequest(http.MethodPost, "/bench", nil)
	b.ReportAllocs()
	for b.Loop() {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
	}
}

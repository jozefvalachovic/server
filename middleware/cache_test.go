package middleware

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jozefvalachovic/server/cache"
)

// newTestStore returns a short-lived CacheStore suitable for unit tests.
// The caller must call store.Stop() when done.
func newTestStore(t *testing.T) *cache.CacheStore {
	t.Helper()
	s, err := cache.NewCacheStore(cache.CacheConfig{
		MaxSize:         100,
		DefaultTTL:      10 * time.Second,
		CleanupInterval: 5 * time.Second,
		MaxMemoryMB:     64,
	})
	if err != nil {
		t.Fatalf("failed to create test cache store: %v", err)
	}
	t.Cleanup(s.Stop)
	return s
}

func staticPrefix(prefix string) func(*http.Request) string {
	return func(_ *http.Request) string { return prefix }
}

// ── Basic bypass cases ──────────────────────────────────────────────────────

func TestHTTPCache_NilStoreBypass(t *testing.T) {
	called := false
	h := HTTPCache(HTTPCacheConfig{})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/test", nil))

	if !called {
		t.Fatal("handler was not called when store is nil")
	}
}

func TestHTTPCache_NilKeyPrefixBypass(t *testing.T) {
	store := newTestStore(t)
	called := false

	h := HTTPCache(HTTPCacheConfig{Store: store})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/test", nil))

	if !called {
		t.Fatal("handler was not called when KeyPrefix is nil")
	}
}

func TestHTTPCache_EmptyPrefixBypass(t *testing.T) {
	store := newTestStore(t)
	calls := 0

	h := HTTPCache(HTTPCacheConfig{
		Store:     store,
		KeyPrefix: staticPrefix(""), // empty → bypass
	})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusOK)
	}))

	for range 2 {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/test", nil))
	}

	if calls != 2 {
		t.Fatalf("expected handler called twice (no caching), got %d", calls)
	}
}

func TestHTTPCache_ColonInPrefixBypass(t *testing.T) {
	store := newTestStore(t)
	calls := 0

	h := HTTPCache(HTTPCacheConfig{
		Store:     store,
		KeyPrefix: staticPrefix("tenant:42"), // colon → bypass
	})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusOK)
	}))

	for range 2 {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/test", nil))
	}

	if calls != 2 {
		t.Fatalf("expected 2 handler calls (colon bypass); got %d", calls)
	}
}

func TestHTTPCache_NoStoreHeaderBypass(t *testing.T) {
	store := newTestStore(t)
	calls := 0

	h := HTTPCache(HTTPCacheConfig{
		Store:     store,
		KeyPrefix: staticPrefix("u1"),
	})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusOK)
	}))

	for range 2 {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/test", nil)
		req.Header.Set("Cache-Control", "no-cache, no-store")
		h.ServeHTTP(rec, req)
	}

	if calls != 2 {
		t.Fatalf("expected 2 handler calls (no-store bypass); got %d", calls)
	}
}

// ── GET miss / hit ──────────────────────────────────────────────────────────

func TestHTTPCache_GET_MissThenHit(t *testing.T) {
	store := newTestStore(t)
	calls := 0

	h := HTTPCache(HTTPCacheConfig{
		Store:     store,
		KeyPrefix: staticPrefix("u1"),
	})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"calls":` + strings.Repeat("1", calls) + `}`))
	}))

	// First request → MISS; handler called.
	rec1 := httptest.NewRecorder()
	h.ServeHTTP(rec1, httptest.NewRequest(http.MethodGet, "/resource", nil))
	if rec1.Header().Get("X-Cache") != "MISS" {
		t.Fatalf("first request: want MISS, got %q", rec1.Header().Get("X-Cache"))
	}

	// Second request → HIT; handler must NOT be called again.
	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, httptest.NewRequest(http.MethodGet, "/resource", nil))
	if rec2.Header().Get("X-Cache") != "HIT" {
		t.Fatalf("second request: want HIT, got %q", rec2.Header().Get("X-Cache"))
	}
	if calls != 1 {
		t.Fatalf("handler called %d times; expected exactly 1 (second request served from cache)", calls)
	}

	// Cached body matches original response body.
	if rec1.Body.String() != rec2.Body.String() {
		t.Fatalf("body mismatch: miss=%q hit=%q", rec1.Body.String(), rec2.Body.String())
	}
}

func TestHTTPCache_GET_XCacheHeader_Miss(t *testing.T) {
	store := newTestStore(t)

	h := HTTPCache(HTTPCacheConfig{
		Store:     store,
		KeyPrefix: staticPrefix("u2"),
	})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("data"))
	}))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/items", nil))

	if rec.Header().Get("X-Cache") != "MISS" {
		t.Fatalf("want X-Cache: MISS, got %q", rec.Header().Get("X-Cache"))
	}
}

func TestHTTPCache_GET_Non200NotCached(t *testing.T) {
	store := newTestStore(t)
	calls := 0

	h := HTTPCache(HTTPCacheConfig{
		Store:     store,
		KeyPrefix: staticPrefix("u3"),
	})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte("not found"))
	}))

	for range 2 {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/missing", nil))
	}

	if calls != 2 {
		t.Fatalf("404 responses must not be cached; handler called %d times", calls)
	}
}

func TestHTTPCache_GET_SetCookieNotCached(t *testing.T) {
	store := newTestStore(t)
	calls := 0

	h := HTTPCache(HTTPCacheConfig{
		Store:     store,
		KeyPrefix: staticPrefix("u4"),
	})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Set-Cookie", "session=abc; HttpOnly")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("session response"))
	}))

	for range 2 {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/login", nil))
	}

	if calls != 2 {
		t.Fatalf("Set-Cookie responses must not be cached; handler called %d times", calls)
	}
}

// ── HEAD ────────────────────────────────────────────────────────────────────

func TestHTTPCache_HEAD_NotCached(t *testing.T) {
	store := newTestStore(t)
	calls := 0

	h := HTTPCache(HTTPCacheConfig{
		Store:     store,
		KeyPrefix: staticPrefix("u5"),
	})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusOK)
	}))

	for range 2 {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodHead, "/resource", nil))
	}

	if calls != 2 {
		t.Fatalf("HEAD responses must not be cached; handler called %d times", calls)
	}
}

func TestHTTPCache_HEAD_XCacheBypass(t *testing.T) {
	store := newTestStore(t)

	h := HTTPCache(HTTPCacheConfig{
		Store:     store,
		KeyPrefix: staticPrefix("u5b"),
	})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodHead, "/resource", nil))
	if rec.Header().Get("X-Cache") != "BYPASS" {
		t.Fatalf("HEAD miss should be BYPASS, got %q", rec.Header().Get("X-Cache"))
	}
}

// ── Mutation invalidation ───────────────────────────────────────────────────

func TestHTTPCache_POST_InvalidatesPrefix(t *testing.T) {
	store := newTestStore(t)
	getHandlerCalls := 0

	cfg := HTTPCacheConfig{Store: store, KeyPrefix: staticPrefix("u6")}
	h := HTTPCache(cfg)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			getHandlerCalls++
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("list"))
			return
		}
		// POST
		w.WriteHeader(http.StatusCreated)
	}))

	// Seed cache with a GET.
	rec1 := httptest.NewRecorder()
	h.ServeHTTP(rec1, httptest.NewRequest(http.MethodGet, "/items", nil))
	if getHandlerCalls != 1 {
		t.Fatalf("expected 1 GET handler call for seeding, got %d", getHandlerCalls)
	}

	// Confirm it is cached.
	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, httptest.NewRequest(http.MethodGet, "/items", nil))
	if rec2.Header().Get("X-Cache") != "HIT" {
		t.Fatalf("expected cached response before POST, got X-Cache: %q", rec2.Header().Get("X-Cache"))
	}
	if getHandlerCalls != 1 {
		t.Fatal("handler should not have been called on cache HIT")
	}

	// POST → invalidates prefix.
	rec3 := httptest.NewRecorder()
	h.ServeHTTP(rec3, httptest.NewRequest(http.MethodPost, "/items", strings.NewReader(`{}`)))
	if rec3.Code != http.StatusCreated {
		t.Fatalf("POST: want 201, got %d", rec3.Code)
	}

	// Next GET should be a MISS again.
	rec4 := httptest.NewRecorder()
	h.ServeHTTP(rec4, httptest.NewRequest(http.MethodGet, "/items", nil))
	if rec4.Header().Get("X-Cache") != "MISS" {
		t.Fatalf("after POST invalidation expected MISS, got %q", rec4.Header().Get("X-Cache"))
	}
	if getHandlerCalls != 2 {
		t.Fatalf("expected handler called again after invalidation, got %d calls", getHandlerCalls)
	}
}

func TestHTTPCache_MutationNon2xx_NoInvalidation(t *testing.T) {
	store := newTestStore(t)
	getHandlerCalls := 0

	cfg := HTTPCacheConfig{Store: store, KeyPrefix: staticPrefix("u7")}
	h := HTTPCache(cfg)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			getHandlerCalls++
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("list"))
			return
		}
		// DELETE → 404 (prefix should NOT be invalidated)
		w.WriteHeader(http.StatusNotFound)
	}))

	// Seed cache.
	rec1 := httptest.NewRecorder()
	h.ServeHTTP(rec1, httptest.NewRequest(http.MethodGet, "/things", nil))

	// Confirm cached.
	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, httptest.NewRequest(http.MethodGet, "/things", nil))
	if rec2.Header().Get("X-Cache") != "HIT" {
		t.Fatal("expected HIT before DELETE")
	}

	// DELETE with 404 response.
	rec3 := httptest.NewRecorder()
	h.ServeHTTP(rec3, httptest.NewRequest(http.MethodDelete, "/things/999", nil))

	// Cache should still be intact.
	rec4 := httptest.NewRecorder()
	h.ServeHTTP(rec4, httptest.NewRequest(http.MethodGet, "/things", nil))
	if rec4.Header().Get("X-Cache") != "HIT" {
		t.Fatalf("cache should survive non-2xx mutation; got X-Cache: %q", rec4.Header().Get("X-Cache"))
	}
}

// ── Pass-through methods ────────────────────────────────────────────────────

func TestHTTPCache_OPTIONS_PassesThrough(t *testing.T) {
	store := newTestStore(t)
	called := false

	h := HTTPCache(HTTPCacheConfig{
		Store:     store,
		KeyPrefix: staticPrefix("u8"),
	})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusNoContent)
	}))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodOptions, "/res", nil))

	if !called {
		t.Fatal("handler was not called for OPTIONS")
	}
}

// ── statusRecorder ──────────────────────────────────────────────────────────

func TestStatusRecorder_CapturesStatusCode(t *testing.T) {
	rec := httptest.NewRecorder()
	sr := newStatusRecorder(rec)

	sr.WriteHeader(http.StatusCreated)

	if sr.statusCode != http.StatusCreated {
		t.Fatalf("want 201, got %d", sr.statusCode)
	}
	if rec.Code != http.StatusCreated {
		t.Fatalf("underlying writer should have code 201, got %d", rec.Code)
	}
}

func TestStatusRecorder_DefaultStatusCode(t *testing.T) {
	sr := newStatusRecorder(httptest.NewRecorder())
	if sr.statusCode != http.StatusOK {
		t.Fatalf("default status should be 200, got %d", sr.statusCode)
	}
}

// ── cacheResponseRecorder ───────────────────────────────────────────────────

func TestCacheResponseRecorder_CapturesBodyAndStatus(t *testing.T) {
	rec := httptest.NewRecorder()
	crr := newCacheResponseRecorder(rec)

	crr.WriteHeader(http.StatusAccepted)
	_, _ = crr.Write([]byte("hello"))

	if crr.statusCode != http.StatusAccepted {
		t.Fatalf("want 202, got %d", crr.statusCode)
	}
	if crr.body.String() != "hello" {
		t.Fatalf("want body %q, got %q", "hello", crr.body.String())
	}
	// Underlying writer must also have received the bytes.
	if rec.Body.String() != "hello" {
		t.Fatalf("underlying writer body %q", rec.Body.String())
	}
}

func TestCacheResponseRecorder_ImplicitWriteHeader(t *testing.T) {
	rec := httptest.NewRecorder()
	crr := newCacheResponseRecorder(rec)

	// Write without explicit WriteHeader → should default to 200.
	_, _ = crr.Write([]byte("data"))

	if crr.statusCode != http.StatusOK {
		t.Fatalf("want implicit 200, got %d", crr.statusCode)
	}
}

func TestCacheResponseRecorder_DuplicateWriteHeaderIsNoop(t *testing.T) {
	rec := httptest.NewRecorder()
	crr := newCacheResponseRecorder(rec)

	crr.WriteHeader(http.StatusOK)
	crr.WriteHeader(http.StatusTeapot) // must be ignored

	if crr.statusCode != http.StatusOK {
		t.Fatalf("status should remain 200 after duplicate WriteHeader, got %d", crr.statusCode)
	}
}

func TestCacheResponseRecorder_FlushMarksStreamed(t *testing.T) {
	rec := httptest.NewRecorder()
	crr := newCacheResponseRecorder(rec)

	crr.Flush()

	if !crr.streamed {
		t.Fatal("streamed should be true after Flush")
	}
}

func TestCacheResponseRecorder_CapturedHeadersForwarded(t *testing.T) {
	rec := httptest.NewRecorder()
	crr := newCacheResponseRecorder(rec)

	crr.Header().Set("Content-Type", "application/json")
	crr.WriteHeader(http.StatusOK)

	if rec.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("captured headers should be forwarded; got %q", rec.Header().Get("Content-Type"))
	}
}

// ── HEAD served from GET cache ─────────────────────────────────────────────

func TestHTTPCache_HEAD_ServedFromGetCache(t *testing.T) {
	store := newTestStore(t)
	handlerCalls := 0

	h := HTTPCache(HTTPCacheConfig{
		Store:     store,
		KeyPrefix: staticPrefix("u9"),
	})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handlerCalls++
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		if r.Method == http.MethodGet {
			_, _ = w.Write([]byte(`{"ok":true}`))
		}
	}))

	// Seed the cache via GET.
	rec1 := httptest.NewRecorder()
	h.ServeHTTP(rec1, httptest.NewRequest(http.MethodGet, "/resource", nil))
	if rec1.Header().Get("X-Cache") != "MISS" {
		t.Fatalf("GET seed: want MISS, got %q", rec1.Header().Get("X-Cache"))
	}
	if handlerCalls != 1 {
		t.Fatalf("expected 1 handler call for GET seed, got %d", handlerCalls)
	}

	// Subsequent HEAD should be served from cache (HIT) without calling handler.
	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, httptest.NewRequest(http.MethodHead, "/resource", nil))
	if rec2.Header().Get("X-Cache") != "HIT" {
		t.Fatalf("HEAD: want X-Cache: HIT, got %q", rec2.Header().Get("X-Cache"))
	}
	if handlerCalls != 1 {
		t.Fatalf("handler must not be called for HEAD HIT; got %d calls", handlerCalls)
	}
	// HEAD must return no body even though the cached entry has one.
	if rec2.Body.Len() != 0 {
		t.Fatalf("HEAD response must have empty body, got %d bytes", rec2.Body.Len())
	}
}

// ── Streamed response not cached ───────────────────────────────────────────

func TestHTTPCache_GET_StreamedResponseNotCached(t *testing.T) {
	store := newTestStore(t)
	calls := 0

	h := HTTPCache(HTTPCacheConfig{
		Store:     store,
		KeyPrefix: staticPrefix("u10"),
	})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		// Calling Flush() marks the response as streamed → must not be cached.
		if f, ok := w.(http.Flusher); ok {
			_, _ = w.Write([]byte("chunk1"))
			f.Flush()
			_, _ = w.Write([]byte("chunk2"))
		}
	}))

	// First request — handler flushes, so response must not enter cache.
	rec1 := httptest.NewRecorder()
	h.ServeHTTP(rec1, httptest.NewRequest(http.MethodGet, "/stream", nil))
	if calls != 1 {
		t.Fatalf("expected 1 handler call, got %d", calls)
	}

	// Second request must also reach the handler (not a HIT).
	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, httptest.NewRequest(http.MethodGet, "/stream", nil))
	if rec2.Header().Get("X-Cache") == "HIT" {
		t.Fatal("streamed response must not be cached; got HIT on second request")
	}
	if calls != 2 {
		t.Fatalf("handler must be called again for non-cached stream; got %d calls", calls)
	}
}

// ── Custom InvalidateMethods ───────────────────────────────────────────────

func TestHTTPCache_CustomInvalidateMethods_OnlyPATCHInvalidates(t *testing.T) {
	store := newTestStore(t)
	getCalls := 0

	h := HTTPCache(HTTPCacheConfig{
		Store:             store,
		KeyPrefix:         staticPrefix("u11"),
		InvalidateMethods: []string{http.MethodPatch}, // only PATCH invalidates
	})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			getCalls++
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("data"))
			return
		}
		w.WriteHeader(http.StatusOK)
	}))

	// Seed cache.
	rec1 := httptest.NewRecorder()
	h.ServeHTTP(rec1, httptest.NewRequest(http.MethodGet, "/items", nil))
	if rec1.Header().Get("X-Cache") != "MISS" {
		t.Fatal("expected MISS on seed")
	}

	// Confirm cached.
	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, httptest.NewRequest(http.MethodGet, "/items", nil))
	if rec2.Header().Get("X-Cache") != "HIT" {
		t.Fatal("expected HIT before mutations")
	}

	// POST is NOT in the custom InvalidateMethods — cache must survive.
	rec3 := httptest.NewRecorder()
	h.ServeHTTP(rec3, httptest.NewRequest(http.MethodPost, "/items", strings.NewReader("{}")))

	rec4 := httptest.NewRecorder()
	h.ServeHTTP(rec4, httptest.NewRequest(http.MethodGet, "/items", nil))
	if rec4.Header().Get("X-Cache") != "HIT" {
		t.Fatalf("POST must not invalidate cache when not in InvalidateMethods; got X-Cache: %q",
			rec4.Header().Get("X-Cache"))
	}
	if getCalls != 1 {
		t.Fatalf("handler must not be called again after non-invalidating POST; got %d calls", getCalls)
	}

	// PATCH IS in the custom InvalidateMethods — cache must be cleared.
	rec5 := httptest.NewRecorder()
	h.ServeHTTP(rec5, httptest.NewRequest(http.MethodPatch, "/items", strings.NewReader("{}")))

	rec6 := httptest.NewRecorder()
	h.ServeHTTP(rec6, httptest.NewRequest(http.MethodGet, "/items", nil))
	if rec6.Header().Get("X-Cache") != "MISS" {
		t.Fatalf("PATCH must invalidate cache; got X-Cache: %q after PATCH", rec6.Header().Get("X-Cache"))
	}
	if getCalls != 2 {
		t.Fatalf("handler must be called again after PATCH invalidation; got %d calls", getCalls)
	}
}

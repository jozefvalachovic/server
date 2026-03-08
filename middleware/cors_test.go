package middleware

import (
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"
)

// ── CORS ─────────────────────────────────────────────────────────────────────

func corsRequest(mw func(http.Handler) http.Handler, method, origin string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, "/", nil)
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	rec := httptest.NewRecorder()
	mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})).ServeHTTP(rec, req)
	return rec
}

func TestCORS_Disabled_Passthrough(t *testing.T) {
	mw := CORS(CORSConfig{Disabled: true})
	rec := corsRequest(mw, http.MethodGet, "https://evil.example.com")
	if rec.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatal("disabled CORS should not set any CORS headers")
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
}

func TestCORS_NoOriginHeader_NoHeaders(t *testing.T) {
	mw := CORS()
	rec := corsRequest(mw, http.MethodGet, "")
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("no Origin header → no CORS headers expected, got %q", got)
	}
}

func TestCORS_WildcardDefault(t *testing.T) {
	mw := CORS()
	rec := corsRequest(mw, http.MethodGet, "https://any.example.com")
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "*" {
		t.Fatalf("want wildcard origin, got %q", got)
	}
}

func TestCORS_AllowedOrigin_Reflected(t *testing.T) {
	mw := CORS(CORSConfig{AllowedOrigins: []string{"https://example.com"}})
	rec := corsRequest(mw, http.MethodGet, "https://example.com")
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "https://example.com" {
		t.Fatalf("want https://example.com, got %q", got)
	}
}

func TestCORS_DisallowedOrigin_NoHeader(t *testing.T) {
	mw := CORS(CORSConfig{AllowedOrigins: []string{"https://example.com"}})
	rec := corsRequest(mw, http.MethodGet, "https://evil.com")
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("disallowed origin should not set header, got %q", got)
	}
	// Underlying request still served (403 is callers' job, not CORS middleware).
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
}

func TestCORS_Preflight_Returns204(t *testing.T) {
	mw := CORS()
	req := httptest.NewRequest(http.MethodOptions, "/", nil)
	req.Header.Set("Origin", "https://example.com")
	rec := httptest.NewRecorder()
	mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("next handler must not be called for OPTIONS preflight")
	})).ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("want 204, got %d", rec.Code)
	}
}

func TestCORS_Preflight_SetsAllowMethods(t *testing.T) {
	mw := CORS()
	req := httptest.NewRequest(http.MethodOptions, "/", nil)
	req.Header.Set("Origin", "https://example.com")
	rec := httptest.NewRecorder()
	mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {})).ServeHTTP(rec, req)
	if got := rec.Header().Get("Access-Control-Allow-Methods"); got == "" {
		t.Fatal("expected Access-Control-Allow-Methods to be set")
	}
}

func TestCORS_AllowCredentials(t *testing.T) {
	mw := CORS(CORSConfig{
		AllowedOrigins:   []string{"https://example.com"},
		AllowCredentials: true,
	})
	rec := corsRequest(mw, http.MethodGet, "https://example.com")
	if got := rec.Header().Get("Access-Control-Allow-Credentials"); got != "true" {
		t.Fatalf("want true, got %q", got)
	}
}

func TestCORS_MaxAge_Set(t *testing.T) {
	mw := CORS(CORSConfig{MaxAge: 2 * time.Hour})
	req := httptest.NewRequest(http.MethodOptions, "/", nil)
	req.Header.Set("Origin", "https://example.com")
	rec := httptest.NewRecorder()
	mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {})).ServeHTTP(rec, req)
	if got := rec.Header().Get("Access-Control-Max-Age"); got != "7200" {
		t.Fatalf("want 7200, got %q", got)
	}
}

func TestCORS_ExposedHeaders(t *testing.T) {
	mw := CORS(CORSConfig{ExposedHeaders: []string{"X-RateLimit-Remaining"}})
	rec := corsRequest(mw, http.MethodGet, "https://example.com")
	if got := rec.Header().Get("Access-Control-Expose-Headers"); got != "X-RateLimit-Remaining" {
		t.Fatalf("want X-RateLimit-Remaining, got %q", got)
	}
}

func TestCORS_VaryOriginAdded(t *testing.T) {
	mw := CORS()
	rec := corsRequest(mw, http.MethodGet, "https://example.com")
	found := slices.Contains(rec.Result().Header["Vary"], "Origin")
	if !found {
		t.Fatal("expected Vary: Origin header")
	}
}

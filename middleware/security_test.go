package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func applySecurityMiddleware(t *testing.T) *httptest.ResponseRecorder {
	t.Helper()

	handler := Security(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	handler.ServeHTTP(rec, req)
	return rec
}

func TestSecurity_XContentTypeOptions(t *testing.T) {
	rec := applySecurityMiddleware(t)
	if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Fatalf("want nosniff, got %q", got)
	}
}

func TestSecurity_XFrameOptions(t *testing.T) {
	rec := applySecurityMiddleware(t)
	if got := rec.Header().Get("X-Frame-Options"); got != "DENY" {
		t.Fatalf("want DENY, got %q", got)
	}
}

func TestSecurity_ReferrerPolicy(t *testing.T) {
	rec := applySecurityMiddleware(t)
	if got := rec.Header().Get("Referrer-Policy"); got != "strict-origin-when-cross-origin" {
		t.Fatalf("unexpected Referrer-Policy: %q", got)
	}
}

func TestSecurity_ContentSecurityPolicy(t *testing.T) {
	rec := applySecurityMiddleware(t)
	want := "default-src 'self'; frame-ancestors 'none'"
	if got := rec.Header().Get("Content-Security-Policy"); got != want {
		t.Fatalf("unexpected CSP: %q", got)
	}
}

func TestSecurity_PermissionsPolicy(t *testing.T) {
	rec := applySecurityMiddleware(t)
	want := "geolocation=(), microphone=(), camera=()"
	if got := rec.Header().Get("Permissions-Policy"); got != want {
		t.Fatalf("unexpected Permissions-Policy: %q", got)
	}
}

func TestSecurity_ServerHeaderOmitted(t *testing.T) {
	rec := applySecurityMiddleware(t)
	got := rec.Header().Get("Server")
	// The Server header must not be set to avoid leaking technology stack info.
	if got != "" {
		t.Fatalf("Server header should not be set, got %q", got)
	}
}

func TestSecurity_HSTSNotSetOutsideProduction(t *testing.T) {
	t.Setenv("ENV", "development")
	rec := applySecurityMiddleware(t)
	if got := rec.Header().Get("Strict-Transport-Security"); got != "" {
		t.Fatalf("HSTS should not be set outside production, got %q", got)
	}
}

func TestSecurity_HSTSSetInProduction(t *testing.T) {
	t.Setenv("ENV", "production")
	rec := applySecurityMiddleware(t)
	want := "max-age=31536000; includeSubDomains; preload"
	if got := rec.Header().Get("Strict-Transport-Security"); got != want {
		t.Fatalf("unexpected HSTS: %q", got)
	}
}

func TestSecurity_NextHandlerCalled(t *testing.T) {
	called := false
	handler := Security(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusTeapot)
	}))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	handler.ServeHTTP(rec, req)

	if !called {
		t.Fatal("next handler was not called")
	}
	if rec.Code != http.StatusTeapot {
		t.Fatalf("want 418, got %d", rec.Code)
	}
}

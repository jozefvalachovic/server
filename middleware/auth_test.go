package middleware

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

// ── Auth ──────────────────────────────────────────────────────────────────────

var alwaysValid = func(_ context.Context, credential string) (string, error) {
	return "user:" + credential, nil
}

var alwaysInvalid = func(_ context.Context, _ string) (string, error) {
	return "", errors.New("bad credential")
}

func authRequest(mw func(http.Handler) http.Handler, header, value string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	if header != "" {
		req.Header.Set(header, value)
	}
	rec := httptest.NewRecorder()
	mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})).ServeHTTP(rec, req)
	return rec
}

func TestAuth_MissingBearer_Returns401(t *testing.T) {
	mw := Auth(AuthConfig{Verify: alwaysValid})
	rec := authRequest(mw, "", "")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d", rec.Code)
	}
}

func TestAuth_MissingBearer_WWWAuthenticateSet(t *testing.T) {
	mw := Auth(AuthConfig{Verify: alwaysValid, Realm: "TestRealm"})
	rec := authRequest(mw, "", "")
	if got := rec.Header().Get("WWW-Authenticate"); got == "" {
		t.Fatal("expected WWW-Authenticate header on 401")
	}
}

func TestAuth_ValidBearerToken_Passes(t *testing.T) {
	mw := Auth(AuthConfig{Verify: alwaysValid})
	rec := authRequest(mw, "Authorization", "Bearer mytoken")
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
}

func TestAuth_InvalidBearerToken_Returns401(t *testing.T) {
	mw := Auth(AuthConfig{Verify: alwaysInvalid})
	rec := authRequest(mw, "Authorization", "Bearer badtoken")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d", rec.Code)
	}
}

func TestAuth_BearerMissingToken_Returns401(t *testing.T) {
	// "Bearer " prefix present but token is empty.
	mw := Auth(AuthConfig{Verify: alwaysValid})
	rec := authRequest(mw, "Authorization", "Bearer ")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("want 401 (empty token), got %d", rec.Code)
	}
}

func TestAuth_IdentityStoredInContext(t *testing.T) {
	mw := Auth(AuthConfig{Verify: alwaysValid})
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer tok123")
	rec := httptest.NewRecorder()

	var identity string
	mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		identity = AuthIdentityFromContext(r)
		w.WriteHeader(http.StatusOK)
	})).ServeHTTP(rec, req)

	if identity != "user:tok123" {
		t.Fatalf("want user:tok123, got %q", identity)
	}
}

func TestAuth_APIKey_Valid(t *testing.T) {
	mw := Auth(AuthConfig{
		Scheme:       AuthSchemeAPIKey,
		APIKeyHeader: "X-API-Key",
		Verify:       alwaysValid,
	})
	rec := authRequest(mw, "X-API-Key", "secret-key")
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
}

func TestAuth_APIKey_Missing_Returns401(t *testing.T) {
	mw := Auth(AuthConfig{
		Scheme:       AuthSchemeAPIKey,
		APIKeyHeader: "X-API-Key",
		Verify:       alwaysValid,
	})
	rec := authRequest(mw, "", "")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d", rec.Code)
	}
}

func TestAuth_APIKey_DefaultHeader(t *testing.T) {
	// APIKeyHeader defaults to "X-API-Key" when empty.
	mw := Auth(AuthConfig{Scheme: AuthSchemeAPIKey, Verify: alwaysValid})
	rec := authRequest(mw, "X-API-Key", "mykey")
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
}

func TestAuth_SkipPaths_Bypasses(t *testing.T) {
	mw := Auth(AuthConfig{
		Verify:    alwaysInvalid, // would always reject
		SkipPaths: []string{"/health"},
	})
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()
	mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200 (skipped), got %d", rec.Code)
	}
}

func TestAuth_SkipPaths_DoesNotBypassOther(t *testing.T) {
	mw := Auth(AuthConfig{
		Verify:    alwaysInvalid,
		SkipPaths: []string{"/health"},
	})
	rec := authRequest(mw, "", "")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("non-skip path should still require auth, got %d", rec.Code)
	}
}

func TestAuth_NilVerify_Panics(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic when Verify is nil")
		}
	}()
	Auth(AuthConfig{Verify: nil})
}

func TestAuth_DefaultScheme_IsBearer(t *testing.T) {
	// Omitting Scheme → defaults to Bearer.
	mw := Auth(AuthConfig{Verify: alwaysValid})
	// Wrong header (API key style) → 401.
	rec := authRequest(mw, "X-API-Key", "somekey")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("default scheme should be Bearer, got %d", rec.Code)
	}
}

func TestAuthIdentityFromContext_EmptyWhenAbsent(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	if got := AuthIdentityFromContext(req); got != "" {
		t.Fatalf("expected empty string, got %q", got)
	}
}

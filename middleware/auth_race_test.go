package middleware_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/jozefvalachovic/server/middleware"
)

// ── MultiTokenVerify ──────────────────────────────────────────────────────

func TestMultiTokenVerify_AcceptsAll(t *testing.T) {
	verify := middleware.MultiTokenVerify("alpha", "beta", "gamma")
	for _, tok := range []string{"alpha", "beta", "gamma"} {
		id, err := verify(context.Background(), tok)
		if err != nil {
			t.Fatalf("expected %q accepted, got error: %v", tok, err)
		}
		if id == "" {
			t.Fatalf("expected non-empty identity for %q", tok)
		}
	}
}

func TestMultiTokenVerify_RejectsUnknown(t *testing.T) {
	verify := middleware.MultiTokenVerify("alpha", "beta")
	_, err := verify(context.Background(), "unknown")
	if err == nil {
		t.Fatal("expected error for unknown token")
	}
}

// ── TokenStore ────────────────────────────────────────────────────────────

func TestTokenStore_Rotate(t *testing.T) {
	store := middleware.NewTokenStore("old-token")

	// Old token should work.
	if _, err := store.Verify(context.Background(), "old-token"); err != nil {
		t.Fatalf("old token should be valid: %v", err)
	}

	// Rotate to new token.
	store.Rotate("new-token")

	// Old token should fail.
	if _, err := store.Verify(context.Background(), "old-token"); err == nil {
		t.Fatal("old token should be rejected after rotation")
	}

	// New token should work.
	if _, err := store.Verify(context.Background(), "new-token"); err != nil {
		t.Fatalf("new token should be valid: %v", err)
	}
}

func TestTokenStore_ConcurrentRotateAndVerify(t *testing.T) {
	store := middleware.NewTokenStore("token-a", "token-b")

	var wg sync.WaitGroup
	const goroutines = 50
	const iterations = 500

	// Concurrent verification.
	for range goroutines / 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range iterations {
				// Either token-a, token-b, token-c, or token-d may be valid
				// depending on rotation timing — we just verify no panics/races.
				_, _ = store.Verify(context.Background(), "token-a")
				_, _ = store.Verify(context.Background(), "token-c")
			}
		}()
	}

	// Concurrent rotation.
	for range goroutines / 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range iterations {
				store.Rotate("token-c", "token-d")
				store.Rotate("token-a", "token-b")
			}
		}()
	}

	wg.Wait()
}

// ── OnAuthFailure callback ───────────────────────────────────────────────

func TestAuth_OnAuthFailure_CalledOnMissing(t *testing.T) {
	var called bool
	mw := middleware.Auth(middleware.AuthConfig{
		Verify: func(_ context.Context, _ string) (string, error) {
			return "id", nil
		},
		OnAuthFailure: func(r *http.Request, err error) {
			called = true
			if err != nil {
				t.Fatal("expected nil error for missing credentials")
			}
		},
	})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/secret", nil)
	mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})).ServeHTTP(rec, req)

	if !called {
		t.Fatal("OnAuthFailure should have been called")
	}
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d", rec.Code)
	}
}

func TestAuth_OnAuthFailure_CalledOnInvalid(t *testing.T) {
	var called bool
	verify := middleware.MultiTokenVerify("good-token")
	mw := middleware.Auth(middleware.AuthConfig{
		Verify: verify,
		OnAuthFailure: func(r *http.Request, err error) {
			called = true
			if err == nil {
				t.Fatal("expected non-nil error for invalid credentials")
			}
		},
	})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/secret", nil)
	req.Header.Set("Authorization", "Bearer bad-token")
	mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})).ServeHTTP(rec, req)

	if !called {
		t.Fatal("OnAuthFailure should have been called")
	}
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d", rec.Code)
	}
}

// ── Auth middleware concurrent stress ─────────────────────────────────────

func TestAuth_ConcurrentRequests(t *testing.T) {
	store := middleware.NewTokenStore("valid-token")
	mw := middleware.Auth(middleware.AuthConfig{
		Verify:        store.Verify,
		OnAuthFailure: middleware.AuditAuthFailure,
	})

	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	var wg sync.WaitGroup
	const n = 200

	for range n {
		wg.Add(2)
		// Valid requests.
		go func() {
			defer wg.Done()
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/api/data", nil)
			req.Header.Set("Authorization", "Bearer valid-token")
			handler.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Errorf("valid: want 200, got %d", rec.Code)
			}
		}()
		// Invalid requests.
		go func() {
			defer wg.Done()
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/api/data", nil)
			req.Header.Set("Authorization", "Bearer wrong-token")
			handler.ServeHTTP(rec, req)
			if rec.Code != http.StatusUnauthorized {
				t.Errorf("invalid: want 401, got %d", rec.Code)
			}
		}()
	}

	// Rotate tokens while requests are in-flight.
	go func() {
		for range n {
			store.Rotate("valid-token", "extra-token")
			store.Rotate("valid-token")
		}
	}()

	wg.Wait()
}

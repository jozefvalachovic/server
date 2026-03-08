package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// ── Timeout ───────────────────────────────────────────────────────────────────

func TestTimeout_FastHandler_Passes(t *testing.T) {
	mw := Timeout(TimeoutConfig{Timeout: 500 * time.Millisecond})
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()

	mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
}

func TestTimeout_SlowHandler_Returns504(t *testing.T) {
	mw := Timeout(TimeoutConfig{Timeout: 20 * time.Millisecond})
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()

	mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Block until the context is cancelled (by the timeout).
		<-r.Context().Done()
	})).ServeHTTP(rec, req)

	if rec.Code != http.StatusGatewayTimeout {
		t.Fatalf("want 504, got %d", rec.Code)
	}
}

func TestTimeout_SlowHandler_CustomMessage(t *testing.T) {
	mw := Timeout(TimeoutConfig{
		Timeout:      20 * time.Millisecond,
		ErrorMessage: "custom timeout message",
	})
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()

	mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	})).ServeHTTP(rec, req)

	if rec.Code != http.StatusGatewayTimeout {
		t.Fatalf("want 504, got %d", rec.Code)
	}
	body := rec.Body.String()
	if body == "" {
		t.Fatal("expected non-empty error body")
	}
}

func TestTimeout_DefaultTimeout_Applied(t *testing.T) {
	// Zero timeout in config → must fall back to DefaultRequestTimeout (30 s),
	// which means a fast handler still succeeds.
	mw := Timeout(TimeoutConfig{Timeout: 0})
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()

	mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})).ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("want 204, got %d", rec.Code)
	}
}

func TestTimeout_HandlerAlreadyWrote_No504(t *testing.T) {
	// Handler writes before the timeout fires; no 504 should be injected.
	mw := Timeout(TimeoutConfig{Timeout: 100 * time.Millisecond})
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()

	done := make(chan struct{})
	mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusAccepted)
		close(done)
		// Simulate work continuing after writing headers.
		time.Sleep(150 * time.Millisecond)
	})).ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("want 202 (handler wrote first), got %d", rec.Code)
	}
}

func TestTimeout_ContextCancelledOnTimeout(t *testing.T) {
	mw := Timeout(TimeoutConfig{Timeout: 20 * time.Millisecond})
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()

	var ctxErr error
	handlerDone := make(chan struct{})
	mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
		ctxErr = r.Context().Err()
		close(handlerDone)
	})).ServeHTTP(rec, req)

	// The handler goroutine may still be running after ServeHTTP returns
	// (the timeout branch of the select fires first). Wait for it.
	<-handlerDone

	if ctxErr == nil {
		t.Fatal("expected context to be cancelled after timeout")
	}
}

package middleware

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRequestSize_SkipsGET(t *testing.T) {
	called := false
	handler := RequestSize(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", strings.NewReader("body"))
	handler.ServeHTTP(rec, req)

	if !called {
		t.Fatal("handler was not called for GET")
	}
}

func TestRequestSize_SkipsHEAD(t *testing.T) {
	called := false
	handler := RequestSize(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodHead, "/", nil)
	handler.ServeHTTP(rec, req)

	if !called {
		t.Fatal("handler was not called for HEAD")
	}
}

func TestRequestSize_SkipsOPTIONS(t *testing.T) {
	called := false
	handler := RequestSize(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodOptions, "/", nil)
	handler.ServeHTTP(rec, req)

	if !called {
		t.Fatal("handler was not called for OPTIONS")
	}
}

func TestRequestSize_POST_ReadsBody(t *testing.T) {
	handler := RequestSize(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if len(body) == 0 {
			t.Error("expected non-empty body")
		}
		w.WriteHeader(http.StatusOK)
	}))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"key":"value"}`))
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
}

func TestRequestSize_PUT_PassesThrough(t *testing.T) {
	handler := RequestSize(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/", strings.NewReader("data"))
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
}

func TestRequestSize_EnvVar(t *testing.T) {
	t.Setenv("MAX_REQUEST_SIZE_MB", "1")
	size := resolveMaxRequestSize(0)
	want := int64(1 * 1024 * 1024)
	if size != want {
		t.Fatalf("want %d bytes, got %d", want, size)
	}
}

func TestRequestSize_DefaultMaxSize(t *testing.T) {
	t.Setenv("MAX_REQUEST_SIZE_MB", "")
	size := resolveMaxRequestSize(0)
	want := int64(10 * 1024 * 1024)
	if size != want {
		t.Fatalf("want %d bytes, got %d", want, size)
	}
}

func TestRequestSize_InvalidEnvVar_FallsBackToDefault(t *testing.T) {
	t.Setenv("MAX_REQUEST_SIZE_MB", "notanumber")
	size := resolveMaxRequestSize(0)
	want := int64(10 * 1024 * 1024)
	if size != want {
		t.Fatalf("want default %d bytes, got %d", want, size)
	}
}

// ── Over-limit body ────────────────────────────────────────────────────────

func TestRequestSize_BodyExceedsLimit_ReturnsMaxBytesError(t *testing.T) {
	// Limit the maximum to 1 MB, then send a 2 MB body.
	t.Setenv("MAX_REQUEST_SIZE_MB", "1")

	var readErr error
	handler := RequestSize(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Reading past the limit must surface an *http.MaxBytesError.
		_, readErr = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))

	body := strings.NewReader(strings.Repeat("x", 2*1024*1024)) // 2 MB
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/upload", body)
	handler.ServeHTTP(rec, req)

	if readErr == nil {
		t.Fatal("expected a read error when body exceeds limit, got nil")
	}
	if _, ok := errors.AsType[*http.MaxBytesError](readErr); !ok {
		t.Fatalf("expected *http.MaxBytesError, got %T: %v", readErr, readErr)
	}
}

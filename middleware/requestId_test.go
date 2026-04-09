package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jozefvalachovic/logger/v4"
)

// serveCapture runs the middleware and exposes the request seen by the inner handler.
func serveCapture(mw func(http.Handler) http.Handler, r *http.Request) (*httptest.ResponseRecorder, *http.Request) {
	rec := httptest.NewRecorder()
	var inner *http.Request
	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		inner = req
		w.WriteHeader(http.StatusOK)
	}))
	handler.ServeHTTP(rec, r)
	return rec, inner
}

// ── RequestID ────────────────────────────────────────────────────────────────

func TestRequestID_GeneratesID(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec, _ := serveCapture(RequestID(), req)

	id := rec.Header().Get(RequestIDHeader)
	if id == "" {
		t.Fatal("expected X-Request-ID to be set in response header")
	}
}

func TestRequestID_IDIsHex32Chars(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec, _ := serveCapture(RequestID(), req)

	id := rec.Header().Get(RequestIDHeader)
	if len(id) != 32 {
		t.Fatalf("expected 32-char hex ID (16 bytes), got %q (len %d)", id, len(id))
	}
}

func TestRequestID_ReusesIncomingID(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set(RequestIDHeader, "upstream-id-abc")

	rec, inner := serveCapture(RequestID(), req)

	if got := rec.Header().Get(RequestIDHeader); got != "upstream-id-abc" {
		t.Fatalf("want upstream-id-abc in response, got %q", got)
	}
	if got := RequestIDFromContext(inner); got != "upstream-id-abc" {
		t.Fatalf("want upstream-id-abc in context, got %q", got)
	}
}

func TestRequestID_StoredInContext(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	_, inner := serveCapture(RequestID(), req)

	id := RequestIDFromContext(inner)
	if id == "" {
		t.Fatal("expected request ID in context, got empty string")
	}
}

func TestRequestID_CustomHeader(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-Correlation-ID", "corr-123")

	mw := RequestID(RequestIDConfig{Header: "X-Correlation-ID"})
	rec, _ := serveCapture(mw, req)

	if got := rec.Header().Get("X-Correlation-ID"); got != "corr-123" {
		t.Fatalf("want corr-123, got %q", got)
	}
}

func TestRequestID_CustomGenerator(t *testing.T) {
	mw := RequestID(RequestIDConfig{Generator: func() string { return "fixed-id" }})
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec, inner := serveCapture(mw, req)

	if got := rec.Header().Get(RequestIDHeader); got != "fixed-id" {
		t.Fatalf("want fixed-id in response, got %q", got)
	}
	if got := RequestIDFromContext(inner); got != "fixed-id" {
		t.Fatalf("want fixed-id in context, got %q", got)
	}
}

func TestRequestID_UniquePerRequest(t *testing.T) {
	mw := RequestID()
	ids := make(map[string]bool)
	for range 20 {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		rec, _ := serveCapture(mw, req)
		id := rec.Header().Get(RequestIDHeader)
		if ids[id] {
			t.Fatalf("duplicate request ID generated: %q", id)
		}
		ids[id] = true
	}
}

func TestRequestIDFromContext_EmptyWhenAbsent(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	if got := RequestIDFromContext(req); got != "" {
		t.Fatalf("expected empty string, got %q", got)
	}
}

func TestRequestID_EnrichesLoggerContext(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	_, inner := serveCapture(RequestID(), req)

	// The middleware should store an enriched logger in the request context.
	l := logger.FromContext(inner.Context())
	if l == nil {
		t.Fatal("expected non-nil logger from context")
	}
	// DefaultLogger() is the fallback — if we get the same pointer, the
	// middleware didn't store a child logger.
	if l == logger.DefaultLogger() {
		t.Fatal("expected enriched child logger, got DefaultLogger")
	}
}

package admin

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCollector_Middleware_Records(t *testing.T) {
	col := NewCollector()
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("hello"))
	})
	handler := col.Middleware(inner)
	mux := http.NewServeMux()
	mux.Handle("GET /test", handler)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/test", nil)
	mux.ServeHTTP(w, r)

	snaps := col.Snapshots()
	if len(snaps) == 0 {
		t.Fatal("expected at least one snapshot")
	}
	found := false
	for _, s := range snaps {
		if s.Count > 0 {
			found = true
			if s.Errors5xx != 0 {
				t.Fatalf("expected 0 errors, got %d", s.Errors5xx)
			}
		}
	}
	if !found {
		t.Fatal("expected a snapshot with Count > 0")
	}
}

func TestCollector_Middleware_5xx(t *testing.T) {
	col := NewCollector()
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	handler := col.Middleware(inner)
	mux := http.NewServeMux()
	mux.Handle("GET /err", handler)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/err", nil)
	mux.ServeHTTP(w, r)

	snaps := col.Snapshots()
	for _, s := range snaps {
		if s.Count > 0 && s.Errors5xx == 0 {
			t.Fatal("5xx response should be counted as error")
		}
	}
}

func TestCollector_Summary_Empty(t *testing.T) {
	col := NewCollector()
	total, errs, avg := col.Summary()
	if total != 0 || errs != 0 || avg != 0 {
		t.Fatalf("empty collector: want 0/0/0, got %d/%d/%.1f", total, errs, avg)
	}
}

func TestCollector_Summary_Aggregates(t *testing.T) {
	col := NewCollector()
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	handler := col.Middleware(inner)
	mux := http.NewServeMux()
	mux.Handle("GET /a", handler)
	mux.Handle("GET /b", handler)

	for _, path := range []string{"/a", "/a", "/b"} {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodGet, path, nil)
		mux.ServeHTTP(w, r)
	}

	total, _, _ := col.Summary()
	if total != 3 {
		t.Fatalf("want 3 total requests, got %d", total)
	}
}

func TestRouteSnapshot_ErrorRate(t *testing.T) {
	rs := RouteSnapshot{Count: 100, Errors5xx: 5}
	rate := rs.ErrorRate()
	if rate != 0.05 {
		t.Fatalf("want 0.05, got %f", rate)
	}
}

func TestRouteSnapshot_ErrorRate_Zero(t *testing.T) {
	rs := RouteSnapshot{Count: 0}
	if rs.ErrorRate() != 0 {
		t.Fatal("zero count should return 0 error rate")
	}
}

func TestCapturingWriter_WriteHeader_Idempotent(t *testing.T) {
	w := httptest.NewRecorder()
	cw := &capturingWriter{ResponseWriter: w, status: http.StatusOK}
	cw.WriteHeader(http.StatusNotFound)
	cw.WriteHeader(http.StatusInternalServerError) // should be ignored
	if cw.status != http.StatusNotFound {
		t.Fatalf("want 404, got %d", cw.status)
	}
}

func TestCapturingWriter_Write_CountsBytes(t *testing.T) {
	w := httptest.NewRecorder()
	cw := &capturingWriter{ResponseWriter: w, status: http.StatusOK}
	n, err := cw.Write([]byte("hello"))
	if err != nil {
		t.Fatal(err)
	}
	if n != 5 || cw.bytes != 5 {
		t.Fatalf("want 5 bytes, got n=%d, cw.bytes=%d", n, cw.bytes)
	}
}

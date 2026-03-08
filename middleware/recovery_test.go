package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// flusherRecorder wraps httptest.ResponseRecorder and tracks Flush calls.
type flusherRecorder struct {
	*httptest.ResponseRecorder
	flushed int
}

func (r *flusherRecorder) Flush() {
	r.flushed++
	r.ResponseRecorder.Flush()
}

func TestRecovery_NormalRequest(t *testing.T) {
	handler := Recovery(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	if rec.Body.String() != "ok" {
		t.Fatalf("unexpected body %q", rec.Body.String())
	}
}

func TestRecovery_PanicReturns500(t *testing.T) {
	handler := Recovery(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		panic("something went wrong")
	}))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("want 500, got %d", rec.Code)
	}
}

func TestRecovery_PanicAfterHeadersWritten_NoDoubleWrite(t *testing.T) {
	handler := Recovery(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		panic("too late")
	}))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("want 200 (headers already committed), got %d", rec.Code)
	}
}

func TestRecovery_PanicNonStringValue(t *testing.T) {
	handler := Recovery(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		panic(42)
	}))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("want 500 for non-string panic, got %d", rec.Code)
	}
}

func TestRecovery_FlushForwarded(t *testing.T) {
	fr := &flusherRecorder{ResponseRecorder: httptest.NewRecorder()}

	handler := Recovery(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		} else {
			t.Error("wrapped writer does not implement http.Flusher")
		}
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	handler.ServeHTTP(fr, req)

	if fr.flushed != 1 {
		t.Fatalf("expected 1 Flush call, got %d", fr.flushed)
	}
}

func TestResponseWriterTracker_WriteSetsWritten(t *testing.T) {
	rw := &responseWriterTracker{ResponseWriter: httptest.NewRecorder()}

	if rw.written {
		t.Fatal("written should start false")
	}
	_, _ = rw.Write([]byte("hello"))
	if !rw.written {
		t.Fatal("written should be true after Write")
	}
}

func TestResponseWriterTracker_WriteHeaderSetsWritten(t *testing.T) {
	rec := httptest.NewRecorder()
	rw := &responseWriterTracker{ResponseWriter: rec}

	rw.WriteHeader(http.StatusAccepted)

	if !rw.written {
		t.Fatal("written should be true after WriteHeader")
	}
	if rec.Code != http.StatusAccepted {
		t.Fatalf("want 202, got %d", rec.Code)
	}
}

func TestResponseWriterTracker_FlushNoFlusher(t *testing.T) {
	type plainWriter struct{ http.ResponseWriter }
	rw := &responseWriterTracker{ResponseWriter: &plainWriter{httptest.NewRecorder()}}
	rw.Flush() // must not panic
}

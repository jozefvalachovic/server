package middleware

import (
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// ── Compress ─────────────────────────────────────────────────────────────────

func compressServe(mw func(http.Handler) http.Handler, acceptEncoding, contentType, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	if acceptEncoding != "" {
		req.Header.Set("Accept-Encoding", acceptEncoding)
	}
	rec := httptest.NewRecorder()
	mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if contentType != "" {
			w.Header().Set("Content-Type", contentType)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, body)
	})).ServeHTTP(rec, req)
	return rec
}

func TestCompress_Disabled_Passthrough(t *testing.T) {
	mw := Compress() // Enabled defaults to false
	rec := compressServe(mw, "gzip", "application/json", `{"ok":true}`)
	if got := rec.Header().Get("Content-Encoding"); got != "" {
		t.Fatalf("disabled compress should not set Content-Encoding, got %q", got)
	}
}

func TestCompress_NoAcceptEncoding_NoCompression(t *testing.T) {
	mw := Compress(CompressConfig{Enabled: true})
	rec := compressServe(mw, "", "application/json", `{"ok":true}`)
	if got := rec.Header().Get("Content-Encoding"); got != "" {
		t.Fatalf("no Accept-Encoding → no compression, got %q", got)
	}
}

func TestCompress_EligibleContentType_Compresses(t *testing.T) {
	mw := Compress(CompressConfig{Enabled: true})
	rec := compressServe(mw, "gzip", "application/json", strings.Repeat("x", 1000))

	if got := rec.Header().Get("Content-Encoding"); got != "gzip" {
		t.Fatalf("want gzip Content-Encoding, got %q", got)
	}

	gr, err := gzip.NewReader(rec.Body)
	if err != nil {
		t.Fatalf("response is not valid gzip: %v", err)
	}
	defer func() { _ = gr.Close() }()
	decoded, _ := io.ReadAll(gr)
	if string(decoded) != strings.Repeat("x", 1000) {
		t.Fatal("decoded body does not match original")
	}
}

func TestCompress_IneligibleContentType_NoCompression(t *testing.T) {
	mw := Compress(CompressConfig{
		Enabled:      true,
		ContentTypes: []string{"application/json"},
	})
	rec := compressServe(mw, "gzip", "image/png", strings.Repeat("x", 1000))
	if got := rec.Header().Get("Content-Encoding"); got == "gzip" {
		t.Fatal("image/png should not be compressed")
	}
}

func TestCompress_VaryHeaderAdded(t *testing.T) {
	mw := Compress(CompressConfig{Enabled: true})
	rec := compressServe(mw, "gzip", "application/json", strings.Repeat("x", 100))
	found := false
	for _, v := range rec.Result().Header["Vary"] {
		if strings.Contains(v, "Accept-Encoding") {
			found = true
		}
	}
	if !found {
		t.Fatal("expected Vary: Accept-Encoding header")
	}
}

func TestCompress_ContentLengthRemoved(t *testing.T) {
	mw := Compress(CompressConfig{Enabled: true})
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	rec := httptest.NewRecorder()
	mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Length", "999")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, strings.Repeat("y", 100))
	})).ServeHTTP(rec, req)

	if got := rec.Header().Get("Content-Length"); got == "999" {
		t.Fatal("Content-Length should be removed when compressing")
	}
}

func TestCompress_BestSpeedLevel(t *testing.T) {
	mw := Compress(CompressConfig{Enabled: true, Level: gzip.BestSpeed})
	rec := compressServe(mw, "gzip", "application/json", strings.Repeat("z", 500))
	if got := rec.Header().Get("Content-Encoding"); got != "gzip" {
		t.Fatalf("want gzip, got %q", got)
	}
}

func TestCompress_CustomContentType(t *testing.T) {
	mw := Compress(CompressConfig{
		Enabled:      true,
		ContentTypes: []string{"text/csv"},
	})
	rec := compressServe(mw, "gzip", "text/csv", strings.Repeat("a,b\n", 200))
	if got := rec.Header().Get("Content-Encoding"); got != "gzip" {
		t.Fatalf("want gzip for text/csv, got %q", got)
	}
}

func TestCompress_NoContent204_NoGzipTrailer(t *testing.T) {
	mw := Compress(CompressConfig{Enabled: true})
	req := httptest.NewRequest(http.MethodPost, "/flush", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	rec := httptest.NewRecorder()
	mw(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})).ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("want 204, got %d", rec.Code)
	}
	if got := rec.Header().Get("Content-Encoding"); got == "gzip" {
		t.Fatal("204 response must not have Content-Encoding: gzip")
	}
	if rec.Body.Len() != 0 {
		t.Fatalf("204 response body must be empty, got %d bytes", rec.Body.Len())
	}
}

func TestCompress_NotModified304_NoGzipTrailer(t *testing.T) {
	mw := Compress(CompressConfig{Enabled: true})
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	rec := httptest.NewRecorder()
	mw(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotModified)
	})).ServeHTTP(rec, req)

	if rec.Code != http.StatusNotModified {
		t.Fatalf("want 304, got %d", rec.Code)
	}
	if got := rec.Header().Get("Content-Encoding"); got == "gzip" {
		t.Fatal("304 response must not have Content-Encoding: gzip")
	}
}

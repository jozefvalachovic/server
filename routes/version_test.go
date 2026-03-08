package routes_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jozefvalachovic/server/routes"
)

func TestVersionedGroup(t *testing.T) {
	mux := http.NewServeMux()

	registrar := func(mux *http.ServeMux) {
		mux.HandleFunc("GET /items", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("v1-items"))
		})
	}
	routes.VersionedGroup(mux, "/v1", registrar)

	// Request to /v1/items should hit the handler.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/items", nil)
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	if got := rec.Body.String(); got != "v1-items" {
		t.Fatalf("want body %q, got %q", "v1-items", got)
	}
}

func TestVersionPrefix(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("path=" + r.URL.Path))
	})

	handler := routes.VersionPrefix("/v2")(inner)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v2/users", nil)
	handler.ServeHTTP(rec, req)

	if got := rec.Body.String(); got != "path=/users" {
		t.Fatalf("want path=/users, got %q", got)
	}
}

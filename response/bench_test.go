package response

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// ── shared fixtures ───────────────────────────────────────────────────────────

type benchProduct struct {
	ID          int     `json:"id"`
	Name        string  `json:"name"`
	Description string  `json:"description"`
	Price       float64 `json:"price"`
	InStock     bool    `json:"inStock"`
}

var sampleProduct = benchProduct{ID: 1, Name: "Widget Pro", Description: "Industrial-grade widget", Price: 49.99, InStock: true}

func makeProducts(n int) []benchProduct {
	out := make([]benchProduct, n)
	for i := range out {
		out[i] = benchProduct{ID: i + 1, Name: "Product", Description: "Desc", Price: 9.99, InStock: true}
	}
	return out
}

// ── APIResponseWriter ─────────────────────────────────────────────────────────

func BenchmarkAPIResponseWriter_Scalar(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		rec := httptest.NewRecorder()
		APIResponseWriter(rec, sampleProduct, http.StatusOK)
	}
}

func BenchmarkAPIResponseWriter_Slice10(b *testing.B) {
	products := makeProducts(10)
	b.ReportAllocs()
	for b.Loop() {
		rec := httptest.NewRecorder()
		APIResponseWriter(rec, products, http.StatusOK)
	}
}

func BenchmarkAPIResponseWriter_Slice100(b *testing.B) {
	products := makeProducts(100)
	b.ReportAllocs()
	for b.Loop() {
		rec := httptest.NewRecorder()
		APIResponseWriter(rec, products, http.StatusOK)
	}
}

// ── APIResponseWriterWithPagination ──────────────────────────────────────────

func BenchmarkAPIResponseWriterWithPagination(b *testing.B) {
	products := makeProducts(50)
	b.ReportAllocs()
	for b.Loop() {
		rec := httptest.NewRecorder()
		APIResponseWriterWithPagination(rec, products, http.StatusOK, 50, 0, 200)
	}
}

// ── APIErrorWriter ────────────────────────────────────────────────────────────

func BenchmarkAPIErrorWriter(b *testing.B) {
	msg := "resource not found"
	apiErr := APIError[benchProduct]{
		Code:    http.StatusNotFound,
		Message: msg,
		Error:   &msg,
	}
	b.ReportAllocs()
	for b.Loop() {
		rec := httptest.NewRecorder()
		APIErrorWriter(rec, apiErr)
	}
}

// ── Shorthand methods ─────────────────────────────────────────────────────────

func BenchmarkAPIUnauthorized(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		rec := httptest.NewRecorder()
		APIUnauthorized(rec, "Authorization header required")
	}
}

func BenchmarkAPIForbidden(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		rec := httptest.NewRecorder()
		APIForbidden(rec, "Insufficient permissions")
	}
}

func BenchmarkAPIBadRequest(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		rec := httptest.NewRecorder()
		APIBadRequest(rec, "Validation failed", "field 'name' is required")
	}
}

func BenchmarkAPINotFound(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		rec := httptest.NewRecorder()
		APINotFound(rec, "Resource not found")
	}
}

func BenchmarkAPINoContent(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		rec := httptest.NewRecorder()
		APINoContent(rec)
	}
}

func BenchmarkAPICreated(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		rec := httptest.NewRecorder()
		APICreated(rec, sampleProduct, "/products/1")
	}
}

func BenchmarkAPIResponseWriterWithMessage(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		rec := httptest.NewRecorder()
		APIResponseWriterWithMessage(rec, sampleProduct, http.StatusOK, "Product retrieved")
	}
}

func BenchmarkAPIResponseWriterWithCursorPagination(b *testing.B) {
	products := makeProducts(50)
	cursor := ResponseCursorPagination{NextCursor: "abc123", HasMore: true, PageSize: 50}
	b.ReportAllocs()
	for b.Loop() {
		rec := httptest.NewRecorder()
		APIResponseWriterWithCursorPagination(rec, products, http.StatusOK, cursor)
	}
}

func BenchmarkAPIResponseWriterWithWarnings(b *testing.B) {
	warnings := []string{"deprecated endpoint", "use /v2/products instead"}
	b.ReportAllocs()
	for b.Loop() {
		rec := httptest.NewRecorder()
		APIResponseWriterWithWarnings(rec, sampleProduct, http.StatusOK, warnings)
	}
}

func BenchmarkAPIResponseWriterWithETag(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		rec := httptest.NewRecorder()
		r, _ := http.NewRequest("GET", "/products/1", nil)
		APIResponseWriterWithETag(rec, r, sampleProduct, http.StatusOK)
	}
}

func BenchmarkAPIResponseWriterWithETag_304(b *testing.B) {
	// Pre-compute the ETag.
	rec0 := httptest.NewRecorder()
	r0, _ := http.NewRequest("GET", "/products/1", nil)
	APIResponseWriterWithETag(rec0, r0, sampleProduct, http.StatusOK)
	etag := rec0.Header().Get("ETag")

	b.ReportAllocs()
	for b.Loop() {
		rec := httptest.NewRecorder()
		r, _ := http.NewRequest("GET", "/products/1", nil)
		r.Header.Set("If-None-Match", etag)
		APIResponseWriterWithETag(rec, r, sampleProduct, http.StatusOK)
	}
}

func BenchmarkSSEWriter_Send(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		rec := httptest.NewRecorder()
		r, _ := http.NewRequest("GET", "/stream", nil)
		sw := NewSSEWriter[benchProduct](rec, r)
		_ = sw.Send(sampleProduct)
	}
}

// ── ValidateAndDecode ─────────────────────────────────────────────────────────

type benchInput struct {
	Name  string  `json:"name"`
	Price float64 `json:"price"`
}

func BenchmarkValidateAndDecode_Valid(b *testing.B) {
	body := `{"name":"Widget","price":19.99}`
	b.ReportAllocs()
	for b.Loop() {
		r, _ := http.NewRequest(http.MethodPost, "/", strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		_, _ = ValidateAndDecode[benchInput](r)
	}
}

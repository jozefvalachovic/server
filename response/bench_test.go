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

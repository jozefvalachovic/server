package main

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jozefvalachovic/server/middleware"
	"github.com/jozefvalachovic/server/routes"
)

// ── JSON payloads ─────────────────────────────────────────────────────────────

const (
	smallPostJSON = `{"name":"Widget","description":"A widget","price":9.99}`
	smallPutJSON  = `{"name":"Updated Widget","price":19.99,"inStock":false}`
)

var (
	bigPostJSON string
	bigPutJSON  string
)

func init() {
	// ~4 KB payload — long description + name.
	bigPostJSON = `{"name":"` + strings.Repeat("Enterprise Widget Pro Max Ultra ", 10) +
		`","description":"` + strings.Repeat("This is a very detailed product description that goes on and on. ", 50) +
		`","price":999.99}`

	bigPutJSON = `{"name":"` + strings.Repeat("Updated Enterprise Widget Pro Max ", 10) +
		`","description":"` + strings.Repeat("This updated description is equally verbose and detailed for testing. ", 50) +
		`","price":1999.99,"inStock":false}`
}

// ── POST benchmarks ──────────────────────────────────────────────────────────

func BenchmarkPOST_SmallJSON(b *testing.B) {
	resetProducts()
	body := []byte(smallPostJSON)
	b.ReportAllocs()
	b.SetBytes(int64(len(body)))
	b.ResetTimer()
	for b.Loop() {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/products", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		createProduct(rec, req)
	}
}

func BenchmarkPOST_BigJSON(b *testing.B) {
	resetProducts()
	body := []byte(bigPostJSON)
	b.ReportAllocs()
	b.SetBytes(int64(len(body)))
	b.ResetTimer()
	for b.Loop() {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/products", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		createProduct(rec, req)
	}
}

// ── PUT benchmarks ───────────────────────────────────────────────────────────

func BenchmarkPUT_SmallJSON(b *testing.B) {
	resetProducts()
	body := []byte(smallPutJSON)
	b.ReportAllocs()
	b.SetBytes(int64(len(body)))
	b.ResetTimer()
	for b.Loop() {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPut, "/products/1", bytes.NewReader(body))
		req.SetPathValue("id", "1")
		req.Header.Set("Content-Type", "application/json")
		updateProduct(rec, req)
	}
}

func BenchmarkPUT_BigJSON(b *testing.B) {
	resetProducts()
	body := []byte(bigPutJSON)
	b.ReportAllocs()
	b.SetBytes(int64(len(body)))
	b.ResetTimer()
	for b.Loop() {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPut, "/products/1", bytes.NewReader(body))
		req.SetPathValue("id", "1")
		req.Header.Set("Content-Type", "application/json")
		updateProduct(rec, req)
	}
}

// ── GET benchmarks (cache miss — direct handler call) ────────────────────────

func BenchmarkGET_SmallJSON_CacheMiss(b *testing.B) {
	resetProducts() // 3 small seed products
	handler := buildCachedListHandler(b)

	// Unique query per iteration guarantees a cache miss + full serialization.
	b.ReportAllocs()
	b.ResetTimer()
	var i int
	for b.Loop() {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/products?_miss=%d", i), nil)
		i++
		handler.ServeHTTP(rec, req)
	}
}

func BenchmarkGET_BigJSON_CacheMiss(b *testing.B) {
	// 50 big products — same dataset as cache-hit benchmark for fair comparison.
	// Each iteration uses a unique query to guarantee a cache miss + full serialization.
	seedBigProducts()
	handler := buildCachedListHandler(b)

	b.ReportAllocs()
	b.ResetTimer()
	var i int
	for b.Loop() {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/products?limit=50&_miss=%d", i), nil)
		i++
		handler.ServeHTTP(rec, req)
	}
}

// ── GET benchmarks (cache hit — listProducts behind cache middleware) ────────

func BenchmarkGET_SmallJSON_CacheHit(b *testing.B) {
	resetProducts() // 3 small seed products
	handler := buildCachedListHandler(b)

	// Prime the cache.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/products", nil)
	handler.ServeHTTP(rec, req)

	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		rec = httptest.NewRecorder()
		req = httptest.NewRequest(http.MethodGet, "/products", nil)
		handler.ServeHTTP(rec, req)
	}
}

func BenchmarkGET_BigJSON_CacheHit(b *testing.B) {
	// Same 50 big products as cache-miss benchmark — only difference is cache state.
	seedBigProducts()
	handler := buildCachedListHandler(b)

	// Prime the cache with a single request.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/products?limit=50", nil)
	handler.ServeHTTP(rec, req)

	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		rec = httptest.NewRecorder()
		req = httptest.NewRequest(http.MethodGet, "/products?limit=50", nil)
		handler.ServeHTTP(rec, req)
	}
}

// ── DELETE benchmarks ────────────────────────────────────────────────────────

func BenchmarkDELETE(b *testing.B) {
	resetProducts()
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		// Seed one product, then delete it.
		mu.Lock()
		id := nextID
		products = append(products, Product{ID: id, Name: "x", Price: 1})
		nextID++
		mu.Unlock()

		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodDelete, fmt.Sprintf("/products/%d", id), nil)
		req.SetPathValue("id", strconv.Itoa(id))
		deleteProduct(rec, req)
	}
}

// ── helpers ──────────────────────────────────────────────────────────────────

// seedBigProducts populates the store with 50 large products (~190 KB total response).
func seedBigProducts() {
	mu.Lock()
	products = make([]Product, 50)
	for i := range products {
		products[i] = Product{
			ID:          i + 1,
			Name:        strings.Repeat("Enterprise Widget Pro Max Ultra ", 10),
			Description: strings.Repeat("Detailed product description repeated many times for benchmarking. ", 50),
			Price:       float64(i+1) * 9.99,
			InStock:     i%2 == 0,
		}
	}
	nextID = 51
	mu.Unlock()
}

func buildCachedListHandler(b *testing.B) http.Handler {
	b.Helper()
	mux := http.NewServeMux()
	cfg := &routes.CacheConfig{
		DefaultTTL:      300 * time.Second,
		CleanupInterval: 30 * time.Second,
		MaxSize:         300,
	}
	store, err := routes.RegisterRoutes(mux, cfg, func(m *http.ServeMux) {
		m.HandleFunc("/products", routes.CachedRouteHandler(
			routes.Routes{http.MethodGet: listProducts},
			middleware.HTTPCacheConfig{
				KeyPrefix: func(r *http.Request) string {
					if q := r.URL.RawQuery; q != "" {
						return "bench_products_" + q
					}
					return "bench_products"
				},
			},
		))
	})
	if err != nil {
		b.Fatalf("register routes: %v", err)
	}
	b.Cleanup(store.Stop)
	return mux
}

package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jozefvalachovic/server/response"
)

// ── helpers ────────────────────────────────────────────────────────────────

// decodeAPIResponse unmarshals the response body into an APIResponse[T].
func decodeAPIResponse[T any](t *testing.T, body []byte) response.APIResponse[T] {
	t.Helper()
	var resp response.APIResponse[T]
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("failed to decode APIResponse: %v\nbody: %s", err, body)
	}
	return resp
}

// decodeAPIError unmarshals the response body into an APIError[any].

// resetProducts resets the in-memory store to the default 3 seed products.
// Call at the beginning of every test that mutates state.
func resetProducts() {
	mu.Lock()
	products = []Product{
		{ID: 1, Name: "Widget Pro", Description: "Industrial-grade widget", Price: 49.99, InStock: true},
		{ID: 2, Name: "Gadget Mini", Description: "Compact gadget for everyday use", Price: 19.99, InStock: true},
		{ID: 3, Name: "Doohickey Max", Description: "The biggest doohickey on the market", Price: 99.99, InStock: false},
	}
	nextID = 4
	mu.Unlock()
}

// ── listProducts ──────────────────────────────────────────────────────────

func TestListProducts_DefaultPagination(t *testing.T) {
	resetProducts()
	r := httptest.NewRequest(http.MethodGet, "/products", nil)
	w := httptest.NewRecorder()
	listProducts(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	resp := decodeAPIResponse[[]Product](t, w.Body.Bytes())
	if resp.Data == nil {
		t.Fatal("expected non-nil data")
	}
	if len(*resp.Data) != 3 {
		t.Fatalf("expected 3 products, got %d", len(*resp.Data))
	}
	if resp.Pagination == nil {
		t.Fatal("expected pagination metadata")
	}
	if resp.Pagination.TotalCount != 3 {
		t.Fatalf("expected totalCount=3, got %d", resp.Pagination.TotalCount)
	}
}

func TestListProducts_CustomLimitOffset(t *testing.T) {
	resetProducts()
	r := httptest.NewRequest(http.MethodGet, "/products?limit=1&offset=1", nil)
	w := httptest.NewRecorder()
	listProducts(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	resp := decodeAPIResponse[[]Product](t, w.Body.Bytes())
	if len(*resp.Data) != 1 {
		t.Fatalf("expected 1 product, got %d", len(*resp.Data))
	}
	if (*resp.Data)[0].ID != 2 {
		t.Fatalf("expected product ID 2, got %d", (*resp.Data)[0].ID)
	}
}

func TestListProducts_OffsetBeyondRange(t *testing.T) {
	resetProducts()
	r := httptest.NewRequest(http.MethodGet, "/products?offset=100", nil)
	w := httptest.NewRecorder()
	listProducts(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	resp := decodeAPIResponse[[]Product](t, w.Body.Bytes())
	if len(*resp.Data) != 0 {
		t.Fatalf("expected 0 products, got %d", len(*resp.Data))
	}
}

func TestListProducts_EmptyStore(t *testing.T) {
	mu.Lock()
	products = nil
	mu.Unlock()
	defer resetProducts()

	r := httptest.NewRequest(http.MethodGet, "/products", nil)
	w := httptest.NewRecorder()
	listProducts(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	resp := decodeAPIResponse[[]Product](t, w.Body.Bytes())
	if len(*resp.Data) != 0 {
		t.Fatalf("expected empty list, got %d", len(*resp.Data))
	}
}

// ── createProduct ─────────────────────────────────────────────────────────

func TestCreateProduct_HappyPath(t *testing.T) {
	resetProducts()
	body := `{"name":"Test Item","description":"A test","price":12.50}`
	r := httptest.NewRequest(http.MethodPost, "/products", bytes.NewBufferString(body))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	createProduct(w, r)

	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}
	resp := decodeAPIResponse[Product](t, w.Body.Bytes())
	if resp.Data.Name != "Test Item" {
		t.Fatalf("expected name 'Test Item', got %q", resp.Data.Name)
	}
	if resp.Data.ID != 4 {
		t.Fatalf("expected ID 4, got %d", resp.Data.ID)
	}
	if !resp.Data.InStock {
		t.Fatal("new products should be InStock=true")
	}
}

func TestCreateProduct_MissingBody(t *testing.T) {
	resetProducts()
	r := httptest.NewRequest(http.MethodPost, "/products", nil)
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	createProduct(w, r)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

func TestCreateProduct_MalformedJSON(t *testing.T) {
	resetProducts()
	r := httptest.NewRequest(http.MethodPost, "/products", bytes.NewBufferString(`{bad`))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	createProduct(w, r)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

// ── getProduct ────────────────────────────────────────────────────────────

func TestGetProduct_Exists(t *testing.T) {
	resetProducts()
	r := httptest.NewRequest(http.MethodGet, "/products/1", nil)
	r.SetPathValue("id", "1")
	w := httptest.NewRecorder()
	getProduct(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	resp := decodeAPIResponse[Product](t, w.Body.Bytes())
	if resp.Data.ID != 1 {
		t.Fatalf("expected product ID 1, got %d", resp.Data.ID)
	}
	if resp.Data.Name != "Widget Pro" {
		t.Fatalf("expected 'Widget Pro', got %q", resp.Data.Name)
	}
}

func TestGetProduct_NotFound(t *testing.T) {
	resetProducts()
	r := httptest.NewRequest(http.MethodGet, "/products/999", nil)
	r.SetPathValue("id", "999")
	w := httptest.NewRecorder()
	getProduct(w, r)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
	}
}

func TestGetProduct_InvalidID(t *testing.T) {
	resetProducts()
	r := httptest.NewRequest(http.MethodGet, "/products/abc", nil)
	r.SetPathValue("id", "abc")
	w := httptest.NewRecorder()
	getProduct(w, r)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

// ── updateProduct ─────────────────────────────────────────────────────────

func TestUpdateProduct_PartialName(t *testing.T) {
	resetProducts()
	body := `{"name":"Updated Widget"}`
	r := httptest.NewRequest(http.MethodPut, "/products/1", bytes.NewBufferString(body))
	r.SetPathValue("id", "1")
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	updateProduct(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	resp := decodeAPIResponse[Product](t, w.Body.Bytes())
	if resp.Data.Name != "Updated Widget" {
		t.Fatalf("expected 'Updated Widget', got %q", resp.Data.Name)
	}
	// Price should be unchanged.
	if resp.Data.Price != 49.99 {
		t.Fatalf("expected price 49.99, got %f", resp.Data.Price)
	}
}

func TestUpdateProduct_InStockToggle(t *testing.T) {
	resetProducts()
	body := `{"inStock":false}`
	r := httptest.NewRequest(http.MethodPut, "/products/1", bytes.NewBufferString(body))
	r.SetPathValue("id", "1")
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	updateProduct(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	resp := decodeAPIResponse[Product](t, w.Body.Bytes())
	if resp.Data.InStock {
		t.Fatal("expected InStock=false after update")
	}
}

func TestUpdateProduct_NotFound(t *testing.T) {
	resetProducts()
	body := `{"name":"ghost"}`
	r := httptest.NewRequest(http.MethodPut, "/products/999", bytes.NewBufferString(body))
	r.SetPathValue("id", "999")
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	updateProduct(w, r)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
	}
}

func TestUpdateProduct_InvalidID(t *testing.T) {
	resetProducts()
	body := `{"name":"x"}`
	r := httptest.NewRequest(http.MethodPut, "/products/abc", bytes.NewBufferString(body))
	r.SetPathValue("id", "abc")
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	updateProduct(w, r)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

// ── deleteProduct ─────────────────────────────────────────────────────────

func TestDeleteProduct_Exists(t *testing.T) {
	resetProducts()
	r := httptest.NewRequest(http.MethodDelete, "/products/1", nil)
	r.SetPathValue("id", "1")
	w := httptest.NewRecorder()
	deleteProduct(w, r)

	if w.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", w.Code)
	}

	// Verify removed.
	mu.RLock()
	for _, p := range products {
		if p.ID == 1 {
			t.Fatal("product 1 should have been deleted")
		}
	}
	mu.RUnlock()
}

func TestDeleteProduct_NotFound(t *testing.T) {
	resetProducts()
	r := httptest.NewRequest(http.MethodDelete, "/products/999", nil)
	r.SetPathValue("id", "999")
	w := httptest.NewRecorder()
	deleteProduct(w, r)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
	}
}

func TestDeleteProduct_InvalidID(t *testing.T) {
	resetProducts()
	r := httptest.NewRequest(http.MethodDelete, "/products/abc", nil)
	r.SetPathValue("id", "abc")
	w := httptest.NewRecorder()
	deleteProduct(w, r)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

// ── getAdmin ──────────────────────────────────────────────────────────────

func TestGetAdmin_WithHeader(t *testing.T) {
	resetProducts()
	r := httptest.NewRequest(http.MethodGet, "/admin", nil)
	r.Header.Set("X-Admin", "1")
	w := httptest.NewRecorder()
	getAdmin(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	resp := decodeAPIResponse[AdminStats](t, w.Body.Bytes())
	if resp.Data.TotalProducts != 3 {
		t.Fatalf("expected 3 products, got %d", resp.Data.TotalProducts)
	}
	if resp.Data.ServerVersion != "1.0.0" {
		t.Fatalf("expected version '1.0.0', got %q", resp.Data.ServerVersion)
	}
}

func TestGetAdmin_Forbidden(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/admin", nil)
	w := httptest.NewRecorder()
	getAdmin(w, r)

	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", w.Code)
	}
}

// ── getIP ─────────────────────────────────────────────────────────────────

func TestGetIP_ReturnsIP(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/ip", nil)
	r.RemoteAddr = "192.168.1.42:12345"
	w := httptest.NewRecorder()
	getIP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var raw map[string]json.RawMessage
	_ = json.Unmarshal(w.Body.Bytes(), &raw)
	var data struct {
		IP string `json:"ip"`
	}
	_ = json.Unmarshal(raw["data"], &data)
	if data.IP != "192.168.1.42" {
		t.Fatalf("expected IP '192.168.1.42', got %q", data.IP)
	}
}

func TestGetIP_XForwardedFor(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/ip", nil)
	r.Header.Set("X-Forwarded-For", "10.0.0.1, 10.0.0.2")
	w := httptest.NewRecorder()
	getIP(w, r)

	var raw map[string]json.RawMessage
	_ = json.Unmarshal(w.Body.Bytes(), &raw)
	var data struct {
		IP string `json:"ip"`
	}
	_ = json.Unmarshal(raw["data"], &data)
	if data.IP != "10.0.0.1" {
		t.Fatalf("expected first IP '10.0.0.1', got %q", data.IP)
	}
}

// ── validateEmail ─────────────────────────────────────────────────────────

func TestValidateEmail_Valid(t *testing.T) {
	body := `{"email":"  Hello@Example.COM  "}`
	r := httptest.NewRequest(http.MethodPost, "/validate-email", bytes.NewBufferString(body))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	validateEmail(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var raw map[string]json.RawMessage
	_ = json.Unmarshal(w.Body.Bytes(), &raw)
	var data struct {
		Sanitized string `json:"sanitized"`
		Valid     bool   `json:"valid"`
	}
	_ = json.Unmarshal(raw["data"], &data)
	if data.Sanitized != "hello@example.com" {
		t.Fatalf("expected lowercase trimmed email, got %q", data.Sanitized)
	}
	if !data.Valid {
		t.Fatal("expected valid=true")
	}
}

func TestValidateEmail_Invalid(t *testing.T) {
	body := `{"email":"not-an-email"}`
	r := httptest.NewRequest(http.MethodPost, "/validate-email", bytes.NewBufferString(body))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	validateEmail(w, r)

	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422, got %d", w.Code)
	}
}

func TestValidateEmail_MissingBody(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/validate-email", nil)
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	validateEmail(w, r)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

// ── cacheDemo ─────────────────────────────────────────────────────────────

func TestCacheDemo_ReturnsStoredValue(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/cache-demo", nil)
	w := httptest.NewRecorder()
	cacheDemo(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var raw map[string]json.RawMessage
	_ = json.Unmarshal(w.Body.Bytes(), &raw)
	var data struct {
		StoredValue string `json:"storedValue"`
	}
	_ = json.Unmarshal(raw["data"], &data)
	if data.StoredValue != "hello from cache" {
		t.Fatalf("expected 'hello from cache', got %q", data.StoredValue)
	}
}

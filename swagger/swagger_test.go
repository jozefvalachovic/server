package swagger

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"
)

// ── Handler ──────────────────────────────────────────────────────────────────

func TestHandler_ServesHTML(t *testing.T) {
	h := Handler(Config{
		Title:   "Test API",
		Version: "1.0.0",
	})
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", w.Code)
	}
	ct := w.Header().Get("Content-Type")
	if !strings.Contains(ct, "text/html") {
		t.Fatalf("want text/html, got %s", ct)
	}
	body := w.Body.String()
	if !strings.Contains(body, "Test API") {
		t.Fatal("page should contain the API title")
	}
}

func TestHandler_ServesCSS(t *testing.T) {
	h := Handler(Config{Title: "Test"})
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/style.css", nil)
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", w.Code)
	}
	if !strings.Contains(w.Header().Get("Content-Type"), "text/css") {
		t.Fatalf("want text/css content type, got %s", w.Header().Get("Content-Type"))
	}
}

func TestHandler_ServesJS(t *testing.T) {
	h := Handler(Config{Title: "Test"})
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/script.js", nil)
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", w.Code)
	}
	if !strings.Contains(w.Header().Get("Content-Type"), "javascript") {
		t.Fatalf("want javascript content type, got %s", w.Header().Get("Content-Type"))
	}
}

func TestHandler_CSP_Header(t *testing.T) {
	h := Handler(Config{Title: "Test"})
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	h.ServeHTTP(w, r)
	csp := w.Header().Get("Content-Security-Policy")
	if csp == "" {
		t.Fatal("CSP header should be set")
	}
	if !strings.Contains(csp, "script-src 'self'") {
		t.Fatalf("CSP should allow self scripts, got %s", csp)
	}
}

func TestHandler_WithEndpoints(t *testing.T) {
	type Item struct {
		ID   int    `json:"id"`
		Name string `json:"name"`
	}
	h := Handler(Config{
		Title:   "Test",
		Version: "2.0",
		Endpoints: []Endpoint{
			{Method: GET, Path: "/items", Summary: "List items", Response: Item{}},
			{Method: POST, Path: "/items", Summary: "Create item", Request: Item{}, Response: Item{}},
		},
	})
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	h.ServeHTTP(w, r)
	body := w.Body.String()
	if !strings.Contains(body, "/items") {
		t.Fatal("page should contain endpoint paths")
	}
	if !strings.Contains(body, "List items") {
		t.Fatal("page should contain endpoint summaries")
	}
}

// ── schemaFields ─────────────────────────────────────────────────────────────

func TestSchemaFields_BasicStruct(t *testing.T) {
	type Example struct {
		ID   int    `json:"id"`
		Name string `json:"name"`
	}
	fields := schemaFields(reflect.TypeFor[Example]())
	if len(fields) != 2 {
		t.Fatalf("want 2 fields, got %d", len(fields))
	}
	if fields[0].Name != "id" || fields[0].Type != "integer" {
		t.Fatalf("field 0: want id/integer, got %s/%s", fields[0].Name, fields[0].Type)
	}
	if fields[1].Name != "name" || fields[1].Type != "string" {
		t.Fatalf("field 1: want name/string, got %s/%s", fields[1].Name, fields[1].Type)
	}
}

func TestSchemaFields_JsonDash_Skipped(t *testing.T) {
	type Example struct {
		Public  string `json:"public"`
		Private string `json:"-"`
	}
	fields := schemaFields(reflect.TypeFor[Example]())
	if len(fields) != 1 {
		t.Fatalf("want 1 field (json:\"-\" skipped), got %d", len(fields))
	}
	if fields[0].Name != "public" {
		t.Fatalf("want public, got %s", fields[0].Name)
	}
}

func TestSchemaFields_Pointer_Unwrapped(t *testing.T) {
	type Example struct {
		Val *string `json:"val"`
	}
	fields := schemaFields(reflect.TypeFor[Example]())
	if len(fields) != 1 {
		t.Fatalf("want 1 field, got %d", len(fields))
	}
	if fields[0].Type != "string" {
		t.Fatalf("pointer to string should resolve to string, got %s", fields[0].Type)
	}
	if fields[0].Required {
		t.Fatal("pointer field should not be required")
	}
}

func TestSchemaFields_Required(t *testing.T) {
	type Example struct {
		Req string `json:"req"`
		Opt string `json:"opt,omitempty"`
	}
	fields := schemaFields(reflect.TypeFor[Example]())
	reqField := fields[0]
	optField := fields[1]
	if !reqField.Required {
		t.Fatal("non-pointer, non-omitempty field should be required")
	}
	if optField.Required {
		t.Fatal("omitempty field should not be required")
	}
}

func TestSchemaFields_EmbeddedStruct_Inlined(t *testing.T) {
	type Base struct {
		ID int `json:"id"`
	}
	type Item struct {
		Base
		Name string `json:"name"`
	}
	fields := schemaFields(reflect.TypeFor[Item]())
	if len(fields) != 2 {
		t.Fatalf("want 2 fields (embedded inlined), got %d", len(fields))
	}
	names := make(map[string]bool)
	for _, f := range fields {
		names[f.Name] = true
	}
	if !names["id"] || !names["name"] {
		t.Fatalf("want id and name fields, got %v", names)
	}
}

func TestSchemaFields_NestedStruct(t *testing.T) {
	type Address struct {
		City string `json:"city"`
	}
	type Person struct {
		Name    string  `json:"name"`
		Address Address `json:"address"`
	}
	fields := schemaFields(reflect.TypeFor[Person]())
	if len(fields) != 2 {
		t.Fatalf("want 2 fields, got %d", len(fields))
	}
	addrField := fields[1]
	if addrField.Name != "address" {
		t.Fatalf("want address, got %s", addrField.Name)
	}
	if len(addrField.Fields) != 1 {
		t.Fatalf("address should have 1 child field, got %d", len(addrField.Fields))
	}
}

func TestSchemaFields_PointerToStruct(t *testing.T) {
	type Example struct {
		ID int `json:"id"`
	}
	fields := schemaFields(reflect.TypeFor[*Example]())
	if len(fields) != 1 {
		t.Fatalf("pointer to struct should resolve, got %d fields", len(fields))
	}
}

func TestSchemaFields_NonStruct(t *testing.T) {
	fields := schemaFields(reflect.TypeFor[string]())
	if fields != nil {
		t.Fatal("non-struct type should return nil")
	}
}

// ── goTypeName ───────────────────────────────────────────────────────────────

func TestGoTypeName(t *testing.T) {
	tests := []struct {
		typ  reflect.Type
		want string
	}{
		{reflect.TypeFor[string](), "string"},
		{reflect.TypeFor[bool](), "boolean"},
		{reflect.TypeFor[int](), "integer"},
		{reflect.TypeFor[int64](), "integer"},
		{reflect.TypeFor[uint32](), "integer"},
		{reflect.TypeFor[float64](), "number"},
		{reflect.TypeFor[float32](), "number"},
		{reflect.TypeFor[time.Time](), "string (datetime)"},
		{reflect.TypeFor[[]string](), "array<string>"},
		{reflect.TypeFor[map[string]int](), "object"},
	}
	for _, tc := range tests {
		got := goTypeName(tc.typ)
		if got != tc.want {
			t.Errorf("goTypeName(%s) = %q, want %q", tc.typ, got, tc.want)
		}
	}
}

func TestGoTypeName_PointerUnwrap(t *testing.T) {
	got := goTypeName(reflect.TypeFor[*time.Time]())
	if got != "string (datetime)" {
		t.Fatalf("pointer to time.Time should be 'string (datetime)', got %q", got)
	}
}

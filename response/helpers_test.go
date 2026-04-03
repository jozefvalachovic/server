package response

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestValidateAndDecode_Success(t *testing.T) {
	type payload struct {
		Name string `json:"name"`
	}

	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"name":"alice"}`))
	got, apiErr := ValidateAndDecode[payload](req)

	if apiErr != nil {
		t.Fatalf("unexpected error: %+v", apiErr)
	}
	if got.Name != "alice" {
		t.Fatalf("want name=alice, got %q", got.Name)
	}
}

func TestValidateAndDecode_NilBody(t *testing.T) {
	type payload struct{ Name string }

	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req.Body = nil

	_, apiErr := ValidateAndDecode[payload](req)
	if apiErr == nil {
		t.Fatal("expected an error for nil body")
		return
	}
	if apiErr.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", apiErr.Code)
	}
}

func TestValidateAndDecode_EmptyBody(t *testing.T) {
	type payload struct{ Name string }

	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(nil))

	_, apiErr := ValidateAndDecode[payload](req)
	if apiErr == nil {
		t.Fatal("expected an error for empty body")
		return
	}
	if apiErr.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", apiErr.Code)
	}
}

func TestValidateAndDecode_InvalidJSON(t *testing.T) {
	type payload struct{ Name string }

	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{not json}`))

	_, apiErr := ValidateAndDecode[payload](req)
	if apiErr == nil {
		t.Fatal("expected an error for invalid JSON")
		return
	}
	if apiErr.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", apiErr.Code)
	}
}

func TestValidateAndDecode_ExtraFieldsRejected(t *testing.T) {
	type payload struct {
		Name string `json:"name"`
	}

	body := `{"name":"bob","extra":"unknown"}`
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))

	_, apiErr := ValidateAndDecode[payload](req)
	if apiErr == nil {
		t.Fatal("expected error for unknown field, got nil")
		return
	}
	if apiErr.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", apiErr.Code)
	}
}

func TestCreateEmptyData_SliceIsNotNil(t *testing.T) {
	result := CreateEmptyData[[]string]()
	if result == nil {
		t.Fatal("result pointer must not be nil")
		return
	}
	if *result == nil {
		t.Fatal("slice must be initialised (not nil)")
	}
}

func TestCreateEmptyData_MapIsNotNil(t *testing.T) {
	result := CreateEmptyData[map[string]int]()
	if result == nil {
		t.Fatal("result pointer must not be nil")
		return
	}
	if *result == nil {
		t.Fatal("map must be initialised (not nil)")
	}
}

func TestCreateEmptyData_StructPointerIsInitialised(t *testing.T) {
	type inner struct{ V int }
	result := CreateEmptyData[*inner]()
	if result == nil {
		t.Fatal("result must not be nil")
		return
	}
	if *result == nil {
		t.Fatal("pointer inside result must be initialised")
	}
}

func TestCreateEmptyData_PlainStruct(t *testing.T) {
	type pojo struct{ X, Y int }
	result := CreateEmptyData[pojo]()
	if result == nil {
		t.Fatal("result must not be nil")
	}
}

func TestCreateEmptyData_AnyType(t *testing.T) {
	result := CreateEmptyData[any]()
	if result == nil {
		t.Fatal("result must not be nil for any type")
	}
}

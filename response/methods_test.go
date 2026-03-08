package response

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAPIUnauthorized_StatusAndBody(t *testing.T) {
	rec := httptest.NewRecorder()
	APIUnauthorized(rec, "please log in")

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d", rec.Code)
	}

	var errResp APIError[any]
	if err := json.Unmarshal(rec.Body.Bytes(), &errResp); err != nil {
		t.Fatalf("decode failed: %v", err)
	}
	if errResp.Code != http.StatusUnauthorized {
		t.Fatalf("body code: want 401, got %d", errResp.Code)
	}
	if errResp.Message != "please log in" {
		t.Fatalf("unexpected message: %q", errResp.Message)
	}
	if errResp.Error == nil || *errResp.Error != "Unauthorized" {
		t.Fatalf("unexpected error field: %v", errResp.Error)
	}
}

func TestAPIUnauthorized_ContentType(t *testing.T) {
	rec := httptest.NewRecorder()
	APIUnauthorized(rec, "msg")

	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("want application/json, got %q", ct)
	}
}

func TestAPIUnauthorized_ValidJSON(t *testing.T) {
	rec := httptest.NewRecorder()
	APIUnauthorized(rec, "msg")

	var errResp APIError[any]
	if err := json.Unmarshal(rec.Body.Bytes(), &errResp); err != nil {
		t.Fatalf("401 response is not valid JSON: %v", err)
	}
}

func TestAPIForbidden_StatusAndBody(t *testing.T) {
	rec := httptest.NewRecorder()
	APIForbidden(rec, "insufficient permissions")

	if rec.Code != http.StatusForbidden {
		t.Fatalf("want 403, got %d", rec.Code)
	}

	var errResp APIError[any]
	if err := json.Unmarshal(rec.Body.Bytes(), &errResp); err != nil {
		t.Fatalf("decode failed: %v", err)
	}
	if errResp.Code != http.StatusForbidden {
		t.Fatalf("body code: want 403, got %d", errResp.Code)
	}
	if errResp.Message != "insufficient permissions" {
		t.Fatalf("unexpected message: %q", errResp.Message)
	}
	if errResp.Error == nil || *errResp.Error != "Forbidden" {
		t.Fatalf("unexpected error field: %v", errResp.Error)
	}
}

func TestAPIForbidden_ContentType(t *testing.T) {
	rec := httptest.NewRecorder()
	APIForbidden(rec, "msg")

	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("want application/json, got %q", ct)
	}
}

func TestAPIForbidden_ValidJSON(t *testing.T) {
	rec := httptest.NewRecorder()
	APIForbidden(rec, "msg")

	var errResp APIError[any]
	if err := json.Unmarshal(rec.Body.Bytes(), &errResp); err != nil {
		t.Fatalf("403 response is not valid JSON: %v", err)
	}
}

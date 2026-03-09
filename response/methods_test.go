package response

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// ── helpers ───────────────────────────────────────────────────────────────────

func decodeError(t *testing.T, rec *httptest.ResponseRecorder) APIError[any] {
	t.Helper()
	var e APIError[any]
	if err := json.Unmarshal(rec.Body.Bytes(), &e); err != nil {
		t.Fatalf("decode failed: %v", err)
	}
	return e
}

func assertStatus(t *testing.T, rec *httptest.ResponseRecorder, want int) {
	t.Helper()
	if rec.Code != want {
		t.Fatalf("want status %d, got %d", want, rec.Code)
	}
}

func assertJSON(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("want application/json, got %q", ct)
	}
}

// ── APIBadRequest ─────────────────────────────────────────────────────────────

func TestAPIBadRequest_StatusAndBody(t *testing.T) {
	rec := httptest.NewRecorder()
	APIBadRequest(rec, "validation failed", "field 'name' is required")

	assertStatus(t, rec, http.StatusBadRequest)
	e := decodeError(t, rec)
	if e.Code != 400 {
		t.Fatalf("body code: want 400, got %d", e.Code)
	}
	if e.Message != "validation failed" {
		t.Fatalf("unexpected message: %q", e.Message)
	}
	if e.Details != "field 'name' is required" {
		t.Fatalf("unexpected details: %q", e.Details)
	}
	if e.Error == nil || *e.Error != "Bad Request" {
		t.Fatalf("unexpected error field: %v", e.Error)
	}
}

func TestAPIBadRequest_ContentType(t *testing.T) {
	rec := httptest.NewRecorder()
	APIBadRequest(rec, "msg", "")
	assertJSON(t, rec)
}

// ── APIUnauthorized ───────────────────────────────────────────────────────────

func TestAPIUnauthorized_StatusAndBody(t *testing.T) {
	rec := httptest.NewRecorder()
	APIUnauthorized(rec, "please log in")

	assertStatus(t, rec, http.StatusUnauthorized)
	e := decodeError(t, rec)
	if e.Code != http.StatusUnauthorized {
		t.Fatalf("body code: want 401, got %d", e.Code)
	}
	if e.Message != "please log in" {
		t.Fatalf("unexpected message: %q", e.Message)
	}
	if e.Error == nil || *e.Error != "Unauthorized" {
		t.Fatalf("unexpected error field: %v", e.Error)
	}
}

func TestAPIUnauthorized_ContentType(t *testing.T) {
	rec := httptest.NewRecorder()
	APIUnauthorized(rec, "msg")
	assertJSON(t, rec)
}

func TestAPIUnauthorized_ValidJSON(t *testing.T) {
	rec := httptest.NewRecorder()
	APIUnauthorized(rec, "msg")
	_ = decodeError(t, rec)
}

// ── APIForbidden ──────────────────────────────────────────────────────────────

func TestAPIForbidden_StatusAndBody(t *testing.T) {
	rec := httptest.NewRecorder()
	APIForbidden(rec, "insufficient permissions")

	assertStatus(t, rec, http.StatusForbidden)
	e := decodeError(t, rec)
	if e.Code != http.StatusForbidden {
		t.Fatalf("body code: want 403, got %d", e.Code)
	}
	if e.Message != "insufficient permissions" {
		t.Fatalf("unexpected message: %q", e.Message)
	}
	if e.Error == nil || *e.Error != "Forbidden" {
		t.Fatalf("unexpected error field: %v", e.Error)
	}
}

func TestAPIForbidden_ContentType(t *testing.T) {
	rec := httptest.NewRecorder()
	APIForbidden(rec, "msg")
	assertJSON(t, rec)
}

func TestAPIForbidden_ValidJSON(t *testing.T) {
	rec := httptest.NewRecorder()
	APIForbidden(rec, "msg")
	_ = decodeError(t, rec)
}

// ── APINotFound ───────────────────────────────────────────────────────────────

func TestAPINotFound_StatusAndBody(t *testing.T) {
	rec := httptest.NewRecorder()
	APINotFound(rec, "user not found")

	assertStatus(t, rec, http.StatusNotFound)
	e := decodeError(t, rec)
	if e.Code != 404 {
		t.Fatalf("body code: want 404, got %d", e.Code)
	}
	if e.Message != "user not found" {
		t.Fatalf("unexpected message: %q", e.Message)
	}
	if e.Error == nil || *e.Error != "Not Found" {
		t.Fatalf("unexpected error: %v", e.Error)
	}
}

// ── APIConflict ───────────────────────────────────────────────────────────────

func TestAPIConflict_StatusAndBody(t *testing.T) {
	rec := httptest.NewRecorder()
	APIConflict(rec, "email already taken")

	assertStatus(t, rec, http.StatusConflict)
	e := decodeError(t, rec)
	if e.Code != 409 {
		t.Fatalf("body code: want 409, got %d", e.Code)
	}
	if e.Message != "email already taken" {
		t.Fatalf("unexpected message: %q", e.Message)
	}
	if e.Error == nil || *e.Error != "Conflict" {
		t.Fatalf("unexpected error: %v", e.Error)
	}
}

// ── APIInternalError ──────────────────────────────────────────────────────────

func TestAPIInternalError_StatusAndBody(t *testing.T) {
	rec := httptest.NewRecorder()
	APIInternalError(rec, "unexpected failure")

	assertStatus(t, rec, http.StatusInternalServerError)
	e := decodeError(t, rec)
	if e.Code != 500 {
		t.Fatalf("body code: want 500, got %d", e.Code)
	}
	if e.Message != "unexpected failure" {
		t.Fatalf("unexpected message: %q", e.Message)
	}
	if e.Error == nil || *e.Error != "Internal Server Error" {
		t.Fatalf("unexpected error: %v", e.Error)
	}
}

// ── APIServiceUnavailable ─────────────────────────────────────────────────────

func TestAPIServiceUnavailable_StatusAndBody(t *testing.T) {
	rec := httptest.NewRecorder()
	APIServiceUnavailable(rec, "try again later")

	assertStatus(t, rec, http.StatusServiceUnavailable)
	e := decodeError(t, rec)
	if e.Code != 503 {
		t.Fatalf("body code: want 503, got %d", e.Code)
	}
	if e.Message != "try again later" {
		t.Fatalf("unexpected message: %q", e.Message)
	}
	if e.Error == nil || *e.Error != "Service Unavailable" {
		t.Fatalf("unexpected error: %v", e.Error)
	}
}

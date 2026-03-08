package response

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAPIResponseWriter_ConcreteValue(t *testing.T) {
	type item struct {
		ID int `json:"id"`
	}

	rec := httptest.NewRecorder()
	APIResponseWriter(rec, item{ID: 42}, http.StatusOK)

	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("want application/json, got %q", ct)
	}

	var resp APIResponse[item]
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode failed: %v", err)
	}
	if resp.Data == nil || resp.Data.ID != 42 {
		t.Fatalf("unexpected data: %+v", resp.Data)
	}
}

func TestAPIResponseWriter_NilPointer_SubstitutesEmpty(t *testing.T) {
	type item struct{ Name string }

	rec := httptest.NewRecorder()
	APIResponseWriter[*item](rec, nil, http.StatusOK)

	var resp APIResponse[*item]
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode failed: %v", err)
	}
	if resp.Data == nil {
		t.Fatal("data field must not be null for nil pointer input")
	}
}

func TestAPIResponseWriter_NilSlice_SubstitutesEmpty(t *testing.T) {
	rec := httptest.NewRecorder()
	APIResponseWriter[[]string](rec, nil, http.StatusOK)

	var resp APIResponse[[]string]
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode failed: %v", err)
	}
	if resp.Data == nil {
		t.Fatal("data must not be null for nil slice input")
	}
}

func TestAPIResponseWriter_CustomStatusCode(t *testing.T) {
	rec := httptest.NewRecorder()
	APIResponseWriter(rec, "created", http.StatusCreated)

	if rec.Code != http.StatusCreated {
		t.Fatalf("want 201, got %d", rec.Code)
	}
}

func TestAPIResponseWriterWithPagination_PopulatesMeta(t *testing.T) {
	rec := httptest.NewRecorder()
	APIResponseWriterWithPagination(rec, []int{1, 2, 3}, http.StatusOK, 3, 0, 9)

	var resp APIResponse[[]int]
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode failed: %v", err)
	}
	if resp.Pagination == nil {
		t.Fatal("pagination must not be nil")
	}
	if resp.Pagination.TotalCount != 9 {
		t.Fatalf("want totalCount=9, got %d", resp.Pagination.TotalCount)
	}
	if resp.Pagination.TotalPages != 3 {
		t.Fatalf("want 3 total pages, got %d", resp.Pagination.TotalPages)
	}
	if resp.Pagination.CurrentPage != 1 {
		t.Fatalf("want currentPage=1, got %d", resp.Pagination.CurrentPage)
	}
}

func TestAPIResponseWriterWithPagination_HasMore(t *testing.T) {
	rec := httptest.NewRecorder()
	APIResponseWriterWithPagination(rec, []int{1, 2, 3}, http.StatusOK, 3, 0, 10)

	var resp APIResponse[[]int]
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode failed: %v", err)
	}
	if resp.Pagination == nil {
		t.Fatal("pagination must not be nil")
	}
	if !resp.Pagination.HasMore {
		t.Fatal("expected HasMore=true when len(data)==limit")
	}
}

func TestAPIResponseWriterWithPagination_HasMoreFalse(t *testing.T) {
	rec := httptest.NewRecorder()
	APIResponseWriterWithPagination(rec, []int{1, 2}, http.StatusOK, 3, 0, 2)

	var resp APIResponse[[]int]
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode failed: %v", err)
	}
	if resp.Pagination == nil {
		t.Fatal("pagination must not be nil")
	}
	if resp.Pagination.HasMore {
		t.Fatal("expected HasMore=false when len(data) < limit")
	}
}

func TestAPIResponseWriterWithPagination_ZeroLimit_NoPanic(t *testing.T) {
	rec := httptest.NewRecorder()
	APIResponseWriterWithPagination(rec, []int{}, http.StatusOK, 0, 0, 0)

	if rec.Code != http.StatusOK {
		t.Fatalf("want 200 with zero limit, got %d", rec.Code)
	}
}

func TestAPIResponseWriterWithPagination_EmptyData(t *testing.T) {
	rec := httptest.NewRecorder()
	APIResponseWriterWithPagination(rec, []string{}, http.StatusOK, 10, 0, 0)

	var resp APIResponse[[]string]
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode failed: %v", err)
	}
	if resp.Data == nil {
		t.Fatal("data must be an empty array, not null")
	}
}

func TestAPIErrorWriter_SetsStatusAndBody(t *testing.T) {
	errStr := "Not Found"
	rec := httptest.NewRecorder()
	APIErrorWriter(rec, APIError[any]{
		Code:    http.StatusNotFound,
		Error:   &errStr,
		Message: "Resource not found",
	})

	if rec.Code != http.StatusNotFound {
		t.Fatalf("want 404, got %d", rec.Code)
	}

	var errResp APIError[any]
	if err := json.Unmarshal(rec.Body.Bytes(), &errResp); err != nil {
		t.Fatalf("decode failed: %v", err)
	}
	if errResp.Message != "Resource not found" {
		t.Fatalf("unexpected message: %q", errResp.Message)
	}
}

func TestAPIErrorWriter_NilData_Substituted(t *testing.T) {
	// Use a concrete slice type so that CreateEmptyData returns an initialised
	// empty slice, which JSON-encodes as [] rather than null.
	errStr := "Bad"
	rec := httptest.NewRecorder()
	APIErrorWriter(rec, APIError[[]string]{
		Code:    http.StatusBadRequest,
		Error:   &errStr,
		Message: "bad input",
		Data:    nil,
	})

	var errResp APIError[[]string]
	if err := json.Unmarshal(rec.Body.Bytes(), &errResp); err != nil {
		t.Fatalf("decode failed: %v", err)
	}
	if errResp.Data == nil {
		t.Fatal("data must not be null in error response")
	}
}

func TestAPIErrorWriter_ContentType(t *testing.T) {
	errStr := "err"
	rec := httptest.NewRecorder()
	APIErrorWriter(rec, APIError[any]{Code: http.StatusInternalServerError, Error: &errStr, Message: "oops"})

	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("want application/json, got %q", ct)
	}
}

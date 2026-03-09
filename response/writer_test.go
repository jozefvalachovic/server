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

// ── APIResponseWriterWithMessage ──────────────────────────────────────────────

func TestAPIResponseWriterWithMessage_IncludesMessage(t *testing.T) {
	rec := httptest.NewRecorder()
	APIResponseWriterWithMessage(rec, "done", http.StatusOK, "Operation completed")

	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}

	var resp APIResponse[string]
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode failed: %v", err)
	}
	if resp.Message == nil || *resp.Message != "Operation completed" {
		t.Fatalf("unexpected message: %v", resp.Message)
	}
	if resp.Data == nil || *resp.Data != "done" {
		t.Fatalf("unexpected data: %v", resp.Data)
	}
}

// ── APICreated ────────────────────────────────────────────────────────────────

func TestAPICreated_StatusAndLocation(t *testing.T) {
	type item struct{ ID int }
	rec := httptest.NewRecorder()
	APICreated(rec, item{ID: 7}, "/items/7")

	if rec.Code != http.StatusCreated {
		t.Fatalf("want 201, got %d", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/items/7" {
		t.Fatalf("want Location=/items/7, got %q", loc)
	}

	var resp APIResponse[item]
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode failed: %v", err)
	}
	if resp.Data == nil || resp.Data.ID != 7 {
		t.Fatalf("unexpected data: %+v", resp.Data)
	}
}

func TestAPICreated_EmptyLocation(t *testing.T) {
	rec := httptest.NewRecorder()
	APICreated(rec, "ok", "")

	if loc := rec.Header().Get("Location"); loc != "" {
		t.Fatalf("expected no Location header, got %q", loc)
	}
}

// ── APINoContent ──────────────────────────────────────────────────────────────

func TestAPINoContent_Status(t *testing.T) {
	rec := httptest.NewRecorder()
	APINoContent(rec)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("want 204, got %d", rec.Code)
	}
	if rec.Body.Len() != 0 {
		t.Fatalf("expected empty body, got %d bytes", rec.Body.Len())
	}
}

// ── APIResponseWriterWithCursorPagination ─────────────────────────────────────

func TestAPIResponseWriterWithCursorPagination_PopulatesCursor(t *testing.T) {
	rec := httptest.NewRecorder()
	data := []int{1, 2, 3}
	APIResponseWriterWithCursorPagination(rec, data, http.StatusOK, ResponseCursorPagination{
		NextCursor: "abc123",
		HasMore:    true,
		PageSize:   3,
	})

	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}

	var resp APIResponse[[]int]
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode failed: %v", err)
	}
	if resp.CursorPagination == nil {
		t.Fatal("cursorPagination must not be nil")
	}
	if resp.CursorPagination.NextCursor != "abc123" {
		t.Fatalf("want nextCursor=abc123, got %q", resp.CursorPagination.NextCursor)
	}
	if !resp.CursorPagination.HasMore {
		t.Fatal("expected hasMore=true")
	}
	if resp.CursorPagination.PageSize != 3 {
		t.Fatalf("want pageSize=3, got %d", resp.CursorPagination.PageSize)
	}
}

func TestAPIResponseWriterWithCursorPagination_EmptyData(t *testing.T) {
	rec := httptest.NewRecorder()
	APIResponseWriterWithCursorPagination(rec, []string{}, http.StatusOK, ResponseCursorPagination{
		HasMore:  false,
		PageSize: 20,
	})

	var resp APIResponse[[]string]
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode failed: %v", err)
	}
	if resp.Data == nil {
		t.Fatal("data must be an empty array, not null")
	}
	if len(*resp.Data) != 0 {
		t.Fatalf("expected empty slice, got %d items", len(*resp.Data))
	}
}

func TestAPIResponseWriterWithCursorPagination_PrevCursor(t *testing.T) {
	rec := httptest.NewRecorder()
	APIResponseWriterWithCursorPagination(rec, []int{4, 5, 6}, http.StatusOK, ResponseCursorPagination{
		NextCursor: "page3",
		PrevCursor: "page1",
		HasMore:    true,
		PageSize:   3,
	})

	var resp APIResponse[[]int]
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode failed: %v", err)
	}
	if resp.CursorPagination.PrevCursor != "page1" {
		t.Fatalf("want prevCursor=page1, got %q", resp.CursorPagination.PrevCursor)
	}
}

// ── APIResponseWriterWithWarnings ─────────────────────────────────────────────

func TestAPIResponseWriterWithWarnings_IncludesWarnings(t *testing.T) {
	rec := httptest.NewRecorder()
	APIResponseWriterWithWarnings(rec, "data", http.StatusOK, []string{"deprecated endpoint", "use /v2"})

	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}

	var resp APIResponse[string]
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode failed: %v", err)
	}
	if len(resp.Warnings) != 2 {
		t.Fatalf("expected 2 warnings, got %d", len(resp.Warnings))
	}
	if resp.Warnings[0] != "deprecated endpoint" {
		t.Fatalf("unexpected warning[0]: %q", resp.Warnings[0])
	}
}

func TestAPIResponseWriterWithWarnings_NilWarnings_OmitsField(t *testing.T) {
	rec := httptest.NewRecorder()
	APIResponseWriterWithWarnings(rec, "data", http.StatusOK, nil)

	// warnings should be omitted from JSON entirely when nil.
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatalf("decode failed: %v", err)
	}
	if _, ok := raw["warnings"]; ok {
		t.Fatal("expected warnings field to be omitted when nil")
	}
}

// ── APIResponseWriterWithETag ─────────────────────────────────────────────────

func TestAPIResponseWriterWithETag_SetsHeader(t *testing.T) {
	rec := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/test", nil)
	APIResponseWriterWithETag(rec, r, "data", http.StatusOK)

	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	etag := rec.Header().Get("ETag")
	if etag == "" {
		t.Fatal("expected ETag header to be set")
	}
	if etag[0] != '"' {
		t.Fatalf("ETag should be quoted, got %q", etag)
	}
}

func TestAPIResponseWriterWithETag_304OnMatch(t *testing.T) {
	// First request to get the ETag.
	rec1 := httptest.NewRecorder()
	r1 := httptest.NewRequest("GET", "/test", nil)
	APIResponseWriterWithETag(rec1, r1, "data", http.StatusOK)

	etag := rec1.Header().Get("ETag")

	// Second request with If-None-Match.
	rec2 := httptest.NewRecorder()
	r2 := httptest.NewRequest("GET", "/test", nil)
	r2.Header.Set("If-None-Match", etag)
	APIResponseWriterWithETag(rec2, r2, "data", http.StatusOK)

	if rec2.Code != http.StatusNotModified {
		t.Fatalf("want 304, got %d", rec2.Code)
	}
	if rec2.Body.Len() != 0 {
		t.Fatalf("expected empty body on 304, got %d bytes", rec2.Body.Len())
	}
}

func TestAPIResponseWriterWithETag_DifferentDataDifferentETag(t *testing.T) {
	rec1 := httptest.NewRecorder()
	r1 := httptest.NewRequest("GET", "/", nil)
	APIResponseWriterWithETag(rec1, r1, "alpha", http.StatusOK)

	rec2 := httptest.NewRecorder()
	r2 := httptest.NewRequest("GET", "/", nil)
	APIResponseWriterWithETag(rec2, r2, "bravo", http.StatusOK)

	if rec1.Header().Get("ETag") == rec2.Header().Get("ETag") {
		t.Fatal("different data should produce different ETags")
	}
}

func TestAPIResponseWriterWithETag_MismatchedETag_FullResponse(t *testing.T) {
	rec := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/test", nil)
	r.Header.Set("If-None-Match", `"stale-etag"`)
	APIResponseWriterWithETag(rec, r, "data", http.StatusOK)

	if rec.Code != http.StatusOK {
		t.Fatalf("want 200 for mismatched ETag, got %d", rec.Code)
	}
	if rec.Body.Len() == 0 {
		t.Fatal("expected a body for mismatched ETag")
	}
}

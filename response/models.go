package response

import "encoding/json"

type HeartbeatData struct {
	Type      string `json:"type"`
	Timestamp int64  `json:"timestamp"`
	Sent      int    `json:"sent"`
}

type ResponseOffsetPagination struct {
	Limit       int  `json:"limit"`
	Offset      int  `json:"offset"`
	TotalCount  int  `json:"totalCount"`
	TotalPages  int  `json:"totalPages"`
	CurrentPage int  `json:"currentPage"`
	HasMore     bool `json:"hasMore"`
}

// ResponseCursorPagination describes a cursor-based page of results.
// Use this instead of offset pagination for large, dynamic, or real-time datasets.
type ResponseCursorPagination struct {
	NextCursor string `json:"nextCursor,omitempty"`
	PrevCursor string `json:"prevCursor,omitempty"`
	HasMore    bool   `json:"hasMore"`
	PageSize   int    `json:"pageSize"`
}

type APIResponse[T any] struct {
	Code     int              `json:"code"`
	Data     *T               `json:"data"`
	Metadata *json.RawMessage `json:"metadata,omitempty"`
	// Message is an optional informational string for success responses.
	Message          *string                   `json:"message,omitempty"`
	Pagination       *ResponseOffsetPagination `json:"pagination,omitempty"`
	CursorPagination *ResponseCursorPagination `json:"cursorPagination,omitempty"`
	Preferences      *json.RawMessage          `json:"preferences,omitempty"`
	Warnings         []string                  `json:"warnings,omitempty"`
}

type APIStream[T any] struct {
	Code    int     `json:"code"`
	Data    *T      `json:"data"`
	Error   *string `json:"error,omitempty"`
	Message *string `json:"message,omitempty"`
	Details string  `json:"details,omitempty"`
}

type APIError[T any] struct {
	Code    int     `json:"code"`
	Data    *T      `json:"data"`
	Error   *string `json:"error"`
	Message string  `json:"message"`
	Details string  `json:"details,omitempty"`
}

// ── Error string sentinels ───────────────────────────────────────────────────
// Pre-allocated *string pointers for common error labels. Using these instead
// of new("...") on every request avoids a heap allocation per error response,
// reducing GC pressure under sustained rejection (rate limiting, auth failures).

func ptr(s string) *string { return &s }

var (
	ErrBadRequest         = ptr("Bad Request")
	ErrUnauthorized       = ptr("Unauthorized")
	ErrForbidden          = ptr("Forbidden")
	ErrNotFound           = ptr("Not Found")
	ErrMethodNotAllowed   = ptr("Method Not Allowed")
	ErrConflict           = ptr("Conflict")
	ErrTooManyRequests    = ptr("Too Many Requests")
	ErrInternalServer     = ptr("Internal Server Error")
	ErrInternalServerLow  = ptr("Internal server error")
	ErrGatewayTimeout     = ptr("Gateway Timeout")
	ErrBadGateway         = ptr("Fetch failed")
	ErrServiceUnavailable = ptr("Service Unavailable")
)

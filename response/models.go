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

type APIResponse[T any] struct {
	Code     int              `json:"code"`
	Data     *T               `json:"data"`
	Metadata *json.RawMessage `json:"metadata,omitempty"`
	// Message is an optional informational string for success responses.
	Message     *string                   `json:"message,omitempty"`
	Pagination  *ResponseOffsetPagination `json:"pagination,omitempty"`
	Preferences *json.RawMessage          `json:"preferences,omitempty"`
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

package response

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/jozefvalachovic/logger/v4"
)

// APIResponseWriterWithPagination writes a response with pagination metadata
func APIResponseWriterWithPagination[T any](w http.ResponseWriter, data []T, statusCode int, limit, offset, totalCount int) {
	var responseData *[]T

	if len(data) == 0 {
		emptySlice := make([]T, 0)
		responseData = &emptySlice
	} else {
		responseData = &data
	}

	// Guard against division by zero when limit is 0.
	totalPages := 0
	currentPage := 1
	if limit > 0 {
		totalPages = (totalCount + limit - 1) / limit
		currentPage = (offset / limit) + 1
	}

	response := APIResponse[[]T]{
		Code: statusCode,
		Data: responseData,
		Pagination: &ResponseOffsetPagination{
			Limit:       limit,
			Offset:      offset,
			TotalCount:  totalCount,
			TotalPages:  totalPages,
			CurrentPage: currentPage,
			HasMore:     limit > 0 && len(data) == limit,
		},
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)

	if err := json.NewEncoder(w).Encode(response); err != nil {
		logger.LogError("Failed to encode API response with pagination", "error", err.Error())
	}
}

// APIResponseWriter writes a consistent API response with proper handling of empty data
func APIResponseWriter[T any](w http.ResponseWriter, data T, statusCode int) {
	responseData := resolveResponseData(data)

	response := APIResponse[T]{
		Code: statusCode,
		Data: responseData,
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)

	if err := json.NewEncoder(w).Encode(response); err != nil {
		logger.LogError("Failed to encode API response", "error", err.Error())
	}
}

// APIErrorWriter writes consistent error responses
func APIErrorWriter[T any](w http.ResponseWriter, apiError APIError[T]) {
	if apiError.Data == nil {
		apiError.Data = CreateEmptyData[T]()
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(apiError.Code)

	// Simplified - use the struct directly
	if err := json.NewEncoder(w).Encode(apiError); err != nil {
		logger.LogError("Failed to encode error response", "error", err.Error())
	}
}

// APIResponseWriterWithMessage writes a success response with an informational message.
func APIResponseWriterWithMessage[T any](w http.ResponseWriter, data T, statusCode int, message string) {
	responseData := resolveResponseData(data)

	response := APIResponse[T]{
		Code:    statusCode,
		Data:    responseData,
		Message: &message,
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)

	if err := json.NewEncoder(w).Encode(response); err != nil {
		logger.LogError("Failed to encode API response with message", "error", err.Error())
	}
}

// APICreated writes a 201 Created response and sets the Location header to the
// URI of the newly created resource.
func APICreated[T any](w http.ResponseWriter, data T, location string) {
	responseData := resolveResponseData(data)

	response := APIResponse[T]{
		Code: http.StatusCreated,
		Data: responseData,
	}

	w.Header().Set("Content-Type", "application/json")
	if location != "" {
		w.Header().Set("Location", location)
	}
	w.WriteHeader(http.StatusCreated)

	if err := json.NewEncoder(w).Encode(response); err != nil {
		logger.LogError("Failed to encode API created response", "error", err.Error())
	}
}

// APINoContent writes a 204 No Content response with no body.
func APINoContent(w http.ResponseWriter) {
	w.WriteHeader(http.StatusNoContent)
}

// APIResponseWriterWithCursorPagination writes a response with cursor-based pagination.
func APIResponseWriterWithCursorPagination[T any](w http.ResponseWriter, data []T, statusCode int, cursor ResponseCursorPagination) {
	var responseData *[]T

	if len(data) == 0 {
		emptySlice := make([]T, 0)
		responseData = &emptySlice
	} else {
		responseData = &data
	}

	response := APIResponse[[]T]{
		Code:             statusCode,
		Data:             responseData,
		CursorPagination: &cursor,
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)

	if err := json.NewEncoder(w).Encode(response); err != nil {
		logger.LogError("Failed to encode API response with cursor pagination", "error", err.Error())
	}
}

// APIResponseWriterWithWarnings writes a success response with attached warning strings.
func APIResponseWriterWithWarnings[T any](w http.ResponseWriter, data T, statusCode int, warnings []string) {
	responseData := resolveResponseData(data)

	response := APIResponse[T]{
		Code:     statusCode,
		Data:     responseData,
		Warnings: warnings,
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)

	if err := json.NewEncoder(w).Encode(response); err != nil {
		logger.LogError("Failed to encode API response with warnings", "error", err.Error())
	}
}

// APIResponseWriterWithETag writes a success response with an ETag header.
// If the request carries a matching If-None-Match header, a 304 Not Modified
// is returned instead of the full body.
func APIResponseWriterWithETag[T any](w http.ResponseWriter, r *http.Request, data T, statusCode int) {
	responseData := resolveResponseData(data)

	response := APIResponse[T]{
		Code: statusCode,
		Data: responseData,
	}

	body, err := json.Marshal(response)
	if err != nil {
		logger.LogError("Failed to encode API response for ETag", "error", err.Error())
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	hash := sha256.Sum256(body)
	etag := `"` + hex.EncodeToString(hash[:16]) + `"`

	w.Header().Set("ETag", etag)

	if etagMatch(r.Header.Get("If-None-Match"), etag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	_, _ = w.Write(body)
	_, _ = w.Write([]byte("\n"))
}

// etagMatch checks whether any ETag in the If-None-Match header matches the
// given strong ETag. Handles comma-separated lists and W/ weak prefixes
// (weak comparison per RFC 9110 §13.1.2).
func etagMatch(header, etag string) bool {
	if header == "" {
		return false
	}
	if header == "*" {
		return true
	}
	for _, part := range strings.Split(header, ",") {
		candidate := strings.TrimSpace(part)
		candidate = strings.TrimPrefix(candidate, "W/")
		if candidate == etag {
			return true
		}
	}
	return false
}

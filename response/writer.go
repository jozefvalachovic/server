package response

import (
	"encoding/json"
	"net/http"
	"reflect"

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
	var responseData *T

	// Only substitute empty data for nil-able kinds (pointer, interface, map, slice, etc).
	// For concrete value types (struct, int, bool, …) always use the actual value so that
	// legitimately zero-valued responses (e.g. {Count: 0}) are not silently replaced.
	dataValue := reflect.ValueOf(data)
	if !dataValue.IsValid() {
		responseData = CreateEmptyData[T]()
	} else {
		switch dataValue.Kind() {
		case reflect.Pointer, reflect.Interface, reflect.Map, reflect.Slice, reflect.Chan, reflect.Func:
			if dataValue.IsNil() {
				responseData = CreateEmptyData[T]()
			} else {
				responseData = &data
			}
		default:
			responseData = &data
		}
	}

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

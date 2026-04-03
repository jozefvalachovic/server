package response

import (
	"net/http"
)

// APIBadRequest writes a 400 Bad Request response.
func APIBadRequest(w http.ResponseWriter, message, details string) {
	APIErrorWriter(w, APIError[any]{
		Code:    http.StatusBadRequest,
		Data:    CreateEmptyData[any](),
		Error:   ErrBadRequest,
		Message: message,
		Details: details,
	})
}

// APIUnauthorized writes a 401 Unauthorized response with a custom message
func APIUnauthorized(w http.ResponseWriter, message string) {
	apiError := APIError[any]{
		Code:    http.StatusUnauthorized,
		Data:    CreateEmptyData[any](),
		Error:   ErrUnauthorized,
		Message: message,
	}

	APIErrorWriter(w, apiError)
}

// APIForbidden writes a 403 Forbidden response with a custom message
func APIForbidden(w http.ResponseWriter, message string) {
	apiError := APIError[any]{
		Code:    http.StatusForbidden,
		Data:    CreateEmptyData[any](),
		Error:   ErrForbidden,
		Message: message,
	}

	APIErrorWriter(w, apiError)
}

// APINotFound writes a 404 Not Found response.
func APINotFound(w http.ResponseWriter, message string) {
	APIErrorWriter(w, APIError[any]{
		Code:    http.StatusNotFound,
		Data:    CreateEmptyData[any](),
		Error:   ErrNotFound,
		Message: message,
	})
}

// APIConflict writes a 409 Conflict response.
func APIConflict(w http.ResponseWriter, message string) {
	APIErrorWriter(w, APIError[any]{
		Code:    http.StatusConflict,
		Data:    CreateEmptyData[any](),
		Error:   ErrConflict,
		Message: message,
	})
}

// APIInternalError writes a 500 Internal Server Error response.
func APIInternalError(w http.ResponseWriter, message string) {
	APIErrorWriter(w, APIError[any]{
		Code:    http.StatusInternalServerError,
		Data:    CreateEmptyData[any](),
		Error:   ErrInternalServer,
		Message: message,
	})
}

// APIServiceUnavailable writes a 503 Service Unavailable response.
func APIServiceUnavailable(w http.ResponseWriter, message string) {
	APIErrorWriter(w, APIError[any]{
		Code:    http.StatusServiceUnavailable,
		Data:    CreateEmptyData[any](),
		Error:   ErrServiceUnavailable,
		Message: message,
	})
}

package response

import (
	"net/http"
)

// APIUnauthorized writes a 401 Unauthorized response with a custom message
func APIUnauthorized(w http.ResponseWriter, message string) {
	apiError := APIError[any]{
		Code:    http.StatusUnauthorized,
		Data:    CreateEmptyData[any](),
		Error:   new("Unauthorized"),
		Message: message,
	}

	APIErrorWriter(w, apiError)
}

// APIForbidden writes a 403 Forbidden response with a custom message
func APIForbidden(w http.ResponseWriter, message string) {
	apiError := APIError[any]{
		Code:    http.StatusForbidden,
		Data:    CreateEmptyData[any](),
		Error:   new("Forbidden"),
		Message: message,
	}

	APIErrorWriter(w, apiError)
}

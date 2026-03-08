package response

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"reflect"
)

// ValidateAndDecode validates and decodes request body
func ValidateAndDecode[T any](r *http.Request) (T, *APIError[T]) {
	var obj T

	if r.Body == nil || r.Body == http.NoBody {
		return obj, &APIError[T]{
			Code:    http.StatusBadRequest,
			Message: "Request body is required",
		}
	}

	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&obj); err != nil {
		if errors.Is(err, io.EOF) {
			return obj, &APIError[T]{
				Code:    http.StatusBadRequest,
				Message: "Request body must not be empty",
			}
		}
		if _, ok := errors.AsType[*http.MaxBytesError](err); ok {
			return obj, &APIError[T]{
				Code:    http.StatusRequestEntityTooLarge,
				Message: "Request body too large",
			}
		}
		return obj, &APIError[T]{
			Code:    http.StatusBadRequest,
			Message: "Invalid JSON in request body",
			Details: err.Error(),
		}
	}

	return obj, nil
}

// CreateEmptyData creates an empty data structure based on the type T.
func CreateEmptyData[T any]() *T {
	var result T

	// Initialize common empty types
	valueOf := reflect.ValueOf(&result).Elem()
	typeOf := valueOf.Type()

	switch typeOf.Kind() {
	case reflect.Slice:
		if valueOf.IsNil() {
			// Create empty slice
			emptySlice := reflect.MakeSlice(typeOf, 0, 0)
			valueOf.Set(emptySlice)
		}
	case reflect.Map:
		if valueOf.IsNil() {
			// Create empty map
			emptyMap := reflect.MakeMap(typeOf)
			valueOf.Set(emptyMap)
		}
	case reflect.Pointer:
		if valueOf.IsNil() && typeOf.Elem().Kind() == reflect.Struct {
			// Create new struct for pointer types
			newStruct := reflect.New(typeOf.Elem())
			valueOf.Set(newStruct)
		}
	}

	return &result
}

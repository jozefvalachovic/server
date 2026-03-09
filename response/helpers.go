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

	// Fast path: interface types (T=any) — nil is correct for JSON.
	if any(result) == nil {
		return &result
	}

	// Fast path: value types can never be nil; no initialization needed.
	switch any(result).(type) {
	case string, bool,
		int, int8, int16, int32, int64,
		uint, uint8, uint16, uint32, uint64,
		float32, float64:
		return &result
	}

	// Composite types (slice, map, pointer) may need initialization
	// so JSON serializes as [] or {} instead of null.
	valueOf := reflect.ValueOf(&result).Elem()
	switch valueOf.Kind() {
	case reflect.Slice:
		valueOf.Set(reflect.MakeSlice(valueOf.Type(), 0, 0))
	case reflect.Map:
		valueOf.Set(reflect.MakeMap(valueOf.Type()))
	case reflect.Pointer:
		if valueOf.Type().Elem().Kind() == reflect.Struct {
			valueOf.Set(reflect.New(valueOf.Type().Elem()))
		}
	}

	return &result
}

// resolveResponseData returns a pointer to data, substituting an initialized
// empty value when data is nil or a typed nil (nil slice, nil map, etc.).
// This consolidates reflect-based nil detection used by APIResponseWriter.
func resolveResponseData[T any](data T) *T {
	// Fast path: untyped nil (T=any with nil interface value).
	if any(data) == nil {
		return CreateEmptyData[T]()
	}
	// Typed nils (nil slice, nil pointer, etc.) require reflect.
	rv := reflect.ValueOf(any(data))
	switch rv.Kind() {
	case reflect.Pointer, reflect.Interface, reflect.Map, reflect.Slice, reflect.Chan, reflect.Func:
		if rv.IsNil() {
			return CreateEmptyData[T]()
		}
	}
	return &data
}

package middleware

import "fmt"

// panicToError normalises a recovered panic value into an error.
// Shared by Recovery and Timeout middleware to ensure consistent handling
// of the two common panic payloads (native error vs arbitrary value).
func panicToError(rec any) error {
	if e, ok := rec.(error); ok {
		return e
	}
	return fmt.Errorf("%v", rec)
}

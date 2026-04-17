package middleware

import (
	"crypto/rand"
	"encoding/hex"
)

// randomHex returns a hex-encoded random string of 2*n characters.
// Centralises the common `make([]byte, n); rand.Read; hex.Encode` pattern used
// by RequestID and TraceContext so every generator path uses the same
// crypto/rand source and length discipline. Panics only if crypto/rand fails,
// which on supported platforms implies a fatal OS state — so we intentionally
// ignore the error (per stdlib precedent in crypto/rand.Text).
func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

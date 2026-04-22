package middleware

import (
	"crypto/rand"
	"encoding/hex"
)

// randomHex returns a hex-encoded random string of 2*n characters.
// Centralises the common `make([]byte, n); rand.Read; hex.Encode` pattern used
// by RequestID and TraceContext so every generator path uses the same
// crypto/rand source and length discipline.
//
// crypto/rand.Read on every supported platform is infallible (kernel
// getrandom(2) / BCryptGenRandom). If it ever returns an error the host is
// in an unrecoverable state — panic immediately rather than silently returning
// predictable all-zero bytes, which would leak into request-IDs and trace-IDs.
func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic("middleware: crypto/rand.Read failed: " + err.Error())
	}
	return hex.EncodeToString(b)
}

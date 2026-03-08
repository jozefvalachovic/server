package middleware

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/http"
)

const (
	// RequestIDHeader is the canonical header name used to propagate the request ID.
	RequestIDHeader = "X-Request-ID"
)

// RequestIDConfig configures the RequestID middleware.
// All fields are optional; zero values fall back to documented defaults.
type RequestIDConfig struct {
	// Header is the header name to read from incoming requests and write to
	// responses. Default: "X-Request-ID".
	Header string

	// Generator returns a new unique ID string for each request that does not
	// already carry one. Default: 16-byte cryptographically random hex string.
	Generator func() string
}

// contextKey is an unexported type for context keys in this package.
type contextKey int

const requestIDKey contextKey = iota

// RequestIDFromContext returns the request ID stored in the context, or an
// empty string when none is present.
func RequestIDFromContext(r *http.Request) string {
	v, _ := r.Context().Value(requestIDKey).(string)
	return v
}

// RequestID injects a unique request identifier into every request.
//
// If the incoming request already carries the configured header (e.g.
// forwarded by a load balancer or API gateway), that value is re-used so the
// ID is stable across the full call chain. Otherwise a new ID is generated.
//
// The ID is both stored in the request context (retrieve with
// RequestIDFromContext) and reflected in the response header so clients can
// correlate requests with logs.
//
// Example — default config:
//
//	middleware.RequestID()
//
// Example — custom header and generator:
//
//	middleware.RequestID(middleware.RequestIDConfig{
//	    Header:    "X-Correlation-ID",
//	    Generator: uuid.NewString,
//	})
func RequestID(cfgs ...RequestIDConfig) func(http.Handler) http.Handler {
	cfg := RequestIDConfig{}
	if len(cfgs) > 0 {
		cfg = cfgs[0]
	}
	if cfg.Header == "" {
		cfg.Header = RequestIDHeader
	}
	if cfg.Generator == nil {
		cfg.Generator = defaultIDGenerator
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			id := r.Header.Get(cfg.Header)
			if id == "" {
				id = cfg.Generator()
			} else {
				// Sanitize client-provided IDs: cap length and strip control
				// characters to prevent log injection.
				id = sanitizeRequestID(id)
			}

			// Propagate via context so handlers can access it without header parsing.
			ctx := r.Context()
			ctx = contextWithRequestID(ctx, id)
			r = r.WithContext(ctx)

			w.Header().Set(cfg.Header, id)
			next.ServeHTTP(w, r)
		})
	}
}

func contextWithRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, requestIDKey, id)
}

func defaultIDGenerator() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// sanitizeRequestID caps the length and removes control characters from
// externally-provided request IDs to prevent log injection attacks.
func sanitizeRequestID(id string) string {
	const maxLen = 128
	if len(id) > maxLen {
		id = id[:maxLen]
	}
	for i := range len(id) {
		if id[i] < 0x20 || id[i] == 0x7f {
			// Contains control characters — rebuild without them.
			var b []byte
			for j := range len(id) {
				if id[j] >= 0x20 && id[j] != 0x7f {
					b = append(b, id[j])
				}
			}
			return string(b)
		}
	}
	return id
}

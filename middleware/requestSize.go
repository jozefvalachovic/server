package middleware

import (
	"net/http"
	"os"
	"strconv"
)

// RequestSizeConfig configures the RequestSize middleware.
type RequestSizeConfig struct {
	// MaxSizeMB is the maximum allowed request body size in megabytes.
	// Default: 10 MiB. The environment variable MAX_REQUEST_SIZE_MB
	// overrides this value when set (for backward compatibility).
	MaxSizeMB int64
}

// RequestSize is a middleware that limits the size of incoming request bodies.
// It reads the limit from the MAX_REQUEST_SIZE_MB environment variable for
// backward compatibility. Use RequestSizeWithConfig for struct-based config.
func RequestSize(next http.Handler) http.Handler {
	return RequestSizeWithConfig(RequestSizeConfig{})(next)
}

// RequestSizeWithConfig returns a request-size-limiting middleware using the
// given config. When MaxSizeMB is 0 the default (10 MiB) applies.
// The MAX_REQUEST_SIZE_MB environment variable, if set, takes precedence over
// the struct value for backward compatibility.
func RequestSizeWithConfig(cfg RequestSizeConfig) func(http.Handler) http.Handler {
	maxSize := resolveMaxRequestSize(cfg.MaxSizeMB)

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Skip size limiting for GET requests (no body)
			if r.Method == "GET" || r.Method == "HEAD" || r.Method == "OPTIONS" {
				next.ServeHTTP(w, r)
				return
			}

			// Set max body size. http.MaxBytesReader will return an error when
			// the limit is exceeded; the standard library writes a 413 response.
			r.Body = http.MaxBytesReader(w, r.Body, maxSize)

			// http.MaxBytesReader writes 413 to the client automatically when
			// the handler reads past the limit, and returns *http.MaxBytesError
			// to the handler. Observability (logging / metrics) of over-limit
			// bodies is the handler's responsibility — it already has the error
			// in hand. See response.DecodeBody for an example helper that turns
			// MaxBytesError into a structured 413 JSON response.
			next.ServeHTTP(w, r)
		})
	}
}

// resolveMaxRequestSize determines the effective limit.
// Environment variable always wins; then the struct value; then the default.
func resolveMaxRequestSize(cfgMB int64) int64 {
	if size := os.Getenv("MAX_REQUEST_SIZE_MB"); size != "" {
		if val, err := strconv.ParseInt(size, 10, 64); err == nil && val > 0 {
			return val * 1024 * 1024
		}
	}
	if cfgMB > 0 {
		return cfgMB * 1024 * 1024
	}
	return 10 * 1024 * 1024 // Default to 10MB
}

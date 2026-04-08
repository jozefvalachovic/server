package middleware

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jozefvalachovic/logger/v4"
)

// CORSConfig configures the CORS middleware.
type CORSConfig struct {
	// Disabled turns the middleware into a no-op passthrough.
	// Default: false (CORS is enabled).
	Disabled bool

	// AllowedOrigins is the list of origins that are allowed.
	// Use ["*"] to allow any origin.
	// Default: ["*"].
	AllowedOrigins []string

	// AllowedMethods lists the HTTP methods permitted for cross-origin requests.
	// Default: GET, POST, PUT, PATCH, DELETE, OPTIONS, HEAD.
	AllowedMethods []string

	// AllowedHeaders lists the request headers that may be used.
	// Default: Content-Type, Authorization, X-Request-ID.
	AllowedHeaders []string

	// ExposedHeaders lists the response headers that browsers may access.
	// Default: empty.
	ExposedHeaders []string

	// AllowCredentials indicates whether the request can include user
	// credentials (cookies, HTTP auth, TLS client certs).
	// Default: false.
	AllowCredentials bool

	// MaxAge sets the preflight cache duration.
	// Default: 1 hour.
	MaxAge time.Duration
}

var defaultCORSMethods = strings.Join([]string{
	http.MethodGet, http.MethodPost, http.MethodPut,
	http.MethodPatch, http.MethodDelete, http.MethodOptions, http.MethodHead,
}, ", ")

var defaultCORSHeaders = "Content-Type, Authorization, X-Request-ID"

// CORS adds Cross-Origin Resource Sharing headers to every response.
//
// By default CORS is fully open ("*" origins, all standard methods) which
// suits internal or public APIs. Pass CORSConfig to restrict origins/methods
// or set Disabled:true to bypass entirely.
//
// Example — defaults (allow all origins):
//
//	middleware.CORS()
//
// Example — restricted origin:
//
//	middleware.CORS(middleware.CORSConfig{
//	    AllowedOrigins: []string{"https://example.com"},
//	    AllowCredentials: true,
//	})
//
// Example — disabled:
//
//	middleware.CORS(middleware.CORSConfig{Disabled: true})
func CORS(cfgs ...CORSConfig) func(http.Handler) http.Handler {
	cfg := CORSConfig{}
	if len(cfgs) > 0 {
		cfg = cfgs[0]
	}

	if cfg.Disabled {
		return func(next http.Handler) http.Handler { return next }
	}

	// Resolve defaults.
	allowedOrigins := cfg.AllowedOrigins
	if len(allowedOrigins) == 0 {
		allowedOrigins = []string{"*"}
	}

	// Per the Fetch spec, Access-Control-Allow-Origin: "*" and
	// Access-Control-Allow-Credentials: true are mutually exclusive.
	// Silently accepting this mis-configuration causes hard-to-debug
	// browser errors. Panic at startup so the operator notices immediately.
	if cfg.AllowCredentials && len(allowedOrigins) == 1 && allowedOrigins[0] == "*" {
		panic("middleware.CORS: AllowCredentials cannot be combined with a wildcard (\"*\") origin; " +
			"list explicit origins instead")
	}
	allowedMethods := defaultCORSMethods
	if len(cfg.AllowedMethods) > 0 {
		allowedMethods = strings.Join(cfg.AllowedMethods, ", ")
	}
	allowedHeaders := defaultCORSHeaders
	if len(cfg.AllowedHeaders) > 0 {
		allowedHeaders = strings.Join(cfg.AllowedHeaders, ", ")
	}
	exposedHeaders := strings.Join(cfg.ExposedHeaders, ", ")
	maxAge := cfg.MaxAge
	if maxAge <= 0 {
		maxAge = time.Hour
	}
	maxAgeStr := strconv.Itoa(int(maxAge.Seconds()))
	wildcard := len(allowedOrigins) == 1 && allowedOrigins[0] == "*"

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := r.Header.Get("Origin")

			// Vary: Origin must be set unconditionally on every response from a
			// CORS-enabled route so that CDN / shared caches store distinct variants
			// for same-origin vs cross-origin requests, regardless of whether the
			// current request carries an Origin header.
			w.Header().Add("Vary", "Origin")

			// Determine the Access-Control-Allow-Origin value.
			var allowOrigin string
			if origin == "" {
				// Not a cross-origin request — Vary already set; skip CORS headers.
				next.ServeHTTP(w, r)
				return
			}
			if wildcard {
				allowOrigin = "*"
			} else {
				for _, o := range allowedOrigins {
					if strings.EqualFold(o, origin) {
						allowOrigin = origin
						break
					}
				}
			}
			if allowOrigin == "" {
				// Origin not in the allowed list — still serve the request because
				// CORS is advisory to the browser; the server does not block
				// cross-origin requests at the HTTP level. Actual access control
				// must be enforced server-side (e.g. auth middleware).
				logger.LogTrace("CORS: origin not allowed", "origin", origin)
				next.ServeHTTP(w, r)
				return
			}

			h := w.Header()
			h.Set("Access-Control-Allow-Origin", allowOrigin)
			h.Set("Access-Control-Allow-Methods", allowedMethods)
			h.Set("Access-Control-Allow-Headers", allowedHeaders)
			h.Set("Access-Control-Max-Age", maxAgeStr)
			if exposedHeaders != "" {
				h.Set("Access-Control-Expose-Headers", exposedHeaders)
			}
			if cfg.AllowCredentials {
				h.Set("Access-Control-Allow-Credentials", "true")
			}
			// Handle preflight — return immediately with 204, no body.
			// Content-Length: 0 is set explicitly for proxy/CDN compatibility.
			if r.Method == http.MethodOptions {
				h.Set("Content-Length", "0")
				w.WriteHeader(http.StatusNoContent)
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

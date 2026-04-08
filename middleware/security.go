package middleware

import (
	"net/http"
	"os"
)

// staticHeaders are the fixed security headers applied on every request.
// Defined as package-level pairs so the strings are allocated once at init.
//
// CSP uses 'self' rather than 'none' so that same-origin assets (admin UI
// scripts, stylesheets) are permitted. JSON API responses are unaffected by
// CSP — browsers only enforce it when rendering HTML documents.
var staticHeaders = [...]struct{ k, v string }{
	{"X-Content-Type-Options", "nosniff"},
	{"X-Frame-Options", "DENY"},
	{"Referrer-Policy", "strict-origin-when-cross-origin"},
	{"Content-Security-Policy", "default-src 'self'; frame-ancestors 'none'"},
	{"Permissions-Policy", "geolocation=(), microphone=(), camera=()"},
	{"Cross-Origin-Opener-Policy", "same-origin"},
	{"Cross-Origin-Resource-Policy", "same-origin"},
}

const hstsValue = "max-age=31536000; includeSubDomains; preload"

// Security adds essential security headers for API protection.
// All header names and values are pre-allocated package-level constants so
// the hot path (every request) performs only map writes, no string allocation.
func Security(next http.Handler) http.Handler {
	production := os.Getenv("ENV") == "production"
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		for _, kv := range staticHeaders {
			h.Set(kv.k, kv.v)
		}
		if production {
			h.Set("Strict-Transport-Security", hstsValue)
		}
		next.ServeHTTP(w, r)
	})
}

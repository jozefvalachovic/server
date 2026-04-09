package request

import (
	"errors"
	"net"
	"net/http"
	"net/mail"
	"strings"
)

// Deprecated: GetIPAddress trusts X-Forwarded-For unconditionally, which is
// trivially spoofable. Use middleware.IPFilter with TrustedProxies for
// security-sensitive IP resolution (right-to-left XFF walk). This function
// is retained only for backward compatibility with non-security use cases.
// It will be removed in the next major version.
//
// GetIPAddress retrieves the IP address from the HTTP request.
// WARNING: X-Forwarded-For and X-Real-IP headers are set by the client and can be
// spoofed unless they are stripped or validated at a trusted reverse proxy/load
// balancer. Do not use the returned IP for security-sensitive decisions (e.g.,
// rate-limiting, banning) unless your infrastructure enforces these headers.
//
//go:deprecated Use middleware.IPFilter with TrustedProxies.
func GetIPAddress(r *http.Request) string {
	return getIPAddress(r)
}

// getIPAddress is the internal implementation. Kept unexported to discourage
// new callers from depending on the spoofable leftmost-XFF strategy.
func getIPAddress(r *http.Request) string {
	// Get IP address from request
	ipAddress := r.Header.Get("X-Forwarded-For")
	if ipAddress != "" {
		// X-Forwarded-For may be a comma-separated list: "client, proxy1, proxy2".
		// Take only the first (leftmost) token, which represents the originating client.
		if idx := strings.Index(ipAddress, ","); idx != -1 {
			ipAddress = strings.TrimSpace(ipAddress[:idx])
		}
		return ipAddress
	}
	if ip := r.Header.Get("X-Real-IP"); ip != "" {
		return ip
	}
	// r.RemoteAddr is "host:port"; return just the host.
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return r.RemoteAddr
}

// SanitizeEmail trims whitespace and converts to lowercase
func SanitizeEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

// ValidateEmail checks if the email is in a valid format
func ValidateEmail(email string) error {
	email = strings.TrimSpace(email)
	if email == "" {
		return errors.New("email is required")
	}

	_, err := mail.ParseAddress(email)
	if err != nil {
		return errors.New("invalid email format")
	}

	return nil
}

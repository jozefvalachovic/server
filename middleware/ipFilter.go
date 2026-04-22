package middleware

import (
	"net"
	"net/http"
	"strings"

	"github.com/jozefvalachovic/server/response"

	"github.com/jozefvalachovic/logger/v4"
)

// IPFilterConfig configures the IPFilter middleware.
// Both lists are optional; zero values produce a passthrough (allow all).
type IPFilterConfig struct {
	// Allowlist is an exclusive list of CIDR ranges or exact IPs that are
	// permitted. An empty list means any IP is allowed (subject to Blocklist).
	// Example: []string{"10.0.0.0/8", "192.168.1.5"}
	Allowlist []string

	// Blocklist is a list of CIDR ranges or exact IPs that are always denied,
	// evaluated after Allowlist.
	// Example: []string{"203.0.113.0/24"}
	Blocklist []string

	// TrustForwardedFor controls whether X-Forwarded-For is used to determine
	// the client IP instead of RemoteAddr. Only enable when behind a trusted
	// reverse proxy. TrustedProxies must also be set; without at least one
	// trusted proxy the middleware ignores XFF and falls back to RemoteAddr.
	// Default: false.
	TrustForwardedFor bool

	// TrustedProxies is the set of CIDR ranges or exact IPs of reverse proxies
	// whose X-Forwarded-For headers are trusted. When set, the middleware walks
	// the XFF list from right to left and skips entries that are trusted proxies,
	// returning the first non-trusted IP as the real client address. This prevents
	// clients from spoofing their IP by prepending arbitrary values to XFF.
	// Ignored when TrustForwardedFor is false.
	TrustedProxies []string
}

type ipFilterState struct {
	allowNets []*net.IPNet
	blockNets []*net.IPNet
	proxyNets []*net.IPNet // trusted reverse-proxy CIDRs for XFF validation
	cfg       IPFilterConfig
}

// IPFilter enforces an IP-based allow/block policy on every request.
//
// Evaluation order:
//  1. If a non-empty Allowlist is set and the client IP is NOT in it → 403.
//  2. If the client IP IS in the Blocklist → 403.
//  3. Otherwise the request is forwarded.
//
// With empty Allowlist and empty Blocklist the middleware is a no-op passthrough.
//
// Example — internal-only endpoint:
//
//	middleware.IPFilter(middleware.IPFilterConfig{
//	    Allowlist: []string{"10.0.0.0/8", "172.16.0.0/12"},
//	})
//
// Example — block known bad actor:
//
//	middleware.IPFilter(middleware.IPFilterConfig{
//	    Blocklist: []string{"203.0.113.42"},
//	})
func IPFilter(cfgs ...IPFilterConfig) func(http.Handler) http.Handler {
	cfg := IPFilterConfig{}
	if len(cfgs) > 0 {
		cfg = cfgs[0]
	}

	if len(cfg.Allowlist) == 0 && len(cfg.Blocklist) == 0 {
		// Nothing to enforce — pure passthrough.
		return func(next http.Handler) http.Handler { return next }
	}

	state := &ipFilterState{cfg: cfg}
	for _, cidr := range cfg.Allowlist {
		raw := cidr
		if !strings.Contains(cidr, "/") {
			if strings.Contains(cidr, ":") {
				cidr += "/128" // IPv6
			} else {
				cidr += "/32" // IPv4
			}
		}
		_, n, err := net.ParseCIDR(cidr)
		if err != nil {
			logger.LogWarn("IPFilter: invalid CIDR in Allowlist, entry skipped", "cidr", raw, "error", err.Error())
			continue
		}
		state.allowNets = append(state.allowNets, n)
	}
	for _, cidr := range cfg.Blocklist {
		raw := cidr
		if !strings.Contains(cidr, "/") {
			if strings.Contains(cidr, ":") {
				cidr += "/128"
			} else {
				cidr += "/32"
			}
		}
		_, n, err := net.ParseCIDR(cidr)
		if err != nil {
			logger.LogWarn("IPFilter: invalid CIDR in Blocklist, entry skipped", "cidr", raw, "error", err.Error())
			continue
		}
		state.blockNets = append(state.blockNets, n)
	}
	for _, cidr := range cfg.TrustedProxies {
		raw := cidr
		if !strings.Contains(cidr, "/") {
			if strings.Contains(cidr, ":") {
				cidr += "/128"
			} else {
				cidr += "/32"
			}
		}
		_, n, err := net.ParseCIDR(cidr)
		if err != nil {
			logger.LogWarn("IPFilter: invalid CIDR in TrustedProxies, entry skipped", "cidr", raw, "error", err.Error())
			continue
		}
		state.proxyNets = append(state.proxyNets, n)
	}

	if cfg.TrustForwardedFor && len(state.proxyNets) == 0 {
		logger.LogWarn("IPFilter: TrustForwardedFor is enabled but no valid TrustedProxies configured — X-Forwarded-For will be ignored, falling back to RemoteAddr")
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ip := clientIPFromRequest(r, cfg.TrustForwardedFor, state.proxyNets)
			parsed := net.ParseIP(ip)
			if parsed == nil {
				// Cannot determine IP — deny for safety.
				writeForbidden(w)
				return
			}

			// 1. Allowlist check.
			if len(state.allowNets) > 0 {
				allowed := false
				for _, n := range state.allowNets {
					if n.Contains(parsed) {
						allowed = true
						break
					}
				}
				if !allowed {
					writeForbidden(w)
					return
				}
			}

			// 2. Blocklist check.
			for _, n := range state.blockNets {
				if n.Contains(parsed) {
					writeForbidden(w)
					return
				}
			}

			next.ServeHTTP(w, r)
		})
	}
}

func writeForbidden(w http.ResponseWriter) {
	response.APIErrorWriter(w, response.APIError[any]{
		Code:    http.StatusForbidden,
		Error:   response.ErrForbidden,
		Message: "Access denied.",
	})
}

package middleware

import (
	"net"
	"net/http"
	"strings"

	"github.com/jozefvalachovic/logger/v4"
)

// parseTrustedProxies parses a list of CIDR ranges or exact IPs into *net.IPNet.
// Invalid entries are logged and skipped so a single typo cannot break startup.
// Single-IP entries are promoted to /32 (IPv4) or /128 (IPv6) automatically.
//
// origin is a short, human-readable label (e.g. "IPFilter.TrustedProxies",
// "HTTPRateLimit.TrustedProxies") that is emitted with each warning so
// operators can attribute the misconfiguration to a specific caller.
func parseTrustedProxies(origin string, cidrs []string) []*net.IPNet {
	if len(cidrs) == 0 {
		return nil
	}
	out := make([]*net.IPNet, 0, len(cidrs))
	for _, cidr := range cidrs {
		raw := cidr
		if !strings.Contains(cidr, "/") {
			if strings.Contains(cidr, ":") {
				cidr += "/128" // IPv6 single address
			} else {
				cidr += "/32" // IPv4 single address
			}
		}
		_, n, err := net.ParseCIDR(cidr)
		if err != nil {
			logger.LogWarn(origin+": invalid CIDR, entry skipped", "cidr", raw, "error", err.Error())
			continue
		}
		out = append(out, n)
	}
	return out
}

// maxXFFEntries caps how many X-Forwarded-For entries the middleware will walk
// per request. http.Server.MaxHeaderBytes already bounds the total header size,
// so this is a belt-and-braces cap that keeps per-request cost predictable even
// under header-abuse patterns.
const maxXFFEntries = 32

// clientIPFromRequest derives the client IP for policy decisions (IP filtering,
// rate limiting). When trustForwarded is true AND proxyNets is non-empty, the
// X-Forwarded-For header is walked right-to-left, skipping trusted-proxy hops,
// and the first non-trusted entry is returned. Otherwise r.RemoteAddr's host
// portion is returned.
//
// Without proxyNets, XFF is ignored unconditionally \u2014 this prevents clients
// from spoofing their IP by injecting XFF values.
func clientIPFromRequest(r *http.Request, trustForwarded bool, proxyNets []*net.IPNet) string {
	if trustForwarded && len(proxyNets) > 0 {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			// Walk the XFF list from right to left, skipping entries that are
			// trusted proxies. The first non-trusted entry is the real client IP.
			//
			// Iterate in place with strings.LastIndexByte rather than allocating
			// a full []string via strings.Split. Under XFF-heavy load (DDoS via
			// header spam, for example) this removes a per-request allocation
			// and cuts GC pressure on the hottest path in the filter.
			end := len(xff)
			walked := 0
			for end > 0 && walked < maxXFFEntries {
				start := strings.LastIndexByte(xff[:end], ',') + 1
				candidate := strings.TrimSpace(xff[start:end])
				end = start - 1 // skip the comma for the next iteration
				walked++
				if candidate == "" {
					continue
				}
				candidateIP := net.ParseIP(candidate)
				if candidateIP == nil {
					continue
				}
				if isTrustedProxy(candidateIP, proxyNets) {
					continue
				}
				return candidate
			}
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// isTrustedProxy reports whether ip is within any of the trusted proxy CIDRs.
// When no CIDRs are configured, every address is treated as untrusted (safe default).
func isTrustedProxy(ip net.IP, proxyNets []*net.IPNet) bool {
	for _, n := range proxyNets {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

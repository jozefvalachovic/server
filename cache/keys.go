package cache

import (
	"crypto/sha256"
	"encoding/hex"
	"maps"
	"net/url"
	"slices"
	"strings"
)

// BuildResponseKey returns a stable, canonical cache key for an HTTP GET response.
//
// Key format: {prefix}:GET:{urlPath}:{sortedQuery}
//
// The prefix is typically "{userID}_{resourceName}" (e.g. "u42_products").
// Query parameters are sorted alphabetically so that different orderings of the
// same parameters — e.g. "?a=1&b=2" and "?b=2&a=1" — resolve to the same key.
// An empty rawQuery produces a key ending in ":" which is still valid and distinct
// from a key with query parameters.
//
// Example:
//
//	BuildResponseKey("u42_products", "/api/products", "page=2&category=shoes")
//	// → "u42_products:GET:/api/products:category=shoes&page=2"
func BuildResponseKey(prefix, urlPath, rawQuery string) string {
	sorted := sortedQuery(rawQuery)
	var b strings.Builder
	b.WriteString(prefix)
	b.WriteString(":GET:")
	b.WriteString(urlPath)
	b.WriteByte(':')
	b.WriteString(sorted)
	return b.String()
}

// sortedQuery returns a deterministic query string with parameters sorted by key,
// then by value. Malformed query strings are replaced with a stable SHA-256
// hash of the raw bytes (prefixed with "h:") so that the cache key remains
// bounded in length and does not echo unparseable user input verbatim.
func sortedQuery(rawQuery string) string {
	if rawQuery == "" {
		return ""
	}

	params, err := url.ParseQuery(rawQuery)
	if err != nil {
		// Cannot parse — fall back to a stable hash of the raw bytes. Two
		// requests with the same malformed query still resolve to the same
		// key (preserving cache benefit) while avoiding unbounded key growth
		// and avoiding leaking raw query bytes into log-sinks that might
		// consume the key verbatim.
		sum := sha256.Sum256([]byte(rawQuery))
		return "h:" + hex.EncodeToString(sum[:8])
	}

	keys := slices.Sorted(maps.Keys(params))

	pairs := make([]string, 0, len(params))
	for _, k := range keys {
		vals := params[k]
		slices.Sort(vals)
		for _, v := range vals {
			pairs = append(pairs, url.QueryEscape(k)+"="+url.QueryEscape(v))
		}
	}

	return strings.Join(pairs, "&")
}

package middleware

import "strings"

// pathSkipper is a precomputed predicate over a set of exact paths and
// trailing-slash prefixes. Used by Auth and Timeout to implement SkipPaths
// with O(1) exact-match and linear prefix scanning without re-allocating
// on each request.
type pathSkipper struct {
	exact    map[string]bool
	prefixes []string
}

// newPathSkipper partitions paths into exact matches and prefix matches.
// A path ending in "/" is treated as a prefix; otherwise as an exact match.
// For example "/admin/" matches "/admin/metrics" and "/admin/users", while
// "/health" matches only "/health".
func newPathSkipper(paths []string) pathSkipper {
	s := pathSkipper{exact: make(map[string]bool, len(paths))}
	for _, p := range paths {
		if len(p) > 0 && p[len(p)-1] == '/' {
			s.prefixes = append(s.prefixes, p)
		} else {
			s.exact[p] = true
		}
	}
	return s
}

// skip reports whether the given request path matches any configured exact
// or prefix entry.
func (s pathSkipper) skip(path string) bool {
	if s.exact[path] {
		return true
	}
	for _, sp := range s.prefixes {
		if strings.HasPrefix(path, sp) {
			return true
		}
	}
	return false
}

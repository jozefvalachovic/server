package routes

import (
	"net/http"
	"strings"
)

// VersionedGroup registers a set of routes under a common version prefix
// (e.g. "/v1"). Each registrar receives the prefixed mux path automatically.
//
// Example:
//
//	routes.VersionedGroup(mux, "/v1",
//	    productRegistrar,
//	    userRegistrar,
//	)
//	// Registers /v1/products, /v1/users, etc.
func VersionedGroup(mux *http.ServeMux, prefix string, registrars ...RegisterRouteRegistrar) {
	prefix = strings.TrimRight(prefix, "/")
	// Create a child mux whose routes are mounted under the prefix.
	child := http.NewServeMux()
	for _, reg := range registrars {
		reg(child)
	}
	mux.Handle(prefix+"/", http.StripPrefix(prefix, child))
}

// VersionPrefix returns middleware that strips a version prefix from the
// request path before forwarding to the next handler. This is useful when
// you want to add versioning to an existing handler without changing its
// route registrations.
//
// Example:
//
//	mux.Handle("/v1/", middleware.VersionPrefix("/v1")(existingHandler))
func VersionPrefix(prefix string) func(http.Handler) http.Handler {
	prefix = strings.TrimRight(prefix, "/")
	return func(next http.Handler) http.Handler {
		return http.StripPrefix(prefix, next)
	}
}

package middleware

import (
	"context"
	"net/http"
	"strings"

	"github.com/jozefvalachovic/server/response"
)

// AuthScheme identifies the authentication scheme expected by the middleware.
type AuthScheme string

const (
	// AuthSchemeBearer expects an "Authorization: Bearer <token>" header.
	AuthSchemeBearer AuthScheme = "Bearer"
	// AuthSchemeAPIKey expects an API key in a configurable header.
	// Default header: "X-API-Key".
	AuthSchemeAPIKey AuthScheme = "APIKey"
)

// AuthConfig configures the Auth middleware.
type AuthConfig struct {
	// Scheme determines how the credential is extracted from the request.
	// Default: AuthSchemeBearer.
	Scheme AuthScheme

	// APIKeyHeader is the header name used when Scheme is AuthSchemeAPIKey.
	// Default: "X-API-Key".
	APIKeyHeader string

	// Verify is called with the raw credential (token or API key) extracted
	// from the request. Return the principal identity string (e.g. subject,
	// user ID) on success, or a non-nil error on failure.
	//
	// Verify is REQUIRED — the middleware panics on construction if nil.
	Verify func(ctx context.Context, credential string) (identity string, err error)

	// Realm is the value of the WWW-Authenticate challenge sent on 401.
	// Default: "API".
	Realm string

	// SkipPaths lists exact paths excluded from authentication.
	// Useful for e.g. "/health", "/readiness".
	SkipPaths []string
}

type authIdentityKey struct{}

// AuthIdentityFromContext returns the authenticated principal stored in the
// request context by the Auth middleware, or an empty string when not present.
func AuthIdentityFromContext(r *http.Request) string {
	v, _ := r.Context().Value(authIdentityKey{}).(string)
	return v
}

// Auth enforces authentication on every request using a caller-supplied
// verification function, keeping the middleware credential-format agnostic.
//
// On success the verified identity is stored in the request context and can be
// retrieved with AuthIdentityFromContext. On failure a 401 Unauthorized
// response is returned with a WWW-Authenticate header.
//
// Example — Bearer JWT validation:
//
//	middleware.Auth(middleware.AuthConfig{
//	    Scheme: middleware.AuthSchemeBearer,
//	    Verify: func(ctx context.Context, token string) (string, error) {
//	        claims, err := jwtParser.ParseWithClaims(token, ...)
//	        if err != nil { return "", err }
//	        return claims.Subject, nil
//	    },
//	})
//
// Example — API key validation:
//
//	middleware.Auth(middleware.AuthConfig{
//	    Scheme:       middleware.AuthSchemeAPIKey,
//	    APIKeyHeader: "X-API-Key",
//	    Verify: func(ctx context.Context, key string) (string, error) {
//	        id, ok := apiKeyStore.Lookup(key)
//	        if !ok { return "", errors.New("invalid API key") }
//	        return id, nil
//	    },
//	})
func Auth(cfg AuthConfig) func(http.Handler) http.Handler {
	if cfg.Verify == nil {
		panic("middleware.Auth: Verify function must not be nil")
	}
	if cfg.Scheme == "" {
		cfg.Scheme = AuthSchemeBearer
	}
	if cfg.APIKeyHeader == "" {
		cfg.APIKeyHeader = "X-API-Key"
	}
	if cfg.Realm == "" {
		cfg.Realm = "API"
	}
	skipPaths := make(map[string]bool, len(cfg.SkipPaths))
	for _, p := range cfg.SkipPaths {
		skipPaths[p] = true
	}

	wwwAuth := string(cfg.Scheme) + ` realm="` + cfg.Realm + `"`

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// SkipPaths supports both exact-match and prefix-match (trailing '/').
			// "/health" matches only "/health"; "/admin/" matches "/admin/metrics".
			path := r.URL.Path
			if skipPaths[path] {
				next.ServeHTTP(w, r)
				return
			}
			for sp := range skipPaths {
				if len(sp) > 0 && sp[len(sp)-1] == '/' && strings.HasPrefix(path, sp) {
					next.ServeHTTP(w, r)
					return
				}
			}

			credential, ok := extractCredential(r, cfg.Scheme, cfg.APIKeyHeader)
			if !ok {
				w.Header().Set("WWW-Authenticate", wwwAuth)
				response.APIErrorWriter(w, response.APIError[any]{
					Code:    http.StatusUnauthorized,
					Error:   new("Unauthorized"),
					Message: "Missing or malformed credentials.",
				})
				return
			}

			identity, err := cfg.Verify(r.Context(), credential)
			if err != nil {
				w.Header().Set("WWW-Authenticate", wwwAuth)
				response.APIErrorWriter(w, response.APIError[any]{
					Code:    http.StatusUnauthorized,
					Error:   new("Unauthorized"),
					Message: "Invalid credentials.",
				})
				return
			}

			ctx := context.WithValue(r.Context(), authIdentityKey{}, identity)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

func extractCredential(r *http.Request, scheme AuthScheme, apiKeyHeader string) (string, bool) {
	switch scheme {
	case AuthSchemeAPIKey:
		key := r.Header.Get(apiKeyHeader)
		return key, key != ""
	default: // Bearer
		auth := r.Header.Get("Authorization")
		if !strings.HasPrefix(auth, "Bearer ") {
			return "", false
		}
		token := strings.TrimPrefix(auth, "Bearer ")
		return token, token != ""
	}
}

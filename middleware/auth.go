package middleware

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"

	"github.com/jozefvalachovic/server/response"

	"github.com/jozefvalachovic/logger/v4"
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

	// OnAuthFailure is an optional callback invoked on every failed
	// authentication attempt (missing credential, invalid token, etc.).
	// Use it for audit logging, metrics, or brute-force detection.
	// Called with the request and the error returned by Verify (nil when
	// the credential was missing/malformed before Verify was called).
	//
	// Brute-force mitigation: the Auth middleware does not rate-limit failed
	// attempts internally. Place the RateLimit middleware before Auth in the
	// stack to bound attempts per client, or implement backoff logic inside
	// OnAuthFailure. AuditAuthFailure is provided as a ready-to-use callback.
	OnAuthFailure func(r *http.Request, err error)
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
	skipExact := make(map[string]bool, len(cfg.SkipPaths))
	var skipPrefixes []string
	for _, p := range cfg.SkipPaths {
		if len(p) > 0 && p[len(p)-1] == '/' {
			skipPrefixes = append(skipPrefixes, p)
		} else {
			skipExact[p] = true
		}
	}

	wwwAuth := string(cfg.Scheme) + ` realm="` + cfg.Realm + `"`

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// SkipPaths supports both exact-match and prefix-match (trailing '/').
			// "/health" matches only "/health"; "/admin/" matches "/admin/metrics".
			path := r.URL.Path
			if skipExact[path] {
				next.ServeHTTP(w, r)
				return
			}
			for _, sp := range skipPrefixes {
				if strings.HasPrefix(path, sp) {
					next.ServeHTTP(w, r)
					return
				}
			}

			credential, ok := extractCredential(r, cfg.Scheme, cfg.APIKeyHeader)
			if !ok {
				if cfg.OnAuthFailure != nil {
					cfg.OnAuthFailure(r, nil)
				}
				w.Header().Set("WWW-Authenticate", wwwAuth)
				response.APIErrorWriter(w, response.APIError[any]{
					Code:    http.StatusUnauthorized,
					Error:   response.ErrUnauthorized,
					Message: "Missing or malformed credentials.",
				})
				return
			}

			identity, err := cfg.Verify(r.Context(), credential)
			if err != nil {
				if cfg.OnAuthFailure != nil {
					cfg.OnAuthFailure(r, err)
				}
				w.Header().Set("WWW-Authenticate", wwwAuth)
				response.APIErrorWriter(w, response.APIError[any]{
					Code:    http.StatusUnauthorized,
					Error:   response.ErrUnauthorized,
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
		token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok {
			return "", false
		}
		return token, token != ""
	}
}

// MultiTokenVerify returns an AuthConfig.Verify function that accepts any one
// of the provided tokens. Tokens are compared in constant time. The returned
// identity is "token:<index>" where index is the 0-based position in the
// supplied slice.
//
// Use this for zero-downtime token rotation: deploy with [old, new], then
// remove the old token once all clients have migrated.
//
//	middleware.Auth(middleware.AuthConfig{
//	    Verify: middleware.MultiTokenVerify("old-secret", "new-secret"),
//	})
func MultiTokenVerify(tokens ...string) func(ctx context.Context, credential string) (string, error) {
	// Snapshot so callers cannot mutate after construction.
	frozen := make([]string, len(tokens))
	copy(frozen, tokens)
	return func(_ context.Context, credential string) (string, error) {
		for i, t := range frozen {
			if subtle.ConstantTimeCompare([]byte(credential), []byte(t)) == 1 {
				return fmt.Sprintf("token:%d", i), nil
			}
		}
		return "", errors.New("invalid token")
	}
}

// RotatingTokenVerify returns an AuthConfig.Verify function that delegates to
// a dynamically-refreshable set of valid tokens. The provided function is
// called on every request to obtain the current token list, enabling live
// rotation from a config file, vault, or environment variable without restart.
//
//	middleware.Auth(middleware.AuthConfig{
//	    Verify: middleware.RotatingTokenVerify(func() []string {
//	        return strings.Split(os.Getenv("API_TOKENS"), ",")
//	    }),
//	})
func RotatingTokenVerify(tokensFn func() []string) func(ctx context.Context, credential string) (string, error) {
	return func(_ context.Context, credential string) (string, error) {
		for i, t := range tokensFn() {
			if subtle.ConstantTimeCompare([]byte(credential), []byte(t)) == 1 {
				return fmt.Sprintf("token:%d", i), nil
			}
		}
		return "", errors.New("invalid token")
	}
}

// tokenStore provides a concurrent-safe, hot-swappable token list.
type tokenStore struct {
	mu     sync.RWMutex
	tokens []string
}

// NewTokenStore creates a token store initialised with the given tokens.
// Use Store.Rotate() to swap in a new set at runtime.
func NewTokenStore(initial ...string) *tokenStore {
	ts := &tokenStore{tokens: make([]string, len(initial))}
	copy(ts.tokens, initial)
	return ts
}

// Rotate atomically replaces the token set with newTokens.
func (s *tokenStore) Rotate(newTokens ...string) {
	s.mu.Lock()
	s.tokens = make([]string, len(newTokens))
	copy(s.tokens, newTokens)
	s.mu.Unlock()
}

// Verify implements the AuthConfig.Verify signature using the current token set.
func (s *tokenStore) Verify(_ context.Context, credential string) (string, error) {
	s.mu.RLock()
	tokens := s.tokens
	s.mu.RUnlock()
	for i, t := range tokens {
		if subtle.ConstantTimeCompare([]byte(credential), []byte(t)) == 1 {
			return fmt.Sprintf("token:%d", i), nil
		}
	}
	return "", errors.New("invalid token")
}

// AuditAuthFailure is a ready-to-use OnAuthFailure callback that emits a
// structured audit log entry for every failed authentication attempt.
func AuditAuthFailure(r *http.Request, err error) {
	msg := "missing credentials"
	if err != nil {
		msg = "invalid credentials"
	}
	logger.LogAudit(msg,
		"path", r.URL.Path,
		"method", r.Method,
		"remote", r.RemoteAddr,
	)
}

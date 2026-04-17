package middleware

import (
	"bytes"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/jozefvalachovic/server/cache"

	"github.com/jozefvalachovic/logger/v4"
)

// CacheBackend is the storage contract for the HTTP cache middleware.
//
// The built-in *cache.CacheStore satisfies this interface out of the box.
// Any alternative backend (e.g. a distributed TCP client for cross-pod
// shared caching) can be injected by implementing these four methods.
type CacheBackend interface {
	Get(key string) (any, error)
	Set(key string, val any, ttl *time.Duration) error
	Delete(key string) bool
	DeleteByPrefix(prefix string) int
}

// HTTPCacheConfig configures the HTTP response cache middleware.
type HTTPCacheConfig struct {
	// Store is the cache backend to use. Required; nil disables caching.
	// *cache.CacheStore satisfies CacheBackend and is the default choice.
	// Substitute a distributed client for cross-pod caching.
	Store CacheBackend

	// TTL overrides the store's default TTL for cached responses.
	// 0 means use the store's configured default.
	TTL time.Duration

	// KeyPrefix derives the cache namespace for each incoming request.
	// It is called once per request and must return a string in the form
	// "{userID}_{resourceName}" (e.g. "u42_products").
	//
	// IMPORTANT: In multi-tenant applications, KeyPrefix MUST include a
	// user or tenant discriminator. If two different authenticated users
	// share the same URL path, omitting the discriminator causes one user
	// to see cached responses belonging to the other. Example:
	//
	//   KeyPrefix: func(r *http.Request) string {
	//       uid := middleware.AuthIdentityFromContext(r)
	//       if uid == "" { return "" } // skip cache for unauthenticated
	//       return uid + "_products"
	//   }
	//
	// Returning "" bypasses the cache entirely for that request, which is the
	// correct behaviour for unauthenticated requests or routes that must never
	// be cached.
	KeyPrefix func(r *http.Request) string

	// InvalidateMethods lists HTTP methods whose 2xx responses cause the entire
	// prefix to be invalidated. Defaults to POST, PUT, PATCH, DELETE.
	InvalidateMethods []string
}

// defaultInvalidateMethods are the HTTP methods that trigger cache invalidation.
var defaultInvalidateMethods = []string{
	http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete,
}

// cacheResponseRecorder forwards all writes to the underlying ResponseWriter
// while simultaneously capturing the status code, response headers, and body.
// Captured headers are flushed to the underlying writer automatically on the
// first WriteHeader or Write call, preserving correct HTTP semantics.
//
// It also implements http.Flusher. If Flush is called the response is marked as
// streamed and will not be stored in the cache (streaming/chunked responses are
// not suitable for replay).
type cacheResponseRecorder struct {
	http.ResponseWriter
	statusCode      int
	capturedHeaders http.Header
	body            bytes.Buffer
	headersFlushed  bool
	writeFailed     bool
	streamed        bool // true if Flush() was called; prevents caching
}

func newCacheResponseRecorder(w http.ResponseWriter) *cacheResponseRecorder {
	return &cacheResponseRecorder{
		ResponseWriter:  w,
		statusCode:      http.StatusOK,
		capturedHeaders: make(http.Header),
	}
}

// statusRecorder captures only the HTTP status code while forwarding all
// writes directly to the underlying ResponseWriter. Used for mutation methods
// where only the status code is needed to decide whether to invalidate the
// cache prefix — buffering the full body would double peak memory use.
type statusRecorder struct {
	http.ResponseWriter
	statusCode int
}

func newStatusRecorder(w http.ResponseWriter) *statusRecorder {
	return &statusRecorder{ResponseWriter: w, statusCode: http.StatusOK}
}

func (r *statusRecorder) WriteHeader(code int) {
	r.statusCode = code
	r.ResponseWriter.WriteHeader(code)
}

// Header returns the recorder's own header map. Values are forwarded to the
// underlying ResponseWriter when WriteHeader (or the implicit 200 on Write) fires.
func (r *cacheResponseRecorder) Header() http.Header {
	return r.capturedHeaders
}

// WriteHeader commits the captured headers to the underlying writer and then
// forwards the status code. Subsequent calls are no-ops.
func (r *cacheResponseRecorder) WriteHeader(code int) {
	if r.headersFlushed {
		return
	}
	r.headersFlushed = true
	r.statusCode = code

	// Flush captured headers to the real response before committing.
	for k, vv := range r.capturedHeaders {
		for _, v := range vv {
			r.ResponseWriter.Header().Add(k, v)
		}
	}
	r.ResponseWriter.WriteHeader(code)
}

// Flush implements http.Flusher. It marks the response as streamed (so it is
// not stored in the cache) and flushes the underlying writer if it supports it.
func (r *cacheResponseRecorder) Flush() {
	r.streamed = true
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Write captures the body and forwards it to the underlying writer. If the
// underlying write fails, writeFailed is set so the entry is not cached.
func (r *cacheResponseRecorder) Write(b []byte) (int, error) {
	if !r.headersFlushed {
		r.WriteHeader(http.StatusOK)
	}
	n, err := r.ResponseWriter.Write(b)
	if err != nil {
		r.writeFailed = true
	} else {
		r.body.Write(b[:n])
	}
	return n, err
}

// HTTPCache returns an HTTPMiddleware that adds request/response caching:
//
//   - GET / HEAD: served from cache on hit; handler response stored on miss.
//   - POST / PUT / PATCH / DELETE (configurable): passes through to the handler,
//     then invalidates all entries for the prefix on a 2xx response.
//   - All other methods: pass through unchanged.
//
// Caching is skipped when:
//   - cfg.Store is nil or cfg.KeyPrefix is nil / returns ""
//   - The client sends Cache-Control: no-store
//   - The response status is not exactly 200
//   - The response sets a Set-Cookie header (session tokens must not be shared)
//   - The underlying write to the client fails mid-stream
func HTTPCache(cfg HTTPCacheConfig) func(http.Handler) http.Handler {
	invalidateMethods := cfg.InvalidateMethods
	if len(invalidateMethods) == 0 {
		invalidateMethods = defaultInvalidateMethods
	}
	invalidateSet := make(map[string]struct{}, len(invalidateMethods))
	for _, m := range invalidateMethods {
		invalidateSet[m] = struct{}{}
	}

	// Fail-fast probe: call KeyPrefix with a minimal synthetic request to catch
	// obviously broken configurations at server startup instead of at the first
	// real request. We only flag a ':' in the output when the probe returns a
	// non-empty string; a KeyPrefix that relies on request-specific fields
	// (headers, host, auth) may legitimately return "" here, and the runtime
	// guard below will still catch per-request breakage.
	if cfg.Store != nil && cfg.KeyPrefix != nil {
		probe := &http.Request{Method: http.MethodGet, URL: &url.URL{Path: "/"}, Header: http.Header{}}
		if pp := cfg.KeyPrefix(probe); pp != "" && strings.ContainsRune(pp, ':') {
			panic("middleware.HTTPCache: KeyPrefix returned a value containing ':' for a probe request; ':' is reserved as the prefix/key separator and must not appear in KeyPrefix output")
		}
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Guard: middleware not fully configured. KeyPrefix is probed at
			// construction time (see HTTPCache's init-time panic on a panicking
			// probe), so a nil KeyPrefix here indicates Store was nil-ed out
			// post-construction or the middleware was constructed via struct
			// literal without calling the factory.
			if cfg.Store == nil || cfg.KeyPrefix == nil {
				next.ServeHTTP(w, r)
				return
			}

			prefix := cfg.KeyPrefix(r)
			if prefix == "" {
				// Empty prefix signals "do not cache this request" (e.g. unauthenticated).
				next.ServeHTTP(w, r)
				return
			}
			// A colon in the prefix would break cacheKeyPrefix extraction: the
			// prefix index buckets keys by the segment before the first ':', so
			// "tenant:42_products:GET:/..." would be keyed under "tenant" instead
			// of the intended prefix. Log a warning and bypass rather than
			// silently serving wrong (or no) invalidations.
			if strings.ContainsRune(prefix, ':') {
				logger.LogWarn("HTTPCache: KeyPrefix contains ':' which breaks prefix isolation; bypassing cache",
					"prefix", prefix)
				next.ServeHTTP(w, r)
				return
			}

			// Honour explicit client opt-out (handles "no-store", "no-cache, no-store", etc.).
			//
			// RFC 7234 §5.2.1.4 "no-cache" semantics (revalidation) are intentionally
			// not implemented: this cache has no ETag/Last-Modified revalidation support.
			// Clients that send "no-cache" without "no-store" will still receive cached
			// responses. Use "Cache-Control: no-store" for guaranteed bypass.
			if strings.Contains(r.Header.Get("Cache-Control"), "no-store") {
				next.ServeHTTP(w, r)
				return
			}

			// ── GET / HEAD ────────────────────────────────────────────────────
			if r.Method == http.MethodGet || r.Method == http.MethodHead {
				key := cache.BuildResponseKey(prefix, r.URL.Path, r.URL.RawQuery)

				if val, err := cfg.Store.Get(key); err == nil {
					if cached, ok := val.(cache.CachedResponse); ok {
						// Cache HIT — replay all stored headers then the body.
						for k, vv := range cached.Headers {
							for _, v := range vv {
								w.Header().Add(k, v)
							}
						}
						w.Header().Set("X-Cache", "HIT")
						w.WriteHeader(cached.StatusCode)
						if r.Method != http.MethodHead {
							if _, werr := w.Write(cached.Body); werr != nil {
								logger.LogWarn("HTTPCache: failed to write cached body",
									"key", key, "error", werr.Error())
							}
						}
						return
					}
				}

				// Cache MISS — record the handler's response.
				// HEAD: mark as BYPASS since the response won't be stored
				// (storing a HEAD response would serve an empty body to GET clients).
				rec := newCacheResponseRecorder(w)
				if r.Method == http.MethodHead {
					rec.capturedHeaders.Set("X-Cache", "BYPASS")
				} else {
					rec.capturedHeaders.Set("X-Cache", "MISS")
				}
				next.ServeHTTP(rec, r)

				// Only populate the cache for GET (never HEAD — HEAD has no body, so
				// storing it would serve an empty body to subsequent GET requests).
				// Also skip non-200, write failures, streamed responses, and Set-Cookie.
				if r.Method == http.MethodGet &&
					rec.statusCode == http.StatusOK &&
					!rec.writeFailed &&
					!rec.streamed &&
					rec.capturedHeaders.Get("Set-Cookie") == "" {

					var ttl *time.Duration
					if cfg.TTL > 0 {
						t := cfg.TTL
						ttl = &t
					}
					// Clone response headers for storage, excluding the per-response
					// X-Cache sentinel (set fresh as HIT/MISS on every response).
					hdrs := make(http.Header, len(rec.capturedHeaders))
					for k, vv := range rec.capturedHeaders {
						if k == "X-Cache" {
							continue
						}
						hdrs[k] = slices.Clone(vv)
					}
					entry := cache.CachedResponse{
						StatusCode: rec.statusCode,
						Headers:    hdrs,
						// Copy the bytes out of the buffer's internal array so the
						// cached slice is fully independent of the recorder's lifetime.
						Body: bytes.Clone(rec.body.Bytes()),
					}
					if serr := cfg.Store.Set(key, entry, ttl); serr != nil {
						logger.LogWarn("HTTPCache: failed to store response",
							"key", key, "error", serr.Error())
					}
				}
				return
			}

			// ── Mutation methods ──────────────────────────────────────────────
			if _, shouldInvalidate := invalidateSet[r.Method]; shouldInvalidate {
				rec := newStatusRecorder(w)
				next.ServeHTTP(rec, r)

				if rec.statusCode >= 200 && rec.statusCode < 300 {
					n := cfg.Store.DeleteByPrefix(prefix)
					logger.LogDebug("HTTPCache: prefix invalidated after mutation",
						"prefix", prefix,
						"method", r.Method,
						"evicted", n,
					)
				}
				return
			}

			// All other methods (OPTIONS, TRACE, …) pass through unchanged.
			next.ServeHTTP(w, r)
		})
	}
}

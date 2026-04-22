package middleware

import (
	"bytes"
	"context"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/jozefvalachovic/server/cache"
	"github.com/jozefvalachovic/server/internal/singleflight"

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

	// StaleWhileRevalidate extends the effective lifetime of a cached entry
	// beyond TTL. Within the stale window the cached body is served
	// immediately with X-Cache: STALE and a background refresh is dispatched
	// via singleflight so only one handler invocation runs per key. Zero
	// disables SWR (entries expire exactly at TTL, as before).
	//
	// Tuning: a typical value is 3–10× TTL. Large windows reduce the chance
	// of a synchronous miss under low traffic but increase the maximum age of
	// data a client may observe. Do NOT enable SWR on endpoints whose
	// correctness depends on up-to-the-second data (authoritative reads,
	// balances, auth scopes).
	//
	// Entries are held in the underlying store for TTL + StaleWhileRevalidate
	// + StaleIfError, so cache memory scales accordingly.
	StaleWhileRevalidate time.Duration

	// StaleIfError extends the stale window further for the specific purpose
	// of shielding clients from downstream failures. When a background refresh
	// (or a fresh miss) returns a non-2xx response and a stale entry is still
	// within TTL + StaleWhileRevalidate + StaleIfError, the stale body is
	// served with X-Cache: STALE-ERROR instead of surfacing the error.
	//
	// Requires a non-zero TTL. Zero disables the behaviour (errors surface
	// normally). Callers who enable this should monitor X-Cache: STALE-ERROR
	// rates — sustained non-zero rates indicate an unhealthy upstream.
	StaleIfError time.Duration
}

// defaultInvalidateMethods are the HTTP methods that trigger cache invalidation.
var defaultInvalidateMethods = []string{
	http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete,
}

// cachedResponseHeaderDenylist lists response headers that must never be
// stored in the cache because they carry per-identity or per-connection state
// that cannot be safely replayed to a different caller. Keys are in
// canonical form as produced by http.CanonicalHeaderKey so the match is O(1).
var cachedResponseHeaderDenylist = map[string]struct{}{
	"Set-Cookie":          {}, // session tokens
	"Proxy-Authenticate":  {}, // hop-by-hop
	"Www-Authenticate":    {}, // challenges are per-request
	"Authorization":       {}, // defensive: never a response header, but guard anyway
	"Proxy-Authorization": {}, // defensive: hop-by-hop
}

// forbiddenPrefixChars are characters that must never appear in a KeyPrefix
// return value. ':' is the prefix/key separator in the cache store's prefix
// index — a prefix containing ':' would be bucketed under the wrong prefix
// and break invalidation. '\n' and '\x00' are rejected defensively so
// prefixes remain safe to embed in log lines, URLs, or future wire formats
// (e.g. peer-invalidation requests) without escaping.
const forbiddenPrefixChars = ":\n\x00"

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

// bufferedRecorder captures the handler's response entirely in memory without
// writing to any real client. It is used by the singleflight-dedup'd miss path
// so that one handler execution can be replayed to many concurrent callers.
//
// Unlike cacheResponseRecorder it implements http.ResponseWriter without an
// embedded real writer — nothing is forwarded anywhere until the caller
// explicitly replays the captured snapshot with writeSnapshot.
type bufferedRecorder struct {
	headers    http.Header
	statusCode int
	body       bytes.Buffer
	wroteHdr   bool
	streamed   bool
}

func newBufferedRecorder() *bufferedRecorder {
	return &bufferedRecorder{
		headers:    make(http.Header),
		statusCode: http.StatusOK,
	}
}

func (r *bufferedRecorder) Header() http.Header { return r.headers }

func (r *bufferedRecorder) WriteHeader(code int) {
	if r.wroteHdr {
		return
	}
	r.wroteHdr = true
	r.statusCode = code
}

func (r *bufferedRecorder) Write(b []byte) (int, error) {
	if !r.wroteHdr {
		r.WriteHeader(http.StatusOK)
	}
	return r.body.Write(b)
}

// Flush marks the response as streamed so it is not stored in the cache.
// There is no underlying writer to flush; callers in the singleflight path
// will still receive the complete buffered body when the handler returns.
func (r *bufferedRecorder) Flush() { r.streamed = true }

// missSnapshot is the result of a handler execution captured by
// bufferedRecorder, safe to replay to multiple clients.
type missSnapshot struct {
	statusCode int
	headers    http.Header
	body       []byte
	streamed   bool
}

func snapshotFrom(rec *bufferedRecorder) *missSnapshot {
	hdrs := make(http.Header, len(rec.headers))
	for k, vv := range rec.headers {
		hdrs[k] = slices.Clone(vv)
	}
	return &missSnapshot{
		statusCode: rec.statusCode,
		headers:    hdrs,
		body:       bytes.Clone(rec.body.Bytes()),
		streamed:   rec.streamed,
	}
}

// writeSnapshot replays a missSnapshot to a client ResponseWriter, adding the
// X-Cache: MISS sentinel. Used by both the singleflight leader and followers.
func (s *missSnapshot) writeSnapshot(w http.ResponseWriter, includeBody bool) {
	for k, vv := range s.headers {
		for _, v := range vv {
			w.Header().Add(k, v)
		}
	}
	w.Header().Set("X-Cache", "MISS")
	w.WriteHeader(s.statusCode)
	if includeBody {
		if _, werr := w.Write(s.body); werr != nil {
			logger.LogWarn("HTTPCache: failed to write miss body",
				"status", s.statusCode, "error", werr.Error())
		}
	}
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
//   - The response is already content-encoded (Content-Encoding header set)
//   - The underlying write to the client fails mid-stream
//
// # Middleware ordering
//
// HTTPCache MUST be placed BEFORE the Compress middleware in the request path
// (i.e. closer to the client, outer wrap). Otherwise the compressed bytes are
// what gets stored, and the cache will replay a gzipped body to a client that
// did not advertise Accept-Encoding: gzip. Correct order (outermost first):
//
//	Compress → HTTPCache → handler  // WRONG — caches gzipped bytes
//	HTTPCache → Compress → handler  // CORRECT — caches identity bytes
//
// As a belt-and-braces guard, responses that already carry a Content-Encoding
// header are never stored.
//
// # Vary awareness
//
// The default cache key is derived from the KeyPrefix plus the request path
// and sorted query string. It does NOT automatically vary by request headers
// (Accept-Language, Accept-Encoding, tenant ID, …). Handlers that produce
// different representations for the same URL must encode the discriminating
// header value into KeyPrefix — for example:
//
//	KeyPrefix: func(r *http.Request) string {
//	    uid := middleware.AuthIdentityFromContext(r)
//	    lang := r.Header.Get("Accept-Language")
//	    return uid + "_" + lang + "_products"
//	}
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
	// real request. We only flag forbidden characters in the output when the
	// probe returns a non-empty string; a KeyPrefix that relies on request-
	// specific fields (headers, host, auth) may legitimately return "" here,
	// and the runtime guard below will still catch per-request breakage.
	if cfg.Store != nil && cfg.KeyPrefix != nil {
		probe := &http.Request{Method: http.MethodGet, URL: &url.URL{Path: "/"}, Header: http.Header{}}
		if pp := cfg.KeyPrefix(probe); pp != "" && strings.ContainsAny(pp, forbiddenPrefixChars) {
			panic("middleware.HTTPCache: KeyPrefix returned a value containing a forbidden character (':', newline, or NUL) for a probe request; these characters are reserved and must not appear in KeyPrefix output")
		}
	}

	// sf deduplicates concurrent cache misses for the same key. When many
	// requests hit the same URL simultaneously and the cache is cold (e.g.
	// immediately after a pod restart), without singleflight every request
	// would run the handler and stampede the underlying data source. With
	// singleflight, only one handler invocation runs per key; all other
	// concurrent requests share its result.
	sf := &singleflight.Group{}

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
			// A forbidden character in the prefix would break cacheKeyPrefix
			// extraction (':' is the separator) or be unsafe to embed in
			// logs/URLs ('\n', '\x00'). Log a warning and bypass rather than
			// silently serving wrong (or no) invalidations.
			if strings.ContainsAny(prefix, forbiddenPrefixChars) {
				logger.LogWarn("HTTPCache: KeyPrefix contains a forbidden character (':', newline, or NUL); bypassing cache",
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
						now := time.Now()
						// Fresh window: either FreshUntil is unset (classic
						// behaviour — entry is fresh until the store evicts it)
						// or we are before the recorded fresh boundary.
						fresh := cached.FreshUntil.IsZero() || now.Before(cached.FreshUntil)
						if fresh {
							replayCached(w, r, cached, "HIT")
							return
						}
						// Stale window: entry is past its fresh boundary but
						// still served from the store. Fire a background
						// refresh (singleflight-deduped) and serve the stale
						// body immediately.
						if cfg.StaleWhileRevalidate > 0 && now.Before(cached.FreshUntil.Add(cfg.StaleWhileRevalidate)) {
							replayCached(w, r, cached, "STALE")
							if r.Method == http.MethodGet {
								go backgroundRefresh(cfg, sf, key, r, next)
							}
							return
						}
						// Beyond SWR but within StaleIfError: only used as a
						// fallback when the foreground refresh fails; fall
						// through to the miss path.
					}
				}

				// Cache MISS.
				//
				// HEAD is never stored in the cache (HEAD has no body, so storing
				// it would serve an empty body to subsequent GET clients) and is
				// also not routed through singleflight — HEADs are cheap and
				// share no work with GETs.
				if r.Method == http.MethodHead {
					rec := newCacheResponseRecorder(w)
					rec.capturedHeaders.Set("X-Cache", "BYPASS")
					next.ServeHTTP(rec, r)
					return
				}

				// GET miss: singleflight-dedup concurrent identical misses so
				// that a cold-cache stampede (e.g. right after a pod restart)
				// results in only one handler execution per key. Followers share
				// the leader's snapshot.
				//
				// The cache Set() happens inside fn so that it is executed by
				// the leader only — singleflight's returned shared flag applies
				// to all callers (leader and followers alike) when a call is
				// deduplicated, so it cannot be used to distinguish the two.
				result, _, _ := sf.Do(key, func() (any, error) {
					rec := newBufferedRecorder()
					next.ServeHTTP(rec, r)
					snap := snapshotFrom(rec)

					if storeResponse(cfg, key, snap) {
						return snap, nil
					}
					return snap, nil
				})
				snap, _ := result.(*missSnapshot)
				if snap == nil {
					// Defensive: singleflight didn't yield a snapshot. Fall back
					// to running the handler against the client writer directly.
					rec := newCacheResponseRecorder(w)
					rec.capturedHeaders.Set("X-Cache", "MISS")
					next.ServeHTTP(rec, r)
					return
				}

				// StaleIfError: if the handler returned a non-2xx and we still
				// have a stale entry within TTL+SWR+StaleIfError, replay it
				// instead of surfacing the error.
				if cfg.StaleIfError > 0 && (snap.statusCode < 200 || snap.statusCode >= 300) {
					if val, err := cfg.Store.Get(key); err == nil {
						if cached, ok := val.(cache.CachedResponse); ok && !cached.FreshUntil.IsZero() {
							limit := cached.FreshUntil.Add(cfg.StaleWhileRevalidate).Add(cfg.StaleIfError)
							if time.Now().Before(limit) {
								replayCached(w, r, cached, "STALE-ERROR")
								return
							}
						}
					}
				}

				snap.writeSnapshot(w, true)
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

// replayCached writes a CachedResponse to the client ResponseWriter, tagging
// it with X-Cache: HIT | STALE | STALE-ERROR. HEAD requests receive headers
// and status only, matching HTTP semantics.
func replayCached(w http.ResponseWriter, r *http.Request, cached cache.CachedResponse, xCache string) {
	for k, vv := range cached.Headers {
		for _, v := range vv {
			w.Header().Add(k, v)
		}
	}
	w.Header().Set("X-Cache", xCache)
	w.WriteHeader(cached.StatusCode)
	if r.Method != http.MethodHead {
		if _, werr := w.Write(cached.Body); werr != nil {
			logger.LogWarn("HTTPCache: failed to write cached body",
				"status", cached.StatusCode, "xCache", xCache, "error", werr.Error())
		}
	}
}

// storeResponse writes an eligible snapshot to the cache store, applying the
// Set-Cookie / Content-Encoding / status guards and the response-header
// denylist. Returns true when the response was stored.
//
// When StaleWhileRevalidate or StaleIfError are configured, the store TTL is
// extended to TTL + SWR + StaleIfError and CachedResponse.FreshUntil marks
// the end of the fresh window so the middleware can distinguish fresh from
// stale on retrieval.
func storeResponse(cfg HTTPCacheConfig, key string, snap *missSnapshot) bool {
	if snap.statusCode != http.StatusOK ||
		snap.streamed ||
		snap.headers.Get("Set-Cookie") != "" ||
		snap.headers.Get("Content-Encoding") != "" {
		return false
	}

	hdrs := make(http.Header, len(snap.headers))
	for k, vv := range snap.headers {
		if _, deny := cachedResponseHeaderDenylist[http.CanonicalHeaderKey(k)]; deny {
			continue
		}
		if k == "X-Cache" {
			continue
		}
		hdrs[k] = slices.Clone(vv)
	}

	entry := cache.CachedResponse{
		StatusCode: snap.statusCode,
		Headers:    hdrs,
		Body:       bytes.Clone(snap.body),
	}

	var ttl *time.Duration
	if cfg.TTL > 0 {
		fresh := cfg.TTL
		entry.FreshUntil = time.Now().Add(fresh)
		total := fresh + cfg.StaleWhileRevalidate + cfg.StaleIfError
		ttl = &total
	}

	if serr := cfg.Store.Set(key, entry, ttl); serr != nil {
		logger.LogWarn("HTTPCache: failed to store response",
			"key", key, "error", serr.Error())
		return false
	}
	return true
}

// backgroundRefresh revalidates a stale cache entry out-of-band. It detaches
// the request from the originating client context (so the refresh survives
// even if the client disconnects) and singleflight-deduplicates concurrent
// refreshes of the same key. Non-2xx responses are not stored — the previous
// (stale) entry continues to be served until its hard expiry. Panics inside
// the handler are recovered and logged.
func backgroundRefresh(
	cfg HTTPCacheConfig,
	sf *singleflight.Group,
	key string,
	r *http.Request,
	next http.Handler,
) {
	defer func() {
		if rec := recover(); rec != nil {
			logger.LogError("HTTPCache: panic in background refresh",
				"key", key, "panic", rec)
		}
	}()

	// Detach the refresh from the originating request's context so cancellation
	// of the client-facing request does not abort the refresh mid-flight.
	// The refresh carries its own deadline derived from the fresh TTL so a
	// wedged handler cannot leak a goroutine forever.
	refreshTimeout := cfg.TTL
	if refreshTimeout <= 0 {
		refreshTimeout = 30 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), refreshTimeout)
	defer cancel()

	cloned := r.Clone(ctx)
	// Preserve only the URL / method / headers for replay. Body has already
	// been consumed by the foreground handler (if any) and SWR only applies
	// to GET so there is nothing to preserve.
	cloned.Body = http.NoBody

	_, _, _ = sf.Do(key+"|refresh", func() (any, error) {
		rec := newBufferedRecorder()
		next.ServeHTTP(rec, cloned)
		snap := snapshotFrom(rec)
		storeResponse(cfg, key, snap)
		return snap, nil
	})
}

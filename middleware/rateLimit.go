package middleware

import (
	"context"
	"errors"
	"hash/maphash"
	"math"
	"net"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jozefvalachovic/server/response"

	"github.com/jozefvalachovic/logger/v4"
)

// ErrRateLimitExceeded is a sentinel error for programmatic detection of
// rate-limit rejections in tests, middleware chains, or error-handling logic.
// Use errors.Is(err, middleware.ErrRateLimitExceeded) to check.
var ErrRateLimitExceeded = errors.New("rate limit exceeded")

// rateLimitShards is the number of independent map shards used by the HTTP
// rate limiter. 16 shards reduce lock contention by ~16× under high-concurrency
// workloads with many distinct client IPs while keeping memory overhead minimal.
const rateLimitShards = 16

type rateBucket struct {
	mu       sync.Mutex
	tokens   float64
	lastSeen atomic.Int64 // UnixNano; read atomically by cleanup without locking mu
}

// rateShard is one independent partition of the bucket map, protected by its
// own RWMutex so that operations on different shards never contend.
type rateShard struct {
	mu      sync.RWMutex
	buckets map[string]*rateBucket
}

// shardedBuckets distributes client keys across rateLimitShards independent
// maps, selected by hashing the key. This reduces mutex contention compared
// to a single global RWMutex when a pod handles thousands of unique IPs/sec.
type shardedBuckets struct {
	shards [rateLimitShards]rateShard
	seed   maphash.Seed
}

func newShardedBuckets() *shardedBuckets {
	sb := &shardedBuckets{seed: maphash.MakeSeed()}
	for i := range sb.shards {
		sb.shards[i].buckets = make(map[string]*rateBucket)
	}
	return sb
}

func (sb *shardedBuckets) shard(key string) *rateShard {
	var h maphash.Hash
	h.SetSeed(sb.seed)
	h.WriteString(key)
	return &sb.shards[h.Sum64()%rateLimitShards]
}

func (sb *shardedBuckets) getOrCreate(key string, burst float64, now time.Time) *rateBucket {
	s := sb.shard(key)
	s.mu.RLock()
	b, ok := s.buckets[key]
	s.mu.RUnlock()
	if ok {
		return b
	}
	s.mu.Lock()
	b, ok = s.buckets[key]
	if !ok {
		b = &rateBucket{tokens: burst}
		b.lastSeen.Store(now.UnixNano())
		s.buckets[key] = b
	}
	s.mu.Unlock()
	return b
}

// cleanup iterates all shards and removes idle entries. Each shard is locked
// independently so live traffic on other shards is never blocked.
func (sb *shardedBuckets) cleanup(idleTimeout time.Duration) {
	now := time.Now()
	for i := range sb.shards {
		s := &sb.shards[i]
		s.mu.RLock()
		var stale []string
		for key, b := range s.buckets {
			idle := now.Sub(time.Unix(0, b.lastSeen.Load()))
			if idle > idleTimeout {
				stale = append(stale, key)
			}
		}
		s.mu.RUnlock()
		if len(stale) > 0 {
			s.mu.Lock()
			for _, key := range stale {
				delete(s.buckets, key)
			}
			s.mu.Unlock()
		}
	}
}

// HTTPRateLimitConfig configures the HTTPRateLimit middleware.
// All fields are optional; zero values fall back to documented defaults.
type HTTPRateLimitConfig struct {
	// RequestsPerSecond is the sustained token refill rate per client key.
	// Default: 10.
	RequestsPerSecond float64

	// Burst is the maximum number of tokens (requests) a client can accumulate
	// and spend in a single burst. Default: 20.
	Burst int

	// KeyFunc extracts the per-client key used to identify a caller.
	// When nil, the key is derived from the client IP. If TrustedProxies is
	// set, the X-Forwarded-For header is walked right-to-left, skipping
	// trusted-proxy hops — otherwise r.RemoteAddr’s host portion is used.
	//
	// SECURITY: behind a reverse proxy (Kubernetes Ingress, CloudFront,
	// Cloudflare, service mesh) r.RemoteAddr is always the proxy’s IP, so the
	// default KeyFunc collapses every client onto one bucket. Either set
	// TrustedProxies (preferred) or supply a KeyFunc that extracts the client
	// identity yourself — otherwise attackers can bypass the limit by spoofing
	// X-Forwarded-For.
	KeyFunc func(*http.Request) string

	// TrustForwardedFor enables X-Forwarded-For-based key extraction when
	// KeyFunc is nil. Requires TrustedProxies to be set; without at least one
	// trusted proxy the middleware ignores XFF and falls back to RemoteAddr.
	// Default: false.
	TrustForwardedFor bool

	// TrustedProxies is the set of CIDR ranges or exact IPs of reverse proxies
	// whose X-Forwarded-For headers are trusted. Used only when KeyFunc is nil
	// and TrustForwardedFor is true. Shares semantics with IPFilterConfig’s
	// field of the same name — see IPFilter docs for the full walker contract.
	TrustedProxies []string

	// StatusCode is returned to rejected requests.
	// Default: 429 Too Many Requests.
	StatusCode int

	// CleanupInterval controls how often idle per-client buckets are evicted
	// from memory. Default: 1 minute.
	CleanupInterval time.Duration

	// IdleTimeout is how long a bucket must be inactive before eviction.
	// Default: 5 minutes.
	IdleTimeout time.Duration

	// Context controls the lifecycle of the background cleanup goroutine.
	// When cancelled the goroutine exits, preventing goroutine leaks in tests
	// or short-lived servers that call HTTPRateLimit multiple times.
	// nil creates an internal context that lives for the process lifetime;
	// pass a cancellable context in tests to avoid leaking goroutines.
	Context context.Context
}

// HTTPRateLimit returns a middleware that enforces a per-client token-bucket rate
// limit. Pass an optional HTTPRateLimitConfig to override defaults.
//
// The token bucket algorithm grants each client a burst allowance that refills
// at RequestsPerSecond. Requests are rejected with 429 once the bucket is
// empty.
//
// Example — default config (10 req/s, burst of 20):
//
//	middleware.HTTPRateLimit()
//
// Example — custom config:
//
//	middleware.HTTPRateLimit(middleware.HTTPRateLimitConfig{
//	    RequestsPerSecond: 50,
//	    Burst:             100,
//	})
func HTTPRateLimit(cfgs ...HTTPRateLimitConfig) func(http.Handler) http.Handler {
	cfg := HTTPRateLimitConfig{}
	if len(cfgs) > 0 {
		cfg = cfgs[0]
	}

	// Apply defaults.
	if cfg.RequestsPerSecond <= 0 {
		cfg.RequestsPerSecond = 10
	}
	if cfg.Burst <= 0 {
		cfg.Burst = 20
	}
	if cfg.StatusCode == 0 {
		cfg.StatusCode = http.StatusTooManyRequests
	}
	if cfg.CleanupInterval <= 0 {
		cfg.CleanupInterval = time.Minute
	}
	if cfg.IdleTimeout <= 0 {
		cfg.IdleTimeout = 5 * time.Minute
	}
	if cfg.KeyFunc == nil {
		proxyNets := parseTrustedProxies("HTTPRateLimit.TrustedProxies", cfg.TrustedProxies)
		if cfg.TrustForwardedFor && len(proxyNets) == 0 {
			logger.LogWarn("HTTPRateLimit: TrustForwardedFor is enabled but no valid TrustedProxies configured — X-Forwarded-For will be ignored, falling back to RemoteAddr")
		}
		cfg.KeyFunc = func(r *http.Request) string {
			return clientIPFromRequest(r, cfg.TrustForwardedFor, proxyNets)
		}
	}

	buckets := newShardedBuckets()

	burst := float64(cfg.Burst)
	rate := cfg.RequestsPerSecond

	// Pre-compute Retry-After header value: ceil(1/rate) seconds, minimum 1.
	retryAfter := strconv.Itoa(int(math.Ceil(1.0 / rate)))

	// Background goroutine removes idle buckets to prevent unbounded memory growth.
	// Each shard is locked independently so cleanup never blocks live traffic
	// on other shards.
	cleanupCtx, cleanupCancel := context.WithCancel(context.Background())
	if cfg.Context != nil {
		cleanupCtx, cleanupCancel = context.WithCancel(cfg.Context)
	}
	go func() {
		defer cleanupCancel()
		ticker := time.NewTicker(cfg.CleanupInterval)
		defer ticker.Stop()
		for {
			select {
			case <-cleanupCtx.Done():
				return
			case <-ticker.C:
				buckets.cleanup(cfg.IdleTimeout)
			}
		}
	}()

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			key := cfg.KeyFunc(r)
			now := time.Now()

			b := buckets.getOrCreate(key, burst, now)

			b.mu.Lock()
			elapsed := now.Sub(time.Unix(0, b.lastSeen.Load())).Seconds()
			b.tokens = min(burst, b.tokens+elapsed*rate)
			b.lastSeen.Store(now.UnixNano())
			allow := b.tokens >= 1
			if allow {
				b.tokens--
			}
			b.mu.Unlock()

			if !allow {
				// RFC 6585 §4: Retry-After tells well-behaved clients how
				// long to wait. We use ceil(1/rate) as a reasonable estimate.
				w.Header().Set("Retry-After", retryAfter)
				response.APIErrorWriter(w, response.APIError[any]{
					Code:    cfg.StatusCode,
					Error:   response.ErrTooManyRequests,
					Message: "Rate limit exceeded. Please slow down.",
				})
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

// ── TCP rate limiting ──────────────────────────────────────────────────────

// TCPRateLimitConfig configures the TCPRateLimit middleware.
// All fields are optional; zero values fall back to documented defaults.
type TCPRateLimitConfig struct {
	// ConnectionsPerSecond is the sustained token refill rate per client key.
	// Default: 10.
	ConnectionsPerSecond float64

	// Burst is the maximum number of tokens (connections) a client can
	// accumulate and spend in a single burst. Default: 20.
	Burst int

	// KeyFunc extracts the per-client key from the accepted connection.
	// Default: remote IP address (host portion only).
	KeyFunc func(net.Conn) string

	// RejectMessage is written to the connection before it is closed when the
	// rate limit is exceeded. Default: "-ERR rate limit exceeded\r\n".
	RejectMessage string

	// CleanupInterval controls how often idle per-client buckets are evicted
	// from memory. Default: 1 minute.
	CleanupInterval time.Duration

	// IdleTimeout is how long a bucket must be inactive before eviction.
	// Default: 5 minutes.
	IdleTimeout time.Duration

	// Context controls the lifecycle of the background cleanup goroutine.
	// When cancelled the goroutine exits, preventing goroutine leaks in tests
	// or short-lived servers that call TCPRateLimit multiple times.
	// nil creates an internal context that lives for the process lifetime;
	// pass a cancellable context in tests to avoid leaking goroutines.
	Context context.Context
}

// TCPRateLimit returns a TCP middleware that enforces a per-client token-bucket
// rate limit on incoming connections. Pass an optional TCPRateLimitConfig to
// override defaults.
//
// When the bucket for a remote IP is empty the connection receives a plain-text
// error message and is closed immediately, consistent with the TCP server's own
// capacity-exceeded behaviour.
//
// The implementation uses the same shardedBuckets structure as HTTPRateLimit
// to reduce lock contention under high-connection-rate workloads.
//
// Example — default config (10 conn/s, burst of 20):
//
//	middleware.TCPRateLimit()
//
// Example — custom config:
//
//	middleware.TCPRateLimit(middleware.TCPRateLimitConfig{
//	    ConnectionsPerSecond: 5,
//	    Burst:                10,
//	})
func TCPRateLimit(cfgs ...TCPRateLimitConfig) func(func(net.Conn)) func(net.Conn) {
	cfg := TCPRateLimitConfig{}
	if len(cfgs) > 0 {
		cfg = cfgs[0]
	}

	// Apply defaults.
	if cfg.ConnectionsPerSecond <= 0 {
		cfg.ConnectionsPerSecond = 10
	}
	if cfg.Burst <= 0 {
		cfg.Burst = 20
	}
	if cfg.RejectMessage == "" {
		cfg.RejectMessage = "-ERR rate limit exceeded\r\n"
	}
	if cfg.CleanupInterval <= 0 {
		cfg.CleanupInterval = time.Minute
	}
	if cfg.IdleTimeout <= 0 {
		cfg.IdleTimeout = 5 * time.Minute
	}
	if cfg.KeyFunc == nil {
		cfg.KeyFunc = connRemoteIP
	}

	buckets := newShardedBuckets()

	burst := float64(cfg.Burst)
	rate := cfg.ConnectionsPerSecond

	// Background goroutine removes idle buckets to prevent unbounded memory growth.
	// Uses the same sharded cleanup as HTTPRateLimit.
	tcpCleanupCtx, tcpCleanupCancel := context.WithCancel(context.Background())
	if cfg.Context != nil {
		tcpCleanupCtx, tcpCleanupCancel = context.WithCancel(cfg.Context)
	}
	go func() {
		defer tcpCleanupCancel()
		ticker := time.NewTicker(cfg.CleanupInterval)
		defer ticker.Stop()
		for {
			select {
			case <-tcpCleanupCtx.Done():
				return
			case <-ticker.C:
				buckets.cleanup(cfg.IdleTimeout)
			}
		}
	}()

	return func(next func(net.Conn)) func(net.Conn) {
		return func(conn net.Conn) {
			key := cfg.KeyFunc(conn)
			now := time.Now()

			b := buckets.getOrCreate(key, burst, now)

			b.mu.Lock()
			elapsed := now.Sub(time.Unix(0, b.lastSeen.Load())).Seconds()
			b.tokens = min(burst, b.tokens+elapsed*rate)
			b.lastSeen.Store(now.UnixNano())
			allow := b.tokens >= 1
			if allow {
				b.tokens--
			}
			b.mu.Unlock()

			if !allow {
				_, _ = conn.Write([]byte(cfg.RejectMessage))
				_ = conn.Close()
				return
			}

			next(conn)
		}
	}
}

// connRemoteIP extracts the host portion of conn.RemoteAddr(), stripping the port.
func connRemoteIP(conn net.Conn) string {
	host, _, err := net.SplitHostPort(conn.RemoteAddr().String())
	if err != nil {
		return conn.RemoteAddr().String()
	}
	return host
}

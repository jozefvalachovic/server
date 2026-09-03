package admin

import (
	"cmp"
	"fmt"
	"math"
	"net/http"
	"slices"
	"sync"
	"sync/atomic"
	"time"
)

// routeStats tracks atomic counters for a single route pattern.
type routeStats struct {
	count   atomic.Int64
	errors  atomic.Int64 // 5xx responses
	totalMs atomic.Int64 // sum of milliseconds
	minMs   int64        // min latency (initialised to MaxInt64)
	maxMs   atomic.Int64 // max latency
	totalRx atomic.Int64 // bytes sent to clients
}

// Collector gathers per-route HTTP metrics via its Middleware method.
// It is safe for concurrent use.
type Collector struct {
	m sync.Map // pattern string -> *routeStats
}

// NewCollector returns an initialised Collector.
func NewCollector() *Collector { return &Collector{} }

func (c *Collector) stats(pattern string) *routeStats {
	if v, ok := c.m.Load(pattern); ok {
		return v.(*routeStats)
	}
	v, _ := c.m.LoadOrStore(pattern, &routeStats{minMs: math.MaxInt64})
	return v.(*routeStats)
}

// capturingWriter wraps http.ResponseWriter to intercept the status code and
// the number of bytes written.
type capturingWriter struct {
	http.ResponseWriter
	status      int
	bytes       int64
	wroteHeader bool
}

func (cw *capturingWriter) WriteHeader(code int) {
	if cw.wroteHeader {
		return
	}
	cw.wroteHeader = true
	cw.status = code
	cw.ResponseWriter.WriteHeader(code)
}

func (cw *capturingWriter) Write(b []byte) (int, error) {
	n, err := cw.ResponseWriter.Write(b)
	cw.bytes += int64(n)
	return n, err
}

// Flush implements http.Flusher so streaming handlers (SSE, chunked) work
// correctly when the collector middleware is active.
func (cw *capturingWriter) Flush() {
	if f, ok := cw.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Unwrap returns the underlying ResponseWriter so net/http's interface
// upgrade checks (http.Flusher, http.Hijacker) propagate through.
func (cw *capturingWriter) Unwrap() http.ResponseWriter { return cw.ResponseWriter }

// Middleware is an http.Handler middleware that records per-route stats.
func (c *Collector) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		cw := &capturingWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(cw, r)
		ms := time.Since(start).Milliseconds()

		// Use the matched pattern when available (Go 1.22+). Falling back to
		// r.URL.Path would turn unmatched requests into an unbounded-cardinality
		// attack vector: any URL the server returns 404 for would allocate a
		// new routeStats entry, letting an attacker exhaust memory by hitting
		// random paths. Bucket all unmatched requests under a single sentinel
		// pattern so the map size stays bounded by the number of registered
		// routes plus one.
		pattern := r.Pattern
		if pattern == "" {
			pattern = "<unmatched>"
		}

		s := c.stats(pattern)
		s.count.Add(1)
		s.totalMs.Add(ms)
		s.totalRx.Add(cw.bytes)
		if cw.status >= 500 {
			s.errors.Add(1)
		}
		// CAS min
		for {
			old := atomic.LoadInt64(&s.minMs)
			if ms >= old || atomic.CompareAndSwapInt64(&s.minMs, old, ms) {
				break
			}
		}
		// CAS max
		for {
			old := s.maxMs.Load()
			if ms <= old || s.maxMs.CompareAndSwap(old, ms) {
				break
			}
		}
	})
}

// RouteSnapshot is a point-in-time copy of stats for one route.
type RouteSnapshot struct {
	Pattern    string
	Count      int64
	Errors5xx  int64
	AvgLatency float64 // milliseconds
	MinLatency int64   // milliseconds
	MaxLatency int64   // milliseconds
	AvgBytes   float64
}

// ErrorRate returns the fraction of requests that resulted in a 5xx status.
func (rs RouteSnapshot) ErrorRate() float64 {
	if rs.Count == 0 {
		return 0
	}
	return float64(rs.Errors5xx) / float64(rs.Count)
}

// String is a handy one-liner for debugging.
func (rs RouteSnapshot) String() string {
	return fmt.Sprintf("%s: n=%d 5xx=%d avg=%.1fms",
		rs.Pattern, rs.Count, rs.Errors5xx, rs.AvgLatency)
}

// Snapshots returns a slice of per-route stats.
func (c *Collector) Snapshots() []RouteSnapshot {
	var out []RouteSnapshot
	c.m.Range(func(k, v any) bool {
		s := v.(*routeStats)
		count := s.count.Load()
		var avgMs, avgBytes float64
		if count > 0 {
			avgMs = float64(s.totalMs.Load()) / float64(count)
			avgBytes = float64(s.totalRx.Load()) / float64(count)
		}
		minMs := atomic.LoadInt64(&s.minMs)
		if minMs == math.MaxInt64 {
			minMs = 0
		}
		out = append(out, RouteSnapshot{
			Pattern:    k.(string),
			Count:      count,
			Errors5xx:  s.errors.Load(),
			AvgLatency: avgMs,
			MinLatency: minMs,
			MaxLatency: s.maxMs.Load(),
			AvgBytes:   avgBytes,
		})
		return true
	})
	slices.SortFunc(out, func(a, b RouteSnapshot) int {
		return cmp.Compare(a.Pattern, b.Pattern)
	})
	return out
}

// Summary returns aggregate totals across all routes.
func (c *Collector) Summary() (totalReqs, total5xx int64, avgLatencyMs float64) {
	var sumMs int64
	c.m.Range(func(_, v any) bool {
		s := v.(*routeStats)
		totalReqs += s.count.Load()
		total5xx += s.errors.Load()
		sumMs += s.totalMs.Load()
		return true
	})
	if totalReqs > 0 {
		avgLatencyMs = float64(sumMs) / float64(totalReqs)
	}
	return
}

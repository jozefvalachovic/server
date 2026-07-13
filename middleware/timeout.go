package middleware

import (
	"context"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jozefvalachovic/server/response"

	"github.com/jozefvalachovic/logger/v4"
)

// DefaultRequestTimeout is the best-practice per-request deadline used when no
// explicit timeout is configured. 30 seconds covers the vast majority of API
// workloads while still preventing runaway handlers from holding connections
// indefinitely.
const DefaultRequestTimeout = 30 * time.Second

// timeoutTotal counts requests that exceeded the handler deadline (a 504 was
// injected or a partial response was already committed). leakedTotal counts
// timed-out handlers that were still running after WarnOnLeakAfter elapsed
// (only tracked when WarnOnLeakAfter > 0). Both are process-global monotonic
// counters exposed via TimeoutMetrics for wiring into a metrics backend.
var (
	timeoutTotal atomic.Int64
	leakedTotal  atomic.Int64
)

// TimeoutStats is a point-in-time snapshot of Timeout middleware counters.
type TimeoutStats struct {
	// Timeouts is the number of requests that exceeded their handler deadline.
	Timeouts int64
	// Leaked is the number of timed-out handlers still running after
	// WarnOnLeakAfter elapsed. Always 0 when WarnOnLeakAfter is disabled.
	Leaked int64
}

// TimeoutMetrics returns a snapshot of the Timeout middleware's global
// counters. Safe to call concurrently. Useful for exporting to Prometheus or
// logging periodic summaries.
func TimeoutMetrics() TimeoutStats {
	return TimeoutStats{
		Timeouts: timeoutTotal.Load(),
		Leaked:   leakedTotal.Load(),
	}
}

// TimeoutConfig configures the Timeout middleware.
type TimeoutConfig struct {
	// Timeout is the maximum duration a handler may take to begin writing its
	// response. When exceeded the client receives a 504 Gateway Timeout and the
	// handler's context is cancelled.
	// Default: DefaultRequestTimeout (30 s).
	Timeout time.Duration

	// ErrorMessage is the human-readable message included in the 504 response.
	// Default: "Request timed out. Please try again."
	ErrorMessage string

	// SkipPaths lists exact URL paths (or prefix paths ending in '/') that are
	// exempt from the timeout. Use this for SSE, WebSocket upgrade, or long-poll
	// endpoints that are expected to exceed the default deadline.
	//
	// Alternatively, handlers that manage their own deadline can call
	// context.WithTimeout directly and set TimeoutConfig.Timeout to 0 globally,
	// but SkipPaths is preferred because it keeps the default protection for all
	// other routes.
	//
	// Example:
	//
	//	middleware.Timeout(middleware.TimeoutConfig{
	//	    SkipPaths: []string{"/events", "/stream/"},
	//	})
	SkipPaths []string

	// SSEPaths is a convenience field equivalent to appending to SkipPaths, with
	// the explicit intent of documenting which routes stream responses (SSE,
	// long-poll, chunked downloads). Entries are merged into the effective
	// skip set at middleware construction.
	//
	// Separating SSEPaths from SkipPaths keeps the call-site intent readable —
	// SkipPaths is "don't time out", SSEPaths is "this route streams". Both
	// accept exact paths or prefix paths ending in '/'.
	//
	// Note: the per-request Timeout middleware is only one layer. The enclosing
	// http.Server also has a WriteTimeout that, when non-zero, caps the total
	// response duration regardless of this middleware. For streaming endpoints
	// ensure HTTPServerConfig.WriteTimeout is 0 (or NoWriteTimeout is true).
	// NewHTTPServer emits a warning at startup if both SSEPaths and a non-zero
	// WriteTimeout are configured together.
	//
	// Example:
	//
	//	middleware.Timeout(middleware.TimeoutConfig{
	//	    Timeout:   30 * time.Second,
	//	    SSEPaths:  []string{"/events", "/stream/"},
	//	})
	SSEPaths []string

	// WarnOnLeakAfter, when > 0, enables leaked-goroutine detection. After a
	// request times out, a lightweight detector goroutine waits up to this
	// additional duration for the handler goroutine to finish. If the handler
	// is still running when the window elapses, a warning is logged so
	// operators can alert on handlers that ignore context cancellation (see
	// the handler-goroutine-lifetime note on Timeout). 0 (default) disables
	// the detector so no extra goroutine is spawned per timed-out request.
	WarnOnLeakAfter time.Duration
}

// Timeout enforces a per-request deadline on every handler in the chain.
//
// When the deadline is exceeded the middleware writes a 504 response and
// cancels the request context — downstream handlers that respect ctx.Done()
// will terminate cleanly. If the handler writes any bytes before the deadline
// fires, no 504 is injected (the response is already committed).
//
// # Handler goroutine lifetime
//
// IMPORTANT: cancelling the request context does NOT forcibly stop the handler
// goroutine. Go has no safe goroutine preemption primitive, so a handler that
// ignores r.Context().Done() (for example, one making a blocking syscall or a
// synchronous DB driver call without a context-aware API) will continue running
// until it returns of its own accord, even though the client has already
// received a 504. This is leaked CPU/memory that the middleware cannot reclaim.
//
// To bound handler lifetime in practice:
//   - Pass r.Context() through every downstream call (http.Client, sql.DB,
//     exec.Cmd, custom workers) so cancellation propagates.
//   - Wrap blocking I/O with context.AfterFunc or a select on ctx.Done().
//   - For handlers that intentionally exceed the deadline, add them to
//     SkipPaths rather than relying on Timeout to mask long-running work.
//
// Concrete anti-pattern — a synchronous database/sql call made WITHOUT a
// context-aware method leaks the goroutine past the deadline:
//
//	// BAD: db.Query ignores the request context, so the query keeps running
//	// (and holds a connection) even after the client received a 504.
//	rows, err := db.Query("SELECT … FROM big_table")
//
//	// GOOD: QueryContext observes r.Context(), so cancellation on timeout
//	// aborts the query and returns the connection to the pool promptly.
//	rows, err := db.QueryContext(r.Context(), "SELECT … FROM big_table")
//
// Set WarnOnLeakAfter to have the middleware log a warning when a handler is
// still running well past its deadline, making such leaks observable.
//
// # Streaming / SSE handlers
//
// Handlers that intentionally outlive the default timeout (SSE, long-poll,
// WebSocket upgrade) should be listed in TimeoutConfig.SkipPaths so they
// bypass the deadline. Alternatively, such handlers can manage their own
// context.WithTimeout and the global timeout can be disabled by setting
// Timeout to 0 — but SkipPaths is preferred to preserve protection on all
// other routes.
//
// Note: when using SSE, ensure the handler sends heartbeats within the
// server's IdleTimeout (default 30 s) to prevent the connection from being
// closed by the HTTP server. See response.SSEWriter.SendHeartbeat.
//
// Example — default timeout (30 s):
//
//	middleware.Timeout()
//
// Example — custom timeout:
//
//	middleware.Timeout(middleware.TimeoutConfig{Timeout: 5 * time.Second})
//
// Example — skip SSE paths:
//
//	middleware.Timeout(middleware.TimeoutConfig{
//	    SkipPaths: []string{"/events", "/stream/"},
//	})
func Timeout(cfgs ...TimeoutConfig) func(http.Handler) http.Handler {
	cfg := TimeoutConfig{}
	if len(cfgs) > 0 {
		cfg = cfgs[0]
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = DefaultRequestTimeout
	}
	if cfg.ErrorMessage == "" {
		cfg.ErrorMessage = "Request timed out. Please try again."
	}
	timeout := cfg.Timeout
	errMsg := cfg.ErrorMessage

	// Merge SSEPaths into the effective skip set — callers set SSEPaths purely
	// for intent documentation; internally it is equivalent to SkipPaths.
	skipPaths := cfg.SkipPaths
	if len(cfg.SSEPaths) > 0 {
		skipPaths = append(skipPaths[:len(skipPaths):len(skipPaths)], cfg.SSEPaths...)
	}
	skip := newPathSkipper(skipPaths)
	leakWarnAfter := cfg.WarnOnLeakAfter

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// SkipPaths bypass — no timeout applied.
			if skip.skip(r.URL.Path) {
				next.ServeHTTP(w, r)
				return
			}

			ctx, cancel := context.WithTimeout(r.Context(), timeout)
			defer cancel()

			r = r.WithContext(ctx)
			tw := &timeoutWriter{ResponseWriter: w}

			done := make(chan struct{})
			go func() {
				// Recover panics from the handler goroutine. Without this, a panic
				// inside next.ServeHTTP crashes the process because the outer
				// Recovery middleware only covers the goroutine that called Timeout,
				// not this inner goroutine. We re-use the same "write 500 if headers
				// not committed" pattern from the Recovery middleware.
				defer func() {
					if rec := recover(); rec != nil {
						panicErr := panicToError(rec)
						logger.LogErrorWithStack(panicErr, "Panic recovered inside Timeout handler goroutine",
							"path", r.URL.Path,
						)
						if tw.timeout() {
							response.APIErrorWriter(w, response.APIError[any]{
								Code:    http.StatusInternalServerError,
								Error:   response.ErrInternalServerLow,
								Message: "An unexpected error occurred",
							})
						}
					}
					close(done)
				}()
				next.ServeHTTP(tw, r)
			}()

			select {
			case <-done:
				// Handler completed normally.
			case <-ctx.Done():
				// A deadline (or client cancellation) fired before the handler
				// returned. Make the event observable: the handler goroutine may
				// still be running because Go cannot forcibly preempt it.
				logger.LogWarn("Request exceeded handler deadline; handler goroutine may still be running",
					"path", r.URL.Path,
					"method", r.Method,
					"timeout", timeout.String(),
				)
				timeoutTotal.Add(1)

				// timeout() atomically marks the writer as timed out.
				// Returns true only if the handler had not yet written anything,
				// meaning we are safe to write the 504 to the original ResponseWriter.
				if tw.timeout() {
					response.APIErrorWriter(w, response.APIError[any]{
						Code:    http.StatusGatewayTimeout,
						Error:   response.ErrGatewayTimeout,
						Message: errMsg,
					})
				}
				// If timeout() returns false the handler already started writing;
				// no 504 is injected — the partial response is the best we can do.

				// Optional leak detector: spawn a short-lived goroutine that
				// distinguishes "slow but eventually completes" from "wedged".
				// Only enabled when WarnOnLeakAfter > 0 so the common path does
				// not pay for an extra goroutine per timed-out request.
				if leakWarnAfter > 0 {
					go func() {
						timer := time.NewTimer(leakWarnAfter)
						defer timer.Stop()
						select {
						case <-done:
							// Handler finished within the grace window — no leak.
						case <-timer.C:
							leakedTotal.Add(1)
							logger.LogWarn("Handler still running long after deadline (possible goroutine leak)",
								"path", r.URL.Path,
								"method", r.Method,
								"timeout", timeout.String(),
								"warnAfter", leakWarnAfter.String(),
							)
						}
					}()
				}
			}
		})
	}
}

// timeoutWriter wraps http.ResponseWriter so that writes from the handler
// goroutine and the timeout path never race. All state is protected by mu.
type timeoutWriter struct {
	http.ResponseWriter
	mu       sync.Mutex
	timedOut bool
	wrote    bool
}

func (tw *timeoutWriter) WriteHeader(code int) {
	tw.mu.Lock()
	defer tw.mu.Unlock()
	if tw.timedOut {
		return
	}
	tw.wrote = true
	tw.ResponseWriter.WriteHeader(code)
}

// Unwrap allows middleware that wraps ResponseWriter to introspect the chain (Go 1.20+).
func (tw *timeoutWriter) Unwrap() http.ResponseWriter {
	return tw.ResponseWriter
}

func (tw *timeoutWriter) Write(b []byte) (int, error) {
	tw.mu.Lock()
	defer tw.mu.Unlock()
	if tw.timedOut {
		return 0, http.ErrHandlerTimeout
	}
	tw.wrote = true
	return tw.ResponseWriter.Write(b)
}

// Flush implements http.Flusher so that SSE and streaming responses work
// correctly when the Timeout middleware wraps the ResponseWriter.
func (tw *timeoutWriter) Flush() {
	tw.mu.Lock()
	defer tw.mu.Unlock()
	if tw.timedOut {
		return
	}
	if f, ok := tw.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// timeout atomically marks the writer as timed out.
// Returns true (and safe to write 504) only when the handler had not yet
// committed any bytes; false when a response was already started.
func (tw *timeoutWriter) timeout() bool {
	tw.mu.Lock()
	defer tw.mu.Unlock()
	if tw.wrote {
		return false
	}
	tw.timedOut = true
	return true
}

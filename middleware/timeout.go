package middleware

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/jozefvalachovic/server/response"

	"github.com/jozefvalachovic/logger/v4"
)

// DefaultRequestTimeout is the best-practice per-request deadline used when no
// explicit timeout is configured. 30 seconds covers the vast majority of API
// workloads while still preventing runaway handlers from holding connections
// indefinitely.
const DefaultRequestTimeout = 30 * time.Second

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
}

// Timeout enforces a per-request deadline on every handler in the chain.
//
// When the deadline is exceeded the middleware writes a 504 response and
// cancels the request context — downstream handlers that respect ctx.Done()
// will terminate cleanly. If the handler writes any bytes before the deadline
// fires, no 504 is injected (the response is already committed).
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

	// Pre-compute skip sets for O(1) exact match and prefix scanning.
	skipExact := make(map[string]bool, len(cfg.SkipPaths))
	var skipPrefixes []string
	for _, p := range cfg.SkipPaths {
		if len(p) > 0 && p[len(p)-1] == '/' {
			skipPrefixes = append(skipPrefixes, p)
		} else {
			skipExact[p] = true
		}
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// SkipPaths bypass — no timeout applied.
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
						var panicErr error
						if e, ok := rec.(error); ok {
							panicErr = e
						} else {
							panicErr = fmt.Errorf("%v", rec)
						}
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

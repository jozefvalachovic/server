package middleware

import (
	"bufio"
	"errors"
	"fmt"
	"net"
	"net/http"

	"github.com/jozefvalachovic/server/response"

	"github.com/jozefvalachovic/logger/v4"
)

// responseWriterTracker wraps http.ResponseWriter and records whether WriteHeader
// (or Write, which implicitly commits headers) has already been called.
// This lets the panic-recovery handler know whether it is safe to write a 500 response.
type responseWriterTracker struct {
	http.ResponseWriter
	written bool
}

func (rw *responseWriterTracker) WriteHeader(code int) {
	rw.written = true
	rw.ResponseWriter.WriteHeader(code)
}

func (rw *responseWriterTracker) Write(b []byte) (int, error) {
	rw.written = true
	return rw.ResponseWriter.Write(b)
}

// Unwrap allows middleware that wraps ResponseWriter to introspect the chain (Go 1.20+).
func (rw *responseWriterTracker) Unwrap() http.ResponseWriter {
	return rw.ResponseWriter
}

// Flush implements http.Flusher so that streaming handlers continue to work
// correctly when wrapped by Recovery. Without this, any type-assertion on the
// outer ResponseWriter to http.Flusher would fail.
func (rw *responseWriterTracker) Flush() {
	if f, ok := rw.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Hijack implements http.Hijacker so that connection-upgrade handlers
// (e.g. WebSockets, HTTP/1.1 protocol switches) continue to work when wrapped
// by Recovery. Without this delegation, a type-assertion to http.Hijacker
// on the outer ResponseWriter would fail at runtime.
func (rw *responseWriterTracker) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if h, ok := rw.ResponseWriter.(http.Hijacker); ok {
		return h.Hijack()
	}
	return nil, nil, errors.New("responseWriterTracker: underlying ResponseWriter does not implement http.Hijacker")
}

// Recovery recovers from panics and returns proper error responses
func Recovery(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rw := &responseWriterTracker{ResponseWriter: w}
		defer func() {
			if rec := recover(); rec != nil {
				var panicErr error
				if e, ok := rec.(error); ok {
					panicErr = e
				} else {
					panicErr = fmt.Errorf("%v", rec)
				}
				logger.LogErrorWithStack(panicErr, "Panic recovered",
					"method", r.Method,
					"path", r.URL.Path,
					"remoteAddr", r.RemoteAddr,
				)

				// Only write an error response if headers have not been committed yet.
				// Once WriteHeader/Write has been called the status line is on the wire
				// and we cannot retroactively change it.
				if !rw.written {
					response.APIErrorWriter(w, response.APIError[any]{
						Code:    http.StatusInternalServerError,
						Data:    response.CreateEmptyData[any](),
						Error:   response.ErrInternalServerLow,
						Message: "An unexpected error occurred",
						Details: "The request could not be completed due to an internal error",
					})
				}
			}
		}()

		next.ServeHTTP(rw, r)
	})
}

package middleware

import (
	"bufio"
	"compress/gzip"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"

	"github.com/jozefvalachovic/logger/v4"
)

// CompressConfig configures the Compress middleware.
type CompressConfig struct {
	// Enabled activates gzip compression for eligible responses.
	// Default: false (opt-in, must set explicitly).
	Enabled bool

	// Level is the gzip compression level.
	// Valid values: gzip.BestSpeed (1) to gzip.BestCompression (9).
	// Default: gzip.DefaultCompression (-1).
	Level int

	// ContentTypes is a list of response Content-Type prefixes eligible for
	// compression. Compression eligibility is evaluated on the first write,
	// after the handler has set its Content-Type header.
	// An empty list falls back to compressing the default text/data types.
	// Default: application/json, application/xml, text/*, application/javascript.
	ContentTypes []string
}

var defaultCompressTypes = []string{
	"application/json",
	"application/xml",
	"application/javascript",
	"text/",
}

// gzipResponseWriter defers the decision to gzip until the first write,
// at which point the handler will have already set its Content-Type header.
type gzipResponseWriter struct {
	http.ResponseWriter
	gz      *gzip.Writer
	types   []string
	decided bool // true once we have resolved compress vs passthrough
	active  bool // true when gzip compression is active
}

func (g *gzipResponseWriter) activate() {
	g.decided = true
	ct := g.Header().Get("Content-Type")
	eligible := ct == "" // optimistic when Content-Type not yet declared
	if !eligible {
		for _, t := range g.types {
			if strings.HasPrefix(ct, t) {
				eligible = true
				break
			}
		}
	}
	g.active = eligible
	if g.active {
		g.Header().Set("Content-Encoding", "gzip")
		g.Header().Del("Content-Length") // length changes after compression
		g.Header().Add("Vary", "Accept-Encoding")
	}
}

func (g *gzipResponseWriter) WriteHeader(code int) {
	if !g.decided {
		// Status codes 1xx, 204, and 304 never carry a body — skip compression
		// entirely so gz.Close() does not attempt to write a gzip trailer.
		if code == http.StatusNoContent || code == http.StatusNotModified || (code >= 100 && code < 200) {
			g.decided = true
			g.active = false
		} else {
			g.activate()
		}
	}
	g.ResponseWriter.WriteHeader(code)
}

func (g *gzipResponseWriter) Write(b []byte) (int, error) {
	if !g.decided {
		g.activate()
	}
	if g.active {
		return g.gz.Write(b)
	}
	return g.ResponseWriter.Write(b)
}

func (g *gzipResponseWriter) Flush() {
	if g.active {
		_ = g.gz.Flush()
	}
	if f, ok := g.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Hijack implements http.Hijacker so WebSocket upgrades work through the
// compression layer. Compression is not applied to the hijacked connection.
func (g *gzipResponseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if h, ok := g.ResponseWriter.(http.Hijacker); ok {
		return h.Hijack()
	}
	return nil, nil, fmt.Errorf("compress: underlying ResponseWriter does not implement http.Hijacker")
}

// Unwrap allows middleware that wraps ResponseWriter to introspect the chain.
func (g *gzipResponseWriter) Unwrap() http.ResponseWriter { return g.ResponseWriter }

// Compress adds gzip content encoding to eligible responses.
//
// Compression is opt-in: Enabled must be explicitly true, otherwise the
// middleware is a no-op passthrough.
//
// Eligibility is determined per-response at write time (after the handler sets
// its Content-Type header), so ContentTypes filtering works correctly even for
// dynamic content.
//
// Example — enable with defaults:
//
//	middleware.Compress(middleware.CompressConfig{Enabled: true})
//
// Example — JSON-only, best speed:
//
//	middleware.Compress(middleware.CompressConfig{
//	    Enabled:      true,
//	    Level:        gzip.BestSpeed,
//	    ContentTypes: []string{"application/json"},
//	})
func Compress(cfgs ...CompressConfig) func(http.Handler) http.Handler {
	cfg := CompressConfig{}
	if len(cfgs) > 0 {
		cfg = cfgs[0]
	}
	if !cfg.Enabled {
		return func(next http.Handler) http.Handler { return next }
	}

	level := cfg.Level
	if level == 0 {
		level = gzip.DefaultCompression
	}
	if level < gzip.HuffmanOnly || level > gzip.BestCompression {
		level = gzip.DefaultCompression
	}
	types := cfg.ContentTypes
	if len(types) == 0 {
		types = defaultCompressTypes
	}

	pool := &sync.Pool{
		New: func() any {
			gz, _ := gzip.NewWriterLevel(io.Discard, level)
			return gz
		},
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
				next.ServeHTTP(w, r)
				return
			}

			gz := pool.Get().(*gzip.Writer)
			gz.Reset(w)

			grw := &gzipResponseWriter{
				ResponseWriter: w,
				gz:             gz,
				types:          types,
			}
			defer func() {
				if grw.active {
					if err := gz.Close(); err != nil {
						logger.LogWarn("gzip close error (client may have disconnected)", "error", err.Error())
					}
				}
				pool.Put(gz)
			}()

			next.ServeHTTP(grw, r)
		})
	}
}

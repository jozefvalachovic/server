package server_test

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/jozefvalachovic/server/middleware"
	"github.com/jozefvalachovic/server/server"
)

// ── Additional Validate coverage ──────────────────────────────────────────

func TestHTTPServerConfig_Validate_NegativeMaxHeaderBytes(t *testing.T) {
	cfg := server.HTTPServerConfig{MaxHeaderBytes: -1}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected error for negative MaxHeaderBytes")
	}
}

func TestHTTPServerConfig_Validate_NegativeTimeoutTimeout(t *testing.T) {
	cfg := server.HTTPServerConfig{Timeout: &middleware.TimeoutConfig{Timeout: -1}}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected error for negative Timeout.Timeout")
	}
}

func TestHTTPServerConfig_Validate_BadRateLimit(t *testing.T) {
	cfg := server.HTTPServerConfig{
		RateLimitConfig: &middleware.HTTPRateLimitConfig{
			RequestsPerSecond: -1,
			Burst:             0,
		},
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected error for bad rate limit config")
	}
}

func TestHTTPServerConfig_Validate_TLSMinVersionZero(t *testing.T) {
	cfg := server.HTTPServerConfig{
		TLSConfig: &tls.Config{MinVersion: 0},
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected error for TLS MinVersion 0")
	}
}

func TestTCPServerConfig_Validate_BadRateLimit(t *testing.T) {
	cfg := server.TCPServerConfig{
		RateLimitConfig: &middleware.TCPRateLimitConfig{
			ConnectionsPerSecond: -1,
		},
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected error for bad TCP rate limit")
	}
}

func TestTCPServerConfig_Validate_TLSMinVersionZero(t *testing.T) {
	cfg := server.TCPServerConfig{
		TLSConfig: &tls.Config{MinVersion: 0},
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected error for TLS MinVersion 0")
	}
}

func TestTCPServerConfig_Validate_TLSWeakVersion(t *testing.T) {
	cfg := server.TCPServerConfig{
		TLSConfig: &tls.Config{MinVersion: tls.VersionTLS10},
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected error for TLS < 1.2")
	}
}

func TestMetricsServerConfig_Validate_NilHandler(t *testing.T) {
	cfg := &server.MetricsServerConfig{Handler: nil}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected error for nil Handler")
	}
}

func TestMetricsServerConfig_Validate_Good(t *testing.T) {
	cfg := &server.MetricsServerConfig{Handler: http.DefaultServeMux}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// ── NewHTTPServer env validation ──────────────────────────────────────────

func TestNewHTTPServer_MissingPort(t *testing.T) {
	t.Setenv("HTTP_HOST", "0.0.0.0")
	t.Setenv("HTTP_PORT", "")
	_, err := server.NewHTTPServer(http.NewServeMux(), "test", "1.0", server.HTTPServerConfig{})
	if err == nil {
		t.Fatal("expected error for missing HTTP_PORT")
	}
}

func TestNewHTTPServer_InvalidPort(t *testing.T) {
	t.Setenv("HTTP_HOST", "0.0.0.0")
	t.Setenv("HTTP_PORT", "99999")
	_, err := server.NewHTTPServer(http.NewServeMux(), "test", "1.0", server.HTTPServerConfig{})
	if err == nil {
		t.Fatal("expected error for invalid HTTP_PORT")
	}
}

func TestNewHTTPServer_MissingHost(t *testing.T) {
	t.Setenv("HTTP_HOST", "")
	t.Setenv("HTTP_PORT", "8080")
	_, err := server.NewHTTPServer(http.NewServeMux(), "test", "1.0", server.HTTPServerConfig{})
	if err == nil {
		t.Fatal("expected error for missing HTTP_HOST")
	}
}

func TestNewHTTPServer_InvalidHost(t *testing.T) {
	t.Setenv("HTTP_HOST", "localhost")
	t.Setenv("HTTP_PORT", "8080")
	_, err := server.NewHTTPServer(http.NewServeMux(), "test", "1.0", server.HTTPServerConfig{})
	if err == nil {
		t.Fatal("expected error for non-IP HTTP_HOST")
	}
}

func TestNewHTTPServer_TLS_MissingCertPaths(t *testing.T) {
	t.Setenv("HTTP_HOST", "0.0.0.0")
	t.Setenv("HTTP_PORT", "8080")
	t.Setenv("HTTP_TLS_CERT_PATH", "")
	t.Setenv("HTTP_TLS_KEY_PATH", "")
	_, err := server.NewHTTPServer(http.NewServeMux(), "test", "1.0", server.HTTPServerConfig{
		TLSConfig: &tls.Config{MinVersion: tls.VersionTLS12},
	})
	if err == nil {
		t.Fatal("expected error for missing TLS cert paths")
	}
}

// ── HTTP server lifecycle ─────────────────────────────────────────────────

func freePort(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to get free port: %v", err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()
	return fmt.Sprintf("%d", port)
}

func TestHTTPServer_StartShutdown(t *testing.T) {
	port := freePort(t)
	t.Setenv("HTTP_HOST", "127.0.0.1")
	t.Setenv("HTTP_PORT", port)

	mux := http.NewServeMux()
	mux.HandleFunc("/ping", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("pong"))
	})

	srv, err := server.NewHTTPServer(mux, "test", "0.1.0", server.HTTPServerConfig{
		NoReadTimeout:       true,
		NoWriteTimeout:      true,
		NoReadHeaderTimeout: true,
	})
	if err != nil {
		t.Fatalf("NewHTTPServer: %v", err)
	}
	if err := srv.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Give the server a moment to start accepting.
	time.Sleep(50 * time.Millisecond)

	resp, err := http.Get("http://127.0.0.1:" + port + "/ping")
	if err != nil {
		t.Fatalf("GET /ping: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if string(body) != "pong" {
		t.Fatalf("want pong, got %s", string(body))
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.GracefulShutdown(ctx); err != nil {
		t.Fatalf("GracefulShutdown: %v", err)
	}

	// Port should be released.
	ln, err := net.Listen("tcp", "127.0.0.1:"+port)
	if err != nil {
		t.Fatalf("port should be released after shutdown: %v", err)
	}
	_ = ln.Close()
}

func TestHTTPServer_ForceShutdown(t *testing.T) {
	port := freePort(t)
	t.Setenv("HTTP_HOST", "127.0.0.1")
	t.Setenv("HTTP_PORT", port)

	mux := http.NewServeMux()
	mux.HandleFunc("/slow", func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(10 * time.Second) // intentionally slow
		w.WriteHeader(http.StatusOK)
	})

	srv, err := server.NewHTTPServer(mux, "test", "0.1.0", server.HTTPServerConfig{
		NoReadTimeout:       true,
		NoWriteTimeout:      true,
		NoReadHeaderTimeout: true,
	})
	if err != nil {
		t.Fatalf("NewHTTPServer: %v", err)
	}
	if err := srv.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	time.Sleep(50 * time.Millisecond)

	// Start a slow request in background.
	go func() {
		_, _ = http.Get("http://127.0.0.1:" + port + "/slow")
	}()
	time.Sleep(50 * time.Millisecond)

	// Force shutdown should terminate immediately.
	srv.ForceShutdown()

	// Port should be released.
	ln, err := net.Listen("tcp", "127.0.0.1:"+port)
	if err != nil {
		t.Fatalf("port should be released after force shutdown: %v", err)
	}
	_ = ln.Close()
}

func TestHTTPServer_Nil(t *testing.T) {
	var srv *server.HTTPServer
	if err := srv.Start(); err == nil {
		t.Fatal("nil server Start should return error")
	}
	// GracefulShutdown on nil should not panic.
	if err := srv.GracefulShutdown(context.Background()); err != nil {
		t.Fatalf("nil GracefulShutdown should return nil, got %v", err)
	}
	// ForceShutdown on nil should not panic.
	srv.ForceShutdown()
}

// ── TCP server env validation ─────────────────────────────────────────────

func TestNewTCPServer_MissingHost(t *testing.T) {
	t.Setenv("TCP_HOST", "")
	t.Setenv("TCP_PORT", "9000")
	_, err := server.NewTCPServer(func(net.Conn) {}, "test", "1.0", server.TCPServerConfig{})
	if err == nil {
		t.Fatal("expected error for missing TCP_HOST")
	}
}

func TestNewTCPServer_InvalidPort(t *testing.T) {
	t.Setenv("TCP_HOST", "0.0.0.0")
	t.Setenv("TCP_PORT", "abc")
	_, err := server.NewTCPServer(func(net.Conn) {}, "test", "1.0", server.TCPServerConfig{})
	if err == nil {
		t.Fatal("expected error for invalid TCP_PORT")
	}
}

func TestTCPServer_Nil(t *testing.T) {
	var srv *server.TCPServer
	if err := srv.Start(); err == nil {
		t.Fatal("nil TCPServer Start should return error")
	}
}

// ── TCP server lifecycle ──────────────────────────────────────────────────

func TestTCPServer_StartShutdown(t *testing.T) {
	port := freePort(t)
	t.Setenv("TCP_HOST", "127.0.0.1")
	t.Setenv("TCP_PORT", port)

	handler := func(conn net.Conn) {
		buf := make([]byte, 64)
		n, err := conn.Read(buf)
		if err != nil {
			return
		}
		_, _ = conn.Write(buf[:n])
	}

	srv, err := server.NewTCPServer(handler, "test", "0.1.0", server.TCPServerConfig{
		NoReadTimeout:  true,
		NoWriteTimeout: true,
	})
	if err != nil {
		t.Fatalf("NewTCPServer: %v", err)
	}
	if err := srv.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	time.Sleep(50 * time.Millisecond)

	// Connect and echo.
	conn, err := net.DialTimeout("tcp", "127.0.0.1:"+port, 2*time.Second)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	_, _ = conn.Write([]byte("hello"))
	buf := make([]byte, 64)
	n, err := conn.Read(buf)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if string(buf[:n]) != "hello" {
		t.Fatalf("want hello, got %s", string(buf[:n]))
	}
	_ = conn.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.GracefulShutdown(ctx); err != nil {
		t.Fatalf("GracefulShutdown: %v", err)
	}
}

func TestTCPServer_ForceShutdown(t *testing.T) {
	port := freePort(t)
	t.Setenv("TCP_HOST", "127.0.0.1")
	t.Setenv("TCP_PORT", port)

	handler := func(conn net.Conn) {
		// Block forever.
		buf := make([]byte, 1)
		_, _ = conn.Read(buf)
	}

	srv, err := server.NewTCPServer(handler, "test", "0.1.0", server.TCPServerConfig{
		NoReadTimeout:  true,
		NoWriteTimeout: true,
	})
	if err != nil {
		t.Fatalf("NewTCPServer: %v", err)
	}
	if err := srv.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	time.Sleep(50 * time.Millisecond)

	// Open a connection that will block.
	conn, err := net.DialTimeout("tcp", "127.0.0.1:"+port, 2*time.Second)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer func() { _ = conn.Close() }()
	time.Sleep(50 * time.Millisecond)

	// Force shutdown should terminate immediately.
	srv.ForceShutdown()

	// Active connections should have been 1 before shutdown.
	// After ForceShutdown, port should be released.
	time.Sleep(100 * time.Millisecond)
	ln, err := net.Listen("tcp", "127.0.0.1:"+port)
	if err != nil {
		t.Fatalf("port should be released after force shutdown: %v", err)
	}
	_ = ln.Close()
}

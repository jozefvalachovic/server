package server

import (
	"cmp"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/pprof"
	"os"
	"strconv"
	"time"

	"github.com/jozefvalachovic/logger/v4"
)

// MetricsServerConfig holds metrics server configuration.
// The metrics port is read from the METRICS_PORT environment variable,
// consistent with how HTTP_PORT and TCP_PORT are resolved for the main servers.
type MetricsServerConfig struct {
	Handler http.Handler
	// EnablePprof exposes the standard /debug/pprof/ endpoints on the metrics
	// server, including Go 1.27's goroutineleak profile. It is disabled by
	// default and should only be enabled on a restricted listener.
	EnablePprof bool
	// TLSConfig enables TLS on the metrics server. When set, the server reads
	// METRICS_TLS_CERT_PATH and METRICS_TLS_KEY_PATH from the environment.
	// nil disables TLS (default).
	TLSConfig *tls.Config
}

// MetricsServer holds the metrics HTTP server instance
type MetricsServer struct {
	server *http.Server
}

// StartMetricsServer starts a simple HTTP server for Prometheus metrics.
// The METRICS_PORT environment variable must be set.
// METRICS_HOST controls the bind address; defaults to 127.0.0.1 so metrics
// are never exposed on public interfaces without an explicit override.
func StartMetricsServer(cfg *MetricsServerConfig) (*MetricsServer, error) {
	if cfg == nil {
		return nil, errors.New("MetricsServerConfig must not be nil")
	}
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("invalid MetricsServerConfig: %w", err)
	}

	port := os.Getenv("METRICS_PORT")
	portNum, portErr := strconv.Atoi(port)
	if portErr != nil || portNum < 1 || portNum > 65535 {
		return nil, fmt.Errorf("METRICS_PORT %q is not a valid port number (1–65535)", port)
	}
	host := cmp.Or(os.Getenv("METRICS_HOST"), "127.0.0.1") // default loopback; set METRICS_HOST=0.0.0.0 to expose externally
	// Warn when the metrics endpoint is bound to a non-loopback address.
	// Metrics can leak request rates, cardinality information, and internal
	// error codes, so exposing them publicly is a configuration smell even
	// when the caller does so deliberately. The warning is informational
	// only — callers who need external exposure should gate access via the
	// network layer (mTLS, firewall, service mesh).
	if ip := net.ParseIP(host); ip != nil && !ip.IsLoopback() {
		logger.LogWarn("Metrics server binding to non-loopback address; ensure access is restricted at the network layer",
			"host", host,
		)
	} else if ip == nil && host != "localhost" {
		logger.LogWarn("Metrics server bound to non-IP host; ensure access is restricted at the network layer",
			"host", host,
		)
	}

	mux := http.NewServeMux()
	// Two metrics endpoints are mounted intentionally:
	//   /metrics         — application metrics supplied by the caller
	//                      (typically a Prometheus collector).
	//   /logger-metrics  — structured logger counters (log-level breakdown,
	//                      dropped-record counters, sampling stats) provided
	//                      by the logger/v4 package. Kept on a separate path
	//                      so scrapers can target one or both independently
	//                      and the application handler never sees logger
	//                      internals on its own route.
	mux.Handle("/metrics", cfg.Handler)
	mux.Handle("/logger-metrics", logger.MetricsHandler())
	if cfg.EnablePprof {
		mux.HandleFunc("/debug/pprof/", pprof.Index)
		mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
		mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
		mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
		mux.HandleFunc("/debug/pprof/trace", pprof.Trace)
	}
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		if _, err := w.Write([]byte("ok")); err != nil {
			logger.LogWarn("Failed to write healthz response", "error", err.Error())
		}
	})

	server := &http.Server{
		Addr:              net.JoinHostPort(host, port),
		Handler:           mux,
		TLSConfig:         cfg.TLSConfig,
		ReadHeaderTimeout: 5 * time.Second, // guard against slow-header attacks
		IdleTimeout:       30 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      15 * time.Second,
	}

	listener, err := net.Listen("tcp", server.Addr)
	if err != nil {
		return nil, fmt.Errorf("failed to bind metrics listener: %w", err)
	}

	if cfg.TLSConfig != nil {
		certFile := os.Getenv("METRICS_TLS_CERT_PATH")
		keyFile := os.Getenv("METRICS_TLS_KEY_PATH")
		cert, err := tls.LoadX509KeyPair(certFile, keyFile)
		if err != nil {
			_ = listener.Close()
			return nil, fmt.Errorf("failed to load metrics TLS certificate: %w", err)
		}
		tlsConfig := cfg.TLSConfig.Clone()
		tlsConfig.Certificates = []tls.Certificate{cert}
		server.TLSConfig = tlsConfig
	}

	go func() {
		defer func() {
			if err := listener.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
				logger.LogWarn("Metrics listener close error", "error", err.Error())
			}
		}()
		logger.LogInfo("Metrics server starting", "address", server.Addr)
		var serveErr error
		if cfg.TLSConfig != nil {
			serveErr = server.ServeTLS(listener, "", "")
		} else {
			serveErr = server.Serve(listener)
		}
		if serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			logger.LogError("Metrics server error", "error", serveErr.Error())
		}
	}()

	return &MetricsServer{server: server}, nil
}

// Shutdown gracefully stops the metrics server
func (m *MetricsServer) Shutdown(ctx context.Context) error {
	if m == nil || m.server == nil {
		return nil
	}
	return m.server.Shutdown(ctx)
}

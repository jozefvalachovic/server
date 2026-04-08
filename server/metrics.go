package server

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net/http"
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
	port := os.Getenv("METRICS_PORT")
	portNum, portErr := strconv.Atoi(port)
	if portErr != nil || portNum < 1 || portNum > 65535 {
		return nil, fmt.Errorf("METRICS_PORT %q is not a valid port number (1–65535)", port)
	}
	host := os.Getenv("METRICS_HOST")
	if host == "" {
		host = "127.0.0.1" // default to loopback; set METRICS_HOST=0.0.0.0 to expose externally
	}

	mux := http.NewServeMux()
	mux.Handle("/metrics", cfg.Handler)
	mux.Handle("/logger-metrics", logger.MetricsHandler())
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		if _, err := w.Write([]byte("ok")); err != nil {
			logger.LogWarn("Failed to write healthz response", "error", err.Error())
		}
	})

	server := &http.Server{
		Addr:              host + ":" + port,
		Handler:           mux,
		TLSConfig:         cfg.TLSConfig,
		ReadHeaderTimeout: 5 * time.Second, // guard against slow-header attacks
		IdleTimeout:       30 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      15 * time.Second,
	}

	go func() {
		logger.LogInfo("Metrics server starting", "port", port)
		var serveErr error
		if cfg.TLSConfig != nil {
			certFile := os.Getenv("METRICS_TLS_CERT_PATH")
			keyFile := os.Getenv("METRICS_TLS_KEY_PATH")
			serveErr = server.ListenAndServeTLS(certFile, keyFile)
		} else {
			serveErr = server.ListenAndServe()
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

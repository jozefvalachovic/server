package server

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"runtime"
	"strconv"
	"sync"
	"time"

	lib "github.com/jozefvalachovic/server"
	"github.com/jozefvalachovic/server/cache"
	"github.com/jozefvalachovic/server/client"
	"github.com/jozefvalachovic/server/internal/admin"
	"github.com/jozefvalachovic/server/internal/config"
	"github.com/jozefvalachovic/server/middleware"

	"github.com/jozefvalachovic/logger/v4"
	loggerMiddleware "github.com/jozefvalachovic/logger/v4/middleware"
)

// OTelBridgeConfig enables the logger OTel bridge handler so that log records
// are enriched with service metadata and level-mapped for OTel-compatible
// collectors (e.g. Grafana Alloy, OpenTelemetry Collector).
//
// When set on HTTPServerConfig or TCPServerConfig, the bridge is registered as
// an additional slog handler during logger initialisation.
type OTelBridgeConfig struct {
	// ServiceName identifies the service in OTel attributes (service.name).
	ServiceName string
	// ServiceVersion is emitted as service.version.
	ServiceVersion string
	// Handler is the inner slog.Handler that receives OTel-mapped records.
	// Default when nil: slog.NewJSONHandler(os.Stderr, {Level: LevelDebug}).
	Handler slog.Handler
}

// initLoggerOnce and shutdownLoggerOnce are process-global singletons.
// The logger is initialised exactly once per process by the first call to
// initLogger (typically from the first NewHTTPServer or NewTCPServer). All
// subsequent calls reuse that configuration; differing OTelBridge configs are
// detected and warned about but not applied.
//
// Implications:
//   - In production (one server per pod) this is the correct behaviour.
//   - In test suites that construct multiple servers in the same process, only
//     the first server's logger configuration takes effect. Use t.Setenv to
//     control log levels if needed, but do not rely on per-test logger config.
//   - shutdownLoggerOnce ensures logger buffers are drained exactly once
//     during the first GracefulShutdown/ForceShutdown call.
var (
	initLoggerOnce sync.Once
	initLoggerOTel *OTelBridgeConfig // first OTel config; used to detect conflicts
)

func initLogger(otel *OTelBridgeConfig) {
	initLoggerOnce.Do(func() {
		initLoggerOTel = otel
		cfg := logger.ConfigFromEnv()
		cfg.EnableDedup = true
		cfg.EnableMetrics = true
		if otel != nil {
			inner := otel.Handler
			if inner == nil {
				inner = slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug})
			}
			bridge := logger.NewOTelBridgeHandler(inner, otel.ServiceName, otel.ServiceVersion)
			cfg.AdditionalHandlers = append(cfg.AdditionalHandlers, bridge)
		}
		logger.SetConfig(cfg)
	})

	// Detect conflicting OTel configurations on subsequent calls.
	switch {
	case otel != nil && initLoggerOTel != nil:
		if otel.ServiceName != initLoggerOTel.ServiceName || otel.ServiceVersion != initLoggerOTel.ServiceVersion {
			logger.LogWarn("initLogger called with a different OTelBridgeConfig; first-caller config is used",
				"firstService", initLoggerOTel.ServiceName, "firstVersion", initLoggerOTel.ServiceVersion,
				"thisService", otel.ServiceName, "thisVersion", otel.ServiceVersion)
		}
	case (otel == nil) != (initLoggerOTel == nil):
		logger.LogWarn("initLogger: OTelBridgeConfig presence differs from first initialization; first-caller config is used")
	}
}

// shutdownLoggerOnce ensures logger.Shutdown is called exactly once per
// process, even when multiple servers shut down concurrently or sequentially.
var shutdownLoggerOnce sync.Once

func shutdownLogger() {
	shutdownLoggerOnce.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := logger.Shutdown(ctx); err != nil {
			fmt.Fprintf(os.Stderr, "logger shutdown error: %v\n", err)
		}
	})
}

type HTTPMiddleware func(http.Handler) http.Handler

// Re-exports — callers only need to import "server", not "middleware".

// HTTPRateLimitConfig is a re-export of middleware.HTTPRateLimitConfig.
type HTTPRateLimitConfig = middleware.HTTPRateLimitConfig

// CORSConfig is a re-export of middleware.CORSConfig.
type CORSConfig = middleware.CORSConfig

// RequestIDConfig is a re-export of middleware.RequestIDConfig.
type RequestIDConfig = middleware.RequestIDConfig

// TimeoutConfig is a re-export of middleware.TimeoutConfig.
type TimeoutConfig = middleware.TimeoutConfig

// IPFilterConfig is a re-export of middleware.IPFilterConfig.
type IPFilterConfig = middleware.IPFilterConfig

// CompressConfig is a re-export of middleware.CompressConfig.
type CompressConfig = middleware.CompressConfig

// AuthConfig is a re-export of middleware.AuthConfig.
type AuthConfig = middleware.AuthConfig

// RequestSizeConfig is a re-export of middleware.RequestSizeConfig.
type RequestSizeConfig = middleware.RequestSizeConfig

// TraceContextConfig is a re-export of middleware.TraceContextConfig.
type TraceContextConfig = middleware.TraceContextConfig

// Client is a re-export of client.Client — a resilient HTTP client with
// circuit breaker and retry support. Callers only need to import "server".
type Client = client.Client

// ClientConfig is a re-export of client.Config.
type ClientConfig = client.Config

// ClientRetryConfig is a re-export of client.RetryConfig.
type ClientRetryConfig = client.RetryConfig

// ClientCircuitBreakerConfig is a re-export of client.CircuitBreakerConfig.
type ClientCircuitBreakerConfig = client.CircuitBreakerConfig

// ClientHTTPError is a re-export of client.HTTPError — returned when the
// server responds with a non-retryable status. Use errors.As to inspect.
type ClientHTTPError = client.HTTPError

// NewClient creates a new resilient Client. Re-export of client.New so callers
// only need to import "server".
var NewClient = client.New

// HTTPAuditConfig controls audit event emission for HTTP requests.
// When Enabled is true, the logger middleware emits a structured audit event
// for every matched request. Set Methods to restrict auditing to specific verbs.
//
// Example — audit only state-changing methods:
//
//	auditCfg := &server.HTTPAuditConfig{
//		Enabled: true,
//		Methods: []string{http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete},
//	}
type HTTPAuditConfig struct {
	// Enabled activates per-request audit event emission.
	Enabled bool
	// Methods restricts auditing to these HTTP methods.
	// nil or empty means all methods are audited.
	Methods []string
	// SkipPaths lists exact URL paths excluded from both access logging and audit
	// (e.g. "/health", "/readiness" to suppress high-frequency probe noise).
	SkipPaths []string
}

// HTTPServerConfig consolidates all server construction options.
// Every field is optional; the zero value produces a secure, production-ready
// server with sensible defaults.
//
// # SSE / long-lived connections
//
// The server's IdleTimeout defaults to 30 s (config.HTTPIdleTimeout). For SSE
// or long-poll endpoints, ensure the handler sends data (or a heartbeat via
// response.SSEWriter.SendHeartbeat) within this interval to prevent the HTTP
// server from closing the idle connection. Alternatively, list such paths in
// TimeoutConfig.SkipPaths to exempt them from the per-request deadline.
type HTTPServerConfig struct {
	// TLSConfig enables TLS. When set, Start() reads HTTP_TLS_CERT_PATH and
	// HTTP_TLS_KEY_PATH from the environment.
	// Default: nil (plain HTTP).
	TLSConfig *tls.Config

	// AutoCertReload enables automatic certificate rotation without restart.
	// When true (and TLSConfig is set), the server polls the cert/key files
	// for changes and hot-swaps the TLS certificate via GetCertificate.
	// Default: false.
	AutoCertReload bool

	// CertReloadInterval is the polling interval for certificate file changes.
	// Only used when AutoCertReload is true. Default: 30s.
	CertReloadInterval time.Duration

	// ReadTimeout is the maximum duration for reading the entire request,
	// including the body. Default: config.DefaultReadTimeout (30 s).
	// Set explicitly to override. See NoReadTimeout to disable entirely.
	ReadTimeout time.Duration

	// WriteTimeout is the maximum duration before timing out writes of the
	// response. Default: config.DefaultWriteTimeout (60 s).
	// Set explicitly to override. See NoWriteTimeout to disable entirely.
	WriteTimeout time.Duration

	// NoReadTimeout disables the read timeout entirely (sets http.Server.ReadTimeout
	// to 0). Use when an explicit zero/infinite timeout is intended and the default
	// should not be applied. Has no effect when ReadTimeout is set to a positive value.
	NoReadTimeout bool

	// NoWriteTimeout disables the write timeout entirely. Same semantics as
	// NoReadTimeout but for http.Server.WriteTimeout.
	NoWriteTimeout bool

	// --- Observability ---

	// MetricsServerConfig starts an embedded metrics server (e.g. Prometheus).
	// nil disables the metrics server.
	MetricsServerConfig *MetricsServerConfig

	// AuditConfig enables structured audit logging per request.
	// nil disables audit logging.
	AuditConfig *HTTPAuditConfig

	// OTelBridge enables the OpenTelemetry log bridge.
	// When set, logger output is duplicated to an OTel-compatible slog handler
	// with service.name, service.version attributes and severity level mapping.
	// nil disables the bridge.
	OTelBridge *OTelBridgeConfig

	// --- Built-in middleware ---
	// Applied in the order listed; all are optional.

	// RateLimitConfig enables per-client token-bucket rate limiting.
	// nil disables. Defaults when non-nil: 10 req/s, burst 20, key = remote IP.
	RateLimitConfig *HTTPRateLimitConfig

	// CORS configures Cross-Origin Resource Sharing headers.
	// nil disables CORS entirely; set an explicit *CORSConfig to enable it.
	CORS *CORSConfig

	// RequestID configures request-ID injection/propagation.
	// nil enables with defaults (X-Request-ID header, random hex ID).
	RequestID *RequestIDConfig

	// TraceContext configures W3C Trace Context (traceparent/tracestate)
	// propagation. nil enables with defaults; set Disabled: true to turn off.
	TraceContext *TraceContextConfig

	// Timeout configures per-request handler timeouts.
	// nil enables 30 s default. Set Timeout.Timeout = 0 to disable.
	Timeout *TimeoutConfig

	// IPFilter configures IP allowlist/blocklist enforcement.
	// nil disables IP filtering (allow all).
	IPFilter *IPFilterConfig

	// Compress configures gzip response compression. Must set Enabled: true.
	// nil disables compression.
	Compress *CompressConfig

	// Admin enables the in-process metrics and cache admin UI.
	// When non-nil, NewHTTPServer automatically registers /metrics/, /cache/,
	// and /admin/* routes and wires the collector middleware.
	// nil disables all admin instrumentation.
	// Requires ADMIN_NAME and ADMIN_SECRET environment variables.
	Admin *AdminConfig

	// Middlewares are additional application middlewares applied after the
	// built-in stack (e.g. auth, context injection, custom CORS).
	// Applied in slice order (index 0 executes first).
	Middlewares []HTTPMiddleware

	// MaxConns limits the number of concurrent HTTP connections accepted by
	// the listener. 0 disables the limit (accept all connections). Mirrors
	// the TCP server’s connSem pattern to bound memory under connection floods.
	MaxConns int
	// MaxHeaderBytes is the maximum size of request headers the server will
	// read. Default: 1 MiB (1 << 20). Override for APIs that use very large
	// JWTs or need a tighter security boundary.
	MaxHeaderBytes int
	// BaseContext optionally provides a base context for all requests.
	// When non-nil, this function is called once when the server starts and
	// the returned context becomes the root context for every request.
	// Default: context.Background().
	BaseContext func(net.Listener) context.Context

	// ConnContext optionally modifies the context used for a new connection.
	// When non-nil, it is called for each incoming connection with the
	// listener's base context and can inject values (e.g. connection ID,
	// peer certificate info). Default: nil (no modification).
	ConnContext func(ctx context.Context, c net.Conn) context.Context
}

// AdminConfig configures the built-in admin UI (metrics + cache explorer).
// Pass it as HTTPServerConfig.Admin; route registration and collector
// middleware wiring are handled automatically by NewHTTPServer.
type AdminConfig struct {
	// AppName is displayed in the admin page headers.
	AppName string
	// AppVersion is displayed alongside AppName.
	AppVersion string
	// Store is the application cache store used by the /cache/ section.
	// nil disables cache-related features.
	Store *cache.CacheStore
}

type HTTPServer struct {
	port      string
	tlsConfig *tls.Config
	certFile  string
	keyFile   string
	maxConns  int

	server        *http.Server
	listener      net.Listener
	metricsConfig *MetricsServerConfig
	metricsServer *MetricsServer
	certReloader  *CertReloader
	log           logger.Logger
}

// limitedListener wraps a net.Listener and enforces a maximum number of
// concurrent accepted connections via a semaphore channel, mirroring the
// TCP server’s connSem pattern.
//
// Unlike the simpler “block on semaphore before Accept” approach, this
// implementation accepts the connection first and then tries to acquire a
// slot non-blockingly. When at capacity the new connection is closed
// immediately so the client learns quickly instead of hanging in the OS
// TCP backlog. A temporary error is returned so http.Server backs off
// and retries Accept without stopping.
type limitedListener struct {
	net.Listener
	sem chan struct{}
}

func newLimitedListener(l net.Listener, maxConns int) *limitedListener {
	return &limitedListener{Listener: l, sem: make(chan struct{}, maxConns)}
}

func (l *limitedListener) Accept() (net.Conn, error) {
	conn, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	select {
	case l.sem <- struct{}{}:
		return &semConn{Conn: conn, release: func() { <-l.sem }}, nil
	default:
		_ = conn.Close()
		return nil, &tempAcceptError{}
	}
}

// tempAcceptError is a temporary net.Error returned when the connection
// semaphore is full. http.Server treats temporary errors by sleeping briefly
// and retrying Accept, which provides natural backpressure.
//
// NOTE: net.Error.Temporary() is deprecated since Go 1.18 but http.Server's
// internal accept loop still checks it to decide whether to back off vs stop.
// Until http.Server provides an alternative signalling mechanism, this is the
// only way to tell the server "transient — retry Accept" without terminating
// the serve loop.
type tempAcceptError struct{}

func (tempAcceptError) Error() string { return "server at max connections" }
func (tempAcceptError) Timeout() bool { return false }

// Deprecated: Temporary is deprecated per the net.Error interface since Go 1.18.
// It is retained here because http.Server still relies on it internally to
// distinguish transient accept failures from fatal ones.
func (tempAcceptError) Temporary() bool { return true } //nolint:staticcheck

// semConn releases the semaphore slot exactly once when the connection closes.
type semConn struct {
	net.Conn
	release func()
	once    sync.Once
}

func (c *semConn) Close() error {
	err := c.Conn.Close()
	c.once.Do(c.release)
	return err
}

// NewHTTPServer initializes a new HTTPServer.
// Environment variables HTTP_HOST and HTTP_PORT must be set.
func NewHTTPServer(mux *http.ServeMux, appName, appVersion string, cfg HTTPServerConfig) (*HTTPServer, error) {
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("invalid HTTPServerConfig: %w", err)
	}

	initLogger(cfg.OTelBridge)
	log := logger.With("component", "http")

	port := os.Getenv("HTTP_PORT")
	portNum, portErr := strconv.Atoi(port)
	if portErr != nil || portNum < 1 || portNum > 65535 {
		return nil, fmt.Errorf("HTTP_PORT %q is not a valid port number (1–65535)", port)
	}
	host := os.Getenv("HTTP_HOST")
	if net.ParseIP(host) == nil {
		return nil, errors.New("environment variable HTTP_HOST is not set or is not an IP literal (use 0.0.0.0 or ::, not 'localhost')")
	}

	var certFile, keyFile string
	var certReloader *CertReloader
	if cfg.TLSConfig != nil {
		certFile = os.Getenv("HTTP_TLS_CERT_PATH")
		keyFile = os.Getenv("HTTP_TLS_KEY_PATH")
		if certFile == "" || keyFile == "" {
			return nil, errors.New("TLS enabled but HTTP_TLS_CERT_PATH or HTTP_TLS_KEY_PATH not set")
		}
		if cfg.AutoCertReload {
			var err error
			_, _, certReloader, err = setupCertReloader(cfg.TLSConfig, "HTTP_TLS_CERT_PATH", "HTTP_TLS_KEY_PATH", cfg.CertReloadInterval)
			if err != nil {
				return nil, fmt.Errorf("auto cert reload: %w", err)
			}
		}
	}

	log.LogDebug("Starting application...",
		"App Name", appName,
		"App Version", appVersion,
		"Library Version", lib.Version,
		"Golang Version", runtime.Version(),
	)

	// ── Admin UI ─────────────────────────────────────────────────────────
	var adminCollector *admin.Collector
	if cfg.Admin != nil {
		adminCollector = admin.NewCollector()
		admin.Register(mux, admin.Config{
			AppName:    cfg.Admin.AppName,
			AppVersion: cfg.Admin.AppVersion,
			Collector:  adminCollector,
			Store:      cfg.Admin.Store,
		})
	}

	// ── Middleware stack ─────────────────────────────────────────────────
	var handler http.Handler = mux

	stack := []HTTPMiddleware{
		middleware.Recovery, // 1. Panic recovery
		middleware.Security, // 2. Security headers
	}

	if cfg.IPFilter != nil {
		stack = append(stack, middleware.IPFilter(*cfg.IPFilter))
	}

	stack = append(stack, middleware.RequestSize) // 3. Body size limiting

	if cfg.RateLimitConfig != nil {
		stack = append(stack, middleware.HTTPRateLimit(*cfg.RateLimitConfig))
	}
	if cfg.CORS != nil {
		stack = append(stack, middleware.CORS(*cfg.CORS))
		// nil → CORS disabled entirely; callers must opt-in with an explicit CORSConfig.
	}
	if cfg.RequestID != nil {
		stack = append(stack, middleware.RequestID(*cfg.RequestID))
	} else {
		stack = append(stack, middleware.RequestID()) // defaults
	}
	if cfg.TraceContext != nil {
		stack = append(stack, middleware.TraceContext(*cfg.TraceContext))
	} else {
		stack = append(stack, middleware.TraceContext()) // defaults
	}
	switch {
	case cfg.Timeout == nil:
		stack = append(stack, middleware.Timeout()) // 30 s default
	case cfg.Timeout.Timeout > 0:
		stack = append(stack, middleware.Timeout(*cfg.Timeout))
		// cfg.Timeout != nil && Timeout == 0 → disabled intentionally.
	}
	if cfg.Compress != nil && cfg.Compress.Enabled {
		stack = append(stack, middleware.Compress(*cfg.Compress))
	}

	if cfg.Admin != nil && adminCollector != nil {
		stack = append(stack, adminCollector.Middleware)
	}

	stack = append(stack, cfg.Middlewares...)

	for i := len(stack) - 1; i >= 0; i-- {
		handler = stack[i](handler)
	}

	// Logging is always the true outermost layer.
	logOpts := []loggerMiddleware.HTTPMiddlewareOption{
		loggerMiddleware.WithLogBodyOnErrors(true),
		loggerMiddleware.WithRequestID(true),
		loggerMiddleware.WithMetrics(true),
	}

	// Inject app identity into every access log entry when known.
	if appName != "" || appVersion != "" {
		fields := make(map[string]any, 2)
		if appName != "" {
			fields["app"] = appName
		}
		if appVersion != "" {
			fields["version"] = appVersion
		}
		logOpts = append(logOpts, loggerMiddleware.WithCustomFields(fields))
	}

	// Always suppress noisy health-probe paths from access logs.
	// User-provided SkipPaths from AuditConfig are merged in.
	skipPaths := []string{"/healthz", "/readyz"}

	if cfg.AuditConfig != nil {
		if cfg.AuditConfig.Enabled {
			logOpts = append(logOpts, loggerMiddleware.WithAudit(true))
			if len(cfg.AuditConfig.Methods) > 0 {
				logOpts = append(logOpts, loggerMiddleware.WithAuditMethods(cfg.AuditConfig.Methods...))
			}
		}
		skipPaths = append(skipPaths, cfg.AuditConfig.SkipPaths...)
	}
	logOpts = append(logOpts, loggerMiddleware.WithSkipPaths(skipPaths...))

	handler = loggerMiddleware.LogHTTPMiddleware(handler, logOpts...)

	// Apply safe defaults when the caller omits read/write timeouts.
	// NoReadTimeout / NoWriteTimeout opt out of defaults explicitly.
	readTimeout := cfg.ReadTimeout
	if !cfg.NoReadTimeout && readTimeout == 0 {
		readTimeout = config.DefaultReadTimeout
	}
	writeTimeout := cfg.WriteTimeout
	if !cfg.NoWriteTimeout && writeTimeout == 0 {
		writeTimeout = config.DefaultWriteTimeout
	}

	srv := &http.Server{
		Addr:              host + ":" + port,
		TLSConfig:         cfg.TLSConfig,
		Handler:           handler,
		IdleTimeout:       config.HTTPIdleTimeout,
		ReadHeaderTimeout: config.HTTPReadHeaderTimeout,
		MaxHeaderBytes: func() int {
			if cfg.MaxHeaderBytes > 0 {
				return cfg.MaxHeaderBytes
			}
			return 1 << 20 // 1 MiB default
		}(),
		ReadTimeout:  readTimeout,
		WriteTimeout: writeTimeout,
	}

	// Wire optional BaseContext / ConnContext hooks so callers can inject
	// values (e.g. trace IDs, connection metadata) into every request.
	if cfg.BaseContext != nil {
		srv.BaseContext = cfg.BaseContext
	}
	if cfg.ConnContext != nil {
		srv.ConnContext = cfg.ConnContext
	}

	return &HTTPServer{
		port:          port,
		tlsConfig:     cfg.TLSConfig,
		certFile:      certFile,
		keyFile:       keyFile,
		maxConns:      cfg.MaxConns,
		server:        srv,
		metricsConfig: cfg.MetricsServerConfig,
		certReloader:  certReloader,
		log:           log,
	}, nil
}

// Start begins listening for incoming HTTP requests.
func (as *HTTPServer) Start() error {
	if as == nil {
		return errors.New("HTTPServer is nil - check configuration")
	}

	// Start embedded metrics server here so it only runs when the main server starts.
	// Starting it in the constructor would leak the server if Start() later fails.
	if as.metricsConfig != nil && as.metricsConfig.Handler != nil {
		ms, err := StartMetricsServer(as.metricsConfig)
		if err != nil {
			return err
		}
		as.metricsServer = ms
	}

	// Create the listener explicitly so we can optionally wrap it with a
	// connection-limiting semaphore before handing it to http.Server.
	ln, err := net.Listen("tcp", as.server.Addr)
	if err != nil {
		return fmt.Errorf("failed to bind listener: %w", err)
	}
	if as.maxConns > 0 {
		ln = newLimitedListener(ln, as.maxConns)
	}
	as.listener = ln

	go func(ln net.Listener) {
		as.log.LogInfo("Server starting", "port", as.port)
		var serveErr error
		if as.tlsConfig != nil {
			serveErr = as.server.ServeTLS(ln, as.certFile, as.keyFile)
		} else {
			serveErr = as.server.Serve(ln)
		}
		if serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			as.log.LogError("Server failed to start", "error", serveErr.Error())
		}
	}(ln)

	return nil
}

// StartWithContext starts the server and spawns a background goroutine that
// triggers GracefulShutdown when ctx is cancelled. This is a convenience for
// callers that manage server lifetime via context cancellation rather than
// explicit shutdown calls.
func (as *HTTPServer) StartWithContext(ctx context.Context) error {
	if err := as.Start(); err != nil {
		return err
	}
	go func() {
		<-ctx.Done()
		shutCtx, cancel := context.WithTimeout(context.Background(), config.ShutdownTimeout)
		defer cancel()
		_ = as.GracefulShutdown(shutCtx)
	}()
	return nil
}

// GracefulShutdown performs graceful shutdown of the HTTP server.
// The caller controls the deadline via ctx (typically context.WithTimeout).
// Returns the first non-nil error encountered during shutdown.
func (as *HTTPServer) GracefulShutdown(ctx context.Context) error {
	if as == nil {
		return nil
	}
	as.log.LogWarn("Starting graceful shutdown...")

	var shutdownErr error
	if err := as.server.Shutdown(ctx); err != nil {
		as.log.LogError("Server shutdown error", "error", err.Error())
		shutdownErr = err
	}

	// Shutdown metrics server using a short independent deadline so a slow
	// main-server shutdown does not prevent metrics cleanup.
	if as.metricsServer != nil {
		mCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := as.metricsServer.Shutdown(mCtx); err != nil {
			as.log.LogError("Metrics server shutdown error", "error", err.Error())
			if shutdownErr == nil {
				shutdownErr = err
			}
		}
	}

	// Stop the certificate reloader (if active) before draining the logger.
	if as.certReloader != nil {
		as.certReloader.Stop()
	}

	as.log.LogInfo("Server stopped gracefully")

	// Drain logger buffers (async, dedup) before exiting.
	shutdownLogger()

	return shutdownErr
}

// ForceShutdown immediately stops the server without waiting for ongoing requests
func (as *HTTPServer) ForceShutdown() {
	if as == nil {
		return
	}
	if err := as.server.Close(); err != nil {
		as.log.LogError("Server forced to close", "error", err.Error())
	}

	// Shutdown metrics server
	if as.metricsServer != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := as.metricsServer.Shutdown(ctx); err != nil {
			as.log.LogError("Metrics server shutdown error during force shutdown", "error", err.Error())
		}
	}

	if as.certReloader != nil {
		as.certReloader.Stop()
	}

	as.log.LogInfo("Server closed forcefully")

	// Drain logger buffers (async, dedup) before exiting.
	shutdownLogger()
}

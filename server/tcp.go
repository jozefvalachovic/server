package server

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"os"
	"runtime"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jozefvalachovic/server/config"
	"github.com/jozefvalachovic/server/middleware"

	"github.com/jozefvalachovic/logger/v4"
	loggerMiddleware "github.com/jozefvalachovic/logger/v4/middleware"
)

type TCPMiddleware func(next func(conn net.Conn)) func(conn net.Conn)

// TCPRateLimitConfig is a re-export of middleware.TCPRateLimitConfig.
// Callers only need to import the server package; no direct dependency on
// the middleware package is required for server construction.
type TCPRateLimitConfig = middleware.TCPRateLimitConfig

// TCPServerConfig consolidates all TCPServer construction options.
// Every field is optional; the zero value produces a server with sensible defaults.
type TCPServerConfig struct {
	// TLSConfig enables TLS on the listener.
	// Default: nil (plain TCP).
	TLSConfig *tls.Config

	// RateLimitConfig enables per-IP connection rate limiting.
	// nil disables rate limiting.
	RateLimitConfig *TCPRateLimitConfig

	// MetricsServerConfig starts an embedded metrics server (e.g. Prometheus).
	// nil disables the metrics server.
	MetricsServerConfig *MetricsServerConfig

	// ReadTimeout overrides the per-operation read deadline.
	// Default: config.TCPReadTimeout.
	ReadTimeout time.Duration

	// WriteTimeout overrides the per-operation write deadline.
	// Default: config.TCPWriteTimeout.
	WriteTimeout time.Duration

	// Middlewares are additional TCP middlewares applied after the built-in
	// stack (logging, rate limiting). Applied in slice order.
	Middlewares []TCPMiddleware

	// RejectMessage is written to connections that are rejected because the
	// server is at maximum capacity. It should be terminated with \r\n and
	// match the application's wire protocol.
	// Default: "-ERR server at max capacity, try again later\r\n".
	RejectMessage string
}

type TCPServer struct {
	addr      string
	tlsConfig *tls.Config
	rejectMsg string

	listener      net.Listener
	ctx           context.Context
	cancel        context.CancelFunc
	connSem       chan struct{}
	conns         sync.Map     // map[*deadlineConn]struct{} — for ForceShutdown
	activeConns   atomic.Int64 // Track active connections for metrics
	metricsConfig *MetricsServerConfig
	metricsServer *MetricsServer

	appReadTimeout  time.Duration
	appWriteTimeout time.Duration

	handler func(conn net.Conn)
	wg      sync.WaitGroup
	log     logger.Logger
}

// NewTCPServer initializes a new TCPServer instance.
// Environment variables TCP_HOST (IP literal, e.g. 0.0.0.0) and TCP_PORT must be set.
// Returns an error if the environment configuration is invalid.
func NewTCPServer(handler func(conn net.Conn), appName, appVersion string, cfg TCPServerConfig) (*TCPServer, error) {
	initLogger()
	log := logger.With("component", "tcp")

	var (
		host = os.Getenv("TCP_HOST")
		port = os.Getenv("TCP_PORT")
	)
	if net.ParseIP(host) == nil {
		return nil, errors.New("environment variable TCP_HOST is not set or is not an IP literal (use 0.0.0.0 or ::, not 'localhost')")
	}
	portNum, portErr := strconv.Atoi(port)
	if portErr != nil || portNum < 1 || portNum > 65535 {
		return nil, fmt.Errorf("TCP_PORT %q is not a valid port number (1–65535)", port)
	}

	var metricsConfig *MetricsServerConfig
	if cfg.MetricsServerConfig != nil && cfg.MetricsServerConfig.Handler != nil {
		metricsConfig = cfg.MetricsServerConfig
	}

	log.LogDebug("Starting application...",
		"App Name", appName,
		"App Version", appVersion,
		"Golang Version", runtime.Version(),
	)

	ctx, cancel := context.WithCancel(context.Background())

	// Middleware stack (outermost to innermost)
	var tcpMiddlewareStack []TCPMiddleware
	tcpMiddlewareStack = append(tcpMiddlewareStack,
		loggerMiddleware.LogTCPMiddleware, // Logging (outermost)
	)
	// Rate limiting — only applied when a config is provided (nil = disabled).
	if cfg.RateLimitConfig != nil {
		tcpMiddlewareStack = append(tcpMiddlewareStack, middleware.TCPRateLimit(*cfg.RateLimitConfig))
	}
	tcpMiddlewareStack = append(tcpMiddlewareStack, cfg.Middlewares...) // App-specific middlewares

	// Apply middleware in reverse order (innermost last)
	for i := len(tcpMiddlewareStack) - 1; i >= 0; i-- {
		handler = tcpMiddlewareStack[i](handler)
	}

	maxConns := config.GetTCPMaxConns()
	log.LogInfo("TCP server configured", "maxConns", maxConns)

	rejectMsg := cfg.RejectMessage
	if rejectMsg == "" {
		rejectMsg = "-ERR server at max capacity, try again later\r\n"
	}

	readTimeout := max(cfg.ReadTimeout, 0)
	writeTimeout := max(cfg.WriteTimeout, 0)

	return &TCPServer{
		addr:      host + ":" + port,
		tlsConfig: cfg.TLSConfig,
		rejectMsg: rejectMsg,

		appReadTimeout:  readTimeout,
		appWriteTimeout: writeTimeout,

		ctx:           ctx,
		cancel:        cancel,
		connSem:       make(chan struct{}, maxConns),
		metricsConfig: metricsConfig,

		handler: handler,
		log:     log,
	}, nil
}

// Start begins listening for incoming TCP connections.
// It binds the listener synchronously so that any bind error is returned immediately.
// The accept loop runs in a background goroutine; use GracefulShutdown or ForceShutdown to stop it.
func (s *TCPServer) Start() error {
	if s == nil {
		return errors.New("TCPServer is nil - check configuration")
	}

	// Start embedded metrics server here so it only runs when the main server starts.
	if s.metricsConfig != nil {
		ms, err := StartMetricsServer(s.metricsConfig)
		if err != nil {
			return err
		}
		s.metricsServer = ms
	}

	var ln net.Listener
	var err error
	if s.tlsConfig != nil {
		ln, err = tls.Listen("tcp", s.addr, s.tlsConfig)
		s.log.LogInfo("TLS enabled for TCP server")
	} else {
		ln, err = net.Listen("tcp", s.addr)
	}
	if err != nil {
		s.log.LogError("Failed to start TCP listener", "error", err.Error())
		return err
	}
	s.listener = ln

	s.log.LogInfo("TCP server listening", "address", s.addr)
	// The accept loop runs in a background goroutine tracked by s.wg so that
	// GracefulShutdown can wait for it to exit. The loop unblocks when
	// GracefulShutdown closes s.listener, which causes Accept() to return a
	// non-nil error — that is the intentional shutdown mechanism.
	s.wg.Add(1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				s.log.LogError("Panic in accept loop", "panic", fmt.Sprint(r))
			}
			s.wg.Done()
		}()
		for {
			conn, err := s.listener.Accept()
			if err != nil {
				select {
				case <-s.ctx.Done():
					return // graceful shutdown
				default:
					s.log.LogError("Accept error", "error", err.Error())
					continue
				}
			}

			readTimeout := s.appReadTimeout
			if readTimeout == 0 {
				readTimeout = config.TCPReadTimeout
			}
			writeTimeout := s.appWriteTimeout
			if writeTimeout == 0 {
				writeTimeout = config.TCPWriteTimeout
			}
			dc := &deadlineConn{
				Conn:         conn,
				readTimeout:  readTimeout,
				writeTimeout: writeTimeout,
			}

			select {
			case s.connSem <- struct{}{}:
				s.wg.Add(1)
				s.activeConns.Add(1)
				s.conns.Store(dc, struct{}{})
				go func() {
					defer func() {
						<-s.connSem
						s.activeConns.Add(-1)
						s.conns.Delete(dc)
						s.wg.Done()
					}()
					s.handleWithDeadlines(dc)
				}()
			default:
				// Send error message before closing
				if _, err := conn.Write([]byte(s.rejectMsg)); err != nil {
					s.log.LogWarn("Failed to write rejection message", "remote", conn.RemoteAddr().String(), "error", err.Error())
				}
				s.log.LogWarn("Too many concurrent connections, rejecting new connection",
					"remote", conn.RemoteAddr().String(),
					"activeConns", s.activeConns.Load(),
					"maxConns", cap(s.connSem))
				if err := conn.Close(); err != nil {
					s.log.LogWarn("Error closing rejected connection", "remote", conn.RemoteAddr().String(), "error", err.Error())
				}
			}
		}
	}()

	return nil
}

// handleWithDeadlines runs the application handler on a deadlineConn whose
// per-operation deadlines are already configured. A deferred recover ensures
// a panicking handler cannot crash the server process.
func (s *TCPServer) handleWithDeadlines(conn *deadlineConn) {
	defer func() {
		if r := recover(); r != nil {
			s.log.LogError("Panic in connection handler", "remote", conn.RemoteAddr().String(), "panic", fmt.Sprint(r))
		}
		_ = conn.Close()
	}()

	s.handler(conn)
}

// deadlineConn resets deadlines after each read/write
type deadlineConn struct {
	net.Conn
	readTimeout  time.Duration
	writeTimeout time.Duration
	// forcedClose is set by ForceShutdown before the underlying connection is
	// closed. Handlers can call IsForceClosed() to distinguish a server-initiated
	// shutdown from a normal client EOF or network error.
	forcedClose atomic.Bool
}

// IsForceClosed reports whether ForceShutdown closed this connection.
// Application handlers receive the deadlineConn as a net.Conn and may
// type-assert to *deadlineConn when they need this information.
func (c *deadlineConn) IsForceClosed() bool { return c.forcedClose.Load() }

// Read resets the read deadline before reading
func (c *deadlineConn) Read(b []byte) (int, error) {
	if c.readTimeout > 0 {
		_ = c.SetReadDeadline(time.Now().Add(c.readTimeout))
	}
	return c.Conn.Read(b)
}

// Write resets the write deadline before writing
func (c *deadlineConn) Write(b []byte) (int, error) {
	if c.writeTimeout > 0 {
		_ = c.SetWriteDeadline(time.Now().Add(c.writeTimeout))
	}
	return c.Conn.Write(b)
}

// Close closes the connection, suppressing "use of closed connection" errors
// which occur normally when client disconnects before server closes
func (c *deadlineConn) Close() error {
	err := c.Conn.Close()
	// errors.Is traverses the Unwrap() chain, so this check covers *net.OpError
	// wrapping net.ErrClosed without needing a separate type assertion.
	if errors.Is(err, net.ErrClosed) {
		return nil
	}
	return err
}

// GracefulShutdown handles graceful shutdown on termination signals.
// The caller controls the deadline via ctx (typically context.WithTimeout).
// Returns the first non-nil error encountered.
func (s *TCPServer) GracefulShutdown(ctx context.Context) error {
	s.log.LogInfo("Starting graceful shutdown...")
	s.cancel()
	if s.listener != nil {
		if err := s.listener.Close(); err != nil {
			s.log.LogWarn("Error closing TCP listener during graceful shutdown", "error", err.Error())
		}
	}

	done := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(done)
	}()

	var shutdownErr error
	select {
	case <-done:
		s.log.LogInfo("All connections closed gracefully.")
	case <-ctx.Done():
		shutdownErr = ctx.Err()
		s.log.LogWarn("Shutdown deadline exceeded, some connections may be aborted.", "activeConns", s.activeConns.Load())
	}

	// Shutdown metrics server using a short independent deadline.
	if s.metricsServer != nil {
		mCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := s.metricsServer.Shutdown(mCtx); err != nil {
			s.log.LogError("Metrics server shutdown error", "error", err.Error())
			if shutdownErr == nil {
				shutdownErr = err
			}
		}
	}

	// Drain logger buffers (async, dedup) before exiting.
	logCtx, logCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer logCancel()
	_ = logger.Shutdown(logCtx)

	return shutdownErr
}

// GetActiveConnections returns the current number of active connections
func (s *TCPServer) GetActiveConnections() int64 {
	return s.activeConns.Load()
}

// ForceShutdown immediately stops the server without waiting for ongoing connections.
// All active connections are closed immediately.
func (s *TCPServer) ForceShutdown() {
	s.log.LogInfo("Forcefully closing TCP server...")
	s.cancel()
	if s.listener != nil {
		if err := s.listener.Close(); err != nil {
			s.log.LogWarn("Error closing TCP listener during force shutdown", "error", err.Error())
		}
	}

	// Close every tracked connection immediately.
	s.conns.Range(func(key, _ any) bool {
		if dc, ok := key.(*deadlineConn); ok {
			dc.forcedClose.Store(true)
			if err := dc.Conn.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
				s.log.LogWarn("Error closing connection during force shutdown", "remote", dc.RemoteAddr().String(), "error", err.Error())
			}
		}
		return true
	})

	// Wait briefly for handler goroutines to finish their defer cleanup now
	// that all connections have been force-closed. Without this, use-after-free
	// log output and metric counter corruption can occur before the metrics
	// server is shut down immediately below.
	forceWaitDone := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(forceWaitDone)
	}()
	select {
	case <-forceWaitDone:
	case <-time.After(5 * time.Second):
		s.log.LogWarn("ForceShutdown: timed out waiting for handler goroutines", "activeConns", s.activeConns.Load())
	}

	// Shutdown metrics server.
	if s.metricsServer != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := s.metricsServer.Shutdown(ctx); err != nil {
			s.log.LogError("Metrics server shutdown error during force shutdown", "error", err.Error())
		}
	}

	// Drain logger buffers (async, dedup) before exiting.
	logCtx, logCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer logCancel()
	_ = logger.Shutdown(logCtx)
}

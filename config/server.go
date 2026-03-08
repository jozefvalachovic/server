package config

import (
	"os"
	"strconv"
	"time"
)

const (
	ShutdownTimeout = 15 * time.Second
	// HTTP
	HTTPIdleTimeout       = 30 * time.Second
	HTTPReadHeaderTimeout = 5 * time.Second
	// TCP
	TCPReadTimeout  = 15 * time.Second
	TCPWriteTimeout = 15 * time.Second
)

// Default TCP max connections (can be overridden via TCP_MAX_CONNS env var).
// The default is intentionally high enough to avoid premature rejection on
// production workloads; tune down via TCP_MAX_CONNS for resource-constrained
// environments.
const DefaultTCPMaxConns = 10_000

// GetTCPMaxConns returns the maximum concurrent TCP connections from environment or default
func GetTCPMaxConns() int {
	if maxConns := os.Getenv("TCP_MAX_CONNS"); maxConns != "" {
		if val, err := strconv.Atoi(maxConns); err == nil && val > 0 {
			return val
		}
	}
	return DefaultTCPMaxConns
}

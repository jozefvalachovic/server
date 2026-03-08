package server_test

import (
	"context"
	"crypto/tls"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/jozefvalachovic/server/server"
)

// ── DefaultTLSConfig ──────────────────────────────────────────────────────

func TestDefaultTLSConfig(t *testing.T) {
	cfg := server.DefaultTLSConfig()
	if cfg.MinVersion != tls.VersionTLS12 {
		t.Fatalf("want MinVersion TLS 1.2, got %#x", cfg.MinVersion)
	}
	if len(cfg.CipherSuites) == 0 {
		t.Fatal("expected non-empty CipherSuites")
	}
}

// ── Validate ──────────────────────────────────────────────────────────────

func TestHTTPServerConfig_Validate_Good(t *testing.T) {
	cfg := server.HTTPServerConfig{
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 30 * time.Second,
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestHTTPServerConfig_Validate_NegativeTimeout(t *testing.T) {
	cfg := server.HTTPServerConfig{ReadTimeout: -1}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected error for negative ReadTimeout")
	}
}

func TestHTTPServerConfig_Validate_WriteLessThanRead(t *testing.T) {
	cfg := server.HTTPServerConfig{
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 5 * time.Second,
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected error when WriteTimeout < ReadTimeout")
	}
}

func TestHTTPServerConfig_Validate_NegativeMaxConns(t *testing.T) {
	cfg := server.HTTPServerConfig{MaxConns: -1}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected error for negative MaxConns")
	}
}

func TestHTTPServerConfig_Validate_WeakTLS(t *testing.T) {
	cfg := server.HTTPServerConfig{
		TLSConfig: &tls.Config{MinVersion: tls.VersionTLS10},
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected error for TLS < 1.2")
	}
}

func TestTCPServerConfig_Validate_Good(t *testing.T) {
	cfg := server.TCPServerConfig{
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestTCPServerConfig_Validate_NegativeTimeout(t *testing.T) {
	cfg := server.TCPServerConfig{WriteTimeout: -1}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected error for negative WriteTimeout")
	}
}

// ── HealthChecker concurrent stress ───────────────────────────────────────

func TestHealthChecker_ConcurrentCheckAndRegister(t *testing.T) {
	hc := server.NewHealthChecker("test", 2*time.Second)

	// Register a check that toggles between healthy and unhealthy.
	var toggle sync.Mutex
	healthy := true
	hc.Register("db", func(_ context.Context) error {
		toggle.Lock()
		defer toggle.Unlock()
		if !healthy {
			return errors.New("down")
		}
		return nil
	})

	var wg sync.WaitGroup
	const goroutines = 50
	const iterations = 200

	// Hammer Result() concurrently.
	for range goroutines / 2 {
		wg.Go(func() {
			for range iterations {
				_ = hc.Result(context.Background())
			}
		})
	}

	// Simultaneously register/deregister checks.
	for i := range goroutines / 2 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			name := "dynamic-" + string(rune('a'+i))
			for range iterations {
				hc.Register(name, func(_ context.Context) error { return nil })
				_ = hc.Result(context.Background())
				hc.Deregister(name)

				toggle.Lock()
				healthy = !healthy
				toggle.Unlock()
			}
		}(i)
	}

	wg.Wait()
}

// ── HealthChecker handler race ────────────────────────────────────────────

func TestHealthChecker_ConcurrentHTTPHandlers(t *testing.T) {
	hc := server.NewHealthChecker("race-test", 2*time.Second)
	hc.Register("always-ok", func(_ context.Context) error { return nil })

	liveness := hc.LivenessHandler()
	readiness := hc.ReadinessHandler()

	var wg sync.WaitGroup
	const n = 100

	for range n {
		wg.Add(2)
		go func() {
			defer wg.Done()
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
			liveness(rec, req)
			if rec.Code != http.StatusOK {
				t.Errorf("liveness: want 200, got %d", rec.Code)
			}
		}()
		go func() {
			defer wg.Done()
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
			readiness(rec, req)
			// 200 or 503 are both valid depending on timing.
		}()
	}

	wg.Wait()
}

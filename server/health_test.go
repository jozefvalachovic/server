package server

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestHealthChecker_ZeroChecks(t *testing.T) {
	hc := NewHealthChecker("1.0.0", 5*time.Second)
	r := hc.Result(context.Background())
	if r.Status != HealthStatusOK {
		t.Fatalf("want ok, got %s", r.Status)
	}
	if r.Version != "1.0.0" {
		t.Fatalf("want version 1.0.0, got %s", r.Version)
	}
}

func TestHealthChecker_AllUp(t *testing.T) {
	hc := NewHealthChecker("", 5*time.Second)
	hc.Register("db", func(ctx context.Context) error { return nil })
	hc.Register("redis", func(ctx context.Context) error { return nil })
	r := hc.Result(context.Background())
	if r.Status != HealthStatusOK {
		t.Fatalf("want ok, got %s", r.Status)
	}
	if len(r.Checks) != 2 {
		t.Fatalf("want 2 checks, got %d", len(r.Checks))
	}
}

func TestHealthChecker_Degraded(t *testing.T) {
	hc := NewHealthChecker("", 5*time.Second)
	hc.Register("db", func(ctx context.Context) error { return nil })
	hc.Register("redis", func(ctx context.Context) error { return errors.New("connection refused") })
	r := hc.Result(context.Background())
	if r.Status != HealthStatusDegraded {
		t.Fatalf("want degraded, got %s", r.Status)
	}
}

func TestHealthChecker_AllDown(t *testing.T) {
	hc := NewHealthChecker("", 5*time.Second)
	hc.Register("db", func(ctx context.Context) error { return errors.New("down") })
	hc.Register("redis", func(ctx context.Context) error { return errors.New("down") })
	r := hc.Result(context.Background())
	if r.Status != HealthStatusDown {
		t.Fatalf("want down, got %s", r.Status)
	}
}

func TestHealthChecker_Deregister(t *testing.T) {
	hc := NewHealthChecker("", 5*time.Second)
	hc.Register("db", func(ctx context.Context) error { return errors.New("fail") })
	hc.Deregister("db")
	r := hc.Result(context.Background())
	if r.Status != HealthStatusOK {
		t.Fatalf("want ok after deregister, got %s", r.Status)
	}
}

func TestHealthChecker_Timeout(t *testing.T) {
	hc := NewHealthChecker("", 50*time.Millisecond)
	hc.Register("slow", func(ctx context.Context) error {
		<-ctx.Done()
		return ctx.Err()
	})
	r := hc.Result(context.Background())
	if r.Status != HealthStatusDown {
		t.Fatalf("want down for timed-out check, got %s", r.Status)
	}
}

func TestHealthChecker_DefaultTimeout(t *testing.T) {
	hc := NewHealthChecker("", 0)
	if hc.timeout != 5*time.Second {
		t.Fatalf("want default 5s timeout, got %s", hc.timeout)
	}
}

func TestHealthChecker_Redact(t *testing.T) {
	hc := NewHealthChecker("", 5*time.Second)
	hc.Register("postgres", func(ctx context.Context) error { return nil })
	hc.SetRedactCheckNames(true)
	r := hc.Result(context.Background())
	if _, ok := r.Checks["postgres"]; ok {
		t.Fatal("check name should be redacted")
	}
	if _, ok := r.Checks["check_0"]; !ok {
		t.Fatal("expected redacted key check_0")
	}
}

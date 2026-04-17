package client

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestNew_Defaults(t *testing.T) {
	c := New(Config{})
	if c.http.Timeout != 30*time.Second {
		t.Fatalf("want 30s timeout, got %s", c.http.Timeout)
	}
	if c.retry != nil {
		t.Fatal("retry should be nil by default")
	}
	if c.cb != nil {
		t.Fatal("circuit breaker should be nil by default")
	}
}

func TestGet_Success(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	c := New(Config{Timeout: 5 * time.Second})
	resp, err := c.Get(context.Background(), ts.URL+"/ping")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}
}

func TestRetry_EventualSuccess(t *testing.T) {
	var calls atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		if n < 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	c := New(Config{
		Timeout: 10 * time.Second,
		Retry: &RetryConfig{
			MaxRetries:     3,
			InitialBackoff: time.Millisecond,
			MaxBackoff:     10 * time.Millisecond,
		},
	})
	resp, err := c.Get(context.Background(), ts.URL)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}
	if n := calls.Load(); n < 3 {
		t.Fatalf("expected at least 3 attempts, got %d", n)
	}
}

func TestCircuitBreaker_OpensAfterThreshold(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer ts.Close()

	c := New(Config{
		Timeout: 5 * time.Second,
		Retry:   &RetryConfig{MaxRetries: 0},
		CircuitBreaker: &CircuitBreakerConfig{
			Threshold:    2,
			OpenDuration: time.Minute,
		},
	})

	// requests to fill the threshold
	for range 2 {
		resp, _ := c.Get(context.Background(), ts.URL)
		if resp != nil {
			_ = resp.Body.Close()
		}
	}

	// next request should be blocked by circuit breaker
	_, err := c.Get(context.Background(), ts.URL)
	if err == nil {
		t.Fatal("expected circuit breaker error")
	}
}

func TestBaseURL(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/users" {
			t.Fatalf("want /v1/users, got %s", r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	c := New(Config{
		Timeout: 5 * time.Second,
		BaseURL: ts.URL + "/v1",
	})
	resp, err := c.Get(context.Background(), "/users")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
}

func TestDefaultShouldRetry(t *testing.T) {
	tests := []struct {
		code int
		want bool
	}{
		{200, false},
		{400, false},
		{429, true},
		{502, true},
		{503, true},
		{504, true},
	}
	for _, tc := range tests {
		resp := &http.Response{StatusCode: tc.code}
		if got := defaultShouldRetry(resp, nil); got != tc.want {
			t.Errorf("defaultShouldRetry(%d): want %v, got %v", tc.code, tc.want, got)
		}
	}
	// nil response with error should retry
	if !defaultShouldRetry(nil, http.ErrServerClosed) {
		t.Error("expected retry on error")
	}
}

func TestCircuitBreaker_HalfOpen_Close(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	c := New(Config{
		Timeout: 5 * time.Second,
		CircuitBreaker: &CircuitBreakerConfig{
			Threshold:        2,
			OpenDuration:     50 * time.Millisecond,
			SuccessThreshold: 1,
		},
	})

	// Force the circuit into the open state, opened "in the past" so it's
	// ready to transition to half-open on next allow().
	c.cb.mu.Lock()
	c.cb.state = cbOpen
	c.cb.failures = c.cb.cfg.Threshold
	c.cb.openedAt = time.Now().Add(-100 * time.Millisecond)
	c.cb.mu.Unlock()

	// Next call should transition to half-open → probe succeeds → close.
	resp, err := c.Get(context.Background(), ts.URL)
	if err != nil {
		t.Fatalf("half-open probe should succeed: %v", err)
	}
	_ = resp.Body.Close()

	// Circuit should be closed again — verify with another request.
	c.cb.mu.Lock()
	state := c.cb.state
	c.cb.mu.Unlock()
	if state != cbClosed {
		t.Fatalf("circuit should be closed after successful probe, got state %d", state)
	}
}

func TestCircuitBreaker_HalfOpen_Reopen(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer ts.Close()

	c := New(Config{
		Timeout: 5 * time.Second,
		CircuitBreaker: &CircuitBreakerConfig{
			Threshold:    2,
			OpenDuration: 50 * time.Millisecond,
		},
	})

	// Force the circuit into the open state, ready for half-open.
	c.cb.mu.Lock()
	c.cb.state = cbOpen
	c.cb.failures = c.cb.cfg.Threshold
	c.cb.openedAt = time.Now().Add(-100 * time.Millisecond)
	c.cb.mu.Unlock()

	// Probe fails (503 without retry = recorded as success by default).
	// We need to verify the state after the attempt. Since no retry config,
	// 503 is treated as success and closes the circuit. Let's use a server
	// that returns a network error instead by closing the connection.
	ts.Close()

	ts2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer ts2.Close()

	// Force open again with a retry config so 503 counts as failure.
	c2 := New(Config{
		Timeout: 5 * time.Second,
		Retry: &RetryConfig{
			MaxRetries:     1,
			InitialBackoff: time.Millisecond,
		},
		CircuitBreaker: &CircuitBreakerConfig{
			Threshold:    2,
			OpenDuration: 50 * time.Millisecond,
		},
	})
	c2.cb.mu.Lock()
	c2.cb.state = cbOpen
	c2.cb.failures = c2.cb.cfg.Threshold
	c2.cb.openedAt = time.Now().Add(-100 * time.Millisecond)
	c2.cb.mu.Unlock()

	// Probe in half-open: 503 → recordFailure → should re-open.
	resp, _ := c2.Get(context.Background(), ts2.URL)
	if resp != nil {
		_ = resp.Body.Close()
	}

	// Verify circuit re-opened.
	c2.cb.mu.Lock()
	state := c2.cb.state
	c2.cb.mu.Unlock()
	if state != cbOpen {
		t.Fatalf("circuit should re-open after failed probe, got state %d", state)
	}
}

func TestDo_NonReplayableBody(t *testing.T) {
	var calls atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer ts.Close()

	c := New(Config{
		Timeout: 5 * time.Second,
		Retry: &RetryConfig{
			MaxRetries:     2,
			InitialBackoff: time.Millisecond,
		},
	})

	// Use a reader without GetBody (pipe has no GetBody).
	pr, pw := io.Pipe()
	go func() {
		_, _ = pw.Write([]byte("data"))
		_ = pw.Close()
	}()

	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, ts.URL, pr)
	if err != nil {
		t.Fatal(err)
	}
	req.GetBody = nil // ensure non-replayable

	_, err = c.Do(req)
	if err == nil {
		t.Fatal("expected error for non-replayable body")
	}
	if !strings.Contains(err.Error(), "not re-readable") {
		t.Fatalf("expected 're-readable' error, got: %v", err)
	}
	// Should have only attempted once (no retry possible).
	if n := calls.Load(); n != 1 {
		t.Fatalf("expected 1 attempt, got %d", n)
	}
}

func TestDo_ContextCancel(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer ts.Close()

	c := New(Config{
		Timeout: 30 * time.Second,
		Retry: &RetryConfig{
			MaxRetries:     5,
			InitialBackoff: 5 * time.Second, // long backoff
		},
	})

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	_, err := c.Get(ctx, ts.URL)
	if err == nil {
		t.Fatal("expected context cancellation error")
	}
}

func TestBackoffDuration_Bounds(t *testing.T) {
	c := New(Config{
		Retry: &RetryConfig{
			MaxRetries:     5,
			InitialBackoff: 100 * time.Millisecond,
			MaxBackoff:     2 * time.Second,
			Multiplier:     2.0,
			JitterFraction: 0.2,
		},
	})

	for attempt := 1; attempt <= 5; attempt++ {
		d := c.backoffDuration(attempt)
		if d < 0 {
			t.Fatalf("attempt %d: negative backoff %v", attempt, d)
		}
		if d > c.retry.MaxBackoff {
			t.Fatalf("attempt %d: backoff %v exceeds MaxBackoff %v", attempt, d, c.retry.MaxBackoff)
		}
	}
}

func TestPost_Success(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("want POST, got %s", r.Method)
		}
		ct := r.Header.Get("Content-Type")
		if ct != "application/json" {
			t.Fatalf("want application/json, got %s", ct)
		}
		w.WriteHeader(http.StatusCreated)
	}))
	defer ts.Close()

	c := New(Config{Timeout: 5 * time.Second})
	resp, err := c.Post(context.Background(), ts.URL, "application/json", strings.NewReader(`{"key":"val"}`))
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("want 201, got %d", resp.StatusCode)
	}
}

func TestDelete_Success(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			t.Fatalf("want DELETE, got %s", r.Method)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer ts.Close()

	c := New(Config{Timeout: 5 * time.Second})
	resp, err := c.Delete(context.Background(), ts.URL+"/resource/1")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("want 204, got %d", resp.StatusCode)
	}
}

func TestHTTPError_Error(t *testing.T) {
	e := &HTTPError{StatusCode: 500, Status: "500 Internal Server Error"}
	msg := e.Error()
	if !strings.Contains(msg, "500 Internal Server Error") {
		t.Fatalf("want status in error message, got %s", msg)
	}
}

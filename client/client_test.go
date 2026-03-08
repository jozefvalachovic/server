package client

import (
	"context"
	"net/http"
	"net/http/httptest"
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

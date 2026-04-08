// Package client provides a resilient HTTP client with built-in circuit
// breaker, retry with exponential backoff, and timeout support.
//
// # Quick start
//
//	c := client.New(client.Config{
//	    Timeout:            5 * time.Second,
//	    MaxRetries:         3,
//	    CircuitBreaker:     &client.CircuitBreakerConfig{Threshold: 5},
//	})
//
//	resp, err := c.Get(ctx, "https://api.example.com/data")
package client

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"math/rand/v2"
	"net/http"
	neturl "net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// ── Circuit breaker ──────────────────────────────────────────────────────────

// ErrCircuitOpen is returned when the circuit breaker is in the open state and
// requests are being short-circuited.
var ErrCircuitOpen = errors.New("circuit breaker open")

type cbState int32

const (
	cbClosed   cbState = iota // normal operation
	cbOpen                    // failing — requests short-circuited
	cbHalfOpen                // probe: allow one request through
)

// CircuitBreakerConfig configures a per-client circuit breaker.
type CircuitBreakerConfig struct {
	// Threshold is the number of consecutive failures required to open the
	// circuit. Default: 5.
	Threshold int

	// OpenDuration is how long the circuit stays open before transitioning to
	// half-open for a probe request. Default: 30 s.
	OpenDuration time.Duration

	// SuccessThreshold is the number of consecutive successes in half-open
	// state required to close the circuit again. Default: 1.
	SuccessThreshold int
}

type circuitBreaker struct {
	cfg       CircuitBreakerConfig
	mu        sync.Mutex
	state     cbState
	failures  int
	successes int
	openedAt  time.Time
	inFlight  atomic.Int32 // half-open in-flight probe count
}

func newCircuitBreaker(cfg CircuitBreakerConfig) *circuitBreaker {
	if cfg.Threshold <= 0 {
		cfg.Threshold = 5
	}
	if cfg.OpenDuration <= 0 {
		cfg.OpenDuration = 30 * time.Second
	}
	if cfg.SuccessThreshold <= 0 {
		cfg.SuccessThreshold = 1
	}
	return &circuitBreaker{cfg: cfg}
}

// allow returns true when the request should be allowed through.
func (cb *circuitBreaker) allow() bool {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	switch cb.state {
	case cbClosed:
		return true
	case cbOpen:
		if time.Since(cb.openedAt) >= cb.cfg.OpenDuration {
			cb.state = cbHalfOpen
			cb.successes = 0
			return true
		}
		return false
	case cbHalfOpen:
		// Allow only one probe at a time.
		return cb.inFlight.Add(1) == 1
	}
	return false
}

func (cb *circuitBreaker) recordSuccess() {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	cb.failures = 0
	if cb.state == cbHalfOpen {
		cb.inFlight.Add(-1)
		cb.successes++
		if cb.successes >= cb.cfg.SuccessThreshold {
			cb.state = cbClosed
		}
	}
}

func (cb *circuitBreaker) recordFailure() {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	if cb.state == cbHalfOpen {
		cb.inFlight.Add(-1)
	}
	cb.failures++
	if cb.failures >= cb.cfg.Threshold {
		cb.state = cbOpen
		cb.openedAt = time.Now()
	}
}

// ── Retry / backoff ──────────────────────────────────────────────────────────

// RetryConfig configures exponential-backoff retries.
type RetryConfig struct {
	// MaxRetries is the maximum number of retry attempts (not counting the
	// first attempt). Default: 3.
	MaxRetries int

	// InitialBackoff is the base wait duration before the first retry.
	// Default: 100 ms.
	InitialBackoff time.Duration

	// MaxBackoff caps the computed backoff. Default: 10 s.
	MaxBackoff time.Duration

	// Multiplier is the exponential growth factor. Default: 2.0.
	Multiplier float64

	// JitterFraction adds random jitter up to this fraction of the backoff
	// duration to spread retry storms. Default: 0.2 (±20%).
	JitterFraction float64

	// ShouldRetry is called with the response (may be nil) and error to decide
	// whether the request should be retried. Default: retry on 429, 502, 503,
	// 504 and all network errors.
	ShouldRetry func(resp *http.Response, err error) bool
}

func defaultShouldRetry(resp *http.Response, err error) bool {
	if err != nil {
		return true
	}
	switch resp.StatusCode {
	case http.StatusTooManyRequests,
		http.StatusBadGateway,
		http.StatusServiceUnavailable,
		http.StatusGatewayTimeout:
		return true
	}
	return false
}

// ── Client ───────────────────────────────────────────────────────────────────

// Config configures the resilient HTTP client.
type Config struct {
	// Timeout is the end-to-end request timeout (including retries).
	// Default: 30 s.
	Timeout time.Duration

	// Retry configures retry behaviour. nil disables retries.
	Retry *RetryConfig

	// CircuitBreaker configures the circuit breaker. nil disables it.
	CircuitBreaker *CircuitBreakerConfig

	// Transport is the underlying HTTP transport. Default: http.DefaultTransport.
	Transport http.RoundTripper

	// BaseURL is an optional prefix prepended to all relative request paths.
	BaseURL string
}

// HTTPError is returned by Client.Do when the server responds with a
// non-retryable status code (or all retries are exhausted). Callers can
// use errors.As to inspect the status code and response body.
type HTTPError struct {
	StatusCode int
	Status     string
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("server returned %s", e.Status)
}

// Client is a resilient HTTP client with circuit breaker and retry support.
type Client struct {
	http    *http.Client
	retry   *RetryConfig
	cb      *circuitBreaker
	baseURL string
}

// New creates a new resilient Client with the given configuration.
func New(cfg Config) *Client {
	if cfg.Timeout <= 0 {
		cfg.Timeout = 30 * time.Second
	}
	transport := cfg.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}

	c := &Client{
		http: &http.Client{
			Timeout:   cfg.Timeout,
			Transport: transport,
		},
		baseURL: cfg.BaseURL,
	}
	if cfg.Retry != nil {
		r := *cfg.Retry
		if r.MaxRetries <= 0 {
			r.MaxRetries = 3
		}
		if r.InitialBackoff <= 0 {
			r.InitialBackoff = 100 * time.Millisecond
		}
		if r.MaxBackoff <= 0 {
			r.MaxBackoff = 10 * time.Second
		}
		if r.Multiplier <= 0 {
			r.Multiplier = 2.0
		}
		if r.JitterFraction <= 0 {
			r.JitterFraction = 0.2
		}
		if r.ShouldRetry == nil {
			r.ShouldRetry = defaultShouldRetry
		}
		c.retry = &r
	}
	if cfg.CircuitBreaker != nil {
		c.cb = newCircuitBreaker(*cfg.CircuitBreaker)
	}
	return c
}

// Do executes an HTTP request with retry and circuit breaker logic applied.
// The caller's req.URL is never modified; a clone is used when BaseURL
// resolution is needed.
func (c *Client) Do(req *http.Request) (*http.Response, error) {
	// Resolve relative URLs against BaseURL when configured. Uses url.JoinPath
	// for correct path joining that avoids double-slash or missing-segment issues.
	// The original req.URL is cloned so the caller's value is never mutated.
	if c.baseURL != "" && !isAbsolute(req.URL.String()) {
		joined, err := neturl.JoinPath(c.baseURL, req.URL.String())
		if err == nil {
			parsed, parseErr := neturl.Parse(joined)
			if parseErr == nil {
				// Preserve the original query string — JoinPath only joins paths.
				parsed.RawQuery = req.URL.RawQuery
				parsed.Fragment = req.URL.Fragment
				req = req.Clone(req.Context())
				req.URL = parsed
				if req.Host == "" {
					req.Host = parsed.Host
				}
			}
		}
	}

	// canRetryBody is true when the body can be re-read for subsequent attempts.
	// For requests with no body this is always true. For requests with a body,
	// GetBody must be set (http.NewRequest sets it automatically for bytes.Buffer,
	// strings.Reader, and bytes.Reader; other readers must set it explicitly).
	// Without GetBody the body is exhausted after the first attempt and retries
	// would silently send an empty payload.
	canRetryBody := req.Body == nil || req.GetBody != nil

	var (
		resp    *http.Response
		lastErr error
		attempt int
	)
	maxAttempts := 1
	if c.retry != nil {
		maxAttempts = 1 + c.retry.MaxRetries
	}

	for attempt = 0; attempt < maxAttempts; attempt++ {
		if attempt > 0 {
			if !canRetryBody {
				// Body is not re-readable; stop here rather than sending empty payload.
				return nil, fmt.Errorf("retry aborted: request body is not re-readable: %w", lastErr)
			}
			// Recreate the body from GetBody so the retry sends the full payload.
			if req.GetBody != nil {
				body, err := req.GetBody()
				if err != nil {
					return nil, fmt.Errorf("retry: failed to recreate request body: %w", err)
				}
				req.Body = body
			}
			backoff := c.backoffDuration(attempt)
			timer := time.NewTimer(backoff)
			select {
			case <-req.Context().Done():
				timer.Stop()
				return nil, req.Context().Err()
			case <-timer.C:
			}
		}

		// Circuit breaker gate.
		if c.cb != nil && !c.cb.allow() {
			return nil, fmt.Errorf("%w: %s", ErrCircuitOpen, req.URL.Host)
		}

		resp, lastErr = c.http.Do(req)

		shouldRetry := c.retry != nil && c.retry.ShouldRetry(resp, lastErr)
		if lastErr != nil || (resp != nil && shouldRetry) {
			if c.cb != nil {
				c.cb.recordFailure()
			}
			if resp != nil {
				// Capture the status so the final error message is informative
				// even when the HTTP round-trip itself succeeded (no transport error).
				if lastErr == nil {
					lastErr = &HTTPError{StatusCode: resp.StatusCode, Status: resp.Status}
				}
				// Drain up to 1 MiB before closing so the transport can reuse the
				// underlying TCP connection. Bodies larger than the cap are not
				// worth keeping — the connection will be discarded instead.
				_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
				_ = resp.Body.Close()
				resp = nil
			}
			if !shouldRetry {
				return nil, lastErr
			}
			continue
		}

		if c.cb != nil {
			c.cb.recordSuccess()
		}
		return resp, nil
	}

	return nil, fmt.Errorf("all %d attempts failed: %w", maxAttempts, lastErr)
}

// Get is a convenience wrapper around Do for GET requests.
// The url parameter can be absolute or relative to BaseURL.
func (c *Client) Get(ctx context.Context, url string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	return c.Do(req)
}

// Post is a convenience wrapper around Do for POST requests.
// The url parameter can be absolute or relative to BaseURL.
func (c *Client) Post(ctx context.Context, url, contentType string, body io.Reader) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", contentType)
	return c.Do(req)
}

// Put is a convenience wrapper around Do for PUT requests.
// The url parameter can be absolute or relative to BaseURL.
func (c *Client) Put(ctx context.Context, url, contentType string, body io.Reader) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, url, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", contentType)
	return c.Do(req)
}

// Patch is a convenience wrapper around Do for PATCH requests.
// The url parameter can be absolute or relative to BaseURL.
func (c *Client) Patch(ctx context.Context, url, contentType string, body io.Reader) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPatch, url, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", contentType)
	return c.Do(req)
}

// Delete is a convenience wrapper around Do for DELETE requests.
// The url parameter can be absolute or relative to BaseURL.
func (c *Client) Delete(ctx context.Context, url string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, url, nil)
	if err != nil {
		return nil, err
	}
	return c.Do(req)
}

func (c *Client) backoffDuration(attempt int) time.Duration {
	base := float64(c.retry.InitialBackoff) * math.Pow(c.retry.Multiplier, float64(attempt-1))
	jitter := base * c.retry.JitterFraction * (rand.Float64()*2 - 1)
	d := max(min(time.Duration(base+jitter), c.retry.MaxBackoff), 0)
	return d
}

func isAbsolute(url string) bool {
	return strings.HasPrefix(url, "http://") || strings.HasPrefix(url, "https://")
}

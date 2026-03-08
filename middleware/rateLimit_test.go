package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// okHandler is a simple 200 handler used across tests.
var rateLimitOKHandler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
})

func TestRateLimit_DefaultConfig_AllowsRequests(t *testing.T) {
	handler := HTTPRateLimit()(rateLimitOKHandler)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "1.2.3.4:9000"

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
}

func TestRateLimit_ExhaustedBurst_Returns429(t *testing.T) {
	burst := 3
	handler := HTTPRateLimit(HTTPRateLimitConfig{
		RequestsPerSecond: 1, // slow refill so burst is the only budget
		Burst:             burst,
	})(rateLimitOKHandler)

	req := func() *http.Request {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.RemoteAddr = "10.0.0.1:1234"
		return r
	}

	// First `burst` requests should be allowed.
	for i := range burst {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req())
		if rec.Code != http.StatusOK {
			t.Fatalf("request %d: expected 200, got %d", i+1, rec.Code)
		}
	}

	// Next request must be rejected.
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req())
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429 after burst exhausted, got %d", rec.Code)
	}
}

func TestRateLimit_CustomStatusCode(t *testing.T) {
	handler := HTTPRateLimit(HTTPRateLimitConfig{
		RequestsPerSecond: 1,
		Burst:             1,
		StatusCode:        http.StatusForbidden,
	})(rateLimitOKHandler)

	ip := "10.0.0.2:5678"
	call := func() int {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.RemoteAddr = ip
		handler.ServeHTTP(rec, req)
		return rec.Code
	}

	call() // consume the single token
	if got := call(); got != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", got)
	}
}

func TestRateLimit_TokensRefillOverTime(t *testing.T) {
	handler := HTTPRateLimit(HTTPRateLimitConfig{
		RequestsPerSecond: 1000, // fast refill for test reliability
		Burst:             1,
	})(rateLimitOKHandler)

	ip := "10.0.0.3:9999"
	call := func() int {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.RemoteAddr = ip
		handler.ServeHTTP(rec, req)
		return rec.Code
	}

	if got := call(); got != http.StatusOK {
		t.Fatalf("first: expected 200, got %d", got)
	}

	// Wait long enough for at least one token to refill (1 ms at 1000 req/s).
	time.Sleep(5 * time.Millisecond)

	if got := call(); got != http.StatusOK {
		t.Fatalf("after refill: expected 200, got %d", got)
	}
}

func TestRateLimit_PerClientIsolation(t *testing.T) {
	handler := HTTPRateLimit(HTTPRateLimitConfig{
		RequestsPerSecond: 1,
		Burst:             1,
	})(rateLimitOKHandler)

	call := func(ip string) int {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.RemoteAddr = ip + ":1234"
		handler.ServeHTTP(rec, req)
		return rec.Code
	}

	// Exhaust client A's bucket.
	call("192.168.1.1")
	if got := call("192.168.1.1"); got != http.StatusTooManyRequests {
		t.Fatalf("client A: expected 429, got %d", got)
	}

	// Client B still has a full bucket.
	if got := call("192.168.1.2"); got != http.StatusOK {
		t.Fatalf("client B: expected 200, got %d", got)
	}
}

func TestRateLimit_CustomKeyFunc(t *testing.T) {
	handler := HTTPRateLimit(HTTPRateLimitConfig{
		RequestsPerSecond: 1,
		Burst:             1,
		// Treat all requests as the same key — global rate limit.
		KeyFunc: func(_ *http.Request) string { return "global" },
	})(rateLimitOKHandler)

	call := func(ip string) int {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.RemoteAddr = ip + ":9000"
		handler.ServeHTTP(rec, req)
		return rec.Code
	}

	call("1.1.1.1") // consume global token

	// Different IP, same key — still blocked.
	if got := call("2.2.2.2"); got != http.StatusTooManyRequests {
		t.Fatalf("expected 429 for global key, got %d", got)
	}
}

func TestRateLimit_RemoteAddrWithoutPort(t *testing.T) {
	handler := HTTPRateLimit()(rateLimitOKHandler)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	// RemoteAddr without port — remoteIP falls back gracefully.
	req.RemoteAddr = "1.2.3.4"
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
}

func TestRateLimit_NoConfig_UsesDefaults(t *testing.T) {
	// Calling HTTPRateLimit() with no args must not panic and must allow requests.
	handler := HTTPRateLimit()(rateLimitOKHandler)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "5.5.5.5:80"
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
}

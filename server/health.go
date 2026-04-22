package server

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"strconv"

	"sync"
	"time"

	"github.com/jozefvalachovic/logger/v4"
)

// CheckFunc is a function that reports whether a dependency is healthy.
// Return a non-nil error with a human-readable description of the failure.
type CheckFunc func(ctx context.Context) error

// HealthStatus represents the overall service health.
type HealthStatus string

const (
	HealthStatusOK       HealthStatus = "ok"
	HealthStatusDegraded HealthStatus = "degraded"
	HealthStatusDown     HealthStatus = "down"
)

// HealthCheckResult is the JSON body returned by the health and readiness endpoints.
type HealthCheckResult struct {
	Status  HealthStatus           `json:"status"`
	Checks  map[string]checkDetail `json:"checks,omitempty"`
	Version string                 `json:"version,omitempty"`
}

type checkDetail struct {
	Status  HealthStatus `json:"status"`
	Error   string       `json:"error,omitempty"`
	Latency string       `json:"latency,omitempty"`
}

// HealthChecker runs a set of named dependency checks and aggregates their
// results into a structured HealthCheckResult.
type HealthChecker struct {
	mu          sync.RWMutex
	checks      map[string]CheckFunc
	critical    map[string]struct{} // names of critical checks; any failure → HealthStatusDown
	version     string
	timeout     time.Duration
	redactNames bool // when true, check names are replaced with generic keys in responses
}

// NewHealthChecker creates a HealthChecker with an optional service version
// string (included in every response for easy identification).
//
// checkTimeout controls how long each individual CheckFunc is allowed to run.
// Default: 5 s.
func NewHealthChecker(version string, checkTimeout time.Duration) *HealthChecker {
	if checkTimeout <= 0 {
		checkTimeout = 5 * time.Second
	}
	return &HealthChecker{
		checks:   make(map[string]CheckFunc),
		critical: make(map[string]struct{}),
		version:  version,
		timeout:  checkTimeout,
	}
}

// RegisterLoggerHealthCheck registers the logger subsystem as a health
// dependency. The check verifies the output writer, async buffer usage,
// and audit state.
func (h *HealthChecker) RegisterLoggerHealthCheck() {
	h.Register("logger", func(_ context.Context) error {
		return logger.HealthCheck()
	})
}

// Register adds or replaces a named dependency check.
//
//	hc.Register("postgres", func(ctx context.Context) error {
//	    return db.PingContext(ctx)
//	})
func (h *HealthChecker) Register(name string, fn CheckFunc) {
	h.mu.Lock()
	h.checks[name] = fn
	h.mu.Unlock()
}

// RegisterCritical adds or replaces a named dependency check and marks it as
// critical. When any critical check fails, the overall status is HealthStatusDown
// and the readiness endpoint returns 503 — causing Kubernetes to remove the pod
// from service. Use this for dependencies without which the service cannot
// function at all (e.g. primary database, message broker).
//
//	hc.RegisterCritical("postgres", func(ctx context.Context) error {
//	    return db.PingContext(ctx)
//	})
func (h *HealthChecker) RegisterCritical(name string, fn CheckFunc) {
	h.mu.Lock()
	h.checks[name] = fn
	h.critical[name] = struct{}{}
	h.mu.Unlock()
}

// Deregister removes a named check. It is a no-op if the name is not registered.
func (h *HealthChecker) Deregister(name string) {
	h.mu.Lock()
	delete(h.checks, name)
	delete(h.critical, name)
	h.mu.Unlock()
}

// SetCritical promotes an already-registered check to critical, or demotes a
// critical check back to non-critical. Returns true when the check exists.
//
// Use this when the criticality of a dependency is determined after
// registration — for example, a cache that is optional at boot but becomes
// critical after a migration window. Safe to call concurrently with Register
// and Result.
func (h *HealthChecker) SetCritical(name string, critical bool) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, ok := h.checks[name]; !ok {
		return false
	}
	if critical {
		h.critical[name] = struct{}{}
	} else {
		delete(h.critical, name)
	}
	return true
}

// SetRedactCheckNames controls whether check names are replaced with generic
// keys (check_0, check_1 …) in ReadinessHandler responses. Enable this for
// externally-exposed probes to avoid leaking dependency topology to untrusted
// clients.
func (h *HealthChecker) SetRedactCheckNames(v bool) {
	h.mu.Lock()
	h.redactNames = v
	h.mu.Unlock()
}

// Result runs all registered checks concurrently and returns the aggregated result.
func (h *HealthChecker) Result(ctx context.Context) HealthCheckResult {
	h.mu.RLock()
	n := len(h.checks)
	redact := h.redactNames
	if n == 0 {
		h.mu.RUnlock()
		return HealthCheckResult{Status: HealthStatusOK, Version: h.version}
	}
	checks := make(map[string]CheckFunc, n)
	maps.Copy(checks, h.checks)
	criticalSet := make(map[string]struct{}, len(h.critical))
	maps.Copy(criticalSet, h.critical)
	h.mu.RUnlock()

	type entry struct {
		name   string
		detail checkDetail
	}

	ch := make(chan entry, len(checks))
	for name, fn := range checks {
		go func(name string, fn CheckFunc) {
			start := time.Now()
			// Recover panics from check implementations so a single bad check
			// cannot deadlock Result() waiting on a channel send that never
			// happens. Report the panic as a down status with latency up to
			// the panic point.
			defer func() {
				if rec := recover(); rec != nil {
					ch <- entry{name: name, detail: checkDetail{
						Status:  HealthStatusDown,
						Error:   fmt.Sprintf("check panicked: %v", rec),
						Latency: time.Since(start).Round(time.Millisecond).String(),
					}}
				}
			}()

			ctx2, cancel := context.WithTimeout(ctx, h.timeout)
			defer cancel()

			err := fn(ctx2)
			latency := time.Since(start)

			d := checkDetail{
				Status:  HealthStatusOK,
				Latency: latency.Round(time.Millisecond).String(),
			}
			if err != nil {
				d.Status = HealthStatusDown
				d.Error = err.Error()
			}
			ch <- entry{name: name, detail: d}
		}(name, fn)
	}

	details := make(map[string]checkDetail, len(checks))
	anyDown := false
	anyCriticalDown := false
	// allDown starts true when there are registered checks and flips false
	// the moment a healthy check is seen. When len(checks)==0 the loop body
	// never executes, allDown stays false, and the switch falls through to
	// HealthStatusOK — which is the desired behaviour: no registered checks
	// means "nothing to report" → healthy.
	allDown := len(checks) > 0
	for range checks {
		e := <-ch
		details[e.name] = e.detail
		if e.detail.Status == HealthStatusDown {
			anyDown = true
			if _, isCritical := criticalSet[e.name]; isCritical {
				anyCriticalDown = true
			}
		} else {
			allDown = false
		}
	}

	var overall HealthStatus
	switch {
	case allDown && anyDown:
		overall = HealthStatusDown
	case anyCriticalDown:
		// Any critical dependency failure makes the service unfit for traffic.
		overall = HealthStatusDown
	case anyDown:
		overall = HealthStatusDegraded
	default:
		overall = HealthStatusOK
	}

	resultChecks := details
	if redact {
		redacted := make(map[string]checkDetail, len(details))
		i := 0
		for _, d := range details {
			redacted["check_"+strconv.Itoa(i)] = d
			i++
		}
		resultChecks = redacted
	}

	return HealthCheckResult{
		Status:  overall,
		Checks:  resultChecks,
		Version: h.version,
	}
}

// LivenessHandler returns an http.HandlerFunc for the /health (liveness) endpoint.
// Liveness checks only verify the process is alive — they do NOT run dependency
// checks to avoid cascading restart loops.
func (h *HealthChecker) LivenessHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		result := HealthCheckResult{
			Status:  HealthStatusOK,
			Version: h.version,
		}
		writeJSON(w, http.StatusOK, result)
	}
}

// ReadinessHandler returns an http.HandlerFunc for the /readiness endpoint.
// HTTP status semantics:
//   - ok       → 200: all dependencies healthy, pod is ready to serve traffic.
//   - degraded → 200: some dependencies degraded but the pod is still functional;
//     Kubernetes keeps it in rotation. Body status field signals the issue.
//   - down     → 503: all dependencies failed; Kubernetes removes it from rotation.
func (h *HealthChecker) ReadinessHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		result := h.Result(r.Context())
		status := http.StatusOK
		if result.Status == HealthStatusDown {
			status = http.StatusServiceUnavailable
		}
		writeJSON(w, status, result)
	}
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		logger.LogError("Failed to encode health JSON response", "error", err.Error())
	}
}

package middleware

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jozefvalachovic/logger/v4"
)

// ── TraceContext ──────────────────────────────────────────────────────────────

func TestTraceContext_GeneratesTraceparent(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec, _ := serveCapture(TraceContext(), req)

	tp := rec.Header().Get(TraceparentHeader)
	if tp == "" {
		t.Fatal("expected traceparent header in response")
	}
	parts := strings.Split(tp, "-")
	if len(parts) != 4 {
		t.Fatalf("expected 4-part traceparent, got %q", tp)
	}
	if parts[0] != "00" {
		t.Fatalf("expected version 00, got %q", parts[0])
	}
	if len(parts[1]) != 32 {
		t.Fatalf("expected 32-char trace-id, got %q (len %d)", parts[1], len(parts[1]))
	}
	if len(parts[2]) != 16 {
		t.Fatalf("expected 16-char span-id, got %q (len %d)", parts[2], len(parts[2]))
	}
	if len(parts[3]) != 2 {
		t.Fatalf("expected 2-char flags, got %q", parts[3])
	}
}

func TestTraceContext_PreservesIncomingTraceID(t *testing.T) {
	traceID := "4bf92f3577b34da6a3ce929d0e0e4736"
	parentID := "00f067aa0ba902b7"
	incoming := "00-" + traceID + "-" + parentID + "-01"

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set(TraceparentHeader, incoming)
	rec, inner := serveCapture(TraceContext(), req)

	tp := rec.Header().Get(TraceparentHeader)
	parts := strings.Split(tp, "-")
	if len(parts) != 4 {
		t.Fatalf("expected 4-part traceparent, got %q", tp)
	}
	if parts[1] != traceID {
		t.Fatalf("trace-id not preserved: want %s, got %s", traceID, parts[1])
	}
	// Span-id should be newly generated (not same as incoming parent-id).
	if parts[2] == parentID {
		t.Fatal("expected new span-id, got same as incoming parent-id")
	}
	if parts[3] != "01" {
		t.Fatalf("expected flags 01, got %q", parts[3])
	}

	info := TraceInfoFromContext(inner)
	if info.TraceID != traceID {
		t.Fatalf("context trace-id: want %s, got %s", traceID, info.TraceID)
	}
	if info.ParentSpanID != parentID {
		t.Fatalf("context parent-span-id: want %s, got %s", parentID, info.ParentSpanID)
	}
}

func TestTraceContext_MalformedHeaderGeneratesNewTrace(t *testing.T) {
	tests := []struct {
		name   string
		header string
	}{
		{"too few parts", "00-abc-def"},
		{"bad version length", "0-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"},
		{"uppercase hex", "00-4BF92F3577B34DA6A3CE929D0E0E4736-00f067aa0ba902b7-01"},
		{"all-zero trace-id", "00-00000000000000000000000000000000-00f067aa0ba902b7-01"},
		{"all-zero parent-id", "00-4bf92f3577b34da6a3ce929d0e0e4736-0000000000000000-01"},
		{"short trace-id", "00-4bf92f3577b34da6-00f067aa0ba902b7-01"},
		{"garbage", "not-a-traceparent"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			req.Header.Set(TraceparentHeader, tt.header)
			rec, inner := serveCapture(TraceContext(), req)

			tp := rec.Header().Get(TraceparentHeader)
			parts := strings.Split(tp, "-")
			if len(parts) != 4 {
				t.Fatalf("expected valid traceparent, got %q", tp)
			}
			info := TraceInfoFromContext(inner)
			if info.ParentSpanID != "" {
				t.Fatalf("expected empty parent-span-id for new trace, got %q", info.ParentSpanID)
			}
		})
	}
}

func TestTraceContext_TracestatePassthrough(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set(TraceparentHeader, "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01")
	req.Header.Set(TracestateHeader, "vendor1=value1,vendor2=value2")

	rec, inner := serveCapture(TraceContext(), req)

	if got := rec.Header().Get(TracestateHeader); got != "vendor1=value1,vendor2=value2" {
		t.Fatalf("tracestate not passed through: got %q", got)
	}
	info := TraceInfoFromContext(inner)
	if info.TraceState != "vendor1=value1,vendor2=value2" {
		t.Fatalf("context tracestate: got %q", info.TraceState)
	}
}

func TestTraceContext_NoTracestateWhenAbsent(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec, _ := serveCapture(TraceContext(), req)

	if got := rec.Header().Get(TracestateHeader); got != "" {
		t.Fatalf("expected no tracestate header, got %q", got)
	}
}

func TestTraceContext_StoredInContext(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	_, inner := serveCapture(TraceContext(), req)

	info := TraceInfoFromContext(inner)
	if info.TraceID == "" {
		t.Fatal("expected trace-id in context")
	}
	if info.SpanID == "" {
		t.Fatal("expected span-id in context")
	}
}

func TestTraceContext_UniqueTracePerRequest(t *testing.T) {
	mw := TraceContext()
	ids := make(map[string]bool)
	for range 20 {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		rec, _ := serveCapture(mw, req)
		tp := rec.Header().Get(TraceparentHeader)
		if ids[tp] {
			t.Fatalf("duplicate traceparent: %q", tp)
		}
		ids[tp] = true
	}
}

func TestTraceContext_Disabled(t *testing.T) {
	mw := TraceContext(TraceContextConfig{Disabled: true})
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec, _ := serveCapture(mw, req)

	if got := rec.Header().Get(TraceparentHeader); got != "" {
		t.Fatalf("expected no traceparent when disabled, got %q", got)
	}
}

func TestTraceInfoFromContext_EmptyWhenAbsent(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	info := TraceInfoFromContext(req)
	if info.TraceID != "" {
		t.Fatalf("expected empty trace-id, got %q", info.TraceID)
	}
}

func TestTraceContext_DefaultFlags(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec, _ := serveCapture(TraceContext(), req)

	tp := rec.Header().Get(TraceparentHeader)
	parts := strings.Split(tp, "-")
	if parts[3] != "00" {
		t.Fatalf("expected default flags 00, got %q", parts[3])
	}
}

func TestTraceContext_EnrichesLoggerContext(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	_, inner := serveCapture(TraceContext(), req)

	// The middleware should store an enriched logger with traceId and spanId.
	l := logger.FromContext(inner.Context())
	if l == nil {
		t.Fatal("expected non-nil logger from context")
	}
	if l == logger.DefaultLogger() {
		t.Fatal("expected enriched child logger, got DefaultLogger")
	}
}

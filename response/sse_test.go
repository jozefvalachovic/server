package response

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestNewSSEWriter_SetsHeaders(t *testing.T) {
	rec := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/stream", nil)

	sw := NewSSEWriter[string](rec, r)
	if sw == nil {
		t.Fatal("expected non-nil SSEWriter")
	}

	if ct := rec.Header().Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("want text/event-stream, got %q", ct)
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "no-cache" {
		t.Fatalf("want no-cache, got %q", cc)
	}
	if conn := rec.Header().Get("Connection"); conn != "keep-alive" {
		t.Fatalf("want keep-alive, got %q", conn)
	}
}

func TestSSEWriter_Send_WritesDataEvent(t *testing.T) {
	rec := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/stream", nil)

	sw := NewSSEWriter[string](rec, r)
	if err := sw.Send("hello"); err != nil {
		t.Fatalf("send failed: %v", err)
	}

	body := rec.Body.String()
	if !strings.HasPrefix(body, "data: ") {
		t.Fatalf("expected data: prefix, got %q", body)
	}

	// Extract JSON payload.
	jsonStr := strings.TrimPrefix(body, "data: ")
	jsonStr = strings.TrimSpace(jsonStr)
	var envelope APIStream[string]
	if err := json.Unmarshal([]byte(jsonStr), &envelope); err != nil {
		t.Fatalf("decode failed: %v", err)
	}
	if envelope.Code != 200 {
		t.Fatalf("want code=200, got %d", envelope.Code)
	}
	if envelope.Data == nil || *envelope.Data != "hello" {
		t.Fatalf("unexpected data: %v", envelope.Data)
	}
}

func TestSSEWriter_Send_MultipleEvents(t *testing.T) {
	rec := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/stream", nil)

	sw := NewSSEWriter[int](rec, r)
	for i := range 3 {
		if err := sw.Send(i); err != nil {
			t.Fatalf("send %d failed: %v", i, err)
		}
	}

	// Each event ends with \n\n, so we should have 3 data: lines.
	lines := strings.Split(strings.TrimSpace(rec.Body.String()), "\n\n")
	if len(lines) != 3 {
		t.Fatalf("expected 3 events, got %d", len(lines))
	}
}

func TestSSEWriter_SendError_WritesErrorEvent(t *testing.T) {
	rec := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/stream", nil)

	sw := NewSSEWriter[string](rec, r)
	if err := sw.SendError("db failure", "connection refused"); err != nil {
		t.Fatalf("send error failed: %v", err)
	}

	body := rec.Body.String()
	if !strings.Contains(body, "event: error") {
		t.Fatalf("expected event: error, got %q", body)
	}

	// Extract the data line.
	dataIdx := strings.Index(body, "data: ")
	if dataIdx == -1 {
		t.Fatal("expected data: line in error event")
	}
	jsonStr := strings.TrimSpace(body[dataIdx+6:])
	var envelope APIStream[string]
	if err := json.Unmarshal([]byte(jsonStr), &envelope); err != nil {
		t.Fatalf("decode failed: %v", err)
	}
	if envelope.Code != http.StatusInternalServerError {
		t.Fatalf("want code=500, got %d", envelope.Code)
	}
	if envelope.Message == nil || *envelope.Message != "db failure" {
		t.Fatalf("unexpected message: %v", envelope.Message)
	}
}

func TestSSEWriter_SendError_ClosesPreventsMoreSends(t *testing.T) {
	rec := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/stream", nil)

	sw := NewSSEWriter[string](rec, r)
	_ = sw.SendError("fail", "")

	if err := sw.Send("after error"); err == nil {
		t.Fatal("expected error from Send after SendError")
	}
}

func TestSSEWriter_SendHeartbeat(t *testing.T) {
	rec := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/stream", nil)

	sw := NewSSEWriter[string](rec, r)
	if err := sw.SendHeartbeat(); err != nil {
		t.Fatalf("heartbeat failed: %v", err)
	}

	body := rec.Body.String()
	// Heartbeats are SSE comments (lines starting with : )
	if !strings.HasPrefix(body, ": ") {
		t.Fatalf("expected heartbeat comment, got %q", body)
	}

	// Verify JSON payload.
	jsonStr := strings.TrimPrefix(body, ": ")
	jsonStr = strings.TrimSpace(jsonStr)
	var hb HeartbeatData
	if err := json.Unmarshal([]byte(jsonStr), &hb); err != nil {
		t.Fatalf("decode failed: %v", err)
	}
	if hb.Type != "heartbeat" {
		t.Fatalf("want type=heartbeat, got %q", hb.Type)
	}
	if hb.Sent != 1 {
		t.Fatalf("want sent=1, got %d", hb.Sent)
	}
}

func TestSSEWriter_SendHeartbeat_Increments(t *testing.T) {
	rec := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/stream", nil)

	sw := NewSSEWriter[string](rec, r)
	_ = sw.SendHeartbeat()
	_ = sw.SendHeartbeat()

	// Parse the second heartbeat.
	parts := strings.Split(strings.TrimSpace(rec.Body.String()), "\n\n")
	if len(parts) != 2 {
		t.Fatalf("expected 2 heartbeats, got %d", len(parts))
	}
	jsonStr := strings.TrimPrefix(parts[1], ": ")
	var hb HeartbeatData
	if err := json.Unmarshal([]byte(jsonStr), &hb); err != nil {
		t.Fatalf("decode failed: %v", err)
	}
	if hb.Sent != 2 {
		t.Fatalf("want sent=2, got %d", hb.Sent)
	}
}

func TestSSEWriter_Close_WritesDoneEvent(t *testing.T) {
	rec := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/stream", nil)

	sw := NewSSEWriter[string](rec, r)
	sw.Close()

	body := rec.Body.String()
	if !strings.Contains(body, "event: done") {
		t.Fatalf("expected event: done, got %q", body)
	}
}

func TestSSEWriter_Close_Idempotent(t *testing.T) {
	rec := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/stream", nil)

	sw := NewSSEWriter[string](rec, r)
	sw.Close()
	lenAfterFirst := rec.Body.Len()
	sw.Close() // second close should be a no-op
	if rec.Body.Len() != lenAfterFirst {
		t.Fatal("second Close should not write anything")
	}
}

func TestSSEWriter_Send_CancelledContext(t *testing.T) {
	rec := httptest.NewRecorder()
	ctx, cancel := context.WithCancel(context.Background())
	r := httptest.NewRequest("GET", "/stream", nil).WithContext(ctx)

	sw := NewSSEWriter[string](rec, r)
	cancel() // cancel before Send

	if err := sw.Send("data"); err == nil {
		t.Fatal("expected error from Send with cancelled context")
	}
}

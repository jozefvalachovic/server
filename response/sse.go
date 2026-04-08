package response

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/jozefvalachovic/logger/v4"
)

// SSEWriter writes Server-Sent Events framed as JSON APIStream envelopes.
// It handles flushing, heartbeats, and respects the request context.
type SSEWriter[T any] struct {
	w       http.ResponseWriter
	f       http.Flusher
	r       *http.Request
	closed  atomic.Bool
	heartNo int
}

// NewSSEWriter initialises an SSE stream. It sets the required headers and
// flushes them to the client immediately. Returns nil if the ResponseWriter
// does not implement http.Flusher.
func NewSSEWriter[T any](w http.ResponseWriter, r *http.Request) *SSEWriter[T] {
	f, ok := w.(http.Flusher)
	if !ok {
		return nil
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	// Connection: keep-alive is a hop-by-hop header meaningful only in
	// HTTP/1.x. HTTP/2+ multiplexes streams over a single connection and
	// ignores this header; setting it there is technically incorrect.
	if r.ProtoMajor == 1 {
		w.Header().Set("Connection", "keep-alive")
	}
	w.WriteHeader(http.StatusOK)
	f.Flush()

	return &SSEWriter[T]{w: w, f: f, r: r}
}

// Send writes a single data event containing an APIStream envelope.
// Returns an error if the client has disconnected or writing fails.
func (s *SSEWriter[T]) Send(data T) error {
	if s.closed.Load() {
		return fmt.Errorf("sse: writer closed")
	}

	select {
	case <-s.r.Context().Done():
		s.closed.Store(true)
		return s.r.Context().Err()
	default:
	}

	envelope := APIStream[T]{
		Code: http.StatusOK,
		Data: &data,
	}

	b, err := json.Marshal(envelope)
	if err != nil {
		logger.LogError("SSE: failed to marshal event", "error", err.Error())
		return err
	}

	if _, err := fmt.Fprintf(s.w, "data: %s\n\n", b); err != nil {
		s.closed.Store(true)
		return err
	}
	s.f.Flush()
	return nil
}

// SendError writes an error event and marks the stream as closed.
func (s *SSEWriter[T]) SendError(message string, details string) error {
	if s.closed.Load() {
		return fmt.Errorf("sse: writer closed")
	}
	s.closed.Store(true)

	errStr := "Error"
	envelope := APIStream[T]{
		Code:    http.StatusInternalServerError,
		Data:    CreateEmptyData[T](),
		Error:   &errStr,
		Message: &message,
		Details: details,
	}

	b, err := json.Marshal(envelope)
	if err != nil {
		return err
	}

	if _, err := fmt.Fprintf(s.w, "event: error\ndata: %s\n\n", b); err != nil {
		return err
	}
	s.f.Flush()
	return nil
}

// SendHeartbeat writes a heartbeat comment event to keep the connection alive.
func (s *SSEWriter[T]) SendHeartbeat() error {
	if s.closed.Load() {
		return fmt.Errorf("sse: writer closed")
	}

	select {
	case <-s.r.Context().Done():
		s.closed.Store(true)
		return s.r.Context().Err()
	default:
	}

	s.heartNo++
	hb := HeartbeatData{
		Type:      "heartbeat",
		Timestamp: time.Now().UnixMilli(),
		Sent:      s.heartNo,
	}
	b, _ := json.Marshal(hb)

	if _, err := fmt.Fprintf(s.w, ": %s\n\n", b); err != nil {
		s.closed.Store(true)
		return err
	}
	s.f.Flush()
	return nil
}

// Close sends a final "done" event and marks the writer as finished.
func (s *SSEWriter[T]) Close() {
	if s.closed.Load() {
		return
	}
	s.closed.Store(true)
	_, _ = fmt.Fprintf(s.w, "event: done\ndata: {}\n\n")
	s.f.Flush()
}

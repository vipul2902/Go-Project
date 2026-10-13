package httpx

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// SSE writes a Server-Sent Events stream: one-way, server -> client, over
// plain HTTP, with automatic reconnection in browsers (EventSource).
type SSE struct {
	w  http.ResponseWriter
	rc *http.ResponseController
}

// MaxStreamDuration bounds every stream; clients reconnect.
const MaxStreamDuration = 10 * time.Minute

// StartSSE sends the stream headers. The server's WriteTimeout (15s) would
// cut a long-lived stream, so it is lifted for this response only; the
// caller must bound the stream itself (MaxStreamDuration).
func StartSSE(w http.ResponseWriter) (*SSE, error) {
	rc := http.NewResponseController(w)
	if err := rc.SetWriteDeadline(time.Time{}); err != nil {
		return nil, fmt.Errorf("disable write deadline: %w", err)
	}
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Accel-Buffering", "no") // ask nginx-style proxies not to buffer
	w.WriteHeader(http.StatusOK)
	s := &SSE{w: w, rc: rc}
	return s, s.flush()
}

// Event writes one named event with a JSON payload. id lets a reconnecting
// client resume via Last-Event-ID.
func (s *SSE) Event(name, id string, data any) error {
	b, err := json.Marshal(data)
	if err != nil {
		return err
	}
	if id != "" {
		if _, err := fmt.Fprintf(s.w, "id: %s\n", id); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprintf(s.w, "event: %s\ndata: %s\n\n", name, b); err != nil {
		return err
	}
	return s.flush()
}

// Heartbeat writes a comment line so idle proxies keep the connection open.
func (s *SSE) Heartbeat() error {
	if _, err := fmt.Fprint(s.w, ": keep-alive\n\n"); err != nil {
		return err
	}
	return s.flush()
}

func (s *SSE) flush() error { return s.rc.Flush() }

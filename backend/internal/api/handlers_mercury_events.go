package api

import (
	"fmt"
	"io"
	"net/http"
	"time"
)

// mercuryEvents is the ONE Server-Sent-Events change-stream (GET /api/mercury/events). It emits a bare
// topic name whenever something changes (axioms|runs|active|progress|deliveries); the client refetches
// through the normal read endpoints, so a dropped or coalesced tick is safe. Guarded (a session is
// required) but CSRF-free — it is a GET. Replaces the resting polls that used to keep the surface current.
func (s *Server) mercuryEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, http.StatusInternalServerError, "Streaming wird nicht unterstützt")
		return
	}
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("Connection", "keep-alive")
	h.Set("X-Accel-Buffering", "no") // defeat nginx/Caddy response buffering
	w.WriteHeader(http.StatusOK)

	// Subscribe BEFORE the first flush so nothing slips through between "connected" and "listening".
	events, cancel := s.live.Subscribe()
	defer cancel()

	_, _ = io.WriteString(w, "retry: 3000\n\n") // EventSource auto-reconnect hint
	flusher.Flush()

	ctx := r.Context()
	ping := time.NewTicker(25 * time.Second) // keep intermediaries from timing out an idle connection
	defer ping.Stop()

	for {
		select {
		case <-ctx.Done():
			return // client disconnect / shutdown → the deferred cancel unsubscribes
		case ev, ok := <-events:
			if !ok {
				return // the broker closed our subscription
			}
			if _, err := fmt.Fprintf(w, "data: %s\n\n", ev.Topic); err != nil {
				return
			}
			flusher.Flush()
		case <-ping.C:
			if _, err := io.WriteString(w, ": ping\n\n"); err != nil { // a comment line, ignored by EventSource
				return
			}
			flusher.Flush()
		}
	}
}

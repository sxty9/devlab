// The ONE SSE endpoint (S12): GET /api/events pushes TOPIC NAMES ONLY (exactly eight),
// guarded, without CSRF (read-only), with a ~25 s heartbeat. The client refetches through
// the normal read endpoints on every tick, so a dropped or coalesced tick is always safe.
package api

import (
	"fmt"
	"io"
	"net/http"
	"time"
)

// eventsHeartbeat keeps intermediaries from timing out an idle connection. A package var so
// tests can shrink it; the production value is the S12 contract (~25 s).
var eventsHeartbeat = 25 * time.Second

// events streams topic ticks to the client. Exactly one stream per surface; every view
// subscribes through the client-side provider.
func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	if s.broker == nil {
		writeErr(w, http.StatusNotImplemented, "GET /api/events is not wired yet (live updates, Welle 1)")
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, http.StatusInternalServerError, "Streaming is not supported")
		return
	}
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("Connection", "keep-alive")
	h.Set("X-Accel-Buffering", "no") // defeat proxy response buffering
	w.WriteHeader(http.StatusOK)

	// Subscribe BEFORE the first flush so nothing slips through between "connected" and
	// "listening". The request context releases the subscription on disconnect/shutdown.
	ticks, cancel := s.broker.Subscribe(r.Context())
	defer cancel()

	_, _ = io.WriteString(w, "retry: 3000\n\n") // EventSource auto-reconnect hint
	flusher.Flush()

	ctx := r.Context()
	ping := time.NewTicker(eventsHeartbeat)
	defer ping.Stop()

	for {
		select {
		case <-ctx.Done():
			return // client disconnect / shutdown → the deferred cancel unsubscribes
		case t, ok := <-ticks:
			if !ok {
				return // the broker closed our subscription
			}
			// The payload is ONLY the topic name — never data (S12).
			if _, err := fmt.Fprintf(w, "data: %s\n\n", string(t)); err != nil {
				return
			}
			flusher.Flush()
		case <-ping.C:
			if _, err := io.WriteString(w, ": ping\n\n"); err != nil { // comment line, ignored by EventSource
				return
			}
			flusher.Flush()
		}
	}
}

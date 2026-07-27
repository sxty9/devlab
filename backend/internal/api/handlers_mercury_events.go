package api

import (
	"fmt"
	"io"
	"net/http"
	"time"
)

// mercuryEvents is Mercury's single live-update stream: a Server-Sent Events connection that pushes a
// coarse "topic X changed" notification whenever the inventory (axioms, rules, runs, ToDos,
// executions, the live runs, deliveries) changes — from THIS session, another browser window, an
// automatic run, or a second instance sharing this process. The client (src/state/mercuryLive.tsx)
// opens exactly one of these per open Mercury surface and refetches the affected views on each
// notification, so the UI stays current without a resting poll (task requirements 9, 10).
//
// Why SSE and not polling or WebSocket: it is one long-lived GET, the browser's EventSource
// reconnects on its own after a drop (task requirement 12: an interrupted connection finds its way
// back), a closed surface closes the connection (no idle load), and there is no per-second request.
// It is half-duplex (server→client only), which is exactly the shape of "tell me when something
// changed".
//
// The payload is deliberately just the topic name — never the changed data. A dropped or coalesced
// notification is therefore always safe: the next fetch reconciles to the truth (see package live).
func (s *Server) mercuryEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		// No streaming support (should not happen with net/http). Fail loudly rather than buffer
		// forever — the client falls back to its own reconnect loop.
		writeErr(w, http.StatusInternalServerError, "Streaming wird nicht unterstützt")
		return
	}

	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("Connection", "keep-alive")
	// Defeat proxy response buffering (nginx / some Caddy setups), which would otherwise hold events
	// until the connection closes and make the stream useless.
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	// Subscribe BEFORE the initial flush so no notification can slip through between "connected" and
	// "listening". The client also refetches once on open, so the very first state is never missed.
	events, cancel := s.live.Subscribe()
	defer cancel()

	// Tell the browser's EventSource how long to wait before auto-reconnecting, and confirm the stream
	// is live so the client can mark itself connected.
	_, _ = io.WriteString(w, "retry: 3000\n\n")
	flusher.Flush()

	ctx := r.Context()
	// A heartbeat comment keeps intermediaries from timing out an idle connection and lets the client
	// notice a dead link promptly (its reconnect then kicks in). Comments (":"-prefixed) are ignored
	// by EventSource, so they cost nothing semantically.
	ping := time.NewTicker(25 * time.Second)
	defer ping.Stop()

	for {
		select {
		case <-ctx.Done():
			return // client disconnected (or server shutting down) → defer cancel() unsubscribes
		case ev, ok := <-events:
			if !ok {
				return // broker closed our subscription
			}
			if _, err := fmt.Fprintf(w, "data: %s\n\n", ev.Topic); err != nil {
				return
			}
			flusher.Flush()
		case <-ping.C:
			if _, err := io.WriteString(w, ": ping\n\n"); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

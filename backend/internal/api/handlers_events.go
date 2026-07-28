// The ONE SSE endpoint (S12): GET /api/events pushes TOPIC NAMES ONLY (exactly eight),
// guarded, without CSRF (read-only), with a ~25 s heartbeat. B7 fills the body over the
// live.Broker.
package api

import "net/http"

// events streams topic ticks to the client. Exactly one stream per surface; every view
// subscribes through the client-side provider.
func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	if s.broker == nil {
		writeErr(w, http.StatusNotImplemented, "GET /api/events is not wired yet (live updates, Welle 1)")
		return
	}
	panic("TODO(B7)")
}

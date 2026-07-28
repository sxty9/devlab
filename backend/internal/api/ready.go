// The restart-readiness endpoint (A2-7): a status-code-only HTTP server on a Unix socket
// UNDER THE STATE ROOT (statepath.ReadySocket) — GET /ready ⇒ 204 (free: no execution
// document is "running") | 423 (busy). No body, no auth: the socket is only locally
// reachable and the tunnel never routes it; a dead daemon reads as free. B2 fills the body
// over sched.RestartReady.
package api

import "context"

// ServeReadySocket runs the readiness endpoint until ctx ends. It must never be mounted on
// the TCP mux.
func (s *Server) ServeReadySocket(ctx context.Context) error {
	panic("TODO(B2)")
}

// The restart-readiness endpoint (A2-7): a status-code-only HTTP server on a Unix socket
// UNDER THE STATE ROOT (statepath.ReadySocket) — GET /ready ⇒ 204 (free: no execution
// document is "running") | 423 (busy). No body, no auth: the socket is only locally
// reachable and the tunnel never routes it; a dead daemon reads as free.
package api

import (
	"context"
	"errors"
	"net"
	"net/http"
	"os"
)

// ServeReadySocket runs the readiness endpoint until ctx ends. It must never be mounted on
// the TCP mux.
func (s *Server) ServeReadySocket(ctx context.Context) error {
	if s.paths == nil {
		return errors.New("ready socket: no state paths configured")
	}
	sock := s.paths.ReadySocket()
	// A stale socket file of a dead predecessor blocks the bind — replace it.
	if err := os.Remove(sock); err != nil && !os.IsNotExist(err) {
		return err
	}
	l, err := net.Listen("unix", sock)
	if err != nil {
		return err
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /ready", func(w http.ResponseWriter, _ *http.Request) {
		if s.restartReady() {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.WriteHeader(http.StatusLocked)
	})
	srv := &http.Server{Handler: mux}

	done := make(chan struct{})
	go func() {
		defer close(done)
		<-ctx.Done()
		_ = srv.Close()
		_ = os.Remove(sock)
	}()
	err = srv.Serve(l)
	<-done
	if errors.Is(err, http.ErrServerClosed) || errors.Is(err, net.ErrClosed) {
		return nil
	}
	return err
}

// restartReady is free ⇔ no execution document is running. Without the execution machinery
// nothing can run — free.
func (s *Server) restartReady() bool {
	if s.scheduler == nil {
		return true
	}
	return s.scheduler.RestartReady()
}

// The restart-readiness endpoint (A2-7): a status-code-only HTTP server on a Unix socket
// UNDER THE STATE ROOT (statepath.ReadySocket) — GET /ready ⇒ 204 (free: no execution
// document is "running") | 423 (busy). No body, no auth: the socket is only locally
// reachable and the tunnel never routes it; a dead daemon reads as free.
package api

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
)

// SocketMode is the readiness socket's EXPLICIT mode: the service user and its operating group,
// nobody else. A Unix socket is created with 0777&~umask, so without this the free signal would be
// world-readable and world-connectable — and it is not a harmless status line: it is the restart
// gate (any local user could read whether an execution is running, and a "busy" answer defers the
// daemon's own restart). Neither the unit's UMask nor the state directory's mode is something this
// file may rely on, so the mode is set here and proven by test.
const SocketMode = 0o660

// MaxSocketPath is the kernel's limit on a Unix-socket path: `sun_path` holds 108 bytes including
// the terminating NUL, so 107 are usable. Go surfaces an overrun as the bare "bind: invalid
// argument", which names neither the limit nor the state root that hit it — so the limit is named
// here, checked before the bind, and reported with the length and the knob that changes it.
const MaxSocketPath = 107

// stagePrefix names the private staging directory the socket is bound in before it is published.
// It is deliberately SHORTER than the socket's own file name (`restart-ready.sock`), so the staged
// path is always shorter than the published one: the staging can never be the thing that overruns
// the limit while the contract path itself would have fitted.
const stagePrefix = ".r"

// ServeReadySocket runs the readiness endpoint until ctx ends. It must never be mounted on
// the TCP mux.
func (s *Server) ServeReadySocket(ctx context.Context) error {
	if s.paths == nil {
		return errors.New("ready socket: no state paths configured")
	}
	sock := s.paths.ReadySocket()
	if len(sock) > MaxSocketPath {
		return fmt.Errorf("ready socket: %s is %d bytes and a Unix socket path may be at most %d "+
			"(the kernel's sun_path) — the restart gate needs a shorter state root (DEVLAB_STATE_DIR)",
			sock, len(sock), MaxSocketPath)
	}
	// A stale socket file of a dead predecessor blocks the bind — replace it.
	if err := os.Remove(sock); err != nil && !os.IsNotExist(err) {
		return err
	}
	// Bind inside a PRIVATE staging directory (0700) and publish the socket only once its mode is
	// fixed. Binding straight at the contract path and chmod'ing afterwards leaves a window in
	// which the socket already accepts connections at the umask's mode; inside the staging
	// directory no foreign process can even traverse to it, so that window does not exist.
	stage, err := os.MkdirTemp(filepath.Dir(sock), stagePrefix)
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage) // a no-op once the rename below succeeded and the dir was removed
	staged := filepath.Join(stage, "s")
	l, err := net.Listen("unix", staged)
	if err != nil {
		return fmt.Errorf("ready socket: binding %s (%d bytes): %w", staged, len(staged), err)
	}
	if err := os.Chmod(staged, SocketMode); err != nil {
		l.Close()
		return err
	}
	// The rename publishes the socket under its contract path. The listener still believes it owns
	// the staged path, so its own Close no longer unlinks the published socket — the explicit
	// removal on shutdown below is what cleans up.
	if err := os.Rename(staged, sock); err != nil {
		l.Close()
		return err
	}
	_ = os.Remove(stage)

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

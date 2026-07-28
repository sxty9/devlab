package api

import (
	"context"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"devlab/backend/internal/execstate"
	"devlab/backend/internal/model"
	"devlab/backend/internal/runs"
	"devlab/backend/internal/sched"
	"devlab/backend/internal/statepath"
)

// readyClient dials the Unix socket directly — exactly what devlab-restart-when-free does with
// curl --unix-socket.
func readyGet(t *testing.T, sock string) int {
	t.Helper()
	c := &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", sock)
		},
	}}
	deadline := time.Now().Add(2 * time.Second)
	for {
		resp, err := c.Get("http://ready/ready")
		if err == nil {
			defer resp.Body.Close()
			return resp.StatusCode
		}
		if time.Now().After(deadline) {
			t.Fatalf("ready socket never answered: %v", err)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// The readiness endpoint lives on the Unix socket UNDER THE STATE ROOT and answers by status
// code alone: 204 free ⇔ no document running, 423 busy (A2-7). It is never on the TCP mux.
func TestReadySocketAnswers204FreeAnd423Busy(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("DEVLAB_MERCURY_RUNS", filepath.Join(dir, "runs.json"))
	t.Setenv("DEVLAB_MERCURY_RUNS_HISTORY", filepath.Join(dir, "hist"))
	t.Setenv("DEVLAB_MERCURY_EXECUTIONS", filepath.Join(dir, "state", "mercury", "executions"))
	paths := &statepath.Paths{Root: filepath.Join(dir, "state")}
	docs, err := execstate.Open(paths)
	if err != nil {
		t.Fatal(err)
	}
	sch := sched.New(sched.Config{}, docs, runs.NewStore(nil), runs.NewResultStore(paths), nil, nil,
		func(ctx context.Context, _ execstate.Doc, _ runs.Run) error { <-ctx.Done(); return nil }, nil, nil)
	s := &Server{paths: paths}
	s.SetExecution(docs, sch)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errc := make(chan error, 1)
	go func() { errc <- s.ServeReadySocket(ctx) }()

	sock := paths.ReadySocket()
	if got := readyGet(t, sock); got != http.StatusNoContent {
		t.Fatalf("empty floor must read free (204), got %d", got)
	}

	// A running document flips it to busy.
	doc, err := docs.Create("run_x", model.KindTodo, []string{"alpha"}, false, model.Actor{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := docs.Update(doc.ID, func(d *execstate.Doc) error {
		d.SetPhase(model.PhaseRunning, "", model.Actor{}, time.Now())
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if got := readyGet(t, sock); got != http.StatusLocked {
		t.Fatalf("a running execution must read busy (423), got %d", got)
	}

	// Paused/queued documents do NOT hold the restart — only running counts.
	if _, err := docs.Update(doc.ID, func(d *execstate.Doc) error {
		d.SetPhase(model.PhasePaused, "deferred", model.Actor{}, time.Now())
		d.Pause = &model.PauseView{Reason: model.PauseDeferredByUser}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if got := readyGet(t, sock); got != http.StatusNoContent {
		t.Fatalf("a paused execution must not hold the restart, got %d", got)
	}

	// Shutdown removes the socket file.
	cancel()
	select {
	case err := <-errc:
		if err != nil {
			t.Fatalf("serve: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("ready socket never shut down")
	}
	if _, err := os.Stat(sock); !os.IsNotExist(err) {
		t.Fatalf("socket file must be removed on shutdown: %v", err)
	}
}

// Without the execution machinery nothing can run — the socket honestly reads free, and a
// stale socket file of a dead predecessor does not block the bind.
func TestReadySocketFreeWithoutSchedulerAndAfterStaleFile(t *testing.T) {
	dir := t.TempDir()
	paths := &statepath.Paths{Root: dir}
	if err := os.WriteFile(paths.ReadySocket(), []byte("stale"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := &Server{paths: paths}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errc := make(chan error, 1)
	go func() { errc <- s.ServeReadySocket(ctx) }()
	if got := readyGet(t, paths.ReadySocket()); got != http.StatusNoContent {
		t.Fatalf("no machinery ⇒ free, got %d", got)
	}
	cancel()
	if err := <-errc; err != nil {
		t.Fatalf("serve: %v", err)
	}
}

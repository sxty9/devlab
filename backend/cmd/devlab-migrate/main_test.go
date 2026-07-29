// Tests of the command itself: the refusal while the daemon is alive (B-9), the read-only dry
// run, and the usage/config-state answers of the uniform CLI convention.
package main

import (
	"bytes"
	"context"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeReadySocket serves the readiness endpoint on the state root's socket path — the daemon's
// own contract (status code only, no body) — and answers with the given code.
func fakeReadySocket(t *testing.T, sock string, code int) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(sock), 0o700); err != nil {
		t.Fatal(err)
	}
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen %s: %v", sock, err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /ready", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(code) })
	srv := &http.Server{Handler: mux}
	go func() { _ = srv.Serve(l) }()
	t.Cleanup(func() {
		_ = srv.Shutdown(context.Background())
		_ = os.Remove(sock)
	})
}

// While the daemon answers — free (204) OR busy (423) — the import declines and writes nothing:
// the pools belong to the running process (B-9).
func TestDeclinesWhileTheDaemonIsAlive(t *testing.T) {
	for _, code := range []int{http.StatusNoContent, http.StatusLocked} {
		p := stateRoot(t)
		fakeReadySocket(t, p.ReadySocket(), code)
		var out, errOut bytes.Buffer
		got := run([]string{"--input", exportFixture}, &out, &errOut)
		if got != exitDeclined {
			t.Fatalf("ready socket answered %d: expected exit %d, got %d (%s)", code, exitDeclined, got, errOut.String())
		}
		if !strings.Contains(errOut.String(), "the service is running") {
			t.Errorf("the refusal must name its reason: %s", errOut.String())
		}
		if _, err := os.Stat(p.Runs()); !os.IsNotExist(err) {
			t.Error("a declined import must not have written the run pool")
		}
	}
}

// A dead daemon reads as free: a leftover socket FILE that nobody listens on must not block the
// cutover.
func TestStaleSocketFileDoesNotBlockTheImport(t *testing.T) {
	p := stateRoot(t)
	if err := os.MkdirAll(p.Root, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p.ReadySocket(), []byte("stale"), 0o600); err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	if got := run([]string{"--input", exportFixture}, &out, &errOut); got != exitOK {
		t.Fatalf("expected the import to run (exit %d), got %d: %s", exitOK, got, errOut.String())
	}
	if !strings.Contains(out.String(), "imported.") {
		t.Errorf("the import did not report completion:\n%s", out.String())
	}
}

// The dry run reports the same protocol and writes NOTHING.
func TestDryRunWritesNothing(t *testing.T) {
	p := stateRoot(t)
	var out, errOut bytes.Buffer
	if got := run([]string{"--input", exportFixture, "--dry-run"}, &out, &errOut); got != exitOK {
		t.Fatalf("dry run exit %d: %s", got, errOut.String())
	}
	text := out.String()
	for _, want := range []string{
		"dry run — nothing is written",
		"automatic runs (2)",
		"open tasks (1)",
		"history entries (3)",
		"own-repository records skipped (2)",
		"migration protocol M1–M8 (8 items to record)",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("the protocol is missing %q:\n%s", want, text)
		}
	}
	entries, err := os.ReadDir(p.Root)
	if err == nil && len(entries) != 0 {
		t.Errorf("the dry run wrote %d entries below the state root", len(entries))
	}
	if strings.Contains(text, "imported.") {
		t.Error("the dry run must not claim it imported anything")
	}
}

// Usage errors are usage errors (exit 2), and a missing state root is a config-state error
// (exit 5) — the uniform CLI convention, so a scripted cutover can react.
func TestExitCodesFollowTheUniformConvention(t *testing.T) {
	t.Run("missing input", func(t *testing.T) {
		stateRoot(t)
		var out, errOut bytes.Buffer
		if got := run(nil, &out, &errOut); got != exitUsage {
			t.Fatalf("expected exit %d, got %d", exitUsage, got)
		}
		if !strings.Contains(errOut.String(), "--input is required") {
			t.Errorf("the usage message must name the missing flag: %s", errOut.String())
		}
	})
	t.Run("stray argument", func(t *testing.T) {
		stateRoot(t)
		var out, errOut bytes.Buffer
		if got := run([]string{"--input", exportFixture, "apply"}, &out, &errOut); got != exitUsage {
			t.Fatalf("expected exit %d, got %d", exitUsage, got)
		}
	})
	t.Run("no state root", func(t *testing.T) {
		t.Setenv("DEVLAB_STATE_DIR", "")
		t.Setenv("STATE_DIRECTORY", "")
		var out, errOut bytes.Buffer
		if got := run([]string{"--input", exportFixture}, &out, &errOut); got != exitConfigState {
			t.Fatalf("expected exit %d, got %d", exitConfigState, got)
		}
		if !strings.Contains(errOut.String(), "state root") {
			t.Errorf("the error must name the unset state root: %s", errOut.String())
		}
	})
	t.Run("unreadable export", func(t *testing.T) {
		stateRoot(t)
		var out, errOut bytes.Buffer
		if got := run([]string{"--input", filepath.Join(t.TempDir(), "absent.json")}, &out, &errOut); got != exitConfigState {
			t.Fatalf("expected exit %d, got %d", exitConfigState, got)
		}
	})
}

// The import is a ONE-TIME tool: it has no endpoint and no verb that could revive it as a
// service. The whole surface is the two flags.
func TestSurfaceIsTwoFlags(t *testing.T) {
	stateRoot(t)
	var out, errOut bytes.Buffer
	_ = run([]string{"--help"}, &out, &errOut)
	text := errOut.String()
	for _, want := range []string{"-input", "-dry-run", "stop → migrate → start"} {
		if !strings.Contains(text, want) {
			t.Errorf("the usage text is missing %q:\n%s", want, text)
		}
	}
	for _, forbidden := range []string{"--serve", "--endpoint", "--port"} {
		if strings.Contains(text, forbidden) {
			t.Errorf("the one-time import must not offer %q", forbidden)
		}
	}
}

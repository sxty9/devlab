// B-22 pins: the ported per-user workspace guards the workbench builds on stay intact —
// path safety, per-user-per-repo serialization, and the token-never-persisted discipline of
// every network op the workbench performs. (The replaced dev-branch/reset machinery is
// covered by the K-1 tests beside this file.)
package workbench

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"devlab/backend/internal/workspace"
)

// TestSafePathGuards: traversal, .git and escapes are refused by the ported path guard the
// mutating executor ops run behind (B-22 "safePath").
func TestSafePathGuards(t *testing.T) {
	wt := t.TempDir()
	if _, err := workspace.SafePath(wt, "../../etc/passwd"); err == nil {
		t.Error("SafePath accepted a traversal")
	}
	if _, err := workspace.SafePath(wt, ".git/config"); err == nil {
		t.Error("SafePath accepted a .git path")
	}
	if _, err := workspace.SafePath(wt, ".git"); err == nil {
		t.Error("SafePath accepted .git itself")
	}
	abs, err := workspace.SafePath(wt, "a/b.txt")
	if err != nil {
		t.Fatalf("SafePath refused a plain repo-relative path: %v", err)
	}
	if want := filepath.Join(wt, "a", "b.txt"); abs != want {
		t.Errorf("SafePath = %q, want %q", abs, want)
	}
}

// TestPerUserPerRepoMutex: one user's checkout of one repo is serialized; distinct repos
// proceed independently; invalid identities never reach the lock table (B-22 "Mutex").
func TestPerUserPerRepoMutex(t *testing.T) {
	t.Setenv("DEVLAB_WORKSPACES", t.TempDir())
	m := workspace.NewManager(nil)

	if _, err := m.Lock("Bad User", "repo"); err == nil {
		t.Error("Lock accepted an invalid user")
	}
	if _, err := m.Lock("alice", "re po"); err == nil {
		t.Error("Lock accepted an invalid repo")
	}

	unlock, err := m.Lock("alice", "repo")
	if err != nil {
		t.Fatalf("Lock: %v", err)
	}
	sameHeld := make(chan struct{})
	go func() {
		ul, err := m.Lock("alice", "repo")
		if err == nil {
			ul()
		}
		close(sameHeld)
	}()
	select {
	case <-sameHeld:
		t.Fatal("second Lock on the same user/repo did not block")
	case <-time.After(100 * time.Millisecond):
	}
	// A different repo of the same user is independent.
	otherDone := make(chan struct{})
	go func() {
		ul, err := m.Lock("alice", "other")
		if err == nil {
			ul()
		}
		close(otherDone)
	}()
	select {
	case <-otherDone:
	case <-time.After(2 * time.Second):
		t.Fatal("Lock on a different repo was blocked")
	}
	unlock()
	select {
	case <-sameHeld:
	case <-time.After(2 * time.Second):
		t.Fatal("unlock did not release the waiter")
	}
}

// TestNetworkOpsNeverPersistToken: the workbench's own network ops (fetch on Prepare, push
// on Publish) inherit the ported credential discipline — the token lives in env for the
// duration of the op and never lands in .git/config (B-22 "Leak-Check").
func TestNetworkOpsNeverPersistToken(t *testing.T) {
	_, wt, b := testRepo(t)
	ctx := context.Background()
	const token = "token-that-must-never-persist"
	b.WithToken(token)

	if _, err := b.Prepare(ctx, LegacyShared, ""); err != nil {
		t.Fatalf("Prepare with token: %v", err)
	}
	writeF(t, filepath.Join(wt, "w.txt"), "w\n")
	gitT(t, wt, "add", "-A")
	gitCommit(t, wt, "w")
	if err := b.Publish(ctx); err != nil {
		t.Fatalf("Publish with token: %v", err)
	}

	cfg := readF(t, filepath.Join(wt, ".git", "config"))
	if strings.Contains(cfg, token) {
		t.Errorf("token leaked into .git/config")
	}
	if strings.Contains(cfg, "x-access-token") {
		t.Errorf("credential-helper identity persisted into .git/config")
	}
}

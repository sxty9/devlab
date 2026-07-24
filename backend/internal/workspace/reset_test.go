package workspace

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestResetToRemote pins the behaviour the autonomous runner depends on: a workspace that was
// cloned once must be brought to the CURRENT remote state before a run reads it. It also pins the
// destructive half — local commits, local edits and leftover artefacts from a previous run must be
// gone, because a run workspace is disposable and stale state silently corrupts the next run.
func TestResetToRemote(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	ctx := context.Background()
	root := t.TempDir()

	origin := filepath.Join(root, "origin.git")
	gitT(t, "", "init", "--bare", "-b", "main", origin)

	seed := filepath.Join(root, "seed")
	gitT(t, "", "clone", origin, seed)
	writeF(t, filepath.Join(seed, "README.md"), "v1\n")
	gitT(t, seed, "add", "-A")
	gitCommit(t, seed, "seed")
	gitT(t, seed, "push", "origin", "main")

	// The runner's workspace: cloned once, then never refreshed by Ensure.
	wt := filepath.Join(root, "work")
	gitT(t, "", "clone", origin, wt)

	// Meanwhile the remote moves on — this is what a run must pick up but currently would not.
	writeF(t, filepath.Join(seed, "README.md"), "v2\n")
	writeF(t, filepath.Join(seed, "CLAUDE.md"), "constitution\n")
	gitT(t, seed, "add", "-A")
	gitCommit(t, seed, "advance")
	gitT(t, seed, "push", "origin", "main")

	// And the workspace is dirty from a previous run: a local commit, an uncommitted edit, an
	// untracked file and an ignored build artefact.
	writeF(t, filepath.Join(wt, "stray.txt"), "left over\n")
	writeF(t, filepath.Join(wt, ".gitignore"), "build/\n")
	gitT(t, wt, "add", "-A")
	gitCommit(t, wt, "local work that must not survive")
	writeF(t, filepath.Join(wt, "README.md"), "locally mangled\n")
	if err := os.MkdirAll(filepath.Join(wt, "build"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeF(t, filepath.Join(wt, "build", "artefact.bin"), "stale\n")

	e := Executor{PerUser: false}
	if err := e.ResetToRemote(ctx, wt, "", "main"); err != nil {
		t.Fatalf("ResetToRemote: %v", err)
	}

	// The remote's current content is present...
	if got := readF(t, filepath.Join(wt, "README.md")); got != "v2\n" {
		t.Errorf("README.md = %q, want %q — workspace did not advance to the remote", got, "v2\n")
	}
	if got := readF(t, filepath.Join(wt, "CLAUDE.md")); got != "constitution\n" {
		t.Errorf("CLAUDE.md = %q, want %q — new remote file missing", got, "constitution\n")
	}
	// ...and every trace of the previous run is gone.
	for _, gone := range []string{"stray.txt", filepath.Join("build", "artefact.bin")} {
		if _, err := os.Stat(filepath.Join(wt, gone)); !os.IsNotExist(err) {
			t.Errorf("%s survived the reset (err=%v)", gone, err)
		}
	}
	if out, _ := runGit(e.gitIn(ctx, wt, "", "status", "--porcelain")); out != "" {
		t.Errorf("workspace not clean after reset: %q", out)
	}

	// A rejected branch name must not reach git.
	if err := e.ResetToRemote(ctx, wt, "", "--upload-pack=evil"); err == nil {
		t.Error("ResetToRemote accepted an option-shaped branch name")
	}
}

// TestCommitsAhead pins the measure the runner uses to decide whether there is anything to push.
// The case that matters is the one that silently broke a real run: the agent committed its own work,
// so the working tree is CLEAN while a finished implementation sits in a commit. A working-tree-only
// check reports "nothing happened" and throws it away.
func TestCommitsAhead(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	ctx := context.Background()
	root := t.TempDir()

	origin := filepath.Join(root, "origin.git")
	gitT(t, "", "init", "--bare", "-b", "main", origin)
	seed := filepath.Join(root, "seed")
	gitT(t, "", "clone", origin, seed)
	writeF(t, filepath.Join(seed, "README.md"), "v1\n")
	gitT(t, seed, "add", "-A")
	gitCommit(t, seed, "seed")
	gitT(t, seed, "push", "origin", "main")

	wt := filepath.Join(root, "work")
	gitT(t, "", "clone", origin, wt)
	e := Executor{PerUser: false}

	// Fresh clone: nothing to push.
	n, err := e.CommitsAhead(ctx, wt, "origin/main")
	if err != nil {
		t.Fatalf("CommitsAhead: %v", err)
	}
	if n != 0 {
		t.Errorf("fresh clone is %d commits ahead, want 0", n)
	}

	// The agent does its work AND commits it — the working tree ends up clean.
	writeF(t, filepath.Join(wt, "IMPLEMENTED.md"), "done by the agent\n")
	gitT(t, wt, "add", "-A")
	gitCommit(t, wt, "agent commits its own work")

	if dirty, _ := runGit(e.gitIn(ctx, wt, "", "status", "--porcelain")); dirty != "" {
		t.Fatalf("precondition: working tree should be clean, got %q", dirty)
	}
	n, err = e.CommitsAhead(ctx, wt, "origin/main")
	if err != nil {
		t.Fatalf("CommitsAhead: %v", err)
	}
	if n != 1 {
		t.Errorf("CommitsAhead = %d, want 1 — a clean tree with an agent commit must still count as work to push", n)
	}

	if _, err := e.CommitsAhead(ctx, wt, "--evil"); err == nil {
		t.Error("CommitsAhead accepted an option-shaped base")
	}
}

func readF(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

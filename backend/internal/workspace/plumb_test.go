package workspace

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestSyncFilePlumbing exercises the rollout plumbing against a real local git repo (no sudo layer):
// it must splice the generated block into CLAUDE.md, rename CLAUDE.MD → CLAUDE.md, preserve every
// other file and the surrounding content, push a single commit, and be idempotent on a second run.
func TestSyncFilePlumbing(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	ctx := context.Background()
	root := t.TempDir()

	// A bare origin, plus a seeded working clone we push from to set up the default branch.
	origin := filepath.Join(root, "origin.git")
	gitT(t, "", "init", "--bare", "-b", "main", origin)

	seed := filepath.Join(root, "seed")
	gitT(t, "", "clone", origin, seed)
	writeF(t, filepath.Join(seed, "README.md"), "# repo\n")
	// wrong-case file (never auto-loaded by Claude Code) carrying old notes
	writeF(t, filepath.Join(seed, "CLAUDE.MD"), "# service notes\n\nlocal stuff\n")
	gitT(t, seed, "add", "-A")
	gitCommit(t, seed, "seed")
	gitT(t, seed, "push", "origin", "main")

	// The rollout operates on its OWN clone, never touching a working tree.
	wt := filepath.Join(root, "work")
	gitT(t, "", "clone", origin, wt)

	e := Executor{PerUser: false}
	// A marker-aware splice (like mercury.SpliceMarker): replaces the region if present, else
	// appends — so re-running yields identical content and SyncFile can prove idempotency.
	splice := func(old string) string {
		region := "<!-- BEGIN -->\nAXIOME\n<!-- END -->"
		b, en := strings.Index(old, "<!-- BEGIN -->"), strings.Index(old, "<!-- END -->")
		if b >= 0 && en > b {
			return old[:b] + region + old[en+len("<!-- END -->"):]
		}
		if strings.TrimSpace(old) == "" {
			return region + "\n"
		}
		return strings.TrimRight(old, "\n") + "\n\n" + region + "\n"
	}

	// Dry-run: reports a change but pushes nothing.
	dry, err := e.SyncFile(ctx, wt, "", "main", "CLAUDE.md", "CLAUDE.MD", splice, true, "")
	if err != nil {
		t.Fatalf("dry-run: %v", err)
	}
	if !dry.Changed {
		t.Fatal("dry-run should report a change")
	}
	if headHasFile(t, origin, "CLAUDE.md") {
		t.Fatal("dry-run must not push")
	}

	// Apply.
	res, err := e.SyncFile(ctx, wt, "", "main", "CLAUDE.md", "CLAUDE.MD", splice, false, "")
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if !res.Changed || res.Commit == "" {
		t.Fatalf("apply should push a commit: %+v", res)
	}

	// Verify the pushed tree: CLAUDE.md present with the block + old notes, CLAUDE.MD gone, README kept.
	claude := showFile(t, origin, "main", "CLAUDE.md")
	if !strings.Contains(claude, "AXIOME") || !strings.Contains(claude, "local stuff") {
		t.Fatalf("CLAUDE.md missing block or old content:\n%s", claude)
	}
	if headHasFile(t, origin, "CLAUDE.MD") {
		t.Fatal("CLAUDE.MD should have been renamed away")
	}
	if !headHasFile(t, origin, "README.md") {
		t.Fatal("README.md must be preserved")
	}

	// Idempotent: a second identical run changes nothing (same tree → no commit).
	again, err := e.SyncFile(ctx, wt, "", "main", "CLAUDE.md", "CLAUDE.MD", splice, false, "")
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if again.Changed {
		t.Errorf("second identical run must be a no-op, got Changed=true")
	}
}

func gitT(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	cmd.Env = gitEnv()
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}

func gitCommit(t *testing.T, dir, msg string) {
	t.Helper()
	cmd := exec.Command("git", "commit", "-m", msg)
	cmd.Dir = dir
	cmd.Env = gitEnv()
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git commit: %v\n%s", err, out)
	}
}

func gitEnv() []string {
	return append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t",
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
}

func writeF(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func headHasFile(t *testing.T, bareRepo, name string) bool {
	t.Helper()
	cmd := exec.Command("git", "-C", bareRepo, "ls-tree", "--name-only", "main")
	cmd.Env = gitEnv()
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("ls-tree: %v\n%s", err, out)
	}
	for _, l := range strings.Split(string(out), "\n") {
		if l == name {
			return true
		}
	}
	return false
}

func showFile(t *testing.T, bareRepo, branch, name string) string {
	t.Helper()
	cmd := exec.Command("git", "-C", bareRepo, "show", branch+":"+name)
	cmd.Env = gitEnv()
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("show %s: %v\n%s", name, err, out)
	}
	return string(out)
}

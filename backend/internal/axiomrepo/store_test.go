package axiomrepo

import (
	"context"
	"errors"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// newLocalStore builds a store over a REAL git repository pair (a bare "remote" plus a clone), so the
// whole write path — apply, commit, push — is exercised rather than mocked. The token is irrelevant
// against a local path remote, which is exactly why it is safe to use one here.
func newLocalStore(t *testing.T) *Store {
	t.Helper()
	root := t.TempDir()
	remote := filepath.Join(root, "remote.git")
	run(t, "", "git", "init", "--quiet", "--bare", "--initial-branch=main", remote)

	seed := filepath.Join(root, "seed")
	run(t, "", "git", "clone", "--quiet", remote, seed)
	run(t, seed, "git", "-c", "user.name=t", "-c", "user.email=t@t",
		"commit", "--quiet", "--allow-empty", "-m", "init")
	run(t, seed, "git", "push", "--quiet", "origin", "HEAD:main")

	s := New(filepath.Join(root, "work"), remote, func() (string, error) { return "unused", nil })
	// A local path remote takes no auth header; strip the credential plumbing for the test.
	s.plain = true
	return s
}

func run(t *testing.T, dir, name string, args ...string) {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	cmd.Env = append(cmd.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%s %s: %v\n%s", name, strings.Join(args, " "), err, out)
	}
}

// TestStoreRoundTrip pins the store's contract: a written record is listed and read back, a second
// create refuses to overwrite, a move carries the content, and a delete removes it — each landing as a
// real commit on the remote, which is what makes the constitution's history reviewable.
func TestStoreRoundTrip(t *testing.T) {
	ctx := context.Background()
	s := newLocalStore(t)

	const p = "axiome/architecture/keine-redundanz.md"
	if err := s.Put(ctx, p, "---\nid: ax_1\ntitel: Keine Redundanz\n---\nKeine Änderung darf Redundanz schaffen.\n", "Axiom angelegt", "tester", false); err != nil {
		t.Fatalf("Put: %v", err)
	}

	paths, err := s.List(ctx, "")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(paths) != 1 || paths[0] != p {
		t.Fatalf("List = %v, want [%s]", paths, p)
	}
	if got, err := s.List(ctx, "regeln"); err != nil || len(got) != 0 {
		t.Errorf("List(regeln) = %v, %v — want empty", got, err)
	}

	data, found, err := s.Get(ctx, p)
	if err != nil || !found || !strings.Contains(string(data), "Keine Redundanz") {
		t.Fatalf("Get = %q, %v, %v", data, found, err)
	}

	// Creating the same record again must not silently replace it.
	if err := s.Put(ctx, p, "andere", "nochmal", "tester", false); !errors.Is(err, ErrExists) {
		t.Errorf("second create: err = %v, want ErrExists", err)
	}
	// …while an explicit edit may.
	if err := s.Put(ctx, p, "geändert", "Axiom bearbeitet", "tester", true); err != nil {
		t.Errorf("overwrite: %v", err)
	}

	const moved = "axiome/minimalism/keine-redundanz.md"
	if err := s.Move(ctx, p, moved, "verschoben", "tester"); err != nil {
		t.Fatalf("Move: %v", err)
	}
	if _, found, _ := s.Get(ctx, p); found {
		t.Error("the old path still resolves after a move")
	}
	if data, found, _ := s.Get(ctx, moved); !found || string(data) != "geändert" {
		t.Errorf("moved record = %q, found=%v — content must travel with the path", data, found)
	}

	if err := s.Delete(ctx, moved, "gelöscht", "tester"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if paths, _ := s.List(ctx, ""); len(paths) != 0 {
		t.Errorf("after delete: %v, want empty", paths)
	}

	// Every step must be on the REMOTE, not just locally — an edit that never left the machine would be
	// invisible to prod and lost with the workspace.
	out, err := exec.Command("git", "-C", s.dir, "log", "--oneline", "origin/main").Output()
	if err != nil {
		t.Fatalf("log: %v", err)
	}
	if n := strings.Count(strings.TrimSpace(string(out)), "\n") + 1; n < 5 {
		t.Errorf("expected a commit per change on the remote, got %d lines:\n%s", n, out)
	}
}

// TestSafePathRejectsEscapes pins that no request can address anything outside the constitution.
func TestSafePathRejectsEscapes(t *testing.T) {
	for _, p := range []string{"", "/etc/passwd", "axiome/../../secret", ".git/config", "../x.md"} {
		if err := safePath(p); err == nil {
			t.Errorf("safePath(%q) accepted an escaping path", p)
		}
	}
	if err := safePath("axiome/a/b.md"); err != nil {
		t.Errorf("safePath rejected a normal record path: %v", err)
	}
}

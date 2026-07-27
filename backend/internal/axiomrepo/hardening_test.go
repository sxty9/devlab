package axiomrepo

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// TestWriteRetriesOnForeignPush proves the concurrency contract two instances (dev + prod) share on one
// repository: a write beaten to the push by a foreign change is not lost — it is retried on the freshly
// fetched state, and both writes survive on the remote.
//
// The race is made deterministic with the onBeforePush seam: a competing commit is landed on the remote
// AFTER our instance has committed but BEFORE it pushes — the exact window that turns our push into a
// non-fast-forward rejection. Without the retry, our write would either be lost or the push would fail.
func TestWriteRetriesOnForeignPush(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	remote := filepath.Join(root, "remote.git")
	run(t, "", "git", "init", "--quiet", "--bare", "--initial-branch=main", remote)

	seed := filepath.Join(root, "seed")
	run(t, "", "git", "clone", "--quiet", remote, seed)
	run(t, seed, "git", "-c", "user.name=t", "-c", "user.email=t@t",
		"commit", "--quiet", "--allow-empty", "-m", "init")
	run(t, seed, "git", "push", "--quiet", "origin", "HEAD:main")

	// Our instance, and a foreign instance (prod beside dev) pre-cloned so its hook can push first.
	a := New(filepath.Join(root, "a"), remote, func() (string, error) { return "unused", nil })
	foreign := filepath.Join(root, "foreign")
	run(t, "", "git", "clone", "--quiet", remote, foreign)

	fired := 0
	a.onBeforePush = func() {
		if fired > 0 {
			return // land the competing commit exactly once, on the first attempt
		}
		fired++
		p := filepath.Join(foreign, "axiome", "foreign.md")
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("FOREIGN"), 0o644); err != nil {
			t.Fatal(err)
		}
		run(t, foreign, "git", "add", "-A")
		run(t, foreign, "git", "-c", "user.name=f", "-c", "user.email=f@f",
			"commit", "--quiet", "-m", "foreign write")
		run(t, foreign, "git", "push", "--quiet", "origin", "HEAD:main")
	}

	if err := a.Put(ctx, "axiome/ours.md", "OURS", "our write", "tester", false); err != nil {
		t.Fatalf("Put must succeed after retrying on the refreshed state: %v", err)
	}
	if fired != 1 {
		t.Fatalf("the collision hook fired %d times, want exactly 1", fired)
	}

	// No lost write: a fresh clone of the remote must carry BOTH the foreign record and ours.
	verify := filepath.Join(root, "verify")
	run(t, "", "git", "clone", "--quiet", remote, verify)
	if _, err := os.Stat(filepath.Join(verify, "axiome", "foreign.md")); err != nil {
		t.Errorf("the foreign write was lost from the remote: %v", err)
	}
	if b, err := os.ReadFile(filepath.Join(verify, "axiome", "ours.md")); err != nil || string(b) != "OURS" {
		t.Errorf("our write was lost or corrupted after the retry: b=%q err=%v", b, err)
	}
}

// TestReadWithoutTokenReportsErrNoToken pins that a read which cannot obtain a GitHub credential fails
// with ErrNoToken and returns NO data — a missing account must never read as an empty constitution.
func TestReadWithoutTokenReportsErrNoToken(t *testing.T) {
	// "owner/repo" is a GitHub remote (not local), so a credential is required.
	erroring := New(filepath.Join(t.TempDir(), "w1"), "owner/axioms",
		func() (string, error) { return "", errors.New("no linked account") })
	if paths, err := erroring.List(context.Background(), ""); !errors.Is(err, ErrNoToken) || paths != nil {
		t.Fatalf("List with an erroring token: paths=%v err=%v, want nil + ErrNoToken", paths, err)
	}

	// An empty token (link present but no secret) must be treated the same, not passed to git.
	empty := New(filepath.Join(t.TempDir(), "w2"), "owner/axioms",
		func() (string, error) { return "   ", nil })
	if _, err := empty.List(context.Background(), ""); !errors.Is(err, ErrNoToken) {
		t.Fatalf("List with an empty token: err=%v, want ErrNoToken", err)
	}
}

// TestReadUnreachableRepoReportsErrUnavailable pins the other robustness half: when the repository
// itself cannot be reached (here, a local path that is not a git repo — deterministic, no network), a
// read returns ErrUnavailable with found=false and no data, never a phantom "there are no axioms".
func TestReadUnreachableRepoReportsErrUnavailable(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "not-a-repo.git") // absolute path ⇒ treated as a local remote
	s := New(filepath.Join(t.TempDir(), "work"), missing, func() (string, error) { return "unused", nil })

	data, found, err := s.Get(context.Background(), "axiome/x.md")
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("Get err = %v, want ErrUnavailable", err)
	}
	if found || data != nil {
		t.Errorf("Get returned found=%v data=%q on an unreachable repo — must be a clean error", found, data)
	}

	if paths, err := s.List(context.Background(), ""); !errors.Is(err, ErrUnavailable) || paths != nil {
		t.Errorf("List on an unreachable repo: paths=%v err=%v, want nil + ErrUnavailable", paths, err)
	}
}

// TestEnsureReadmeSeedsOnceAndStaysHidden pins that the constitution repo documents itself: the README
// is seeded once (with the where/how/why content), never surfaces as a record, and re-seeding is a
// no-op rather than a churning commit.
func TestEnsureReadmeSeedsOnceAndStaysHidden(t *testing.T) {
	ctx := context.Background()
	s := newLocalStore(t)

	if err := s.EnsureReadme(ctx); err != nil {
		t.Fatalf("EnsureReadme: %v", err)
	}
	data, found, err := s.Get(ctx, ReadmePath)
	if err != nil || !found {
		t.Fatalf("README was not seeded: found=%v err=%v", found, err)
	}
	// where / how / why — the three things the documentation must name.
	for _, want := range []string{"single source of truth", "Through Mercury", "unprotected"} {
		if !strings.Contains(string(data), want) {
			t.Errorf("seeded README is missing the expected phrase %q", want)
		}
	}
	if paths, _ := s.List(ctx, ""); len(paths) != 0 {
		t.Errorf("README leaked into the record list: %v — it must be documentation only", paths)
	}

	before := commitCount(t, s.dir)
	if err := s.EnsureReadme(ctx); err != nil {
		t.Fatalf("second EnsureReadme: %v", err)
	}
	if after := commitCount(t, s.dir); after != before {
		t.Errorf("re-seeding created a commit (%d → %d); it must be idempotent", before, after)
	}
}

func commitCount(t *testing.T, dir string) int {
	t.Helper()
	out, err := exec.Command("git", "-C", dir, "rev-list", "--count", "HEAD").Output()
	if err != nil {
		t.Fatalf("rev-list: %v", err)
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil {
		t.Fatalf("parse commit count %q: %v", out, err)
	}
	return n
}

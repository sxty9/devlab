package workbench

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"devlab/backend/internal/workspace"
)

// testRepo builds a bare origin with a seeded default branch and a workspace clone, and
// returns a hermetic bench on it. Global/system git config is neutralized so a developer's
// ~/.gitconfig (signing, merge defaults, identity) can never perturb the assertions —
// including inside the bench's own subprocesses, which inherit the test env.
func testRepo(t *testing.T) (origin, wt string, b *Bench) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_CONFIG_SYSTEM", "/dev/null")
	root := t.TempDir()
	origin = filepath.Join(root, "origin.git")
	gitT(t, "", "init", "--bare", "-b", "main", origin)

	seed := filepath.Join(root, "seed")
	gitT(t, "", "clone", origin, seed)
	writeF(t, filepath.Join(seed, "README.md"), "v1\n")
	gitT(t, seed, "add", "-A")
	gitCommit(t, seed, "seed")
	gitT(t, seed, "push", "origin", "main")

	wt = filepath.Join(root, "work")
	gitT(t, "", "clone", origin, wt)
	return origin, wt, benchAt(t, wt)
}

// benchAt builds a bench for an existing working tree with the hermetic executor form
// (no user identity — the B 1.3a guard has no claim to verify in the sandbox).
func benchAt(t *testing.T, wt string) *Bench {
	t.Helper()
	b, err := New(&workspace.Executor{PerUser: false}, wt)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	// Every bench names the branch it works on — there is no shared fallback any more. The tests
	// of this package are about the branch MECHANICS, so they use one fixed name; which name a
	// task gets is the executor's decision, tested there.
	on, err := b.On(LegacyShared)
	if err != nil {
		t.Fatalf("On: %v", err)
	}
	return on
}

// advanceMain advances origin's default branch from a throwaway clone — an external change
// the workbench must fold in, never be reset onto.
func advanceMain(t *testing.T, origin, name, content string) {
	t.Helper()
	seed := filepath.Join(t.TempDir(), "advance")
	gitT(t, "", "clone", origin, seed)
	writeF(t, filepath.Join(seed, name), content)
	gitT(t, seed, "add", "-A")
	gitCommit(t, seed, "advance main: "+name)
	gitT(t, seed, "push", "origin", "main")
}

// advanceDev advances origin's workbench branch from a throwaway clone — another worker's
// published state this workspace has not seen yet.
func advanceDev(t *testing.T, origin, name, content string) {
	t.Helper()
	other := filepath.Join(t.TempDir(), "other")
	gitT(t, "", "clone", "-b", LegacyShared, origin, other)
	writeF(t, filepath.Join(other, name), content)
	gitT(t, other, "add", "-A")
	gitCommit(t, other, "remote work: "+name)
	gitT(t, other, "push", "origin", LegacyShared)
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

func gitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	cmd.Env = gitEnv()
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git %s: %v", strings.Join(args, " "), err)
	}
	return strings.TrimSpace(string(out))
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

func readF(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

func exists(wt, rel string) bool {
	_, err := os.Stat(filepath.Join(wt, rel))
	return err == nil
}

// commitReachable reports whether sha is an ancestor of (or equal to) the workbench tip.
func commitReachable(t *testing.T, wt, sha string) bool {
	t.Helper()
	cmd := exec.Command("git", "-C", wt, "merge-base", "--is-ancestor", sha, "refs/heads/"+LegacyShared)
	cmd.Env = gitEnv()
	return cmd.Run() == nil
}

// TestNewGuardRefusesForeignWorkspace is B 1.3a: an executor bound to one OS user never
// operates a bench inside another user's workspace — the "night run erases a human's IDE
// edits" pitfall is refused at construction.
func TestNewGuardRefusesForeignWorkspace(t *testing.T) {
	runner := &workspace.Executor{User: "runner", PerUser: true}
	if _, err := New(runner, "/srv/state/workspaces/human/somerepo"); err == nil {
		t.Fatal("New accepted a foreign user's workspace path")
	}
	if _, err := New(runner, "/srv/state/workspaces/runner/somerepo"); err != nil {
		t.Fatalf("New refused the runner's own workspace: %v", err)
	}
	if _, err := New(nil, "/srv/state/workspaces/runner/somerepo"); err == nil {
		t.Fatal("New accepted a nil executor")
	}
	if _, err := New(runner, "  "); err == nil {
		t.Fatal("New accepted an empty working-tree path")
	}
	// The hermetic form carries no user identity — no claim to verify.
	if _, err := New(&workspace.Executor{PerUser: false}, "/anywhere/at/all"); err != nil {
		t.Fatalf("New refused the identity-less test executor: %v", err)
	}
}

// TestHeadNamesTheTip is REQ-022.3: the delivered state is nameable — Head resolves the
// workbench branch itself, even while the working tree is parked elsewhere.
func TestHeadNamesTheTip(t *testing.T) {
	_, wt, b := testRepo(t)
	ctx := context.Background()
	if _, err := b.Prepare(ctx); err != nil {
		t.Fatal(err)
	}
	writeF(t, filepath.Join(wt, "work.txt"), "work\n")
	gitT(t, wt, "add", "-A")
	gitCommit(t, wt, "work")
	want := gitOut(t, wt, "rev-parse", "refs/heads/"+LegacyShared)

	got, err := b.Head(ctx)
	if err != nil {
		t.Fatalf("Head: %v", err)
	}
	if got != want {
		t.Errorf("Head = %s, want %s", got, want)
	}
	// Parked on the default branch, the workbench tip stays the answer.
	gitT(t, wt, "switch", "main")
	got, err = b.Head(ctx)
	if err != nil {
		t.Fatalf("Head (parked): %v", err)
	}
	if got != want {
		t.Errorf("Head while parked = %s, want %s", got, want)
	}
}

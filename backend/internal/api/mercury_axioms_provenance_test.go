package api

import (
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"devlab/backend/internal/axiomrepo"
)

// gitCmd runs a git command in dir (dir="" for a repo-less command like init/clone), failing the test
// on error. Author/committer identity is forced so a commit works in a bare environment.
func gitCmd(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}

// seedAxiomRemote builds a bare "remote" constitution repo containing exactly one axiom record, whose
// body carries the given marker, and returns the remote's filesystem path.
func seedAxiomRemote(t *testing.T, recordPath, bodyMarker string) string {
	t.Helper()
	root := t.TempDir()
	remote := filepath.Join(root, "remote.git")
	gitCmd(t, "", "init", "--quiet", "--bare", "--initial-branch=main", remote)

	seed := filepath.Join(root, "seed")
	gitCmd(t, "", "clone", "--quiet", remote, seed)
	abs := filepath.Join(seed, filepath.FromSlash(recordPath))
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(abs, []byte("---\nid: ax_proof\ntitel: Proof\n---\n"+bodyMarker+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitCmd(t, seed, "add", "-A")
	gitCmd(t, seed, "-c", "user.name=t", "-c", "user.email=t@t", "commit", "--quiet", "-m", "seed")
	gitCmd(t, seed, "push", "--quiet", "origin", "HEAD:main")
	return remote
}

// getItem drives the real read handler the running service exposes.
func getItem(t *testing.T, s *Server, recordPath string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest("GET", "/api/mercury/item?path="+url.QueryEscape(recordPath), nil)
	w := httptest.NewRecorder()
	s.mercuryItem(w, r)
	return w
}

// TestMercuryItemServedFromWorkingCopy proves the running service serves the constitution from the
// repository's WORKING COPY — not from some other store, and not from an in-memory shortcut. It asserts
// two things a "both stores hold the same content" test could never establish:
//
//  1. The working copy comes into being. s.dir/.git does not exist until the first read triggers the
//     clone.
//  2. The served bytes ARE the working copy's bytes. After the clone, the on-disk record is rewritten
//     directly — no git add/commit/push — so that state exists nowhere but on this disk. A second read
//     (within the fetch window, so no refresh intervenes) returns exactly that edit. A service reading
//     the remote, a cache, or a second store would still return the original bytes and fail here.
func TestMercuryItemServedFromWorkingCopy(t *testing.T) {
	const recordPath = "axiome/architecture/keine-redundanz.md"
	remote := seedAxiomRemote(t, recordPath, "REMOTE-ORIGINAL-BODY")

	workDir := filepath.Join(t.TempDir(), "work")
	// A local (filesystem) remote needs no credential; the token func is never consulted.
	s := &Server{axioms: axiomrepo.New(workDir, remote, func() (string, error) { return "", nil })}

	// (1) The working copy must not exist yet — nothing has touched the repository.
	if _, err := os.Stat(filepath.Join(workDir, ".git")); !os.IsNotExist(err) {
		t.Fatalf("working copy must not exist before the first read (stat err=%v)", err)
	}

	first := getItem(t, s, recordPath)
	if first.Code != 200 {
		t.Fatalf("first read: status = %d, body = %s", first.Code, first.Body.String())
	}
	if !strings.Contains(first.Body.String(), "REMOTE-ORIGINAL-BODY") {
		t.Fatalf("first read did not return the seeded record: %s", first.Body.String())
	}
	// The read has now brought the working copy into being — the clone happened.
	if _, err := os.Stat(filepath.Join(workDir, ".git")); err != nil {
		t.Fatalf("the read did not create the working copy: %v", err)
	}

	// (2) Rewrite the record ONLY in the working copy — no commit, no push. This body exists on this
	// disk alone; the remote still holds REMOTE-ORIGINAL-BODY.
	onDisk := filepath.Join(workDir, filepath.FromSlash(recordPath))
	if err := os.WriteFile(onDisk, []byte("---\nid: ax_proof\ntitel: Proof\n---\nLOCAL-WORKINGCOPY-EDIT\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	second := getItem(t, s, recordPath)
	if !strings.Contains(second.Body.String(), "LOCAL-WORKINGCOPY-EDIT") {
		t.Fatalf("the read is NOT served from the working copy — got %s, want the on-disk-only edit", second.Body.String())
	}
	if strings.Contains(second.Body.String(), "REMOTE-ORIGINAL-BODY") {
		t.Fatalf("the read still reflects the remote rather than the working copy: %s", second.Body.String())
	}
}

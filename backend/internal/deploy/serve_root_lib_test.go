package deploy

// A web face is copied into a serve root with `rsync -a`, which carries the SOURCE's owner and mode.
// On a freshly built production host the source is the artifact built on the workbench under the
// unprivileged build account with a group-only umask, so the delivered web root arrived drwxr-x--- and
// the edge (a DIFFERENT account) answered 404 over a present index.html. The fix makes the serving side
// derive the permissions from the PUBLIC ROLE — never from whoever built — and PROVES the edge can read
// the start page. Both halves live once in devlab-setup-lib.sh (setup_serve_root_readable /
// setup_serve_root_check), shared by the dev installer's self web + dashboard serve and the production
// receiver's web root, so no serve-root copy re-implements the rule or can drift.
//
// These exercise the shared functions FOR REAL against a group-only source that reproduces the bug: the
// tests run unprivileged, so setup_serve_root_check takes its other-read-bit branch — the same property
// `nobody` would prove under root. That is the CI-faithful form of "assert the edge account can read the
// served index, and the root path returns the face rather than 404": a 404 happens precisely because the
// edge cannot read, so an index the role check passes is an index the edge serves.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// buildAccountWebRoot stages a serve root the way `rsync -a` delivers one built under the workbench
// build account's group-only umask: a directory the owner+group may enter but no one else, holding an
// index.html readable only to owner+group. This is the exact shape measured on the new host
// (drwxr-x--- / files without other-read).
func buildAccountWebRoot(t *testing.T) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "www")
	if err := os.MkdirAll(root, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "index.html"), []byte("<html>face</html>"), 0o640); err != nil {
		t.Fatal(err)
	}
	// os.MkdirAll/WriteFile apply the process umask; force the group-only bits so the reproduction does
	// not depend on the test runner's umask.
	if err := os.Chmod(root, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(root, "index.html"), 0o640); err != nil {
		t.Fatal(err)
	}
	return root
}

// TEST: a serve root delivered with the build account's group-only mode is REFUSED by name — the edge
// could not read the start page, so the delivery is a failure that says so, never a silent green.
func TestLibServeRootCheckRefusesBuildAccountMode(t *testing.T) {
	root := buildAccountWebRoot(t)
	res := sourceLib(t, nil, "setup_serve_root_check "+root)
	if res.exit == 0 {
		t.Fatalf("a group-only serve root must be refused (the edge cannot read it), got exit 0\n%s", res.out)
	}
	// Unprivileged (the test runner), the check reads the mode bits and names the missing other-read and
	// its consequence — the edge answers 404 over a page that is present.
	if !strings.Contains(res.out, "other-read") || !strings.Contains(res.out, "404 over a present page") {
		t.Errorf("the refusal must name the missing other-read and the 404-over-a-present-page consequence:\n%s", res.out)
	}
}

// TEST: setup_serve_root_readable makes the group-only serve root readable by its PUBLIC ROLE, and the
// proof then passes — the edge can read the served index. This is the fix, end to end at the shared
// function both the receiver and the installer call.
func TestLibServeRootReadableThenCheckPasses(t *testing.T) {
	root := buildAccountWebRoot(t)
	// Before the fix the check fails (guarded above); apply the serving-side rule and it must pass.
	fix := sourceLib(t, nil, "setup_serve_root_readable "+root+" && setup_serve_root_check "+root)
	if fix.exit != 0 {
		t.Fatalf("after setup_serve_root_readable the edge must be able to read the served index (exit 0), got %d\n%s", fix.exit, fix.out)
	}
	// And the property is concrete: index.html now carries other-read, so any edge identity can fetch it.
	mode, err := os.Stat(filepath.Join(root, "index.html"))
	if err != nil {
		t.Fatal(err)
	}
	if mode.Mode().Perm()&0o004 == 0 {
		t.Errorf("the served start page still lacks other-read after the role treatment: %v", mode.Mode().Perm())
	}
	dir, err := os.Stat(root)
	if err != nil {
		t.Fatal(err)
	}
	// a+rX makes directories traversable for every reader (the edge must enter to reach index.html).
	if dir.Mode().Perm()&0o001 == 0 || dir.Mode().Perm()&0o004 == 0 {
		t.Errorf("the serve root directory is not traversable+readable by others after the role treatment: %v", dir.Mode().Perm())
	}
}

// TEST: a serve root with no index.html at all is refused by name — there is no start page for the
// browser to fetch, so the delivery is incomplete rather than silently green.
func TestLibServeRootCheckRefusesMissingIndex(t *testing.T) {
	root := filepath.Join(t.TempDir(), "www")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	res := sourceLib(t, nil, "setup_serve_root_check "+root)
	if res.exit == 0 {
		t.Fatalf("a serve root without index.html must be refused, got exit 0\n%s", res.out)
	}
	if !strings.Contains(res.out, "no index.html") {
		t.Errorf("the refusal must name the missing start page:\n%s", res.out)
	}
}

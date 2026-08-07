package deploy

// artifact-build stages the deployable artifact AS THE WORKSPACE OWNER. These tests pin the fix for
// the wrapper that reported 'nothing to build' immediately after a SUCCESSFUL build: it hunted the
// web bundle at a fixed ./dist|./build at the repo root, so a pnpm monorepo whose bundle lives in
// frontend/app/dist found nothing and the wrapper accused the innocent repository. The bundle is now
// READ FROM THE BUILD'S OWN RESULT — the directory into which the build wrote an index.html — so it
// survives any layout, and the two states 'nothing to build' vs 'built but result not found' are kept
// as distinct, named errors.

import (
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"
	"testing"
)

// artifactWorkspace stages a per-user workspace the devlab-exec wrapper accepts (a workspace root
// owned by the invoking user) and writes the given files into it (relative path → contents). It
// returns the env (the direct-invocation state-dir seam) and the working-tree path.
func artifactWorkspace(t *testing.T, files map[string]string) (env map[string]string, wt string) {
	t.Helper()
	me, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	state := t.TempDir()
	wt = filepath.Join(state, "workspaces", me.Username, "svc")
	for rel, body := range files {
		p := filepath.Join(wt, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(wt, 0o755); err != nil {
		t.Fatal(err)
	}
	return map[string]string{"DEVLAB_STATE_DIR": state}, wt
}

// needNPM skips a test that must actually run `npm run build`; the detection under test only has
// meaning after a real build. On a host without npm the build stage cannot run, so the test is
// skipped rather than reported as a spurious failure (same pattern as the curl-gated probe test).
func needNPM(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("npm"); err != nil {
		t.Skipf("npm not available: %v", err)
	}
}

// DECISIVE (task test a): a repository whose bundle is NOT at the root (a monorepo) is built and its
// result lands in the artifact. The build writes frontend/app/dist/index.html; the wrapper must find
// it there — the exact case the fixed root-only ./dist|./build list could never see.
func TestArtifactBuildFindsMonorepoBundle(t *testing.T) {
	needNPM(t)
	// A dependency-free package whose build writes a nested bundle. No lockfile ⇒ `npm ci` fails and
	// the wrapper falls back to `npm install`, which needs no network for an empty dependency set.
	env, wt := artifactWorkspace(t, map[string]string{
		"package.json": `{"name":"mono","private":true,"version":"0.0.0","scripts":{` +
			`"build":"mkdir -p frontend/app/dist && printf '<!doctype html><title>ok</title>' > frontend/app/dist/index.html && printf 'x' > frontend/app/dist/app.js"}}`,
	})
	res := runWrapper(t, "deploy/devlab-exec", env, "artifact-build", wt)
	if res.exit != 0 {
		t.Fatalf("a monorepo build must succeed and stage its nested bundle (exit 0), got %d\n%s", res.exit, res.out)
	}
	web := filepath.Join(wt, ".mercury-artifact", "web")
	got, err := os.ReadFile(filepath.Join(web, "index.html"))
	if err != nil {
		t.Fatalf("the nested bundle (frontend/app/dist) must be staged into the artifact web/: %v\n%s", err, res.out)
	}
	if !strings.Contains(string(got), "<title>ok</title>") {
		t.Errorf("the staged start page must be the built one, got:\n%s", string(got))
	}
	if _, err := os.Stat(filepath.Join(web, "app.js")); err != nil {
		t.Errorf("the whole bundle must be staged, not just index.html: %v", err)
	}
}

// Task test b: a repository with no package.json and no go.mod (and no ui) is genuinely 'nothing to
// build' — the message stays, and it must NOT be confused with the built-but-not-found fault below.
func TestArtifactBuildNothingToBuild(t *testing.T) {
	env, wt := artifactWorkspace(t, nil)
	res := runWrapper(t, "deploy/devlab-exec", env, "artifact-build", wt)
	if res.exit != 64 {
		t.Fatalf("nothing to build must die with exit 64, got %d\n%s", res.exit, res.out)
	}
	if !strings.Contains(res.out, "nothing to build") {
		t.Errorf("the refusal must name the empty state: %s", res.out)
	}
	// It must be the plain empty-state message, NOT the wrapper-fault message reserved for a build that
	// ran but whose result was not found.
	if strings.Contains(res.out, "fault of devlab-exec") {
		t.Errorf("an empty repository must not be accused of a wrapper output-detection fault: %s", res.out)
	}
}

// Task test c: the build SUCCEEDS but writes no web bundle. This is a fault of the wrapper's output
// detection, not of the repository, and gets its OWN named error — never the 'nothing to build' state.
func TestArtifactBuildBuiltButResultMissing(t *testing.T) {
	needNPM(t)
	env, wt := artifactWorkspace(t, map[string]string{
		"package.json": `{"name":"n","private":true,"version":"0.0.0","scripts":{"build":"echo built-ok"}}`,
	})
	res := runWrapper(t, "deploy/devlab-exec", env, "artifact-build", wt)
	if res.exit != 64 {
		t.Fatalf("a build with no findable result must die (exit 64), got %d\n%s", res.exit, res.out)
	}
	if !strings.Contains(res.out, "fault of devlab-exec") || !strings.Contains(res.out, "the build itself ran") {
		t.Errorf("the error must point at the wrapper, not the repository, and confirm the build ran: %s", res.out)
	}
	// The two states stay cleanly apart: this is NOT the empty 'nothing to build' message.
	if strings.Contains(res.out, "nothing to build") {
		t.Errorf("a built-but-not-found fault must not masquerade as 'nothing to build': %s", res.out)
	}
}

// A build that writes the bundle at the repo ROOT (the classic single-app SPA, e.g. devlab's own vite
// dist) must keep working — the result-reading detection subsumes the old root-only case, it does not
// replace one narrow guess with another.
func TestArtifactBuildFindsRootBundle(t *testing.T) {
	needNPM(t)
	env, wt := artifactWorkspace(t, map[string]string{
		"package.json": `{"name":"root","private":true,"version":"0.0.0","scripts":{` +
			`"build":"mkdir -p dist && printf '<!doctype html><title>root</title>' > dist/index.html"}}`,
	})
	res := runWrapper(t, "deploy/devlab-exec", env, "artifact-build", wt)
	if res.exit != 0 {
		t.Fatalf("a root-level bundle must still be staged (exit 0), got %d\n%s", res.exit, res.out)
	}
	got, err := os.ReadFile(filepath.Join(wt, ".mercury-artifact", "web", "index.html"))
	if err != nil {
		t.Fatalf("the root bundle must be staged into the artifact web/: %v", err)
	}
	if !strings.Contains(string(got), "<title>root</title>") {
		t.Errorf("the staged start page must be the built one, got:\n%s", string(got))
	}
}

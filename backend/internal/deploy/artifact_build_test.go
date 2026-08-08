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

// A service that SHIPS its own first-time setup product (a top-level setup/ dir carrying its unit under
// its own name) has that product staged verbatim into the artifact — so the installer reads the unit's
// real name from it rather than computing <repo>.service. No npm/go is needed: the setup/ dir alone is a
// deliverable the build carries.
func TestArtifactBuildStagesRepoProvidedSetup(t *testing.T) {
	unit := "[Unit]\n[Service]\nUser=svc\nExecStart=/opt/svc/bin/svcd --listen 127.0.0.1:8770\n"
	env, wt := artifactWorkspace(t, map[string]string{
		"setup/svc-dashboard.service": unit,
		"setup/svc.caddy":             "handle /api/services/svc/* {\n}\n",
	})
	res := runWrapper(t, "deploy/devlab-exec", env, "artifact-build", wt)
	if res.exit != 0 {
		t.Fatalf("a repo-provided setup/ must be staged (exit 0), got %d\n%s", res.exit, res.out)
	}
	staged := filepath.Join(wt, ".mercury-artifact", "setup", "svc-dashboard.service")
	got, err := os.ReadFile(staged)
	if err != nil {
		t.Fatalf("the repo-provided unit must be staged into the artifact setup/: %v\n%s", err, res.out)
	}
	if string(got) != unit {
		t.Errorf("the staged unit must be the repo's own bytes verbatim, got:\n%s", string(got))
	}
	if _, err := os.Stat(filepath.Join(wt, ".mercury-artifact", "setup", "svc.caddy")); err != nil {
		t.Errorf("the whole setup product must travel (route included): %v", err)
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

// ── build.kind: the artifact SAYS what it is (so the installer never guesses a Bauart from a filename) ──

func needTool(t *testing.T, tool string) {
	t.Helper()
	if _, err := exec.LookPath(tool); err != nil {
		t.Skipf("%s not available: %v", tool, err)
	}
}

// A Go service needs NO declaration: when the build produces a binary, artifact-build records the kind as
// go-daemon in the artifact, so the installer reads it (a Go service is delivered exactly as before).
func TestArtifactBuildStampsGoDaemon(t *testing.T) {
	needTool(t, "go")
	env, wt := artifactWorkspace(t, map[string]string{
		"go.mod":  "module svc\n\ngo 1.21\n",
		"main.go": "package main\n\nfunc main() {}\n",
	})
	res := runWrapper(t, "deploy/devlab-exec", env, "artifact-build", wt)
	if res.exit != 0 {
		t.Fatalf("a go build must succeed (exit 0), got %d\n%s", res.exit, res.out)
	}
	got, err := os.ReadFile(filepath.Join(wt, ".mercury-artifact", "build.kind"))
	if err != nil {
		t.Fatalf("a go-daemon artifact must carry a build.kind stamp: %v\n%s", err, res.out)
	}
	if strings.TrimSpace(string(got)) != "go-daemon" {
		t.Errorf("the stamp must record go-daemon, got %q", string(got))
	}
}

// A python-app DECLARES python-app in build.kind; artifact-build then builds a relocatable venv payload and
// stamps the kind — no go.mod, no <repo>d. The venv's console-script shebangs are relocated to the final
// /opt/<repo>/venv so the installer's job stays a pure copy.
func TestArtifactBuildBuildsPythonPayload(t *testing.T) {
	needTool(t, "python3")
	// A python-app must ship its own unit (the source of truth for how it starts).
	unit := "[Unit]\n[Service]\nUser=svc\nExecStart=/opt/svc/venv/bin/uvicorn app.main:app\n"
	env, wt := artifactWorkspace(t, map[string]string{
		"build.kind":                  "python-app\n",
		"setup/svc-dashboard.service": unit,
	})
	res := runWrapper(t, "deploy/devlab-exec", env, "artifact-build", wt)
	if res.exit != 0 {
		t.Fatalf("a declared python-app must build its payload (exit 0), got %d\n%s", res.exit, res.out)
	}
	art := filepath.Join(wt, ".mercury-artifact")
	if got, err := os.ReadFile(filepath.Join(art, "build.kind")); err != nil || strings.TrimSpace(string(got)) != "python-app" {
		t.Fatalf("the artifact must be stamped python-app, got %q err %v", string(got), err)
	}
	py := filepath.Join(art, "payload", "venv", "bin", "python")
	if _, err := os.Stat(py); err != nil {
		t.Fatalf("the payload must carry a built venv interpreter at payload/venv/bin/python: %v\n%s", err, res.out)
	}
	// The venv's pip console script must have had its shebang relocated to the FINAL install path, so a
	// plain copy to /opt/svc lands runnable — the whole point of building it here and not on the target.
	if b, err := os.ReadFile(filepath.Join(art, "payload", "venv", "bin", "pip")); err == nil {
		first := strings.SplitN(string(b), "\n", 2)[0]
		if !strings.HasPrefix(first, "#!/opt/svc/venv/bin/python") {
			t.Errorf("the venv console-script shebang must be relocated to /opt/svc/venv, got %q", first)
		}
	}

	// The payload must be SELF-CONTAINED: it bundles the interpreter and its standard library, so it runs
	// on a target whose python differs from the build host's. artifact-build stamps the bundled X.Y version.
	pv, err := os.ReadFile(filepath.Join(art, "build.python"))
	if err != nil {
		t.Fatalf("a python-app artifact must stamp its bundled python version (build.python): %v\n%s", err, res.out)
	}
	minor := strings.TrimSpace(string(pv))
	base := filepath.Join(art, "payload", "pybase", "bin", "python"+minor)
	if _, err := os.Stat(base); err != nil {
		t.Fatalf("the payload must bundle the interpreter at pybase/bin/python%s: %v", minor, err)
	}
	if _, err := os.Stat(filepath.Join(art, "payload", "pybase", "lib", "python"+minor, "encodings")); err != nil {
		t.Fatalf("the payload must bundle the standard library (encodings) under pybase/lib/python%s: %v", minor, err)
	}
	// pyvenv.cfg must point the venv at the BUNDLED base at its final /opt path, not the build host's /usr —
	// that re-binding is what makes the venv independent of the target's own python version.
	cfg, err := os.ReadFile(filepath.Join(art, "payload", "venv", "pyvenv.cfg"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(cfg), "home = /opt/svc/pybase/bin") {
		t.Errorf("the venv must be re-pointed at the bundled base (home = /opt/svc/pybase/bin), got:\n%s", cfg)
	}
	if strings.Contains(string(cfg), "home = /usr") {
		t.Errorf("the venv must NOT still point at the build host's /usr — that is the version-binding bug:\n%s", cfg)
	}
}

// A setup-only artifact (a service that ships neither a Go build nor a python declaration) carries NO
// build.kind — so the installer refuses it by name rather than assume a Bauart. This pins the boundary
// that makes task test c a real, named refusal rather than a silent go-daemon guess.
func TestArtifactBuildSetupOnlyHasNoStamp(t *testing.T) {
	env, wt := artifactWorkspace(t, map[string]string{
		"setup/svc.service": "[Service]\nUser=svc\n",
	})
	res := runWrapper(t, "deploy/devlab-exec", env, "artifact-build", wt)
	if res.exit != 0 {
		t.Fatalf("a setup-only artifact still builds (the setup travels), got %d\n%s", res.exit, res.out)
	}
	if _, err := os.Stat(filepath.Join(wt, ".mercury-artifact", "build.kind")); !os.IsNotExist(err) {
		t.Errorf("a setup-only artifact must carry NO build.kind stamp (the installer refuses it by name), err=%v", err)
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

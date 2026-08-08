package deploy

// The unified install path once assumed EVERY service is a Go daemon: it demanded a prebuilt <repo>d and
// refused anything else — trapping `holistic`, a Python (uvicorn) service, out of production. The path now
// carries TWO named build kinds (go-daemon, python-app), declared by the artifact (build.kind) and never
// guessed from a filename. These tests pin the four task tests on BOTH the dev installer and the prod
// receiver, off the host, in --check mode:
//   a) a python-app is set up from what its delivered unit describes and its payload contains;
//   b) a go-daemon is delivered exactly as before (the existing suite already proves this);
//   c) an artifact that does not declare its build kind is refused BY NAME — no guessing;
//   d) the production receiver carries the identical capability through the SAME shared dispatch.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// pythonInstallFixture stages a well-formed python-app delivery for the DEV installer: the checkout under
// the workspace root, a build.kind declaring python-app, a prebuilt payload/ tree (a venv the installer
// copies verbatim — its mere presence is what --check checks), the delivered setup/<unit>.service that
// alone knows how to start it (User=<repo>, its own loopback port), and an origin under the managed org.
// onHostFrag is the FragmentPath systemd should report ("" → first-time; the unit path → an update).
func pythonInstallFixture(t *testing.T, repo, unit, onHostFrag string) (env map[string]string, artifact string) {
	t.Helper()
	state := t.TempDir()
	unitDir := t.TempDir()
	checkout := filepath.Join(state, "workspaces", "runner", repo)
	artifact = filepath.Join(checkout, ".mercury-artifact")
	// The prebuilt payload: a --copies venv the installer copies to /opt/<repo>. A bin/ with one script is
	// enough for the dry run — the real bytes are produced by artifact-build and never built by the installer.
	venvBin := filepath.Join(artifact, "payload", "venv", "bin")
	if err := os.MkdirAll(venvBin, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(venvBin, "uvicorn"), []byte("#!/opt/"+repo+"/venv/bin/python\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	stampBuildKind(t, artifact, "python-app")
	// The delivered unit — the source of truth for how a python-app starts (an interpreter out of the venv).
	setup := filepath.Join(artifact, "setup")
	if err := os.MkdirAll(setup, 0o755); err != nil {
		t.Fatal(err)
	}
	u := "[Unit]\nDescription=" + repo + "\n\n[Service]\nUser=" + repo +
		"\nEnvironment=HOLISTIC_SECRET_FILE=/etc/holistic/jwt-secret" +
		"\nExecStart=/opt/" + repo + "/venv/bin/uvicorn app.main:app --host 127.0.0.1 --port 8770\n\n[Install]\nWantedBy=multi-user.target\n"
	if err := os.WriteFile(filepath.Join(setup, unit+".service"), []byte(u), 0o644); err != nil {
		t.Fatal(err)
	}
	writeOrigin(t, checkout, testOwner, repo)
	env = map[string]string{
		"DEVLAB_STATE_DIR": state,
		"DEVLAB_GH_OWNER":  testOwner,
		"DEVLAB_UNIT_DIR":  unitDir,
		"DEVLAB_SYSTEMCTL": fakeSystemctl(t, onHostFrag),
	}
	return env, artifact
}

// TASK TEST a (dev half): a python-app with no <repo>d installs from what the delivery describes — the
// delivered unit is installed and the prebuilt payload is COPIED to /opt/<repo> (no build, no binary).
func TestInstallPythonAppFirstTimeCopiesPayload(t *testing.T) {
	env, artifact := pythonInstallFixture(t, "svc-a", "svc-a", "")
	res := runWrapper(t, "deploy/devlab-install", env, "svc-a", artifact, "dev", "--check")
	if res.exit != 0 {
		t.Fatalf("a python-app first-time setup must plan cleanly (exit 0), got %d\n%s", res.exit, res.out)
	}
	// The program half is a copy of the prebuilt payload, NEVER a <repo>d binary install.
	if !strings.Contains(res.out, "payload") || !strings.Contains(res.out, "/opt/svc-a") {
		t.Errorf("the plan must copy the prebuilt payload into /opt/svc-a: %s", res.out)
	}
	if !strings.Contains(res.out, "rsync") || !strings.Contains(res.out, "--safe-links") {
		t.Errorf("the payload copy must use rsync --safe-links (no escaping symlink into the service tree): %s", res.out)
	}
	if strings.Contains(res.out, "/opt/svc-a/bin/svc-ad") {
		t.Errorf("a python-app must NOT install a <repo>d binary: %s", res.out)
	}
	// The delivered unit is the source of truth for how it starts.
	if !strings.Contains(res.out, "install delivered unit") || !strings.Contains(res.out, "restart svc-a") {
		t.Errorf("the delivered unit must be installed and the service restarted: %s", res.out)
	}
}

// TASK TEST a (dev, update): an already-installed python-app is renewed by copying the new payload and
// restarting — no delivered unit re-install needed (the unit is already ours on the host).
func TestInstallPythonAppUpdateCopiesPayload(t *testing.T) {
	own := filepath.Join(t.TempDir(), "svc-a.service")
	env, artifact := pythonInstallFixture(t, "svc-a", "svc-a", own)
	// point the on-host unit at the DEVLAB_UNIT_DIR the fixture created, so FragmentPath == our own file
	env["DEVLAB_SYSTEMCTL"] = fakeSystemctl(t, filepath.Join(env["DEVLAB_UNIT_DIR"], "svc-a.service"))
	if err := os.WriteFile(filepath.Join(env["DEVLAB_UNIT_DIR"], "svc-a.service"), []byte("[Service]\nUser=svc-a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	res := runWrapper(t, "deploy/devlab-install", env, "svc-a", artifact, "dev", "--check")
	if res.exit != 0 {
		t.Fatalf("a python-app update must plan cleanly (exit 0), got %d\n%s", res.exit, res.out)
	}
	if !strings.Contains(res.out, "payload") || !strings.Contains(res.out, "rsync") {
		t.Errorf("an update must copy the new payload: %s", res.out)
	}
	if strings.Contains(res.out, "install delivered unit") {
		t.Errorf("an update must not re-install the delivered unit (it is already ours): %s", res.out)
	}
}

// TASK TEST c (dev half): an artifact that declares NO build kind is refused by name — the installer never
// falls back to guessing go-daemon from the presence of some file.
func TestInstallUndeclaredBuildKindRefused(t *testing.T) {
	state := t.TempDir()
	checkout := filepath.Join(state, "workspaces", "runner", "svc-a")
	artifact := filepath.Join(checkout, ".mercury-artifact")
	if err := os.MkdirAll(artifact, 0o755); err != nil {
		t.Fatal(err)
	}
	// A file that LOOKS like a go binary is present — but without build.kind the installer must not guess.
	if err := os.WriteFile(filepath.Join(artifact, "svc-ad"), []byte("bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeOrigin(t, checkout, testOwner, "svc-a")
	env := map[string]string{"DEVLAB_STATE_DIR": state, "DEVLAB_GH_OWNER": testOwner}
	res := runWrapper(t, "deploy/devlab-install", env, "svc-a", artifact, "dev", "--check", "--port", "8772")
	if res.exit != 2 {
		t.Fatalf("an undeclared build kind must be refused with exit 2, got %d\n%s", res.exit, res.out)
	}
	if !strings.Contains(res.out, "does not declare its build kind") || !strings.Contains(res.out, "refusing to guess") {
		t.Errorf("the refusal must name the missing declaration and the no-guessing rule: %s", res.out)
	}
	if strings.Contains(res.out, "PLAN:") {
		t.Errorf("a refused install must plan nothing: %s", res.out)
	}
}

// An artifact that declares an UNKNOWN build kind is refused by name too (not one of the two named kinds).
func TestInstallUnknownBuildKindRefused(t *testing.T) {
	env, _, artifact := installEnv(t)
	stampBuildKind(t, artifact, "ruby-gem")
	res := runWrapper(t, "deploy/devlab-install", env, "svc-a", artifact, "dev", "--check", "--port", "8772")
	if res.exit != 2 {
		t.Fatalf("an unknown build kind must be refused with exit 2, got %d\n%s", res.exit, res.out)
	}
	if !strings.Contains(res.out, "unknown build kind 'ruby-gem'") {
		t.Errorf("the refusal must name the unknown kind and the known set: %s", res.out)
	}
}

// A python-app whose FIRST-TIME setup ships NO delivered unit is refused by name: there is no <repo>d
// template that could start a python-app, so the unit must travel in the package.
func TestInstallPythonAppFirstTimeNeedsDeliveredUnit(t *testing.T) {
	state := t.TempDir()
	checkout := filepath.Join(state, "workspaces", "runner", "svc-a")
	artifact := filepath.Join(checkout, ".mercury-artifact")
	if err := os.MkdirAll(filepath.Join(artifact, "payload", "venv", "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	stampBuildKind(t, artifact, "python-app")
	writeOrigin(t, checkout, testOwner, "svc-a")
	env := map[string]string{
		"DEVLAB_STATE_DIR": state, "DEVLAB_GH_OWNER": testOwner,
		"DEVLAB_UNIT_DIR": t.TempDir(), "DEVLAB_SYSTEMCTL": fakeSystemctl(t, ""),
	}
	res := runWrapper(t, "deploy/devlab-install", env, "svc-a", artifact, "dev", "--check", "--port", "8772")
	if res.exit != 5 {
		t.Fatalf("a python-app first-time without a delivered unit must be refused (exit 5), got %d\n%s", res.exit, res.out)
	}
	if !strings.Contains(res.out, "needs a delivered setup") || !strings.Contains(res.out, "only the unit knows how to start it") {
		t.Errorf("the refusal must name the missing unit as the source of truth: %s", res.out)
	}
}

// ─── the production receiver carries the identical capability (TASK TEST d) ────────────────────────────

// pythonRecvFixture stages a python-app under a staging root for the receiver: build.kind, the payload/
// tree, and the delivered setup/<unit>.service. onHostUser is the account the on-host unit runs as
// ("" → no unit → first-time). It returns the env plus the FragmentPath systemctl should report.
func pythonRecvFixture(t *testing.T, repo, unit, onHostUser string) (env map[string]string, fragment string) {
	t.Helper()
	staging := t.TempDir()
	unitDir := t.TempDir()
	art := filepath.Join(staging, repo)
	venvBin := filepath.Join(art, "payload", "venv", "bin")
	if err := os.MkdirAll(venvBin, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(venvBin, "uvicorn"), []byte("#!/opt/"+repo+"/venv/bin/python\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	stampBuildKind(t, art, "python-app")
	setup := filepath.Join(art, "setup")
	if err := os.MkdirAll(setup, 0o755); err != nil {
		t.Fatal(err)
	}
	u := "[Unit]\nDescription=" + repo + "\n\n[Service]\nUser=" + repo +
		"\nEnvironment=HOLISTIC_SECRET_FILE=/etc/holistic/jwt-secret" +
		"\nExecStart=/opt/" + repo + "/venv/bin/uvicorn app.main:app --host 127.0.0.1 --port 8770\n\n[Install]\nWantedBy=multi-user.target\n"
	if err := os.WriteFile(filepath.Join(setup, unit+".service"), []byte(u), 0o644); err != nil {
		t.Fatal(err)
	}
	if onHostUser != "" {
		onHost := filepath.Join(unitDir, unit+".service")
		if err := os.WriteFile(onHost, []byte("[Service]\nUser="+onHostUser+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		fragment = onHost
	}
	return map[string]string{"DEVLAB_STAGING": staging, "DEVLAB_UNIT_DIR": unitDir}, fragment
}

// TASK TEST d: the receiver installs a python-app on the production host through the SAME dispatch — the
// delivered unit is installed and the prebuilt payload is COPIED to /opt/<repo> (this host never builds).
func TestRecvPythonAppFirstTimeCopiesPayload(t *testing.T) {
	env, fragment := pythonRecvFixture(t, "svc-a", "svc-a", "")
	res := runRecv(t, env, fragment, "svc-a")
	if res.exit != 0 {
		t.Fatalf("the receiver must set up a python-app (exit 0), got %d\n%s", res.exit, res.out)
	}
	if !strings.Contains(res.out, "copy prebuilt payload") || !strings.Contains(res.out, "/opt/svc-a") {
		t.Errorf("the receiver must copy the prebuilt payload into /opt/svc-a: %s", res.out)
	}
	if strings.Contains(res.out, "/opt/svc-a/bin/svc-ad") {
		t.Errorf("the receiver must NOT install a <repo>d binary for a python-app: %s", res.out)
	}
	if !strings.Contains(res.out, "install delivered unit") {
		t.Errorf("the delivered unit is the source of truth and must be installed: %s", res.out)
	}
}

// The receiver refuses an artifact that declares no build kind — the identical refusal the dev installer
// makes, proving there is no second, laxer path on production.
func TestRecvUndeclaredBuildKindRefused(t *testing.T) {
	staging := t.TempDir()
	art := filepath.Join(staging, "svc-a")
	if err := os.MkdirAll(filepath.Join(art, "setup"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(art, "svc-ad"), []byte("bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	env := map[string]string{"DEVLAB_STAGING": staging, "DEVLAB_UNIT_DIR": t.TempDir()}
	res := runRecv(t, env, "", "svc-a")
	if res.exit != 2 {
		t.Fatalf("the receiver must refuse an undeclared build kind (exit 2), got %d\n%s", res.exit, res.out)
	}
	if !strings.Contains(res.out, "does not declare its build kind") {
		t.Errorf("the receiver's refusal must name the missing declaration: %s", res.out)
	}
}

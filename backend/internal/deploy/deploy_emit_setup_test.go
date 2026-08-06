package deploy

// emit-setup lays the first-time SETUP product into the built artifact so the production receiver can
// INSTALL it rather than generate anything of its own. A uniform service gets the GENERATED unit/route
// from the shared template at the resolved port; the self repo (devlab) is the one exception in SHAPE —
// its daemon is named devlabd and its unit is a CHECKED-IN template (deploy/devlabd.service) whose
// loopback port is fixed, so the route must take its port FROM the unit, not from the argument. These
// tests pin both shapes; the receiver tests above pin that the emitted product is then installed.

import (
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"testing"
)

// emitSetupTree builds a workspace root owned by the current user (devlab-exec refuses any other) with a
// checkout <repo>/.mercury-artifact already "built", and returns the env + the working-tree path.
func emitSetupTree(t *testing.T, repo string) (env map[string]string, wt string) {
	t.Helper()
	u, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	state := t.TempDir()
	wt = filepath.Join(state, "workspaces", u.Username, repo)
	if err := os.MkdirAll(filepath.Join(wt, ".mercury-artifact"), 0o755); err != nil {
		t.Fatal(err)
	}
	return map[string]string{"DEVLAB_STATE_DIR": state}, wt
}

// A uniform service: the unit and route are GENERATED from the shared template at the argument port, and
// the unit lives under the repo name (svc-a.service) pointing at the template daemon layout.
func TestEmitSetupUniformServiceGeneratesTemplate(t *testing.T) {
	env, wt := emitSetupTree(t, "svc-a")
	res := runWrapper(t, "deploy/devlab-exec", env, "emit-setup", wt, "svc-a", "8811")
	if res.exit != 0 {
		t.Fatalf("emit-setup for a uniform service must succeed, got %d\n%s", res.exit, res.out)
	}
	setup := filepath.Join(wt, ".mercury-artifact", "setup")
	unit := readFileT(t, filepath.Join(setup, "svc-a.service"))
	if !strings.Contains(unit, "User=svc-a") || !strings.Contains(unit, "/opt/svc-a/bin/svc-ad --listen 127.0.0.1:8811") {
		t.Errorf("the generated unit must carry the template layout at the resolved port:\n%s", unit)
	}
	route := readFileT(t, filepath.Join(setup, "svc-a.caddy"))
	if !strings.Contains(route, "127.0.0.1:8811") {
		t.Errorf("the generated route must proxy the resolved port:\n%s", route)
	}
	// No self unit was staged under devlabd.service — a uniform service never carries its own unit.
	if _, err := os.Stat(filepath.Join(setup, "svc-ad.service")); err == nil {
		t.Errorf("a uniform service must not emit a daemon-named unit")
	}
}

// The self repo (devlab): the CHECKED-IN deploy/devlabd.service is emitted under its real name, and the
// route takes its port FROM that unit (8781), ignoring the argument port (an atlas proposal irrelevant
// to a daemon whose port is fixed) — so unit and route can never disagree.
func TestEmitSetupSelfRepoShipsCheckedInUnit(t *testing.T) {
	env, wt := emitSetupTree(t, "devlab")
	// The checked-in unit shape the self repo carries: a fixed loopback address.
	if err := os.MkdirAll(filepath.Join(wt, "deploy"), 0o755); err != nil {
		t.Fatal(err)
	}
	unitSrc := "[Unit]\nDescription=DevLab backend (devlabd)\n\n[Service]\nUser=devlab\n" +
		"Environment=DEVLAB_ADDR=127.0.0.1:8781\nExecStart=/usr/local/bin/devlabd\n\n[Install]\nWantedBy=multi-user.target\n"
	if err := os.WriteFile(filepath.Join(wt, "deploy", "devlabd.service"), []byte(unitSrc), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(wt, "permissions"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wt, "permissions", "devlab.json"), []byte(`{"group":"hp_devlab_access"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	// The argument port is deliberately NOT 8781 — the self route must still bind the unit's port.
	res := runWrapper(t, "deploy/devlab-exec", env, "emit-setup", wt, "devlab", "9999")
	if res.exit != 0 {
		t.Fatalf("emit-setup for the self repo must succeed, got %d\n%s", res.exit, res.out)
	}
	setup := filepath.Join(wt, ".mercury-artifact", "setup")
	// The daemon-named unit is emitted verbatim; NO generic devlab.service is produced.
	unit := readFileT(t, filepath.Join(setup, "devlabd.service"))
	if !strings.Contains(unit, "ExecStart=/usr/local/bin/devlabd") || !strings.Contains(unit, "User=devlab") {
		t.Errorf("the self unit must be the checked-in devlabd.service:\n%s", unit)
	}
	if _, err := os.Stat(filepath.Join(setup, "devlab.service")); err == nil {
		t.Errorf("the self repo must not emit a generic devlab.service beside its devlabd.service")
	}
	// The route follows the unit's port (8781), not the argument (9999).
	route := readFileT(t, filepath.Join(setup, "devlab.caddy"))
	if !strings.Contains(route, "127.0.0.1:8781") || strings.Contains(route, "9999") {
		t.Errorf("the self route must bind the unit's fixed port 8781, not the argument port:\n%s", route)
	}
	if !strings.Contains(res.out, "port 8781") {
		t.Errorf("the emit report must name the port taken from the unit: %s", res.out)
	}
	// Rights travel as a copy.
	if _, err := os.Stat(filepath.Join(setup, "devlab.json")); err != nil {
		t.Errorf("the rights manifest must travel into the setup product: %v", err)
	}
}

func readFileT(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read %s: %v", p, err)
	}
	return string(b)
}

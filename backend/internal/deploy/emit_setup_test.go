package deploy

// emit-setup lays the first-time SETUP product beside the built artifact. For a UNIFORM service it
// generates unit + route from the shared templates; for the SELF repo (devlab) it ships devlab's
// CHECKED-IN unit verbatim under its real name devlabd.service plus devlab's own /api/* route on the
// port the unit itself declares. These tests pin the self branch — the half that was missing, so the
// receiver had nothing correct to install for a first-time devlab host.

import (
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"testing"
)

// emitSetupWorkspace stages a per-user workspace the devlab-exec wrapper accepts (a root owned by the
// invoking user) with an already-built artifact, the checked-in unit and the rights manifest, and
// returns the env plus the working-tree path to run emit-setup against.
func emitSetupWorkspace(t *testing.T) (env map[string]string, wt string) {
	t.Helper()
	me, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	state := t.TempDir()
	wt = filepath.Join(state, "workspaces", me.Username, "devlab")
	for _, d := range []string{filepath.Join(wt, "deploy"), filepath.Join(wt, "permissions"), filepath.Join(wt, ".mercury-artifact")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// devlab's real, committed unit — the exact bytes a first-time host must run (User=devlab, its fixed
	// loopback port). emit-setup must ship THIS, not a generated /opt/<repo> template.
	unit, err := os.ReadFile(filepath.Join(repoRoot(t), "deploy", "devlabd.service"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wt, "deploy", "devlabd.service"), unit, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wt, "permissions", "devlab.json"), []byte(`{"group":"hp_devlab_access"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	return map[string]string{
		"DEVLAB_STATE_DIR": state,
		"DEVLAB_SETUP_LIB": filepath.Join(repoRoot(t), "deploy", "devlab-setup-lib.sh"),
	}, wt
}

// The self emit ships the CHECKED-IN devlabd.service verbatim (not a generated template), a devlab
// /api/* route on the unit's OWN fixed port (the atlas-proposed port is ignored for self), and the
// rights manifest — exactly the product the receiver installs on a bare host.
func TestEmitSetupSelfShipsCheckedInUnit(t *testing.T) {
	env, wt := emitSetupWorkspace(t)
	// Pass a deliberately WRONG atlas port to prove the self branch reads the port from the unit itself.
	res := runWrapper(t, "deploy/devlab-exec", env, "emit-setup", wt, "devlab", "9999")
	if res.exit != 0 {
		t.Fatalf("emit-setup for the self repo must succeed (exit 0), got %d\n%s", res.exit, res.out)
	}
	setup := filepath.Join(wt, ".mercury-artifact", "setup")

	got, err := os.ReadFile(filepath.Join(setup, "devlabd.service"))
	if err != nil {
		t.Fatalf("the self setup must contain the unit under its real name devlabd.service: %v", err)
	}
	want, _ := os.ReadFile(filepath.Join(repoRoot(t), "deploy", "devlabd.service"))
	if string(got) != string(want) {
		t.Errorf("the delivered unit must be the checked-in devlabd.service verbatim, got:\n%s", string(got))
	}

	route, err := os.ReadFile(filepath.Join(setup, "devlab.caddy"))
	if err != nil {
		t.Fatalf("the self setup must contain devlab's route: %v", err)
	}
	// devlab's own port (8781, from its committed unit), never the wrong atlas port 9999 we passed.
	if !strings.Contains(string(route), "handle /api/* {") || !strings.Contains(string(route), "127.0.0.1:8781") {
		t.Errorf("the self route must proxy the whole /api/* to devlab's own port :8781, got:\n%s", string(route))
	}
	if strings.Contains(string(route), "9999") {
		t.Errorf("the self route must use the unit's fixed port, not the atlas proposal:\n%s", string(route))
	}

	if _, err := os.Stat(filepath.Join(setup, "devlab.json")); err != nil {
		t.Errorf("the self setup must carry the rights manifest devlab.json: %v", err)
	}
	// It must NOT emit the generic /opt template shape under a devlab.service name.
	if _, err := os.Stat(filepath.Join(setup, "devlab.service")); err == nil {
		t.Errorf("the self setup must ship devlabd.service, not a generated devlab.service")
	}
}

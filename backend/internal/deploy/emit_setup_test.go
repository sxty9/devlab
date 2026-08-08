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

// emitSetupRepoWorkspace stages a per-user workspace for an arbitrary <repo> with an already-built
// artifact. When shipsOwnSetup is set, the artifact carries the repo's OWN divergently-named unit under
// setup/ (as artifact-build would stage it), so emit-setup leaves it and generates nothing.
func emitSetupRepoWorkspace(t *testing.T, repo, unit string, shipsOwnSetup bool) (env map[string]string, wt string) {
	t.Helper()
	me, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	state := t.TempDir()
	wt = filepath.Join(state, "workspaces", me.Username, repo)
	art := filepath.Join(wt, ".mercury-artifact")
	if err := os.MkdirAll(art, 0o755); err != nil {
		t.Fatal(err)
	}
	if shipsOwnSetup {
		setup := filepath.Join(art, "setup")
		if err := os.MkdirAll(setup, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(setup, unit+".service"), []byte(deliveredUnit(repo, repo)), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return map[string]string{
		"DEVLAB_STATE_DIR": state,
		"DEVLAB_SETUP_LIB": filepath.Join(repoRoot(t), "deploy", "devlab-setup-lib.sh"),
	}, wt
}

// A RESERVED-named service that SHIPS ITS OWN setup product (holistic → holistic-dashboard.service) must
// NOT be refused at emit-setup: the build only PACKAGES the repo-provided unit, which NAMES its own
// identity; the root installer alone decides ownership on the host. Refusing here trapped the landscape's
// own dashboard out of every production build (the defect this run removes).
func TestEmitSetupReservedNameShippingOwnSetupIsPackaged(t *testing.T) {
	env, wt := emitSetupRepoWorkspace(t, "holistic", "holistic-dashboard", true)
	res := runWrapper(t, "deploy/devlab-exec", env, "emit-setup", wt, "holistic", "8811")
	if res.exit != 0 {
		t.Fatalf("emit-setup of a reserved name that ships its own setup must succeed (exit 0), got %d\n%s", res.exit, res.out)
	}
	if strings.Contains(res.out, "refusing to emit setup") {
		t.Errorf("the shipped setup product must be packaged, not refused as reserved:\n%s", res.out)
	}
	// The repo-provided unit is left exactly as staged; nothing is generated over it.
	if _, err := os.Stat(filepath.Join(wt, ".mercury-artifact", "setup", "holistic-dashboard.service")); err != nil {
		t.Errorf("the repo-provided unit must be left in place: %v", err)
	}
	if _, err := os.Stat(filepath.Join(wt, ".mercury-artifact", "setup", "holistic.service")); err == nil {
		t.Errorf("emit-setup must NOT generate a holistic.service over the shipped product")
	}
}

// A RESERVED-named service that ships NO setup product would make emit-setup GENERATE holistic.service
// (User=holistic) from the template — a colliding identity a freshly generated uniform unit can never own.
// That is still refused, at the one place emit-setup coins a name into an identity.
func TestEmitSetupReservedNameGeneratedIsRefused(t *testing.T) {
	env, wt := emitSetupRepoWorkspace(t, "holistic", "holistic", false)
	res := runWrapper(t, "deploy/devlab-exec", env, "emit-setup", wt, "holistic", "8811")
	if res.exit == 0 {
		t.Fatalf("emit-setup that would GENERATE a reserved identity must be refused, got exit 0\n%s", res.out)
	}
	if !strings.Contains(res.out, "reserved") {
		t.Errorf("the refusal must name the reservation:\n%s", res.out)
	}
	if _, err := os.Stat(filepath.Join(wt, ".mercury-artifact", "setup", "holistic.service")); err == nil {
		t.Errorf("no unit may be generated for the refused reserved name")
	}
}

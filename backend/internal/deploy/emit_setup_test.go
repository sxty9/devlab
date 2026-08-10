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

// The self emit ships the CHECKED-IN devlabd.service verbatim (not a generated template) and the rights
// manifest — exactly the product the receiver installs on a bare host.
//
// AND IT EMITS NO EDGE ROUTE AT ALL, because devlab declares itself a root application. That is the point
// of this test. The self branch used to emit a naked `handle /api/*` fragment, and that fragment is what
// collided with the landscape dashboard's identical claim inside one site block, where the alphabet
// decided whose API existed (measured on production 2026-08-09). A root application's site block cannot be
// written here in any case: emit-setup runs in a developer's working tree and cannot know the hostname of
// the TARGET host. It is rendered at install, where the host is visible.
func TestEmitSetupSelfShipsCheckedInUnit(t *testing.T) {
	env, wt := emitSetupWorkspace(t)
	// devlab's own declaration, as it stands in its repository root: a root application, not the dashboard.
	if err := os.WriteFile(filepath.Join(wt, "holistic-service.json"),
		[]byte(`{"edge":{"role":"application","serveRoot":"/var/lib/devlab/www"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
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

	// NO route travels with a root application's package.
	if b, err := os.ReadFile(filepath.Join(setup, "devlab.caddy")); err == nil {
		t.Errorf("a root application must ship no route fragment — its site block is written at install, where the host's hostname is known; got:\n%s", string(b))
	}
	// …but the ROLE does, so the installer reads it rather than deriving it from the repository name.
	role, err := os.ReadFile(filepath.Join(wt, ".mercury-artifact", "edge.role"))
	if err != nil {
		t.Fatalf("the setup product must carry how this delivery is reached: %v", err)
	}
	if strings.TrimSpace(string(role)) != "application" {
		t.Errorf("devlab's declared edge role must travel with its artifact, got %q", strings.TrimSpace(string(role)))
	}

	if _, err := os.Stat(filepath.Join(setup, "devlab.json")); err != nil {
		t.Errorf("the self setup must carry the rights manifest devlab.json: %v", err)
	}
	// It must NOT emit the generic /opt template shape under a devlab.service name.
	if _, err := os.Stat(filepath.Join(setup, "devlab.service")); err == nil {
		t.Errorf("the self setup must ship devlabd.service, not a generated devlab.service")
	}
}

// The self repo's LAYOUT exception and its EDGE ROLE are two different questions, and only the first is
// answered by the repository's name. A repo that IS the self repo but declares no edge role still gets a
// uniform service fragment: the checked-in unit is a layout matter, the route is not.
func TestEmitSetupSelfNameDoesNotDecideTheEdge(t *testing.T) {
	env, wt := emitSetupWorkspace(t) // no holistic-service.json: the package declares nothing
	res := runWrapper(t, "deploy/devlab-exec", env, "emit-setup", wt, "devlab", "9999")
	if res.exit != 0 {
		t.Fatalf("emit-setup must succeed (exit 0), got %d\n%s", res.exit, res.out)
	}
	setup := filepath.Join(wt, ".mercury-artifact", "setup")
	if _, err := os.Stat(filepath.Join(setup, "devlabd.service")); err != nil {
		t.Errorf("the self repo's LAYOUT exception still holds — its checked-in unit is shipped: %v", err)
	}
	route, err := os.ReadFile(filepath.Join(setup, "devlab.caddy"))
	if err != nil {
		t.Fatalf("a package that declares no edge role is a uniform service and gets a fragment: %v", err)
	}
	if !strings.Contains(string(route), "handle /api/services/devlab/*") {
		t.Errorf("the fragment must be the uniform per-service shape, not a claim on the whole /api/*:\n%s", string(route))
	}
	// The unit's own port (8781) still wins over the atlas proposal — that part is unchanged.
	if !strings.Contains(string(route), "127.0.0.1:8781") || strings.Contains(string(route), "9999") {
		t.Errorf("the route must use the port the unit fixes, not the atlas proposal:\n%s", string(route))
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

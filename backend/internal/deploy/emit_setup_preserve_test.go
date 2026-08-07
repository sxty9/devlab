package deploy

// emit-setup must NOT clobber a service's OWN setup product: a service that ships its own unit (staged
// into the artifact by artifact-build) carries its unit under its own name, and emit-setup leaves it
// untouched. A repo WITHOUT its own setup product still gets the generated uniform product as before.

import (
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"testing"
)

// uniformEmitWorkspace stages a per-user workspace for a uniform (non-self) repo with a built artifact.
func uniformEmitWorkspace(t *testing.T, repo string) (env map[string]string, wt, setup string) {
	t.Helper()
	me, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	state := t.TempDir()
	wt = filepath.Join(state, "workspaces", me.Username, repo)
	if err := os.MkdirAll(filepath.Join(wt, ".mercury-artifact"), 0o755); err != nil {
		t.Fatal(err)
	}
	return map[string]string{
		"DEVLAB_STATE_DIR": state,
		"DEVLAB_SETUP_LIB": filepath.Join(repoRoot(t), "deploy", "devlab-setup-lib.sh"),
	}, wt, filepath.Join(wt, ".mercury-artifact", "setup")
}

// A repo-provided setup product (its own divergently named unit) survives emit-setup untouched — no
// generated <repo>.service is written over it.
func TestEmitSetupPreservesRepoProvidedUnit(t *testing.T) {
	env, wt, setup := uniformEmitWorkspace(t, "svc-a")
	if err := os.MkdirAll(setup, 0o755); err != nil {
		t.Fatal(err)
	}
	own := "[Unit]\n[Service]\nUser=svc-a\nExecStart=/opt/svc-a/bin/svc-ad --listen 127.0.0.1:8770\n"
	if err := os.WriteFile(filepath.Join(setup, "svc-dashboard.service"), []byte(own), 0o644); err != nil {
		t.Fatal(err)
	}

	res := runWrapper(t, "deploy/devlab-exec", env, "emit-setup", wt, "svc-a", "8811")
	if res.exit != 0 {
		t.Fatalf("emit-setup over a repo-provided product must succeed, got %d\n%s", res.exit, res.out)
	}
	if !strings.Contains(res.out, "repo-provided") {
		t.Errorf("emit-setup must name that it left the repo-provided product: %s", res.out)
	}
	if got, _ := os.ReadFile(filepath.Join(setup, "svc-dashboard.service")); string(got) != own {
		t.Errorf("the repo-provided unit must survive verbatim, got:\n%s", string(got))
	}
	if _, err := os.Stat(filepath.Join(setup, "svc-a.service")); err == nil {
		t.Errorf("emit-setup must NOT generate svc-a.service over a repo-provided unit")
	}
}

// A repo WITHOUT its own setup product still gets the generated uniform unit under the repo name.
func TestEmitSetupGeneratesWhenNoRepoProduct(t *testing.T) {
	env, wt, setup := uniformEmitWorkspace(t, "svc-a")

	res := runWrapper(t, "deploy/devlab-exec", env, "emit-setup", wt, "svc-a", "8811")
	if res.exit != 0 {
		t.Fatalf("emit-setup must generate a uniform product, got %d\n%s", res.exit, res.out)
	}
	got, err := os.ReadFile(filepath.Join(setup, "svc-a.service"))
	if err != nil {
		t.Fatalf("the generated unit svc-a.service must be present: %v", err)
	}
	if !strings.Contains(string(got), "User=svc-a") || !strings.Contains(string(got), "127.0.0.1:8811") {
		t.Errorf("the generated unit must carry the repo account and the passed port:\n%s", string(got))
	}
}

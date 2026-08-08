package deploy

// The production receiver READS the unit name from the delivered setup product, not from the repo name —
// the same mechanism the dev installer uses, on the second machine. A service whose unit is legitimately
// named otherwise ships setup/<name>.service, and the receiver installs/renews it under THAT name while
// its foreign-unit and User=<repo> guards stay put.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// recvDivergentFixture stages a repo whose delivered unit is named unitName (≠ repo). Everything else
// mirrors recvFixture: the prebuilt <repo>d, the route/rights keyed by the SERVICE, and a temp unit dir.
func recvDivergentFixture(t *testing.T, repo, unitName string) (env map[string]string, unitPath string) {
	t.Helper()
	staging := t.TempDir()
	unitDir := t.TempDir()
	art := filepath.Join(staging, repo)
	setup := filepath.Join(art, "setup")
	if err := os.MkdirAll(setup, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(art, repo+"d"), []byte("bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	unit := "[Unit]\nDescription=x\n\n[Service]\nUser=" + repo +
		"\nEnvironment=HOLISTIC_SECRET_FILE=/etc/holistic/jwt-secret" +
		"\nExecStart=/opt/" + repo + "/bin/" + repo + "d --listen 127.0.0.1:8811\n\n[Install]\nWantedBy=multi-user.target\n"
	if err := os.WriteFile(filepath.Join(setup, unitName+".service"), []byte(unit), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(setup, repo+".caddy"), []byte("handle /api/services/"+repo+"/* {\n}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return map[string]string{"DEVLAB_STAGING": staging, "DEVLAB_UNIT_DIR": unitDir},
		filepath.Join(unitDir, unitName+".service")
}

// A missing unit under the DELIVERED name → first-time setup that installs the delivered unit under THAT
// name and enables/starts it (never a generated <repo>.service).
func TestRecvDivergentUnitFirstTime(t *testing.T) {
	env, _ := recvDivergentFixture(t, "svc-a", "svc-dashboard")
	res := runRecv(t, env, "", "svc-a")
	if res.exit != 0 {
		t.Fatalf("a missing divergent unit must dry-run a first-time setup (exit 0), got %d\n%s", res.exit, res.out)
	}
	for _, want := range []string{
		"no unit svc-dashboard.service on this host",
		"install delivered unit",
		"svc-dashboard.service",
		"enable svc-dashboard && ",
		"start svc-dashboard",
	} {
		if !strings.Contains(res.out, want) {
			t.Errorf("the first-time plan must mention %q: %s", want, res.out)
		}
	}
	if strings.Contains(res.out, "svc-a.service") {
		t.Errorf("nothing must key on the repo-name unit svc-a.service: %s", res.out)
	}
}

// The service's OWN divergent unit already present → UPDATE (restart the delivered name), not a duplicate.
func TestRecvDivergentUnitUpdate(t *testing.T) {
	env, unitPath := recvDivergentFixture(t, "svc-a", "svc-dashboard")
	res := runRecv(t, env, unitPath, "svc-a") // FragmentPath == our own unit → update
	if res.exit != 0 {
		t.Fatalf("an own divergent unit must dry-run an update (exit 0), got %d\n%s", res.exit, res.out)
	}
	if !strings.Contains(res.out, "restart svc-dashboard") {
		t.Errorf("the update must restart the DELIVERED unit name: %s", res.out)
	}
	if strings.Contains(res.out, "Erstinstallation") || strings.Contains(res.out, "install delivered unit") {
		t.Errorf("an existing own unit must be renewed, not set up afresh: %s", res.out)
	}
}

// A FOREIGN unit under the delivered name → refused (unchanged receiver rule).
func TestRecvDivergentUnitForeignRefused(t *testing.T) {
	env, _ := recvDivergentFixture(t, "svc-a", "svc-dashboard")
	res := runRecv(t, env, "/lib/systemd/system/svc-dashboard.service", "svc-a")
	if res.exit != 5 {
		t.Fatalf("a foreign holder of the delivered unit must die with exit 5, got %d\n%s", res.exit, res.out)
	}
	if !strings.Contains(res.out, "svc-dashboard.service already exists outside this receiver's control") {
		t.Errorf("the refusal must name the foreign delivered unit: %s", res.out)
	}
}

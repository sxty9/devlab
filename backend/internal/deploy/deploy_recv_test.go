package deploy

// The production receiver's FIRST-TIME-vs-UPDATE decision and its setup validations, proved in
// --check mode (no root, no effect) exactly like the install wrapper's namespace tests. Before this,
// the receiver only ever restarted an EXISTING unit, so on a bare production host every non-self
// service failed `systemctl restart: Unit not found`. Now a missing unit triggers a first-time setup
// from the DELIVERED product (unit/route/rights the build laid into the artifact), and these tests
// pin each branch of that decision: missing unit → first-time; present unit → update; a foreign
// vendor unit, a delivered unit that would not run as its own account, and a reserved name → refused.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// recvFixture stages one repo's artifact under a staging root — the prebuilt <repo>d plus the setup/
// product the build attaches (unit, and optionally route/rights). It points DEVLAB_UNIT_DIR at a temp
// dir (so "does the unit exist?" is decided by the test, not the host) and returns the env plus the
// unit path a matching FragmentPath would carry.
func recvFixture(t *testing.T, repo, userLine string, withRoute, withRights bool) (env map[string]string, unitPath string) {
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
	// The delivered unit mirrors setup_unit_text: it declares the shared JWT secret it reads, so the
	// receiver's first-time setup derives and mints that instance secret from the unit itself.
	unit := "[Unit]\nDescription=x\nAfter=network.target\n\n[Service]\n" + userLine +
		"\nEnvironment=HOLISTIC_SECRET_FILE=/etc/holistic/jwt-secret" +
		"\nExecStart=/opt/" + repo + "/bin/" + repo + "d --listen 127.0.0.1:8811\n\n[Install]\nWantedBy=multi-user.target\n"
	if err := os.WriteFile(filepath.Join(setup, repo+".service"), []byte(unit), 0o644); err != nil {
		t.Fatal(err)
	}
	if withRoute {
		if err := os.WriteFile(filepath.Join(setup, repo+".caddy"), []byte("handle /api/services/"+repo+"/* {\n}\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if withRights {
		if err := os.WriteFile(filepath.Join(setup, repo+".json"), []byte(`{"group":"hp_`+strings.ReplaceAll(repo, "-", "_")+`"}`), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return map[string]string{"DEVLAB_STAGING": staging, "DEVLAB_UNIT_DIR": unitDir},
		filepath.Join(unitDir, repo+".service")
}

// runRecv drives the receiver in --check mode against the given systemd FragmentPath answer.
func runRecv(t *testing.T, env map[string]string, fragment, repo string) wrapperResult {
	t.Helper()
	env["DEVLAB_SYSTEMCTL"] = fakeSystemctl(t, fragment)
	return runWrapper(t, "deploy/devlab-deploy-recv", env, "--check", repo)
}

// A missing unit is a first-time setup: the receiver installs the DELIVERED product and creates the
// account — it does not fail like `systemctl restart` on a bare host, and it does not generate a unit
// of its own (it plans installing the shipped one, verified to run as User=<repo>).
func TestRecvCheckFirstTimeWhenUnitMissing(t *testing.T) {
	env, _ := recvFixture(t, "svc-a", "User=svc-a", true, true)
	res := runRecv(t, env, "", "svc-a") // empty FragmentPath — systemd knows no unit
	if res.exit != 0 {
		t.Fatalf("a missing unit must dry-run a first-time setup (exit 0), got %d\n%s", res.exit, res.out)
	}
	for _, want := range []string{"Erstinstallation", "create nologin system account 'svc-a'",
		"install delivered unit", "verified User=svc-a", "install delivered route", "groupadd", "start svc-a",
		// the instance secret the unit demands is planned to be minted on THIS host (BEFUND 1)
		"mint instance secret 'jwt-secret'",
		// the honest gate now proves the service STAYS up, holding its port (BEFUND 2)
		"STAYS up"} {
		if !strings.Contains(res.out, want) {
			t.Errorf("first-time plan must mention %q:\n%s", want, res.out)
		}
	}
}

// An already-installed unit is an UPDATE: replace the program and restart (today's behaviour), never a
// first-time setup — no account creation, no unit write.
func TestRecvCheckUpdateWhenUnitPresent(t *testing.T) {
	env, unitPath := recvFixture(t, "svc-a", "User=svc-a", true, true)
	res := runRecv(t, env, unitPath, "svc-a") // FragmentPath is OUR unit → update
	if res.exit != 0 {
		t.Fatalf("an already-installed unit must dry-run an update (exit 0), got %d\n%s", res.exit, res.out)
	}
	if !strings.Contains(res.out, "Aktualisierung") || !strings.Contains(res.out, "restart svc-a") {
		t.Errorf("an update must be named and plan the restart:\n%s", res.out)
	}
	if strings.Contains(res.out, "Erstinstallation") || strings.Contains(res.out, "install delivered unit") {
		t.Errorf("an update must NOT set up a unit:\n%s", res.out)
	}
}

// A FOREIGN vendor unit (FragmentPath under /lib) is refused — the receiver neither shadows nor
// overwrites a unit that is not its own, exactly as devlab-install refuses one.
func TestRecvCheckRefusesForeignUnit(t *testing.T) {
	env, _ := recvFixture(t, "svc-a", "User=svc-a", false, false)
	res := runRecv(t, env, "/lib/systemd/system/svc-a.service", "svc-a")
	if res.exit != 5 {
		t.Fatalf("a foreign unit must be refused (exit 5), got %d\n%s", res.exit, res.out)
	}
	if !strings.Contains(res.out, "foreign unit") {
		t.Errorf("the refusal must name the foreign unit:\n%s", res.out)
	}
	if strings.Contains(res.out, "PLAN:") {
		t.Errorf("a refused install must plan nothing:\n%s", res.out)
	}
}

// A delivered unit that would NOT run as its own service account (here User=root) is refused — a unit
// that starts something else or runs as root is never installed.
func TestRecvCheckRefusesWrongUser(t *testing.T) {
	env, _ := recvFixture(t, "svc-a", "User=root", false, false)
	res := runRecv(t, env, "", "svc-a")
	if res.exit != 2 {
		t.Fatalf("a delivered unit with the wrong User= must be refused (exit 2), got %d\n%s", res.exit, res.out)
	}
	if !strings.Contains(res.out, "User=svc-a") {
		t.Errorf("the refusal must name the required account:\n%s", res.out)
	}
}

// A reserved name (an OS/package/landscape identity) is refused before any setup — the SAME namespace
// the install wrapper guards, shared through devlab-setup-lib.sh.
func TestRecvCheckRefusesReservedName(t *testing.T) {
	env, _ := recvFixture(t, "root", "User=root", false, false)
	res := runRecv(t, env, "", "root")
	if res.exit != 2 {
		t.Fatalf("a reserved name must be refused (exit 2), got %d\n%s", res.exit, res.out)
	}
	if !strings.Contains(res.out, "name/namespace") && !strings.Contains(res.out, "reserved") {
		t.Errorf("the refusal must name the namespace rule:\n%s", res.out)
	}
}

// A first-time setup with NO delivered setup/ product is a named failure (the build did not attach the
// unit) — never a silent green, and never a fall-through to a restart that would fail on a bare host.
func TestRecvCheckFirstTimeNeedsDeliveredSetup(t *testing.T) {
	env, _ := recvFixture(t, "svc-a", "User=svc-a", false, false)
	// Remove the whole setup/ dir the fixture staged.
	staging := env["DEVLAB_STAGING"]
	if err := os.RemoveAll(filepath.Join(staging, "svc-a", "setup")); err != nil {
		t.Fatal(err)
	}
	res := runRecv(t, env, "", "svc-a")
	if res.exit != 10 {
		t.Fatalf("a first-time setup without a delivered product must fail (exit 10), got %d\n%s", res.exit, res.out)
	}
	if !strings.Contains(res.out, "did not attach the unit") {
		t.Errorf("the failure must name the missing delivered setup:\n%s", res.out)
	}
}

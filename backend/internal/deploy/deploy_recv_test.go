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
	stampBuildKind(t, art, "go-daemon")
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

// recvSelfFixture stages the SELF repo's artifact (devlab): its daemon binary devlabd, its web SPA
// directory, and the setup/ product a self build attaches — the CHECKED-IN devlabd.service (User=devlab,
// its fixed port) plus devlab's own /api/* route and rights manifest. devlab is a layout special case
// (daemon + web, unit named devlabd.service), so it needs its own fixture; the FRAGMENT is still faked
// so the test decides whether devlabd.service already exists. Returns env plus the own unit path.
func recvSelfFixture(t *testing.T, userLine string) (env map[string]string, unitPath string) {
	t.Helper()
	staging := t.TempDir()
	unitDir := t.TempDir()
	art := filepath.Join(staging, "devlab")
	setup := filepath.Join(art, "setup")
	web := filepath.Join(art, "web")
	for _, d := range []string{setup, web} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(art, "devlabd"), []byte("bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(web, "index.html"), []byte("<html>"), 0o644); err != nil {
		t.Fatal(err)
	}
	// The delivered unit is devlab's real unit name (devlabd.service), declares the JWT secret it reads,
	// and binds its fixed loopback port — exactly what emit-setup ships (the checked-in unit verbatim).
	unit := "[Unit]\nDescription=DevLab backend (devlabd)\nAfter=network.target\n\n[Service]\nType=simple\n" +
		userLine + "\nEnvironment=HOLISTIC_SECRET_FILE=/etc/holistic/jwt-secret" +
		"\nEnvironment=DEVLAB_ADDR=127.0.0.1:8781\nExecStart=/usr/local/bin/devlabd\n\n[Install]\nWantedBy=multi-user.target\n"
	if err := os.WriteFile(filepath.Join(setup, "devlabd.service"), []byte(unit), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(setup, "devlab.caddy"), []byte("handle /api/* {\n\treverse_proxy 127.0.0.1:8781\n}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(setup, "devlab.json"), []byte(`{"group":"hp_devlab_access"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	return map[string]string{"DEVLAB_STAGING": staging, "DEVLAB_UNIT_DIR": unitDir},
		filepath.Join(unitDir, "devlabd.service")
}

// TEST a) DECISIVE: a host with NO devlabd.service — the regular delivery of the SELF repo sets devlab up
// completely (account, the delivered unit + /api/* route + rights, the minted JWT secret) and proves it
// running, instead of dying on `systemctl restart: Unit devlabd.service not found` (measured 2026-08-06).
// It reaches the SAME first-time decision the uniform branch makes, only with devlab's own layout.
func TestRecvSelfFirstTimeWhenUnitMissing(t *testing.T) {
	env, _ := recvSelfFixture(t, "User=devlab")
	res := runRecv(t, env, "", "devlab") // empty FragmentPath — systemd knows no devlabd unit
	if res.exit != 0 {
		t.Fatalf("a missing devlabd.service must dry-run a first-time setup (exit 0), got %d\n%s", res.exit, res.out)
	}
	for _, want := range []string{
		"Erstinstallation of 'devlab'",
		"create nologin system account 'devlab'",
		"install delivered unit", "verified User=devlab",
		// the web anteil AND the route belong to the setup (task point 4)
		"/usr/local/bin/devlabd", "/var/lib/devlab/www",
		"install delivered route", "devlab.caddy",
		"groupadd",
		// the instance secret the delivered unit demands is minted on THIS host
		"mint instance secret 'jwt-secret'",
		// restarted through the EXISTING self restart, and proven to STAY up holding its fixed port
		"restart devlabd", "STAYS up", "(:8781)"} {
		if !strings.Contains(res.out, want) {
			t.Errorf("first-time self setup plan must mention %q:\n%s", want, res.out)
		}
	}
	// No inline second start path is built (task point 3): the restart is the ONE self restart path, so
	// the plan names it as such and does not add a separate `systemctl start devlabd` step beside it.
	if !strings.Contains(res.out, "no second, inline start path is built") {
		t.Errorf("the self setup must restart through the existing path, not build an inline start:\n%s", res.out)
	}
}

// TEST b) a host with devlabd.service already present is a RENEWAL: replace the program + web and restart
// (today's behaviour), never a first-time setup — no account creation, no unit write, no route write.
func TestRecvSelfUpdateWhenUnitPresent(t *testing.T) {
	env, unitPath := recvSelfFixture(t, "User=devlab")
	res := runRecv(t, env, unitPath, "devlab") // FragmentPath is OUR devlabd.service → renewal
	if res.exit != 0 {
		t.Fatalf("an already-installed devlabd.service must dry-run a renewal (exit 0), got %d\n%s", res.exit, res.out)
	}
	if !strings.Contains(res.out, "Aktualisierung of 'devlab'") || !strings.Contains(res.out, "restart devlabd") {
		t.Errorf("a renewal must be named and plan the restart:\n%s", res.out)
	}
	if strings.Contains(res.out, "Erstinstallation") || strings.Contains(res.out, "install delivered unit") {
		t.Errorf("a renewal must NOT set up a unit:\n%s", res.out)
	}
}

// TEST c) a FOREIGN devlabd.service (one this receiver did not install, e.g. a vendor unit under /lib) is
// refused — the receiver neither shadows nor overwrites it, exactly as it refuses a foreign uniform unit.
func TestRecvSelfRefusesForeignUnit(t *testing.T) {
	env, _ := recvSelfFixture(t, "User=devlab")
	res := runRecv(t, env, "/lib/systemd/system/devlabd.service", "devlab")
	if res.exit != 5 {
		t.Fatalf("a foreign devlabd.service must be refused (exit 5), got %d\n%s", res.exit, res.out)
	}
	if !strings.Contains(res.out, "foreign unit") {
		t.Errorf("the refusal must name the foreign unit:\n%s", res.out)
	}
	if strings.Contains(res.out, "PLAN:") {
		t.Errorf("a refused self install must plan nothing:\n%s", res.out)
	}
}

// A first-time self setup with NO delivered devlabd.service in the artifact is a named failure (the build
// did not attach the unit) — never a silent green, never a fall-through to a restart that would fail.
func TestRecvSelfFirstTimeNeedsDeliveredUnit(t *testing.T) {
	env, _ := recvSelfFixture(t, "User=devlab")
	if err := os.RemoveAll(filepath.Join(env["DEVLAB_STAGING"], "devlab", "setup")); err != nil {
		t.Fatal(err)
	}
	res := runRecv(t, env, "", "devlab")
	if res.exit != 10 {
		t.Fatalf("a first-time self setup without a delivered unit must fail (exit 10), got %d\n%s", res.exit, res.out)
	}
	if !strings.Contains(res.out, "did not attach the unit") {
		t.Errorf("the failure must name the missing delivered setup:\n%s", res.out)
	}
}

// A delivered self unit that would NOT run as User=devlab (here User=root) is refused — a unit that runs
// as root or starts something else is never installed, the same gate the uniform branch applies.
func TestRecvSelfRefusesWrongUser(t *testing.T) {
	env, _ := recvSelfFixture(t, "User=root")
	res := runRecv(t, env, "", "devlab")
	if res.exit != 2 {
		t.Fatalf("a delivered self unit with the wrong User= must be refused (exit 2), got %d\n%s", res.exit, res.out)
	}
	if !strings.Contains(res.out, "User=devlab") {
		t.Errorf("the refusal must name the required account:\n%s", res.out)
	}
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

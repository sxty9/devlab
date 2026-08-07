package deploy

// The dev installer READS the unit name from the delivered setup product instead of computing
// <repo>.service — proved attack-by-case in --check mode (no root, no effect). Each case maps to one
// of the task's four tests: a service under a divergently named unit is RENEWED not duplicated (a);
// a first install takes the name from the package (b); a foreign unit under that name is refused (c);
// a package with no unit keeps the unchanged <repo> derivation (d).

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// withDeliveredUnit stages a setup/ product into the artifact: a unit under unitName that runs as its own
// account (User=<repo>) and declares a loopback port, plus the service-keyed route. This is what a service
// whose unit is legitimately named otherwise ships in its package.
func withDeliveredUnit(t *testing.T, artifact, unitName, repo string) {
	t.Helper()
	setup := filepath.Join(artifact, "setup")
	if err := os.MkdirAll(setup, 0o755); err != nil {
		t.Fatal(err)
	}
	unit := "[Unit]\nDescription=" + repo + "\n[Service]\nUser=" + repo +
		"\nExecStart=/opt/" + repo + "/bin/" + repo + "d --listen 127.0.0.1:8770\n[Install]\nWantedBy=multi-user.target\n"
	if err := os.WriteFile(filepath.Join(setup, unitName+".service"), []byte(unit), 0o644); err != nil {
		t.Fatal(err)
	}
	route := "handle /api/services/" + repo + "/* {\n\treverse_proxy 127.0.0.1:8770\n}\n"
	if err := os.WriteFile(filepath.Join(setup, repo+".caddy"), []byte(route), 0o644); err != nil {
		t.Fatal(err)
	}
}

// (a) THE DECIDING CASE: the service already runs under svc-dashboard.service (its own fragment), and the
// package delivers that unit. The installer asks systemd about the DELIVERED name, finds its own unit, and
// RENEWS it — no first-time, no second unit, no port demanded. This is the renewal the repo-name
// derivation used to miss (it asked about svc-a.service, found none, and set up a duplicate).
func TestInstallDeliveredUnitDivergentNameIsRenewal(t *testing.T) {
	env, artifact := foreignFixture(t, "svc-a")
	withDeliveredUnit(t, artifact, "svc-dashboard", "svc-a")
	env["DEVLAB_SYSTEMCTL"] = fakeSystemctl(t, "/etc/systemd/system/svc-dashboard.service")

	res := runWrapper(t, "deploy/devlab-install", env, "svc-a", artifact, "dev", "--check")
	if res.exit != 0 {
		t.Fatalf("a service under its own divergent unit must renew without --port, got %d\n%s", res.exit, res.out)
	}
	if strings.Contains(res.out, "first-time") || strings.Contains(res.out, "install delivered unit") {
		t.Errorf("an existing own unit must be renewed, not set up afresh: %s", res.out)
	}
	if !strings.Contains(res.out, "restart svc-dashboard") {
		t.Errorf("the renewal must restart the DELIVERED unit name, not svc-a: %s", res.out)
	}
	if strings.Contains(res.out, "restart svc-a\n") || strings.Contains(res.out, "svc-a.service") {
		t.Errorf("nothing must key on the repo-name unit svc-a.service: %s", res.out)
	}
}

// (b) No unit on this host: the FIRST install installs the DELIVERED unit under its own name (no inline
// generation, no --port), and enables/starts that name.
func TestInstallDeliveredUnitFirstTimeTakesPackageName(t *testing.T) {
	env, artifact := foreignFixture(t, "svc-a")
	withDeliveredUnit(t, artifact, "svc-dashboard", "svc-a")
	env["DEVLAB_SYSTEMCTL"] = fakeSystemctl(t, "") // systemd knows no unit → first-time

	res := runWrapper(t, "deploy/devlab-install", env, "svc-a", artifact, "dev", "--check")
	if res.exit != 0 {
		t.Fatalf("a delivered first-time setup must not demand --port, got %d\n%s", res.exit, res.out)
	}
	for _, want := range []string{
		"install delivered unit",
		filepath.Join(artifact, "setup", "svc-dashboard.service"),
		"/etc/systemd/system/svc-dashboard.service",
		"enable svc-dashboard",
	} {
		if !strings.Contains(res.out, want) {
			t.Errorf("the delivered first-time plan must mention %q: %s", want, res.out)
		}
	}
	// It must NOT fall into the inline-generation path (which would write svc-a.service from the template).
	if strings.Contains(res.out, "from the inline template") || strings.Contains(res.out, "needs --port") {
		t.Errorf("a delivered unit must be installed, never generated as svc-a.service: %s", res.out)
	}
}

// (c) A FOREIGN unit already holds the delivered name (a vendor fragment outside this wrapper's dir): the
// receiver's rule is untouched — it is refused, never shadowed or overwritten.
func TestInstallDeliveredUnitForeignHolderRefused(t *testing.T) {
	env, artifact := foreignFixture(t, "svc-a")
	withDeliveredUnit(t, artifact, "svc-dashboard", "svc-a")
	env["DEVLAB_SYSTEMCTL"] = fakeSystemctl(t, "/lib/systemd/system/svc-dashboard.service")

	res := runWrapper(t, "deploy/devlab-install", env, "svc-a", artifact, "dev", "--check")
	if res.exit != 5 {
		t.Fatalf("a foreign holder of the delivered unit must die with exit 5, got %d\n%s", res.exit, res.out)
	}
	if !strings.Contains(res.out, "svc-dashboard.service already exists outside this wrapper's control") {
		t.Errorf("the refusal must name the foreign delivered unit: %s", res.out)
	}
	if strings.Contains(res.out, "PLAN:") {
		t.Errorf("a refusal must precede every planned effect: %s", res.out)
	}
}

// (d) A package that ships NO unit keeps the unchanged behaviour: the name is derived from the repo, and a
// first-time setup generates <repo>.service from the inline template (still demanding --port).
func TestInstallNoDeliveredUnitFallsBackToRepoName(t *testing.T) {
	env, artifact := foreignFixture(t, "svc-a") // no setup/ dir
	env["DEVLAB_SYSTEMCTL"] = fakeSystemctl(t, "")

	// Without --port the fallback first-time setup still refuses (the inline template needs its port).
	res := runWrapper(t, "deploy/devlab-install", env, "svc-a", artifact, "dev", "--check")
	if res.exit != 5 || !strings.Contains(res.out, "needs --port") {
		t.Fatalf("no delivered unit must fall back to the inline template that needs --port, got %d\n%s", res.exit, res.out)
	}

	// With --port it generates svc-a.service exactly as before — the derivation is unchanged.
	res = runWrapper(t, "deploy/devlab-install", env, "svc-a", artifact, "dev", "--check", "--port", "8772")
	if res.exit != 0 {
		t.Fatalf("the unchanged fallback must plan cleanly with --port, got %d\n%s", res.exit, res.out)
	}
	if !strings.Contains(res.out, "/etc/systemd/system/svc-a.service") || !strings.Contains(res.out, "from the inline template") {
		t.Errorf("with no delivered unit the name must derive from the repo (svc-a.service): %s", res.out)
	}
}

// A delivered unit that does NOT run as the service's own account is refused even when its NAME is fine:
// the unit name may diverge from the repo, the account may not (the User=<repo> guard is unchanged).
func TestInstallDeliveredUnitForeignAccountRefused(t *testing.T) {
	env, artifact := foreignFixture(t, "svc-a")
	setup := filepath.Join(artifact, "setup")
	if err := os.MkdirAll(setup, 0o755); err != nil {
		t.Fatal(err)
	}
	// The unit name is fine, but it runs as root — the guard must refuse it.
	unit := "[Unit]\n[Service]\nUser=root\nExecStart=/opt/svc-a/bin/svc-ad --listen 127.0.0.1:8770\n"
	if err := os.WriteFile(filepath.Join(setup, "svc-dashboard.service"), []byte(unit), 0o644); err != nil {
		t.Fatal(err)
	}
	env["DEVLAB_SYSTEMCTL"] = fakeSystemctl(t, "") // first-time

	res := runWrapper(t, "deploy/devlab-install", env, "svc-a", artifact, "dev", "--check")
	if res.exit != 2 || !strings.Contains(res.out, "does not run as 'User=svc-a'") {
		t.Fatalf("a delivered unit not running as its own account must be refused (exit 2), got %d\n%s", res.exit, res.out)
	}
}

// A delivered unit whose NAME is not a safe systemd token is refused before any effect (the lexical gate).
func TestInstallDeliveredUnitInvalidNameRefused(t *testing.T) {
	env, artifact := foreignFixture(t, "svc-a")
	setup := filepath.Join(artifact, "setup")
	if err := os.MkdirAll(setup, 0o755); err != nil {
		t.Fatal(err)
	}
	// A doubled dash is not admissible for a delivered unit name.
	if err := os.WriteFile(filepath.Join(setup, "svc--evil.service"), []byte("[Service]\nUser=svc-a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	env["DEVLAB_SYSTEMCTL"] = fakeSystemctl(t, "")

	res := runWrapper(t, "deploy/devlab-install", env, "svc-a", artifact, "dev", "--check")
	if res.exit != 2 || !strings.Contains(res.out, "invalid delivered unit name") {
		t.Fatalf("an unsafe delivered unit name must be refused (exit 2), got %d\n%s", res.exit, res.out)
	}
	if strings.Contains(res.out, "PLAN:") {
		t.Errorf("a refusal must precede every planned effect: %s", res.out)
	}
}

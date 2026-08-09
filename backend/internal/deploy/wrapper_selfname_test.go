package deploy

// The install wrapper's namespace, part two (repair wave 3): the three defects an audit of the FIRST
// namespace found, each driven in --check mode (no root, no effect).
//
//  1. The reservation list blocked names the ORGANISATION legitimately uses. `mail` is a real service
//     of this landscape (its unit runs `/opt/mail/bin/maild`), and the list made it permanently
//     undeliverable while the origin check — the narrowing that actually asks whose repository this is
//     — never got to run. So the list now holds OS, package and landscape identities only.
//  2. The whole cascade was wrapped in `if [ "$REPO" != "$SELF_REPO" ]`, which exempted the most
//     privileged name of all: any checkout's artifact could be installed as this service's own binary.
//  3. The route step wrote into the SHARED edge configuration and swallowed the reload's failure.
//
// Every test states the change to deploy/devlab-install it would catch.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// selfFixture builds a complete self delivery: <state>/workspaces/<user>/<self>/.mercury-artifact
// carrying the daemon binary, the built web dir AND the declaration of where that face belongs on the
// host (web.root — a package says where its face goes; the installer never derives it from the name).
// origin decides whether the checkout presents one.
func selfFixture(t *testing.T, repo, origin string) (env map[string]string, artifact string) {
	t.Helper()
	state := t.TempDir()
	checkout := filepath.Join(state, "workspaces", "runner", repo)
	artifact = filepath.Join(checkout, ".mercury-artifact")
	if err := os.MkdirAll(filepath.Join(artifact, "web"), 0o755); err != nil {
		t.Fatal(err)
	}
	stampWebRoot(t, artifact, "/var/lib/"+repo+"/www")
	if err := os.WriteFile(filepath.Join(artifact, "devlabd"), []byte("bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if origin != "" {
		writeOrigin(t, checkout, origin, repo)
	}
	return map[string]string{"DEVLAB_STATE_DIR": state, "DEVLAB_GH_OWNER": testOwner}, artifact
}

// ── 1. the reservation list: OS identities are refused, the organisation's own services are not ──

// A service of the organisation whose name collides with an OS account is DELIVERABLE. The account
// judgment belongs to first-time setup (where `useradd` and `User=<repo>` happen); on an update the
// unit is root-owned and already carries its `User=`, so there is nothing to adopt. Reserving the name
// outright, or judging the account on every install, makes an existing service undeliverable forever —
// which is what this instance's `mail` service ran into.
// The first-time half of the same rule — squatting an existing account is still refused — is proven by
// TestInstallCheckRefusesForeignServiceAccount, which derives such an account from this host's own
// passwd database; it is not repeated here.
func TestInstallCheckDeliversAnOrganisationServiceNamedLikeAnOSAccount(t *testing.T) {
	const repo = "mail" // an OS account on every Debian-family host AND a service of this landscape
	env, artifact := foreignFixture(t, repo)
	// The service is already installed: its unit is the one in this wrapper's own unit directory.
	env["DEVLAB_SYSTEMCTL"] = fakeSystemctl(t, "/etc/systemd/system/"+repo+".service")

	res := runWrapper(t, "deploy/devlab-install", env, repo, artifact, "dev", "--check")
	if res.exit != 0 {
		t.Fatalf("%q is a service of the organisation and must be deliverable, got exit %d\n%s", repo, res.exit, res.out)
	}
	if strings.Contains(res.out, "reserved") {
		t.Errorf("%q must not be refused for its spelling: %s", repo, res.out)
	}
	if strings.Contains(res.out, "foreign account") {
		t.Errorf("an update adopts no account — the judgment belongs to first-time setup: %s", res.out)
	}
	if !strings.Contains(res.out, "PLAN: install") || !strings.Contains(res.out, "restart "+repo) {
		t.Errorf("the update must plan install + restart: %s", res.out)
	}
}

// The identities that must stay refused whatever else the list drops: uid 0 and the packaged daemons
// whose unit and account this wrapper would collide with.
func TestInstallCheckStillRefusesOperatingSystemIdentities(t *testing.T) {
	for _, repo := range []string{"root", "sshd", "caddy"} {
		env, artifact := foreignFixture(t, repo)
		env["DEVLAB_SYSTEMCTL"] = fakeSystemctl(t, "")
		res := runWrapper(t, "deploy/devlab-install", env, repo, artifact, "dev", "--check", "--port", "8779")
		if res.exit != 2 {
			t.Errorf("repo %q must die with exit 2, got %d\n%s", repo, res.exit, res.out)
		}
		if !strings.Contains(res.out, "reserved") {
			t.Errorf("repo %q must be refused as a reserved identity: %s", repo, res.out)
		}
		if strings.Contains(res.out, "PLAN:") {
			t.Errorf("repo %q must be refused before any planned effect: %s", repo, res.out)
		}
	}
}

// ── 2. the self repo is measured too ────────────────────────────────────────

// A FOREIGN artifact handed in under the self name is refused. The self install writes
// /usr/local/bin/<unit> and rsyncs the served web root, so an exemption here is the widest hole in the
// wrapper: it would install another repository's binary as this service.
func TestInstallCheckRefusesAForeignArtifactUnderTheSelfName(t *testing.T) {
	// The artifact is the staged artifact of a checkout named `svc-a`, handed in as `devlab`.
	env, artifact := selfFixture(t, "svc-a", testOwner)
	refused(t, env, 2, "does not name the checkout", "devlab", artifact, "dev", "--check", "--handover")
}

// A self checkout cloned from ANOTHER organisation is refused — the origin check applies to the self
// name exactly as it does to a foreign one.
func TestInstallCheckRefusesASelfCheckoutOfAnotherOrganisation(t *testing.T) {
	env, artifact := selfFixture(t, "devlab", "somebody-else")
	refused(t, env, 2, "is not "+testOwner+"/devlab", "devlab", artifact, "dev", "--check", "--handover")
}

// A directory that is not the runner's staged artifact is refused for EVERY name: the workspace root is
// user-writable, so "somewhere under the root" was never enough to be an artifact.
func TestInstallCheckRefusesADirectoryThatIsNotTheStagedArtifact(t *testing.T) {
	env, artifact := selfFixture(t, "devlab", testOwner)
	loose := filepath.Join(filepath.Dir(artifact), "loose")
	if err := os.MkdirAll(filepath.Join(loose, "web"), 0o755); err != nil {
		t.Fatal(err)
	}
	stampWebRoot(t, loose, "/var/lib/devlab/www")
	if err := os.WriteFile(filepath.Join(loose, "devlabd"), []byte("bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	refused(t, env, 2, "is not a staged .mercury-artifact", "devlab", loose, "dev", "--check", "--handover")

	// The staged artifact of the same checkout is accepted.
	res := runWrapper(t, "deploy/devlab-install", env, "devlab", artifact, "dev", "--check", "--handover")
	if res.exit != 0 || !strings.Contains(res.out, "systemd-run") {
		t.Fatalf("the staged self artifact must plan the handover (exit %d): %s", res.exit, res.out)
	}
}

// The self install is also held to the reservation list and to the configured organisation, so an
// unconfigured host fails closed instead of installing whatever it is handed.
func TestInstallCheckSelfFailsClosedWithoutConfiguredOrganisation(t *testing.T) {
	env, artifact := selfFixture(t, "devlab", testOwner)
	delete(env, "DEVLAB_GH_OWNER")
	env["DEVLAB_GH_OWNER_FILE"] = filepath.Join(t.TempDir(), "absent")
	refused(t, env, 5, "no managed organisation configured", "devlab", artifact, "dev", "--check", "--handover")
}

// ── 3. the shared edge configuration ────────────────────────────────────────

// The route step writes into a directory the edge serves EVERY other service from, so it must validate
// the assembled configuration before adopting the route, take its own file back out when validation or
// the reload fails, and never swallow that failure. The real branch needs root, so the guarantee is
// pinned where it is decided: in the wrapper's own text plus the decision it prints in a dry run.
func TestInstallRouteIsValidatedBeforeTheSharedEdgeAdoptsIt(t *testing.T) {
	b, err := os.ReadFile(filepath.Join(repoRoot(t), "deploy/devlab-install"))
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)

	iValidate := strings.Index(src, `"$CADDY_BIN" validate --config "$CADDY_MAIN"`)
	iReload := strings.Index(src, `"$SYSTEMCTL" reload caddy`)
	switch {
	case iValidate < 0:
		t.Error("devlab-install never validates the assembled edge configuration before adopting a route")
	case iReload < 0:
		t.Error("devlab-install never reloads the edge after writing a route")
	case iValidate > iReload:
		t.Error("the edge configuration must be validated BEFORE the reload, not after")
	}
	if strings.Contains(src, `reload caddy 2>/dev/null ||`) || strings.Contains(src, `reload caddy || log`) {
		t.Error("a failed reload of the SHARED edge must be named, never logged away as unavailable")
	}
	// The unwind must take the generated unit with it. A unit left behind makes the NEXT attempt read as
	// an update, which skips the route step entirely and reports success over a service with no route.
	if !strings.Contains(src, `rm -f "$ROUTE_FILE" "$UNIT_FILE"`) {
		t.Error("a failed route step must take both the route and the generated unit back out, or the retry silently becomes an update without a route")
	}
	for _, path := range []string{"validate --config", "reload caddy"} {
		if !strings.Contains(src, path) {
			t.Errorf("the route step no longer performs %q", path)
		}
	}

	// The dry run states the same decision, so an operator reading a plan sees it.
	env, artifact := foreignFixture(t, "svc-a")
	env["DEVLAB_SYSTEMCTL"] = fakeSystemctl(t, "")
	res := runWrapper(t, "deploy/devlab-install", env, "svc-a", artifact, "dev", "--check", "--port", "8779")
	if res.exit != 0 {
		t.Fatalf("a valid first-time check must exit 0, got %d\n%s", res.exit, res.out)
	}
	for _, want := range []string{"validate", "remove", "shared"} {
		if !strings.Contains(res.out, want) {
			t.Errorf("the first-time plan must name the route validation (%q missing): %s", want, res.out)
		}
	}
}

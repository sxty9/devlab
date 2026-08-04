package deploy

// The install wrapper's NAMESPACE, proved attack by attack in --check mode (no root, no effect).
//
// The name grammar ^[a-z][a-z0-9-]{2,30}$ is a shape, not a namespace: `root`, `caddy`, `sshd` and
// `postgres` all satisfy it. The generated unit carries `User=<repo>` and `ExecStart=/opt/<repo>/bin/
// <repo>d`, so a repository named `root` used to make root install a repository's own binary as a
// unit running as uid 0 — and "first time" was decided by the ABSENCE of /etc/systemd/system/
// <repo>.service, which cannot see the vendor unit of an existing system service under /lib.
// Every test below drives one of those paths and requires the refusal to happen before any PLAN.

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// foreignFixture builds a complete, well-formed foreign delivery: the per-user workspace layout
// <state>/workspaces/<user>/<repo>, the prebuilt <repo>d in the artifact and a git config that
// presents the checkout as a clone of testOwner/<repo>. Everything an attacker could forge is
// forged, so what the tests exercise is the namespace itself.
func foreignFixture(t *testing.T, repo string) (env map[string]string, artifact string) {
	t.Helper()
	state := t.TempDir()
	checkout := filepath.Join(state, "workspaces", "runner", repo)
	artifact = filepath.Join(checkout, ".mercury-artifact")
	if err := os.MkdirAll(artifact, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(artifact, repo+"d"), []byte("bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeOrigin(t, checkout, testOwner, repo)
	return map[string]string{"DEVLAB_STATE_DIR": state, "DEVLAB_GH_OWNER": testOwner}, artifact
}

// fakeSystemctl is the systemd seam: it answers the FragmentPath question with the given value and
// succeeds silently for everything else. It is how a vendor unit, a masked unit and an already
// generated unit are simulated on a host that has none of them.
func fakeSystemctl(t *testing.T, fragment string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "systemctl")
	script := "#!/bin/sh\ncase \"$1\" in\n  show) printf '%s\\n' '" + fragment + "' ;;\nesac\nexit 0\n"
	if err := os.WriteFile(p, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

// refused runs the wrapper in --check mode and asserts it died with the expected code, named the
// reason, and planned NOTHING — a refusal after a PLAN line would mean effects had already begun.
func refused(t *testing.T, env map[string]string, wantExit int, wantMsg string, args ...string) {
	t.Helper()
	res := runWrapper(t, "deploy/devlab-install", env, args...)
	if res.exit != wantExit {
		t.Fatalf("exit = %d, want %d\n%s", res.exit, wantExit, res.out)
	}
	if !strings.Contains(res.out, wantMsg) {
		t.Errorf("the refusal must name the reason %q: %s", wantMsg, res.out)
	}
	if strings.Contains(res.out, "PLAN:") {
		t.Errorf("a refusal must precede every planned effect: %s", res.out)
	}
}

// A repository named after a system identity is refused. `root` is the sharp case: the generated
// unit's User= would be uid 0, so the repository's own binary would run as root.
func TestInstallCheckRefusesReservedSystemNames(t *testing.T) {
	for _, repo := range []string{"root", "caddy", "sshd", "postgres", "www-data", "nobody", "systemd-foo"} {
		env, artifact := foreignFixture(t, repo)
		env["DEVLAB_SYSTEMCTL"] = fakeSystemctl(t, "")
		res := runWrapper(t, "deploy/devlab-install", env, repo, artifact, "dev", "--check", "--port", "8772")
		if res.exit != 2 {
			t.Errorf("repo %q must die with exit 2, got %d\n%s", repo, res.exit, res.out)
		}
		if strings.Contains(res.out, "PLAN:") {
			t.Errorf("repo %q must be refused before any planned effect: %s", repo, res.out)
		}
		if !strings.Contains(res.out, "reserved") && !strings.Contains(res.out, "systemd's own namespace") {
			t.Errorf("repo %q: the refusal must name the reservation: %s", repo, res.out)
		}
	}
}

// The <repo> argument must BE the checkout the artifact was built in — never a free-form string that
// merely satisfies the grammar. Otherwise one clone can be installed under any name.
func TestInstallCheckRefusesNameThatIsNotTheCheckout(t *testing.T) {
	env, artifact := foreignFixture(t, "svc-a")
	env["DEVLAB_SYSTEMCTL"] = fakeSystemctl(t, "")
	refused(t, env, 2, "does not name the checkout",
		"other-name", artifact, "dev", "--check", "--port", "8772")
}

// Only repositories of the CONFIGURED organisation are installed: a checkout cloned from somewhere
// else is refused, and so is one whose repository cannot be identified at all.
func TestInstallCheckRefusesForeignOrganisation(t *testing.T) {
	env, artifact := foreignFixture(t, "svc-a")
	env["DEVLAB_SYSTEMCTL"] = fakeSystemctl(t, "")
	checkout := filepath.Dir(artifact)

	writeOrigin(t, checkout, "somebody-else", "svc-a")
	refused(t, env, 2, "not example-org/svc-a", "svc-a", artifact, "dev", "--check", "--port", "8772")

	// A decoy: the first url matches, a second one does not. git resolves a repeated key to the LAST
	// value, so accepting the first would accept a remote nobody configured.
	conf := "[remote \"origin\"]\n\turl = https://github.com/" + testOwner + "/svc-a.git\n" +
		"\turl = https://github.com/somebody-else/svc-a.git\n"
	if err := os.WriteFile(filepath.Join(checkout, ".git", "config"), []byte(conf), 0o644); err != nil {
		t.Fatal(err)
	}
	refused(t, env, 2, "somebody-else", "svc-a", artifact, "dev", "--check", "--port", "8772")

	// No origin at all: the repository cannot be identified, so it is not installed.
	if err := os.WriteFile(filepath.Join(checkout, ".git", "config"), []byte("[core]\n\tbare = false\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	refused(t, env, 2, "no origin remote", "svc-a", artifact, "dev", "--check", "--port", "8772")

	// No git config at all.
	if err := os.RemoveAll(filepath.Join(checkout, ".git")); err != nil {
		t.Fatal(err)
	}
	refused(t, env, 2, "carries no git config", "svc-a", artifact, "dev", "--check", "--port", "8772")
}

// The organisation is root-owned configuration, so an unconfigured host FAILS CLOSED instead of
// installing whatever it is handed.
func TestInstallCheckFailsClosedWithoutConfiguredOrganisation(t *testing.T) {
	env, artifact := foreignFixture(t, "svc-a")
	delete(env, "DEVLAB_GH_OWNER")
	env["DEVLAB_GH_OWNER_FILE"] = filepath.Join(t.TempDir(), "absent")
	env["DEVLAB_SYSTEMCTL"] = fakeSystemctl(t, "")
	refused(t, env, 5, "no managed organisation configured", "svc-a", artifact, "dev", "--check", "--port", "8772")
}

// Unit existence is asked of SYSTEMD, not of a path. A vendor unit lives outside this wrapper's unit
// directory, so the old path test declared "first time" and generated a SECOND unit shadowing a
// running system service — after which `systemctl restart` would start the repository's artifact in
// its place. A masked unit (/dev/null) is refused for the same reason.
func TestInstallCheckRefusesForeignAndMaskedUnits(t *testing.T) {
	for _, frag := range []string{
		"/usr/lib/systemd/system/svc-a.service",
		"/lib/systemd/system/svc-a.service",
		"/run/systemd/system/svc-a.service",
		"/dev/null",
	} {
		env, artifact := foreignFixture(t, "svc-a")
		env["DEVLAB_SYSTEMCTL"] = fakeSystemctl(t, frag)
		res := runWrapper(t, "deploy/devlab-install", env, "svc-a", artifact, "dev", "--check", "--port", "8772")
		if res.exit != 5 {
			t.Errorf("fragment %s must die with exit 5, got %d\n%s", frag, res.exit, res.out)
		}
		if !strings.Contains(res.out, "outside this wrapper's control") {
			t.Errorf("fragment %s: the refusal must name the foreign unit: %s", frag, res.out)
		}
		if strings.Contains(res.out, "PLAN:") {
			t.Errorf("fragment %s must be refused before any planned effect: %s", frag, res.out)
		}
	}
}

// Our OWN generated unit is the one fragment that counts as "already set up": the install proceeds
// without regenerating the unit and without demanding a port.
func TestInstallCheckOwnUnitIsAnUpdateNotASetup(t *testing.T) {
	env, artifact := foreignFixture(t, "svc-a")
	env["DEVLAB_SYSTEMCTL"] = fakeSystemctl(t, "/etc/systemd/system/svc-a.service")

	res := runWrapper(t, "deploy/devlab-install", env, "svc-a", artifact, "dev", "--check")
	if res.exit != 0 {
		t.Fatalf("an already-installed service must update without --port, got %d\n%s", res.exit, res.out)
	}
	if strings.Contains(res.out, "first-time setup") || strings.Contains(res.out, "write /etc/systemd/system") {
		t.Errorf("an existing unit must not be regenerated: %s", res.out)
	}
	if !strings.Contains(res.out, "PLAN: install") || !strings.Contains(res.out, "restart svc-a") {
		t.Errorf("the update must still plan install + restart: %s", res.out)
	}
}

// If systemd cannot be asked, the wrapper fails closed (exit 4) — it never falls back to the path
// test whose blindness is the whole problem.
func TestInstallCheckFailsClosedWhenSystemdCannotBeAsked(t *testing.T) {
	env, artifact := foreignFixture(t, "svc-a")
	env["DEVLAB_SYSTEMCTL"] = filepath.Join(t.TempDir(), "no-such-systemctl")
	refused(t, env, 4, "could not ask systemd", "svc-a", artifact, "dev", "--check", "--port", "8772")
}

// A rights manifest that is a symlink OUT of the checkout is refused: the install would otherwise
// copy an arbitrary root-readable file into the world-readable permissions directory.
func TestInstallCheckRefusesEscapingRightsManifest(t *testing.T) {
	env, artifact := foreignFixture(t, "svc-a")
	env["DEVLAB_SYSTEMCTL"] = fakeSystemctl(t, "")
	checkout := filepath.Dir(artifact)

	outside := filepath.Join(t.TempDir(), "secret.json")
	if err := os.WriteFile(outside, []byte(`{"secret":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(checkout, "permissions"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(checkout, "permissions", "svc-a.json")); err != nil {
		t.Fatal(err)
	}
	refused(t, env, 2, "outside the checkout", "svc-a", artifact, "dev", "--check", "--port", "8772")

	// A regular manifest inside the checkout is taken, and the plan says which file.
	if err := os.Remove(filepath.Join(checkout, "permissions", "svc-a.json")); err != nil {
		t.Fatal(err)
	}
	manifest := filepath.Join(checkout, "permissions", "svc-a.json")
	if err := os.WriteFile(manifest, []byte(`{"service":"svc-a","categories":[]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	res := runWrapper(t, "deploy/devlab-install", env, "svc-a", artifact, "dev", "--check", "--port", "8772")
	if res.exit != 0 || !strings.Contains(res.out, manifest) {
		t.Errorf("an honest manifest must be planned (exit %d): %s", res.exit, res.out)
	}
}

// ─── devlab-exec: the state root is derived, not hardcoded (Befund 6) ────────
//
// The workspace root used to be an absolute literal inside the wrapper, so DEVLAB_STATE_DIR was
// configurable in name only: an instance with a different state root got a daemon writing in one
// place and a wrapper confining against another. The derivation now matches devlab-install's and
// statepath.Workspaces(): <state root>/workspaces/<user>.
func TestExecDerivesWorkspaceRootFromTheStateRoot(t *testing.T) {
	self, err := exec.Command("id", "-un").Output()
	if err != nil {
		t.Skipf("cannot resolve the current user: %v", err)
	}
	user := strings.TrimSpace(string(self))

	state := t.TempDir()
	root := filepath.Join(state, "workspaces", user)
	if err := os.MkdirAll(root, 0o750); err != nil {
		t.Fatal(err)
	}
	env := map[string]string{"DEVLAB_STATE_DIR": state}

	// A write INSIDE the derived root lands — the override is honoured, so the root is genuinely
	// configurable rather than nominally.
	target := filepath.Join(root, "repo", "note.txt")
	cmd := exec.Command("bash", filepath.Join(repoRoot(t), "deploy/devlab-exec"), "write", target)
	cmd.Env = append(os.Environ(), "DEVLAB_STATE_DIR="+state)
	cmd.Stdin = strings.NewReader("hello")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("write inside the derived root must succeed: %v\n%s", err, out)
	}
	if b, err := os.ReadFile(target); err != nil || string(b) != "hello" {
		t.Fatalf("write landed as b=%q err=%v", b, err)
	}

	// A path outside the derived root is refused — the confinement follows the derivation.
	outside := filepath.Join(t.TempDir(), "elsewhere.txt")
	res := runWrapper(t, "deploy/devlab-exec", env, "write", outside)
	if res.exit == 0 || !strings.Contains(res.out, "escapes workspace root") {
		t.Errorf("a path outside the derived root must be refused (exit %d): %s", res.exit, res.out)
	}

	// A symlink out of the root is refused too (the resolution, not the string, decides).
	if err := os.Symlink(filepath.Dir(outside), filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	res = runWrapper(t, "deploy/devlab-exec", env, "write", filepath.Join(root, "link", "x.txt"))
	if res.exit == 0 || !strings.Contains(res.out, "escapes workspace root") {
		t.Errorf("a symlink out of the root must be refused (exit %d): %s", res.exit, res.out)
	}

	// Without a workspace root at the derived location nothing runs at all.
	res = runWrapper(t, "deploy/devlab-exec", map[string]string{"DEVLAB_STATE_DIR": t.TempDir()},
		"write", target)
	if res.exit == 0 || !strings.Contains(res.out, "no workspace root owned by") {
		t.Errorf("a missing workspace root must refuse every verb (exit %d): %s", res.exit, res.out)
	}
}

// EVERY wrapper must derive its state paths the SAME way — ONE definition, mirrored verbatim, and no
// absolute path assigned to a root variable anywhere. A second, drifting derivation is the defect this
// pins (four scripts once carried four definitions of the same root); the pattern below catches it
// whatever literal it drifts to.
//
// The state-root line is owed by all four. The workspaces line is owed by the three that reach into a
// per-user working tree; devlab-deploy-recv is the prod receiver, which has no workspaces and derives
// the staging and web roots from the same state root instead.
func TestWrappersShareOneWorkspaceDerivation(t *testing.T) {
	const (
		stateRoot  = `STATE_DIR="${DEVLAB_STATE_DIR:-`
		workspaces = `WORKSPACES_ROOT="${DEVLAB_WORKSPACES:-$STATE_DIR/workspaces}"`
	)
	scripts := map[string][]string{
		"deploy/devlab-install":     {stateRoot, workspaces},
		"deploy/devlab-exec":        {stateRoot, workspaces},
		"deploy/devlab-mkworkspace": {stateRoot, workspaces},
		"deploy/devlab-deploy-recv": {stateRoot, `STAGING_ROOT="${DEVLAB_STAGING:-$STATE_DIR/staging}"`},
	}
	hardcoded := regexp.MustCompile(`(?m)^[A-Z_]*(ROOT|STATE_DIR|WORKSPACES)[A-Z_]*="/[^"$]*"`)
	for script, want := range scripts {
		b, err := os.ReadFile(filepath.Join(repoRoot(t), script))
		if err != nil {
			t.Fatal(err)
		}
		for _, w := range want {
			if !strings.Contains(string(b), w) {
				t.Errorf("%s does not carry the shared derivation %s…", script, w)
			}
		}
		if hit := hardcoded.FindString(string(b)); hit != "" {
			t.Errorf("%s assigns an absolute path to a root variable (%s) instead of deriving it", script, hit)
		}
	}
}

// reservedRepoNames reads the shared library's reservation list, so this test file holds no second copy
// of that knowledge (a list that drifted would be worse than none). The list moved out of devlab-install
// into deploy/devlab-setup-lib.sh, the ONE source both the dev installer and the prod receiver share.
func reservedRepoNames(t *testing.T) map[string]bool {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(repoRoot(t), "deploy", "devlab-setup-lib.sh"))
	if err != nil {
		t.Fatal(err)
	}
	const marker = `SETUP_RESERVED_REPOS="`
	i := strings.Index(string(b), marker)
	if i < 0 {
		t.Fatal("deploy/devlab-setup-lib.sh carries no SETUP_RESERVED_REPOS list")
	}
	rest := string(b)[i+len(marker):]
	j := strings.Index(rest, `"`)
	if j < 0 {
		t.Fatal("the SETUP_RESERVED_REPOS list is unterminated")
	}
	set := map[string]bool{}
	for _, f := range strings.Fields(rest[:j]) {
		set[f] = true
	}
	if len(set) < 20 {
		t.Fatalf("the reservation list holds only %d names — it is not a namespace guard", len(set))
	}
	return set
}

// An account that already exists but is NOT the one this wrapper creates (system account, nologin,
// home /var/lib/<repo>) is a foreign identity: `User=<repo>` in the generated unit would silently
// borrow it. The candidate is discovered from this host's own passwd database, so the test exercises
// a real, unreserved account rather than a name invented for it.
func TestInstallCheckRefusesForeignServiceAccount(t *testing.T) {
	reserved := reservedRepoNames(t)
	passwd, err := os.ReadFile("/etc/passwd")
	if err != nil {
		t.Skipf("no passwd database to read: %v", err)
	}
	candidate := ""
	for _, line := range strings.Split(string(passwd), "\n") {
		f := strings.Split(line, ":")
		if len(f) < 6 {
			continue
		}
		name, home := f[0], f[5]
		// systemd-* is reserved by prefix, not by list — skip it here for the same reason.
		if reserved[name] || strings.HasPrefix(name, "systemd-") || name == "devlab" || home == "/var/lib/"+name {
			continue
		}
		if len(name) < 3 || len(name) > 31 || strings.Contains(name, "--") || strings.HasSuffix(name, "-") {
			continue
		}
		if strings.TrimLeft(name, "abcdefghijklmnopqrstuvwxyz0123456789-") != "" ||
			strings.TrimLeft(name[:1], "abcdefghijklmnopqrstuvwxyz") != "" {
			continue
		}
		candidate = name
		break
	}
	if candidate == "" {
		t.Skip("this host has no unreserved foreign account matching the repo grammar")
	}

	env, artifact := foreignFixture(t, candidate)
	env["DEVLAB_SYSTEMCTL"] = fakeSystemctl(t, "")
	refused(t, env, 2, "refusing to adopt a foreign account",
		candidate, artifact, "dev", "--check", "--port", "8772")
}

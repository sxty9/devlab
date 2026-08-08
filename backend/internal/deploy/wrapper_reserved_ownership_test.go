package deploy

// The reserved-name lock must tell two cases apart that its spelling alone cannot:
//
//   forbid — a service CLAIMS an identity (an OS account, a package unit, a landscape component) that is
//            not its own;
//   admit  — the service to WHICH that identity already belongs renews itself.
//
// The landscape's own dashboard repository is named `holistic`, which is on the reserved list because a
// NEW service of that name would collide with the landscape's account and unit. But the account `holistic`
// and the unit `holistic-dashboard.service` already exist AND BELONG TO THAT DASHBOARD — so refusing it
// locks the one service the list was never meant to trap out of its own renewal. Ownership is decided from
// the HOST (systemd's unit state) and the delivered package (its unit name and its `User=`), never guessed
// from the repository name. These tests pin the decision on both the dev installer and the prod receiver.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// deliveredUnit renders a unit that declares its own service account and the shared JWT secret it reads,
// mirroring what setup_unit_text / a shipped setup/<unit>.service carries — so the ownership check reads a
// real `User=` line and the secrets plan derives from the unit.
func deliveredUnit(user, repo string) string {
	return "[Unit]\nDescription=x\nAfter=network.target\n\n[Service]\nUser=" + user +
		"\nEnvironment=HOLISTIC_SECRET_FILE=/etc/holistic/jwt-secret" +
		"\nExecStart=/opt/" + repo + "/bin/" + repo + "d --listen 127.0.0.1:8811\n\n[Install]\nWantedBy=multi-user.target\n"
}

// reservedInstallFixture stages a full, well-formed delivery of a RESERVED-named repo whose unit is
// divergently named (like holistic → holistic-dashboard.service): the checkout under the workspace root,
// the prebuilt <repo>d, the shipped setup/<unit>.service (so the installer reads the divergent unit name),
// and an origin under the managed org. It points DEVLAB_UNIT_DIR at a temp dir; whether the on-host unit
// exists there — and what account it runs as — is what the individual tests set, because that is the
// ownership evidence the gate reads. onHostUser=="" leaves the host with NO unit (a first-time setup).
func reservedInstallFixture(t *testing.T, repo, unit, onHostUser string) (env map[string]string, artifact string) {
	t.Helper()
	state := t.TempDir()
	unitDir := t.TempDir()
	checkout := filepath.Join(state, "workspaces", "runner", repo)
	artifact = filepath.Join(checkout, ".mercury-artifact")
	setup := filepath.Join(artifact, "setup")
	if err := os.MkdirAll(setup, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(artifact, repo+"d"), []byte("bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	// The delivered setup names the unit — a divergent name, so UNIT_NAME is read from it, not from <repo>.
	if err := os.WriteFile(filepath.Join(setup, unit+".service"), []byte(deliveredUnit(repo, repo)), 0o644); err != nil {
		t.Fatal(err)
	}
	writeOrigin(t, checkout, testOwner, repo)

	fragment := "" // no on-host unit → systemd knows none → first-time
	if onHostUser != "" {
		onHost := filepath.Join(unitDir, unit+".service")
		if err := os.WriteFile(onHost, []byte(deliveredUnit(onHostUser, repo)), 0o644); err != nil {
			t.Fatal(err)
		}
		fragment = onHost // FragmentPath == our own file → update
	}
	env = map[string]string{
		"DEVLAB_STATE_DIR": state,
		"DEVLAB_GH_OWNER":  testOwner,
		"DEVLAB_UNIT_DIR":  unitDir,
		"DEVLAB_SYSTEMCTL": fakeSystemctl(t, fragment),
	}
	return env, artifact
}

// TEST a) DECISIVE: a reserved-named service that ALREADY OWNS its unit and account — the on-host
// holistic-dashboard.service is ours and runs as User=holistic — is RENEWED, not refused, even though
// `holistic` is on the reserved list. This is the whole point: the identity's owner may renew itself.
func TestInstallReservedNameRenewedWhenOwned(t *testing.T) {
	env, artifact := reservedInstallFixture(t, "holistic", "holistic-dashboard", "holistic")
	res := runWrapper(t, "deploy/devlab-install", env, "holistic", artifact, "dev", "--check")
	if res.exit != 0 {
		t.Fatalf("a reserved name whose unit+account are already ours must renew (exit 0), got %d\n%s", res.exit, res.out)
	}
	if strings.Contains(res.out, "is reserved (operating-system") {
		t.Errorf("the owner renewing itself must NOT be refused as reserved:\n%s", res.out)
	}
	if !strings.Contains(res.out, "renewing itself; admitted") {
		t.Errorf("the admission must be named (ownership lifts the reserved refusal):\n%s", res.out)
	}
	if !strings.Contains(res.out, "restart holistic-dashboard") {
		t.Errorf("the renewal must plan the restart of the divergently-named unit:\n%s", res.out)
	}
}

// TEST b) A reserved name whose on-host unit runs as a DIFFERENT account is NOT the owner: the identity
// belongs to someone else, so it stays refused. Ownership needs BOTH the unit and the account — an update
// alone does not prove it.
func TestInstallReservedNameRefusedWhenAccountIsAnothers(t *testing.T) {
	env, artifact := reservedInstallFixture(t, "holistic", "holistic-dashboard", "someone-else")
	res := runWrapper(t, "deploy/devlab-install", env, "holistic", artifact, "dev", "--check")
	if res.exit != 2 {
		t.Fatalf("a reserved name whose unit runs as another account must be refused (exit 2), got %d\n%s", res.exit, res.out)
	}
	if !strings.Contains(res.out, "is reserved") || !strings.Contains(res.out, "no unit under this name is yet ours") {
		t.Errorf("the refusal must name the reservation and the missing ownership:\n%s", res.out)
	}
	if strings.Contains(res.out, "PLAN:") {
		t.Errorf("a refused install must plan nothing:\n%s", res.out)
	}
}

// TEST c) A reserved name that owns NOTHING — no unit on the host, a first-time setup — is refused, exactly
// as before: a first install may never take a foreign identity.
func TestInstallReservedNameRefusedOnFirstTime(t *testing.T) {
	env, artifact := reservedInstallFixture(t, "holistic", "holistic-dashboard", "")
	res := runWrapper(t, "deploy/devlab-install", env, "holistic", artifact, "dev", "--check")
	if res.exit != 2 {
		t.Fatalf("a first-time install of a reserved name must be refused (exit 2), got %d\n%s", res.exit, res.out)
	}
	if !strings.Contains(res.out, "is reserved") {
		t.Errorf("the refusal must name the reservation:\n%s", res.out)
	}
	if strings.Contains(res.out, "PLAN:") {
		t.Errorf("a refused first-time install must plan nothing:\n%s", res.out)
	}
}

// ─── the production receiver applies the IDENTICAL rule (shared library, no second path) ──────────────

// recvReservedFixture stages a reserved-named repo's artifact for the receiver, with a divergently-named
// delivered unit. onHostUser is the account the on-host unit at DEVLAB_UNIT_DIR runs as (""==no unit → a
// first-time setup). It returns the env plus the FragmentPath systemctl should report.
func recvReservedFixture(t *testing.T, repo, unit, onHostUser string) (env map[string]string, fragment string) {
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
	if err := os.WriteFile(filepath.Join(setup, unit+".service"), []byte(deliveredUnit(repo, repo)), 0o644); err != nil {
		t.Fatal(err)
	}
	if onHostUser != "" {
		onHost := filepath.Join(unitDir, unit+".service")
		if err := os.WriteFile(onHost, []byte(deliveredUnit(onHostUser, repo)), 0o644); err != nil {
			t.Fatal(err)
		}
		fragment = onHost
	}
	return map[string]string{"DEVLAB_STAGING": staging, "DEVLAB_UNIT_DIR": unitDir}, fragment
}

// TEST d-precondition: the receiver RENEWS a reserved-named service that already owns its unit+account,
// so holistic can reach production through the regular path instead of dying on its own name.
func TestRecvReservedNameRenewedWhenOwned(t *testing.T) {
	env, fragment := recvReservedFixture(t, "holistic", "holistic-dashboard", "holistic")
	res := runRecv(t, env, fragment, "holistic")
	if res.exit != 0 {
		t.Fatalf("the receiver must renew a reserved name it already owns (exit 0), got %d\n%s", res.exit, res.out)
	}
	if strings.Contains(res.out, "no unit under this name is yet ours") {
		t.Errorf("the owner renewing itself must NOT be refused as reserved:\n%s", res.out)
	}
	if !strings.Contains(res.out, "renewing itself; admitted") {
		t.Errorf("the admission must be named (ownership lifts the reserved refusal):\n%s", res.out)
	}
	if !strings.Contains(res.out, "Aktualisierung") || !strings.Contains(res.out, "restart holistic-dashboard") {
		t.Errorf("the renewal must be named and plan the restart:\n%s", res.out)
	}
}

// A reserved name the receiver does NOT own (its on-host unit runs as another account) stays refused.
func TestRecvReservedNameRefusedWhenAccountIsAnothers(t *testing.T) {
	env, fragment := recvReservedFixture(t, "holistic", "holistic-dashboard", "someone-else")
	res := runRecv(t, env, fragment, "holistic")
	if res.exit != 2 {
		t.Fatalf("a reserved name the receiver does not own must be refused (exit 2), got %d\n%s", res.exit, res.out)
	}
	if !strings.Contains(res.out, "is reserved") || !strings.Contains(res.out, "no unit under this name is yet ours") {
		t.Errorf("the refusal must name the reservation and the missing ownership:\n%s", res.out)
	}
	if strings.Contains(res.out, "PLAN:") {
		t.Errorf("a refused install must plan nothing:\n%s", res.out)
	}
}

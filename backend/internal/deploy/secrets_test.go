package deploy

// Tests for the instance-secret half of devlab-setup-lib.sh (BEFUND 1): the setup of a host MINTS its
// instance secrets ON the host, derived from what the services' units demand, and NAMES the ones that
// come from outside and cannot be minted. No secret is ever transported between hosts. The library
// functions are exercised directly by sourcing the shared library — the same file devlab-install and
// devlab-deploy-recv source — with the DEVLAB_RECV_TEST seam so the mint runs unprivileged.

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// sourceLib runs a bash snippet with devlab-setup-lib.sh sourced under the test seam. It returns the
// combined output and exit code.
func sourceLib(t *testing.T, env map[string]string, snippet string) wrapperResult {
	t.Helper()
	lib := filepath.Join(repoRoot(t), "deploy", "devlab-setup-lib.sh")
	cmd := exec.Command("bash", "-c", ". "+lib+"\n"+snippet)
	cmd.Env = append(os.Environ(), "DEVLAB_RECV_TEST=1")
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	out, err := cmd.CombinedOutput()
	res := wrapperResult{out: string(out)}
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			res.exit = ee.ExitCode()
		} else {
			t.Fatalf("sourcing the library did not run: %v\n%s", err, out)
		}
	}
	return res
}

// writeUnit writes a systemd unit that references the given /etc/holistic paths (under holisticDir, so
// the derivation is testable off a real /etc) and returns its path.
func writeUnit(t *testing.T, dir, holisticDir string, refs ...string) string {
	t.Helper()
	body := "[Service]\nUser=svc-a\n"
	for _, r := range refs {
		body += "Environment=X=" + filepath.Join(holisticDir, r) + "\n"
	}
	body += "ExecStart=/opt/svc-a/bin/svc-ad --listen 127.0.0.1:8811\n"
	p := filepath.Join(dir, "svc-a.service")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// The secret paths a service demands are DERIVED from its unit — not a hand-kept list. Shared state
// directories (permissions.d, config.d) are excluded; they are host state, not secrets.
func TestSecretsDerivedFromUnit(t *testing.T) {
	dir := t.TempDir()
	unit := writeUnit(t, dir, dir, "jwt-secret", "notify-secret", "ses.env", "permissions.d", "config.d")
	res := sourceLib(t, map[string]string{"DEVLAB_HOLISTIC_DIR": dir},
		`setup_unit_secret_files "`+unit+`"`)
	got := strings.Fields(strings.TrimSpace(res.out))
	want := map[string]bool{
		filepath.Join(dir, "jwt-secret"):    true,
		filepath.Join(dir, "notify-secret"): true,
		filepath.Join(dir, "ses.env"):       true,
	}
	if len(got) != len(want) {
		t.Fatalf("derived %v, want exactly the three secret files (no permissions.d/config.d)", got)
	}
	for _, g := range got {
		if !want[g] {
			t.Errorf("unexpected derived path %q (a directory must not be treated as a secret)", g)
		}
	}
}

// A *-secret is generatable (a random internal token); anything else is an outside credential.
func TestSecretGeneratableRule(t *testing.T) {
	for name, gen := range map[string]bool{
		"jwt-secret": true, "notify-secret": true, "aigentic-internal-secret": true,
		"ses.env": false, "github-oauth.json": false, "link-key": false,
	} {
		res := sourceLib(t, nil, `setup_secret_is_generatable "`+name+`" && echo GEN || echo EXT`)
		got := strings.TrimSpace(res.out)
		if (got == "GEN") != gen {
			t.Errorf("%s: classified %s, want gen=%v", name, got, gen)
		}
	}
}

// A generatable secret is MINTED on the host with real content; an external one is NAMED as missing and
// NOT minted (BEFUND 1 point 3, test d). Kein stummes Ausbleiben.
func TestEnsureSecretsMintsAndNames(t *testing.T) {
	dir := t.TempDir()
	unit := writeUnit(t, dir, dir, "jwt-secret", "ses.env")
	res := sourceLib(t, map[string]string{"DEVLAB_HOLISTIC_DIR": dir},
		`setup_ensure_secrets svc-a "`+unit+`"`)

	// generatable → minted, non-empty
	jwt, err := os.ReadFile(filepath.Join(dir, "jwt-secret"))
	if err != nil {
		t.Fatalf("jwt-secret was not minted: %v\n%s", err, res.out)
	}
	if len(strings.TrimSpace(string(jwt))) < 20 {
		t.Errorf("minted jwt-secret is too short to be a real token: %q", jwt)
	}
	// external → NOT minted, but NAMED
	if _, err := os.Stat(filepath.Join(dir, "ses.env")); !os.IsNotExist(err) {
		t.Errorf("an outside credential must NOT be minted on the host (ses.env should be absent)")
	}
	if !strings.Contains(res.out, "MISSING-SECRET: ses.env") {
		t.Errorf("a missing outside credential must be NAMED, not swallowed:\n%s", res.out)
	}
}

// Each host mints its OWN secret: two independent setups never produce the same token (test c — no
// secret of one host is found on the other, because none is transported and each is minted fresh).
func TestEachHostMintsItsOwnSecret(t *testing.T) {
	mint := func() string {
		dir := t.TempDir()
		unit := writeUnit(t, dir, dir, "jwt-secret")
		sourceLib(t, map[string]string{"DEVLAB_HOLISTIC_DIR": dir}, `setup_ensure_secrets svc-a "`+unit+`"`)
		b, err := os.ReadFile(filepath.Join(dir, "jwt-secret"))
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	a, b := mint(), mint()
	if a == b {
		t.Fatalf("two hosts minted the SAME secret — secrets must be per-host, never shared: %q", a)
	}
}

// A host's own secret is never overwritten: re-running the setup keeps the existing token (so a re-run
// never rotates a live secret, and an already-provisioned host is left as is).
func TestEnsureSecretsIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	unit := writeUnit(t, dir, dir, "jwt-secret")
	path := filepath.Join(dir, "jwt-secret")
	if err := os.WriteFile(path, []byte("PRE-EXISTING"), 0o640); err != nil {
		t.Fatal(err)
	}
	res := sourceLib(t, map[string]string{"DEVLAB_HOLISTIC_DIR": dir}, `setup_ensure_secrets svc-a "`+unit+`"`)
	b, _ := os.ReadFile(path)
	if string(b) != "PRE-EXISTING" {
		t.Errorf("an existing secret must never be overwritten, got %q", b)
	}
	if !strings.Contains(res.out, "already present") {
		t.Errorf("an existing secret should be reported as left as is:\n%s", res.out)
	}
}

// The unit's loopback port is read for the honest gate (BEFUND 2) — from --listen or DEVLAB_ADDR.
func TestUnitListenPort(t *testing.T) {
	dir := t.TempDir()
	unit := filepath.Join(dir, "u.service")
	if err := os.WriteFile(unit, []byte("[Service]\nExecStart=/opt/x/bin/xd --listen 127.0.0.1:8811\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	res := sourceLib(t, nil, `setup_unit_listen_port "`+unit+`"`)
	if strings.TrimSpace(res.out) != "8811" {
		t.Errorf("port = %q, want 8811", res.out)
	}
}

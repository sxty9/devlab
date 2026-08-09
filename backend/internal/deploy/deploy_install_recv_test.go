package deploy

// devlab-install-recv is the operator's one-pass installer for the prod-side receiver — the one root
// artifact the delivery chain cannot deliver to itself (the forced-command deploy key can only rsync
// into staging and trigger an install, never overwrite the receiver). These tests prove its logic in
// its DIRECT-INVOCATION test seam (DEVLAB_RECV_TEST=1, no root, a fixture sbin), the same way the
// receiver's own decisions are proved in --check mode: a fresh install lands both files, a re-run is
// idempotent, an existing copy is backed up before replacement, and a service whose staged setup is
// missing is NEVER triggered (fail-closed, no half-setup).

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// installRecvEnv builds a fixture sbin + staging and returns the env the installer runs against under
// its test seam. When stageOK, a first-time-ready artifact for repo is staged (binary + delivered unit
// running as User=<repo>); otherwise the artifact is staged WITHOUT its setup/ product, which the
// receiver must refuse.
func installRecvEnv(t *testing.T, repo string, stageOK bool) (env map[string]string, sbin string) {
	t.Helper()
	root := t.TempDir()
	sbin = filepath.Join(root, "sbin")
	staging := filepath.Join(root, "staging")
	art := filepath.Join(staging, repo)
	if err := os.MkdirAll(sbin, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(art, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(art, repo+"d"), []byte("bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	stampBuildKind(t, art, "go-daemon")
	if stageOK {
		setup := filepath.Join(art, "setup")
		if err := os.MkdirAll(setup, 0o755); err != nil {
			t.Fatal(err)
		}
		unit := "[Service]\nUser=" + repo + "\nExecStart=/opt/" + repo + "/bin/" + repo + "d --listen 127.0.0.1:8811\n"
		if err := os.WriteFile(filepath.Join(setup, repo+".service"), []byte(unit), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	env = map[string]string{
		"DEVLAB_RECV_TEST": "1",
		"DEVLAB_SBIN":      sbin,
		"DEVLAB_STAGING":   staging,
		"DEVLAB_UNIT_DIR":  filepath.Join(root, "units"), // empty → systemd knows no unit → first-time
		"DEVLAB_SYSTEMCTL": fakeSystemctl(t, ""),         // empty FragmentPath → first-time
	}
	return env, sbin
}

func runInstallRecv(t *testing.T, env map[string]string, args ...string) wrapperResult {
	t.Helper()
	return runWrapper(t, "deploy/devlab-install-recv", env, args...)
}

// A fresh install lands BOTH files (the sourced library and the receiver) into the fixture sbin; with
// no service named it triggers nothing.
func TestInstallRecvFreshInstall(t *testing.T) {
	env, sbin := installRecvEnv(t, "svc-a", true)
	res := runInstallRecv(t, env)
	if res.exit != 0 {
		t.Fatalf("a fresh install must succeed (exit 0), got %d\n%s", res.exit, res.out)
	}
	for _, f := range []string{"devlab-deploy-recv", "devlab-setup-lib.sh"} {
		if _, err := os.Stat(filepath.Join(sbin, f)); err != nil {
			t.Errorf("the installer must land %s: %v", f, err)
		}
	}
	if !strings.Contains(res.out, "nothing was triggered") {
		t.Errorf("with no service named nothing should be triggered:\n%s", res.out)
	}
}

// A second run installs nothing new — an already-current receiver is left untouched, so re-running is
// safe (idempotent).
func TestInstallRecvIdempotent(t *testing.T) {
	env, _ := installRecvEnv(t, "svc-a", true)
	if res := runInstallRecv(t, env); res.exit != 0 {
		t.Fatalf("first install failed: %d\n%s", res.exit, res.out)
	}
	res := runInstallRecv(t, env)
	if res.exit != 0 {
		t.Fatalf("the idempotent re-run must succeed (exit 0), got %d\n%s", res.exit, res.out)
	}
	if !strings.Contains(res.out, "already current") {
		t.Errorf("a re-run must report the receiver as already current:\n%s", res.out)
	}
}

// An existing (older) copy is backed up before it is replaced, so the operator can always step back to
// the receiver that was running before.
func TestInstallRecvBacksUpExisting(t *testing.T) {
	env, sbin := installRecvEnv(t, "svc-a", true)
	old := filepath.Join(sbin, "devlab-deploy-recv")
	if err := os.WriteFile(old, []byte("OLD RECEIVER"), 0o755); err != nil {
		t.Fatal(err)
	}
	res := runInstallRecv(t, env)
	if res.exit != 0 {
		t.Fatalf("install over an existing receiver must succeed, got %d\n%s", res.exit, res.out)
	}
	if !strings.Contains(res.out, "backed up the existing devlab-deploy-recv") {
		t.Errorf("replacing an existing receiver must back it up:\n%s", res.out)
	}
	// The backup carries the OLD bytes.
	entries, _ := filepath.Glob(filepath.Join(sbin, "devlab-deploy-recv.bak-*"))
	if len(entries) == 0 {
		t.Fatalf("no backup file was written:\n%s", res.out)
	}
	if b, _ := os.ReadFile(entries[0]); string(b) != "OLD RECEIVER" {
		t.Errorf("the backup must hold the replaced bytes, got %q", string(b))
	}
}

// A named service whose staged artifact is MISSING its setup/ product is refused by the receiver's
// dry-run and therefore NEVER triggered — the installer fails closed (exit non-zero) rather than
// half-set-up a service, and the receiver is still installed for the others.
func TestInstallRecvFailsClosedOnMissingSetup(t *testing.T) {
	env, sbin := installRecvEnv(t, "svc-a", false) // no setup/ staged
	res := runInstallRecv(t, env, "svc-a")
	if res.exit == 0 {
		t.Fatalf("a service with no delivered setup must not be reported as done (exit != 0):\n%s", res.out)
	}
	if !strings.Contains(res.out, "will NOT be triggered") {
		t.Errorf("the installer must refuse to trigger a service whose dry-run did not pass:\n%s", res.out)
	}
	// The receiver itself was still installed — the fault is the service's, not the receiver's.
	if _, err := os.Stat(filepath.Join(sbin, "devlab-deploy-recv")); err != nil {
		t.Errorf("the receiver must be installed even when a named service is refused: %v", err)
	}
}

// provisionEnv builds a fixture host (sbin + staging/www/caddy/authorized_keys under a temp root) and
// returns the env the --provision path runs against in its test seam. Everything is derived under the
// temp root so no real host path is touched; the caddy validate + sshd checks are skipped by the seam.
func provisionEnv(t *testing.T) (env map[string]string, sbin, staging, www, caddyMain, ak string) {
	t.Helper()
	root := t.TempDir()
	sbin = filepath.Join(root, "sbin")
	staging = filepath.Join(root, "staging")
	// The host's INSTANCE ROOT is not a provisioning input: it is the root application's serve root,
	// derived from the ONE decision in the shared library. The fixture can only move the whole service
	// tree under the temp root; it cannot name a different directory, because no such input exists.
	www = filepath.Join(root, "opt", "holistic", "www")
	caddyMain = filepath.Join(root, "caddy", "Caddyfile")
	ak = filepath.Join(root, "home", ".ssh", "authorized_keys")
	if err := os.MkdirAll(sbin, 0o755); err != nil {
		t.Fatal(err)
	}
	env = map[string]string{
		"DEVLAB_RECV_TEST":    "1",
		"DEVLAB_SBIN":         sbin,
		"DEVLAB_STAGING":      staging,
		"DEVLAB_SERVICE_ROOT": filepath.Join(root, "opt"),
		"DEVLAB_CADDY_CONF":   filepath.Join(root, "caddy", "conf.d"),
		"DEVLAB_CADDY_MAIN":   caddyMain,
		"DEVLAB_RECV_AK":      ak, // test seam: authorized_keys at a fixture, not a real home
		// The ONE declaration of where this environment's edge answers, at a fixture path (never the real
		// /etc/holistic). Seeded here as a PRE-EXISTING declaration (the operator's runtime config), so
		// provision runs that are not ABOUT the edge address read it and proceed; tests that ARE about the
		// declaration clear it and pass --edge-address, or assert the fail-closed absence.
		"DEVLAB_EDGE_ADDRESS_FILE": filepath.Join(root, "holistic", "edge-address"),
	}
	if err := os.MkdirAll(filepath.Join(root, "holistic"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(env["DEVLAB_EDGE_ADDRESS_FILE"], []byte(testEdgeAddress+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return env, sbin, staging, www, caddyMain, ak
}

const testDeployPubKey = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIFIXTURE000000000000000000000000000000 devlab-prod-deploy"

// testEdgeAddress is where this environment's edge answers in the fixtures — a specific overlay socket,
// so a test proves the edge is BUILT ON the declared address (not a baked-in ":80") and the routing
// layer reads the very same value.
const testEdgeAddress = "10.10.0.1:8080"

// A bare host is brought to a production target in ONE pass: rrsync is present, the staging and web
// roots exist, the edge imports the per-service route directory, the deploy key is pinned behind the
// forced command, and the closing self-check passes (proof, not a claim — Part 1.6).
func TestProvisionBareHost(t *testing.T) {
	env, sbin, staging, www, caddyMain, ak := provisionEnv(t)
	res := runInstallRecv(t, env, "--provision", "--deploy-pubkey", testDeployPubKey)
	if res.exit != 0 {
		t.Fatalf("provisioning a bare host must succeed (exit 0), got %d\n%s", res.exit, res.out)
	}
	for _, d := range []string{staging, www} {
		if fi, err := os.Stat(d); err != nil || !fi.IsDir() {
			t.Errorf("provisioning must create the root %s: %v", d, err)
		}
	}
	for _, f := range []string{"devlab-deploy-recv", "devlab-setup-lib.sh"} {
		if _, err := os.Stat(filepath.Join(sbin, f)); err != nil {
			t.Errorf("provisioning must install %s (reusing the receiver install): %v", f, err)
		}
	}
	// The forced command pins EVERY login of this key to the receiver — the line that turns a normal
	// key into a locked-down deploy key.
	akb, err := os.ReadFile(ak)
	if err != nil {
		t.Fatalf("authorized_keys not written: %v", err)
	}
	if !strings.Contains(string(akb), `command="`+filepath.Join(sbin, "devlab-deploy-recv")+`",restrict `) {
		t.Errorf("authorized_keys must carry the forced command + restrict:\n%s", string(akb))
	}
	if !strings.Contains(string(akb), "AAAAC3NzaC1lZDI1NTE5AAAAIFIXTURE") {
		t.Errorf("authorized_keys must carry the deploy public key:\n%s", string(akb))
	}
	// The edge imports the route directory the receiver drops per-service .caddy files into.
	cb, _ := os.ReadFile(caddyMain)
	if !strings.Contains(string(cb), "import "+filepath.Join(env["DEVLAB_CADDY_CONF"], "*.caddy")) {
		t.Errorf("the Caddyfile must import the route directory:\n%s", string(cb))
	}
	if !strings.Contains(res.out, "all self-checks passed") {
		t.Errorf("provisioning must end with a passing self-check:\n%s", res.out)
	}
	if !strings.Contains(res.out, "rejects a shell request") {
		t.Errorf("the self-check must prove the forced command rejects a shell request:\n%s", res.out)
	}
}

// A second provision run changes nothing — the deploy key line is not duplicated, the edge import is
// not re-added, and the receiver is left untouched (idempotent).
func TestProvisionIdempotent(t *testing.T) {
	env, _, _, _, caddyMain, ak := provisionEnv(t)
	if res := runInstallRecv(t, env, "--provision", "--deploy-pubkey", testDeployPubKey); res.exit != 0 {
		t.Fatalf("first provision failed: %d\n%s", res.exit, res.out)
	}
	res := runInstallRecv(t, env, "--provision", "--deploy-pubkey", testDeployPubKey)
	if res.exit != 0 {
		t.Fatalf("the idempotent re-run must succeed, got %d\n%s", res.exit, res.out)
	}
	if !strings.Contains(res.out, "already carries the forced command") {
		t.Errorf("a re-run must report the deploy key already pinned:\n%s", res.out)
	}
	akb, _ := os.ReadFile(ak)
	if n := strings.Count(string(akb), "devlab-prod-deploy"); n != 1 {
		t.Errorf("the deploy key line must appear exactly once after a re-run, got %d:\n%s", n, string(akb))
	}
	cb, _ := os.ReadFile(caddyMain)
	if n := strings.Count(string(cb), "import "); n != 1 {
		t.Errorf("the edge import must appear exactly once after a re-run, got %d:\n%s", n, string(cb))
	}
}

// A PRIVATE key handed to --deploy-pubkey is refused — a private key is never written to the target
// (Geheimnisse entstehen nicht auf dem Ziel). Likewise a missing or malformed key.
func TestProvisionRefusesBadKey(t *testing.T) {
	cases := []struct {
		name, key, want string
		omit            bool
	}{
		{name: "private", key: "-----BEGIN OPENSSH PRIVATE KEY-----\nxxx\n-----END OPENSSH PRIVATE KEY-----", want: "looks like a PRIVATE key"},
		{name: "garbage", key: "hello world", want: "does not look like an ssh public key"},
		{name: "missing", omit: true, want: "needs --deploy-pubkey"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			env, _, _, _, _, _ := provisionEnv(t)
			args := []string{"--provision"}
			if !c.omit {
				args = append(args, "--deploy-pubkey", c.key)
			}
			res := runInstallRecv(t, env, args...)
			if res.exit == 0 {
				t.Fatalf("provisioning must refuse the %s key (exit != 0):\n%s", c.name, res.out)
			}
			if !strings.Contains(res.out, c.want) {
				t.Errorf("the refusal must name %q, got:\n%s", c.want, res.out)
			}
		})
	}
}

// ── the edge shape: the delivered routes must have a site block to live in ──────────────────────────
// The receiver drops each service's route into the route directory as a NAKED `handle` block, valid in
// Caddy only INSIDE a site block. Provisioning must therefore build the edge as a site block that
// imports the route directory from inside it — not as a bare top-level import (the state that made
// every first install fail with "ambiguous site definition" on a freshly built host).

// The built edge is a SITE BLOCK whose body imports the route directory — the import is indented inside
// braces, never a bare top-level directive.
func TestProvisionEdgeWrapsRoutesInSiteBlock(t *testing.T) {
	env, _, _, _, caddyMain, _ := provisionEnv(t)
	if res := runInstallRecv(t, env, "--provision", "--deploy-pubkey", testDeployPubKey); res.exit != 0 {
		t.Fatalf("provisioning must succeed: %d\n%s", res.exit, res.out)
	}
	edge, err := os.ReadFile(caddyMain)
	if err != nil {
		t.Fatalf("Caddyfile not written: %v", err)
	}
	s := string(edge)
	conf := env["DEVLAB_CADDY_CONF"]
	// the import lives on an INDENTED line (inside the site block), never at column zero.
	if !strings.Contains(s, "\n\timport "+conf+"/*.caddy") {
		t.Errorf("the route import must be indented inside a site block, got:\n%s", s)
	}
	// a site block opens and closes, and the static fallback is present (as a grown holistic host has).
	for _, want := range []string{"{", "\n}", "file_server"} {
		if !strings.Contains(s, want) {
			t.Errorf("the edge must be a site block with a file_server fallback (missing %q):\n%s", want, s)
		}
	}
}

// Ubuntu's shipped example Caddyfile (root * /usr/share/caddy) is NOT the holistic edge; provisioning
// replaces it (backing it up) rather than appending an import beside its site block — the appended-import
// state is exactly what failed. This models the freshly built host the bug report measured.
func TestProvisionReplacesUbuntuExample(t *testing.T) {
	env, _, _, _, caddyMain, _ := provisionEnv(t)
	if err := os.MkdirAll(filepath.Dir(caddyMain), 0o755); err != nil {
		t.Fatal(err)
	}
	ubuntu := ":80 {\n\troot * /usr/share/caddy\n\tfile_server\n}\n"
	if err := os.WriteFile(caddyMain, []byte(ubuntu), 0o644); err != nil {
		t.Fatal(err)
	}
	res := runInstallRecv(t, env, "--provision", "--deploy-pubkey", testDeployPubKey)
	if res.exit != 0 {
		t.Fatalf("provisioning over Ubuntu's example must succeed: %d\n%s", res.exit, res.out)
	}
	edge, _ := os.ReadFile(caddyMain)
	if strings.Contains(string(edge), "/usr/share/caddy") {
		t.Errorf("Ubuntu's example must be replaced, not kept:\n%s", string(edge))
	}
	if !strings.Contains(string(edge), "import "+env["DEVLAB_CADDY_CONF"]+"/*.caddy") {
		t.Errorf("the replacement must import the route directory:\n%s", string(edge))
	}
	if !strings.Contains(res.out, "replaced Ubuntu's shipped example") {
		t.Errorf("the replacement must be named:\n%s", res.out)
	}
	if m, _ := filepath.Glob(caddyMain + ".bak-*"); len(m) == 0 {
		t.Errorf("the replaced Ubuntu example must be backed up first:\n%s", res.out)
	}
}

// A grown holistic edge — one that already imports the route directory from inside a site block, as the
// home host does — is left EXACTLY as it is and named, never overwritten (the home host must keep
// running unchanged when its shape is adopted as the source).
func TestProvisionKeepsGrownEdge(t *testing.T) {
	env, _, _, _, caddyMain, _ := provisionEnv(t)
	conf := env["DEVLAB_CADDY_CONF"]
	if err := os.MkdirAll(filepath.Dir(caddyMain), 0o755); err != nil {
		t.Fatal(err)
	}
	grown := "example.test {\n\timport " + conf + "/*.caddy\n\thandle /api/* {\n\t\treverse_proxy 127.0.0.1:9000\n\t}\n\thandle {\n\t\troot * /opt/holistic/www\n\t\tfile_server\n\t}\n}\n"
	if err := os.WriteFile(caddyMain, []byte(grown), 0o644); err != nil {
		t.Fatal(err)
	}
	res := runInstallRecv(t, env, "--provision", "--deploy-pubkey", testDeployPubKey)
	if res.exit != 0 {
		t.Fatalf("provisioning over a grown edge must succeed: %d\n%s", res.exit, res.out)
	}
	edge, _ := os.ReadFile(caddyMain)
	if string(edge) != grown {
		t.Errorf("a grown edge must be left byte-for-byte unchanged, got:\n%s", string(edge))
	}
	if !strings.Contains(res.out, "left untouched (a grown") {
		t.Errorf("keeping a grown edge must be named:\n%s", res.out)
	}
}

// A foreign Caddyfile — one that is neither Ubuntu's example nor a holistic edge and does not import the
// route directory — is NAMED and refused, never destroyed. The operator reconciles it by hand.
func TestProvisionRefusesForeignEdge(t *testing.T) {
	env, _, _, _, caddyMain, _ := provisionEnv(t)
	if err := os.MkdirAll(filepath.Dir(caddyMain), 0o755); err != nil {
		t.Fatal(err)
	}
	foreign := "someones.site {\n\treverse_proxy 127.0.0.1:3000\n}\n"
	if err := os.WriteFile(caddyMain, []byte(foreign), 0o644); err != nil {
		t.Fatal(err)
	}
	res := runInstallRecv(t, env, "--provision", "--deploy-pubkey", testDeployPubKey)
	if res.exit == 0 {
		t.Fatalf("provisioning must refuse a foreign edge (exit != 0):\n%s", res.out)
	}
	if !strings.Contains(res.out, "refusing to overwrite a foreign edge") {
		t.Errorf("the refusal must be named:\n%s", res.out)
	}
	if edge, _ := os.ReadFile(caddyMain); string(edge) != foreign {
		t.Errorf("a foreign edge must be left untouched, got:\n%s", string(edge))
	}
}

// ── the edge address: ONE declaration, read by both the edge and the routing layer ──────────────────
// WHERE THIS ENVIRONMENT'S EDGE ANSWERS was decided in two places that did not agree: the generated edge
// listened on a baked-in ":80" while the routing layer forwarded production's hostnames to :8080, so
// every production request ended as a 502 in front of a face listening elsewhere. These tests pin the
// fix: the address is stated in exactly ONE runtime-config file; --provision BUILDS the edge on it and
// answering "where does this environment answer?" (--print-edge-address, the routing layer's read)
// yields the very same address; and a missing declaration is a NAMED deficiency, never a guessed ":80".

// The edge is BUILT ON the ONE declared address (not a baked-in default): the Caddy site block opens on
// exactly the address the declaration holds, and asking the declaration back yields the same address —
// the two places that used to disagree now read one source.
func TestProvisionEdgeBoundToDeclaredAddress(t *testing.T) {
	env, _, _, _, caddyMain, _ := provisionEnv(t) // seeds the declaration with testEdgeAddress
	res := runInstallRecv(t, env, "--provision", "--deploy-pubkey", testDeployPubKey)
	if res.exit != 0 {
		t.Fatalf("provisioning with a declared edge address must succeed, got %d\n%s", res.exit, res.out)
	}
	cb, _ := os.ReadFile(caddyMain)
	if !strings.Contains(string(cb), testEdgeAddress+" {") {
		t.Errorf("the edge must be a site block on the declared address %q, got:\n%s", testEdgeAddress, string(cb))
	}
	// It must NOT fall back to the old baked-in :80 (the very bug — the edge answering where nothing forwards).
	if strings.Contains(string(cb), ":80 {") {
		t.Errorf("the edge must not carry the baked-in :80 default, got:\n%s", string(cb))
	}
	if !strings.Contains(res.out, "edge answers on the ONE declared address "+testEdgeAddress) {
		t.Errorf("the self-check must prove the edge answers on the declared address:\n%s", res.out)
	}
	// Asking the routing layer where this environment answers yields the SAME address (single source).
	ask := runInstallRecv(t, env, "--print-edge-address")
	if ask.exit != 0 {
		t.Fatalf("--print-edge-address must succeed after provisioning, got %d\n%s", ask.exit, ask.out)
	}
	if got := strings.TrimSpace(ask.out); got != testEdgeAddress {
		t.Errorf("asking where the environment answers must yield the built address %q, got %q", testEdgeAddress, got)
	}
}

// --edge-address WRITES the ONE declaration on a host that has none, and the edge is built on it. This is
// the operator declaring the address once; both the edge and the routing layer then read that one file.
func TestProvisionWritesEdgeDeclarationFromFlag(t *testing.T) {
	env, _, _, _, caddyMain, _ := provisionEnv(t)
	edgeFile := env["DEVLAB_EDGE_ADDRESS_FILE"]
	if err := os.Remove(edgeFile); err != nil { // a bare host: no declaration yet
		t.Fatal(err)
	}
	res := runInstallRecv(t, env, "--provision", "--deploy-pubkey", testDeployPubKey, "--edge-address", ":8080")
	if res.exit != 0 {
		t.Fatalf("provisioning with --edge-address must succeed, got %d\n%s", res.exit, res.out)
	}
	b, err := os.ReadFile(edgeFile)
	if err != nil {
		t.Fatalf("the declaration must be written: %v", err)
	}
	if !strings.Contains(string(b), ":8080") {
		t.Errorf("the declaration must record the address given, got:\n%s", string(b))
	}
	if cb, _ := os.ReadFile(caddyMain); !strings.Contains(string(cb), ":8080 {") {
		t.Errorf("the edge must be built on the address just declared, got:\n%s", string(cb))
	}
	if !strings.Contains(res.out, "declared this environment's edge address as ':8080'") {
		t.Errorf("writing the declaration must be named:\n%s", res.out)
	}
}

// A bare host with NO declaration and NO --edge-address is a NAMED deficiency, fail-closed — never a
// silent fallback to :80. Absence of the setup is a deficiency, not a property (Kein stummes Ausbleiben).
func TestProvisionRefusesUndeclaredEdgeAddress(t *testing.T) {
	env, _, _, _, caddyMain, _ := provisionEnv(t)
	if err := os.Remove(env["DEVLAB_EDGE_ADDRESS_FILE"]); err != nil {
		t.Fatal(err)
	}
	res := runInstallRecv(t, env, "--provision", "--deploy-pubkey", testDeployPubKey)
	if res.exit == 0 {
		t.Fatalf("provisioning without a declared edge address must fail closed (exit != 0):\n%s", res.out)
	}
	if !strings.Contains(res.out, "edge address is not declared") {
		t.Errorf("the deficiency must be named:\n%s", res.out)
	}
	// Fail-closed: no edge is left written on a guessed address.
	if _, err := os.Stat(caddyMain); err == nil {
		if cb, _ := os.ReadFile(caddyMain); strings.Contains(string(cb), ":80 {") {
			t.Errorf("a refused provision must not leave an edge on a guessed :80, got:\n%s", string(cb))
		}
	}
}

// A malformed --edge-address (a bare port, no ':') is refused with its own reason — the routing layer
// forwards to a socket, not a naked number.
func TestProvisionRefusesMalformedEdgeAddress(t *testing.T) {
	env, _, _, _, _, _ := provisionEnv(t)
	if err := os.Remove(env["DEVLAB_EDGE_ADDRESS_FILE"]); err != nil {
		t.Fatal(err)
	}
	res := runInstallRecv(t, env, "--provision", "--deploy-pubkey", testDeployPubKey, "--edge-address", "8080")
	if res.exit == 0 {
		t.Fatalf("a malformed edge address must be refused (exit != 0):\n%s", res.out)
	}
	if !strings.Contains(res.out, "not a valid edge address") {
		t.Errorf("the refusal must name the malformed address:\n%s", res.out)
	}
}

// The query mode with no declaration present is a NAMED non-zero — the routing layer learns the address
// is undeclared rather than being handed a guess.
func TestPrintEdgeAddressUndeclared(t *testing.T) {
	env, _, _, _, _, _ := provisionEnv(t)
	if err := os.Remove(env["DEVLAB_EDGE_ADDRESS_FILE"]); err != nil {
		t.Fatal(err)
	}
	res := runInstallRecv(t, env, "--print-edge-address")
	if res.exit == 0 {
		t.Fatalf("--print-edge-address with no declaration must be non-zero:\n%s", res.out)
	}
	if !strings.Contains(res.out, "not declared") {
		t.Errorf("the undeclared answer must be named:\n%s", res.out)
	}
}

// bashRenderTemplate sources the shared library and echoes ONE template function's output — proving the
// caddy tests below run against the SAME template the receiver and installer use, not a Go copy of it.
func bashRenderTemplate(t *testing.T, call string) string {
	t.Helper()
	lib := filepath.Join(repoRoot(t), "deploy", "devlab-setup-lib.sh")
	out, err := exec.Command("bash", "-c", ". "+lib+" && "+call).CombinedOutput()
	if err != nil {
		t.Fatalf("rendering %q failed: %v\n%s", call, err, out)
	}
	return string(out)
}

// The edge template carries NO baked-in address: it renders the site block on the address it is GIVEN
// (from the ONE declaration) and REFUSES to render at all without one — an invented default is exactly
// the guess that made the edge answer where the routing layer does not forward.
func TestEdgeTemplateRequiresDeclaredAddress(t *testing.T) {
	lib := filepath.Join(repoRoot(t), "deploy", "devlab-setup-lib.sh")
	// Given an address, the site block opens on it.
	got := bashRenderTemplate(t, "setup_edge_caddyfile_text /etc/caddy/conf.d 10.10.0.1:8080")
	if !strings.Contains(got, "10.10.0.1:8080 {") {
		t.Errorf("the edge template must open the site block on the given address, got:\n%s", got)
	}
	// Without an address the template fails (non-zero) and emits nothing usable — no ":80" fallback.
	out, err := exec.Command("bash", "-c", ". "+lib+" && setup_edge_caddyfile_text /etc/caddy/conf.d").CombinedOutput()
	if err == nil {
		t.Fatalf("the edge template must refuse to render without a declared address, got:\n%s", out)
	}
	if strings.Contains(string(out), ":80 {") {
		t.Errorf("the edge template must not emit a :80 site block when no address is given, got:\n%s", out)
	}
	// setup_edge_address reads the declaration back; a missing file yields nothing and a non-zero status.
	f := filepath.Join(t.TempDir(), "edge-address")
	if err := os.WriteFile(f, []byte("# a comment\n\n  10.10.0.1:8080  \n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(bashRenderTemplate(t, "setup_edge_address "+f)); got != "10.10.0.1:8080" {
		t.Errorf("setup_edge_address must read the declared address (trimmed, comments ignored), got %q", got)
	}
	if out, err := exec.Command("bash", "-c", ". "+lib+" && setup_edge_address /nope/edge-address").CombinedOutput(); err == nil {
		t.Errorf("setup_edge_address must fail on a missing declaration, got:\n%s", out)
	}
}

// DECISIVE, against the real caddy: the edge shell the template builds VALIDATES with a delivered route
// present, and the old bare-top-level-import shape does NOT ("ambiguous site definition"). This is the
// bug and its fix, proven at the level caddy actually parses — not a claim on an empty edge.
func TestEdgeShellHoldsADeliveredRouteCaddy(t *testing.T) {
	if _, err := exec.LookPath("caddy"); err != nil {
		t.Skip("caddy not installed — the shape is proven by the seam tests above")
	}
	root := t.TempDir()
	conf := filepath.Join(root, "conf.d")
	www := filepath.Join(root, "www")
	if err := os.MkdirAll(conf, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(www, 0o755); err != nil {
		t.Fatal(err)
	}
	// a delivered route, from the very template the receiver drops into conf.d.
	route := bashRenderTemplate(t, "setup_route_text prizm 18811")
	if err := os.WriteFile(filepath.Join(conf, "prizm.caddy"), []byte(route), 0o644); err != nil {
		t.Fatal(err)
	}
	validate := func(caddyfile string) (string, bool) {
		p := filepath.Join(root, "Caddyfile")
		if err := os.WriteFile(p, []byte(caddyfile), 0o644); err != nil {
			t.Fatal(err)
		}
		out, err := exec.Command("caddy", "validate", "--config", p, "--adapter", "caddyfile").CombinedOutput()
		return string(out), err == nil
	}

	// The FIX: the edge shell from the shared template, WITH the delivered route present, must validate.
	edge := bashRenderTemplate(t, "SETUP_SERVICE_ROOT="+root+"; setup_edge_caddyfile_text "+conf+" :8080")
	if out, ok := validate(edge); !ok {
		t.Fatalf("the holistic edge shell must validate WITH a delivered route present:\n%s", out)
	}

	// The DEFECT: Ubuntu's example plus a bare top-level import beside it (what --provision used to
	// leave) must be rejected — the delivered route cannot live at top level next to a site block.
	broken := ":80 {\n\troot * /usr/share/caddy\n\tfile_server\n}\n\nimport " + conf + "/*.caddy\n"
	if out, ok := validate(broken); ok {
		t.Fatalf("the old bare-top-level-import edge must NOT validate (that was the bug):\n%s", out)
	}
}

// The SELF route (devlab's /api/* block, its layout exception) must live in the SAME shared route
// directory beside a uniform service's /api/services/<repo>/* route and the whole edge must still
// validate — proving devlab's first-time route coexists with every other service's, at the level caddy
// actually parses. This is the "Der Web-Anteil und die Route gehoeren zur Ersteinrichtung" half (task
// point 4): a devlabd that runs but is unreachable is not set up.
func TestSelfRouteCoexistsInEdgeCaddy(t *testing.T) {
	if _, err := exec.LookPath("caddy"); err != nil {
		t.Skip("caddy not installed — the template shape is proven by the seam tests above")
	}
	root := t.TempDir()
	conf := filepath.Join(root, "conf.d")
	www := filepath.Join(root, "www")
	for _, d := range []string{conf, www} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// devlab's OWN route (from the self template) and a uniform service's route, both in the shared dir.
	self := bashRenderTemplate(t, "setup_self_route_text 8781")
	if err := os.WriteFile(filepath.Join(conf, "devlab.caddy"), []byte(self), 0o644); err != nil {
		t.Fatal(err)
	}
	uniform := bashRenderTemplate(t, "setup_route_text prizm 18811")
	if err := os.WriteFile(filepath.Join(conf, "prizm.caddy"), []byte(uniform), 0o644); err != nil {
		t.Fatal(err)
	}
	edge := bashRenderTemplate(t, "SETUP_SERVICE_ROOT="+root+"; setup_edge_caddyfile_text "+conf+" :8080")
	p := filepath.Join(root, "Caddyfile")
	if err := os.WriteFile(p, []byte(edge), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("caddy", "validate", "--config", p, "--adapter", "caddyfile").CombinedOutput(); err != nil {
		t.Fatalf("the edge shell must validate with BOTH the self /api/* route and a uniform service route present:\n%s", out)
	}
	// The self template must target the whole /api/* prefix, not the per-service /api/services path.
	if !strings.Contains(self, "handle /api/* {") {
		t.Errorf("the self route must handle the whole /api/* prefix, got:\n%s", self)
	}
}

// A host provisioned BEFORE the instance root was decided carries the edge of that day: its last block
// serves ONE service's directory, so calling the instance answers with that service. The edge is written
// at provisioning time, so nothing would have collected the correction — the receiver refresh (the hand
// step such a host needs anyway, since the receiver cannot deliver itself) now brings it up to the
// current shell in the SAME run.
func TestReceiverRefreshCatchesTheEdgeUp(t *testing.T) {
	env, _, _, _, caddyMain, _ := provisionEnv(t)
	if err := os.MkdirAll(filepath.Dir(caddyMain), 0o755); err != nil {
		t.Fatal(err)
	}
	// exactly what such a host carries: our own marker, and one service's directory at the instance root
	old := "# Managed by devlab-install-recv — the Holistic edge shell.\n" + testEdgeAddress +
		" {\n\timport " + env["DEVLAB_CADDY_CONF"] + "/*.caddy\n\thandle {\n\t\troot * /var/lib/devlab/www\n\t\tfile_server\n\t}\n}\n"
	if err := os.WriteFile(caddyMain, []byte(old), 0o644); err != nil {
		t.Fatal(err)
	}

	res := runInstallRecv(t, env) // NO --provision: the plain receiver refresh
	if res.exit != 0 {
		t.Fatalf("a receiver refresh must succeed (exit 0), got %d\n%s", res.exit, res.out)
	}
	got, err := os.ReadFile(caddyMain)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(got), "/var/lib/devlab/www") {
		t.Errorf("the refresh must take one service's directory out of the instance root:\n%s", string(got))
	}
	if !strings.Contains(string(got), "root * "+filepath.Join(env["DEVLAB_SERVICE_ROOT"], "holistic", "www")) {
		t.Errorf("the refreshed edge must serve the root application at the instance root:\n%s", string(got))
	}
	if !strings.Contains(string(got), "has no root application") {
		t.Errorf("the refreshed edge must answer honestly while the root application is absent:\n%s", string(got))
	}
	if m, _ := filepath.Glob(caddyMain + ".bak-*"); len(m) == 0 {
		t.Errorf("the replaced edge must be backed up first:\n%s", res.out)
	}
}

// …but a refresh touches ONLY an edge this script wrote. A grown, hand-built edge is left exactly as it
// stands — and NAMED, so the operator learns that this host's instance root is not this script's to fix.
func TestReceiverRefreshLeavesAGrownEdgeAlone(t *testing.T) {
	env, _, _, _, caddyMain, _ := provisionEnv(t)
	if err := os.MkdirAll(filepath.Dir(caddyMain), 0o755); err != nil {
		t.Fatal(err)
	}
	grown := "holistic.local {\n\timport " + env["DEVLAB_CADDY_CONF"] +
		"/*.caddy\n\thandle {\n\t\troot * /opt/holistic/www\n\t\tfile_server\n\t}\n}\n"
	if err := os.WriteFile(caddyMain, []byte(grown), 0o644); err != nil {
		t.Fatal(err)
	}

	res := runInstallRecv(t, env)
	if res.exit != 0 {
		t.Fatalf("a receiver refresh must succeed (exit 0), got %d\n%s", res.exit, res.out)
	}
	got, _ := os.ReadFile(caddyMain)
	if string(got) != grown {
		t.Errorf("a hand-grown edge must be left exactly as it stands, got:\n%s", string(got))
	}
	if !strings.Contains(res.out, "was not written by this script") {
		t.Errorf("leaving it alone must be NAMED, not silent:\n%s", res.out)
	}
}

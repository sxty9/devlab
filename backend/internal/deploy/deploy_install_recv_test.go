package deploy

// devlab-install-recv is the operator's one-pass installer for the prod-side receiver — the one root
// artifact the delivery chain cannot deliver to itself (the forced-command deploy key can only rsync
// into staging and trigger an install, never overwrite the receiver). These tests prove its logic in
// its DIRECT-INVOCATION test seam (DEVLAB_RECV_TEST=1, no root, a fixture sbin), the same way the
// receiver's own decisions are proved in --check mode: a fresh install lands both files, a re-run is
// idempotent, an existing copy is backed up before replacement, and a service whose staged setup is
// missing is NEVER triggered (fail-closed, no half-setup).

import (
	"fmt"
	"net"
	"net/http"
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
		// And WHICH hostname each root application answers to — the other half of the same runtime
		// configuration, at a fixture path for the same reason: a test must never write into the real
		// /etc/holistic/edge/hosts, and a test host must be able to declare names of its own.
		"DEVLAB_EDGE_HOSTS_DIR": filepath.Join(root, "holistic", "edge", "hosts"),
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
	// The edge imports the two SHELVES the receiver drops delivered parts onto: whole site blocks for
	// root applications, naked fragments for uniform services. One flat directory is what made a
	// fragment and a site block indistinguishable.
	cb, _ := os.ReadFile(caddyMain)
	for _, shelf := range []string{"apps", "services"} {
		if !strings.Contains(string(cb), "import "+filepath.Join(env["DEVLAB_CADDY_CONF"], shelf, "*.caddy")) {
			t.Errorf("the Caddyfile must import the %s shelf:\n%s", shelf, string(cb))
		}
	}
	for _, shelf := range []string{"apps", "services"} {
		d := filepath.Join(env["DEVLAB_CADDY_CONF"], shelf)
		if fi, err := os.Stat(d); err != nil || !fi.IsDir() {
			t.Errorf("provisioning must create the %s shelf %s: %v", shelf, d, err)
		}
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
	for _, once := range []string{
		"import " + filepath.Join(env["DEVLAB_CADDY_CONF"], "apps", "*.caddy"),
		"import " + filepath.Join(env["DEVLAB_CADDY_CONF"], "services", "*.caddy"),
	} {
		if n := strings.Count(string(cb), once); n != 1 {
			t.Errorf("%q must appear exactly once after a re-run, got %d:\n%s", once, n, string(cb))
		}
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
// The two kinds of delivered part are of two different SHAPES, and a Caddyfile treats them differently:
// a uniform service's route is a NAKED `handle` fragment, valid only INSIDE a site block, while a root
// application brings a WHOLE site block of its own, valid only at the TOP level. The shell must import
// each where its kind is valid — that is the whole reason there are two shelves rather than one flat
// directory, and one flat directory is what let two applications land in one site block and fight over
// `/api/*` (measured on production 2026-08-09).

// The built edge imports the apps shelf at TOP LEVEL (a site block is only valid there) and the services
// shelf from INSIDE a snippet the dashboard application imports (a naked fragment is only valid there).
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
	// the apps shelf is imported at COLUMN ZERO — a site block may stand nowhere else.
	if !strings.Contains(s, "\nimport "+conf+"/apps/*.caddy") {
		t.Errorf("the apps shelf must be imported at top level, got:\n%s", s)
	}
	// the services shelf is imported INDENTED, inside the snippet an application pulls into its own block.
	if !strings.Contains(s, "\n\timport "+conf+"/services/*.caddy") {
		t.Errorf("the services shelf must be imported from inside a block, got:\n%s", s)
	}
	// The shell defines the three pieces a delivered part is built from, and answers for names nobody
	// claimed. It serves no application of its own — a file_server in the SHELL is what used to make
	// every hostname answer with the same page.
	for _, want := range []string{"(holistic_service_routes) {", "(app_web) {", "(edge_absage) {", "import edge_absage"} {
		if !strings.Contains(s, want) {
			t.Errorf("the edge shell must define %q:\n%s", want, s)
		}
	}
	if strings.Contains(s, "\n\tfile_server") {
		t.Errorf("the shell itself must serve no application (a file_server belongs in an app's own block):\n%s", s)
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
	if !strings.Contains(string(edge), "import "+env["DEVLAB_CADDY_CONF"]+"/apps/*.caddy") {
		t.Errorf("the replacement must import the apps shelf:\n%s", string(edge))
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
	// A grown edge is recognised by the fact that it imports OUR route directory from inside a site block
	// — the flat spelling included, because that is exactly what a host grown before the two shelves
	// existed carries. Recognising it is what keeps it from being mistaken for a foreign edge.
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
	// Two halves of ONE declaration: the shell answers on the declared PORT, and it BINDS the declared
	// address rather than every interface. MEASURED against caddy 2.11.4: without `default_bind` a site
	// block binds *:<port>, which would put a host meant to answer only on its private overlay address
	// onto the public internet, past the tunnel that is supposed to front it.
	host, port, _ := strings.Cut(testEdgeAddress, ":")
	if !strings.Contains(string(cb), "http://:"+port+" {") {
		t.Errorf("the edge must answer on the declared port %q, got:\n%s", port, string(cb))
	}
	if !strings.Contains(string(cb), "default_bind "+host) {
		t.Errorf("the edge must bind the declared address %q and nothing else, got:\n%s", host, string(cb))
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
	// Given an address, the shell answers on its port and binds its host part — the two halves of the one
	// declaration. MEASURED against caddy 2.11.4: without `default_bind` a host-bearing site label binds
	// *:<port>, which would expose an overlay-only edge on every interface.
	got := bashRenderTemplate(t, "setup_edge_caddyfile_text /etc/caddy/conf.d 10.10.0.1:8080")
	if !strings.Contains(got, "http://:8080 {") {
		t.Errorf("the edge shell must answer on the declared port, got:\n%s", got)
	}
	if !strings.Contains(got, "default_bind 10.10.0.1") {
		t.Errorf("the edge shell must bind the declared address and nothing else, got:\n%s", got)
	}
	// `:8080` MEANS every interface, so it gets no bind line — the declaration is honoured, not overruled.
	all := bashRenderTemplate(t, "setup_edge_caddyfile_text /etc/caddy/conf.d :8080")
	if strings.Contains(all, "default_bind") {
		t.Errorf("an address with no host part must not be narrowed to one, got:\n%s", all)
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
	// A delivered service fragment on the services shelf, and the dashboard application that imports that
	// shelf — BOTH are needed. MEASURED against caddy 2.11.4: a fragment on a shelf no application imports
	// is never parsed at all, so an edge with a BROKEN fragment on it still reports "Valid configuration".
	// A validation over the services shelf alone therefore proves nothing.
	mustWrite(t, filepath.Join(conf, "services", "prizm.caddy"), bashRenderTemplate(t, "setup_route_text prizm 18811"))
	mustWrite(t, filepath.Join(conf, "apps", "holistic.caddy"),
		bashRenderTemplate(t, "setup_app_route_text holistic dash.example.test 8080 18770 "+www+" 1"))
	validate := func(caddyfile string) (string, bool) {
		p := filepath.Join(root, "Caddyfile")
		if err := os.WriteFile(p, []byte(caddyfile), 0o644); err != nil {
			t.Fatal(err)
		}
		out, err := exec.Command("caddy", "validate", "--config", p, "--adapter", "caddyfile").CombinedOutput()
		return string(out), err == nil
	}

	// The FIX: the edge shell from the shared template, WITH both delivered kinds present, must validate.
	edge := bashRenderTemplate(t, "SETUP_SERVICE_ROOT="+root+"; setup_edge_caddyfile_text "+conf+" :8080")
	if out, ok := validate(edge); !ok {
		t.Fatalf("the holistic edge shell must validate WITH delivered parts on both shelves:\n%s", out)
	}
	// An EMPTY host validates too — the shelves are globs and a bare host simply has nothing on them.
	empty := t.TempDir()
	for _, d := range []string{"apps", "services"} {
		if err := os.MkdirAll(filepath.Join(empty, "conf.d", d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if out, ok := validate(bashRenderTemplate(t, "setup_edge_caddyfile_text "+filepath.Join(empty, "conf.d")+" :8080")); !ok {
		t.Fatalf("a bare host's edge (both shelves empty) must validate:\n%s", out)
	}

	// The DEFECT: Ubuntu's example plus a bare top-level import beside it (what --provision used to
	// leave) must be rejected — a naked fragment cannot live at top level next to a site block.
	broken := ":80 {\n\troot * /usr/share/caddy\n\tfile_server\n}\n\nimport " + conf + "/services/*.caddy\n"
	if out, ok := validate(broken); ok {
		t.Fatalf("the old bare-top-level-import edge must NOT validate (that was the bug):\n%s", out)
	}
	// AND THE SECOND DEFECT, the one this change is about: two root applications under the SAME hostname
	// are what caddy calls an ambiguous site definition. That is why each must carry a name of its own —
	// and why devlab-install refuses a second dashboard before it ever writes the file.
	mustWrite(t, filepath.Join(conf, "apps", "zzdevlab.caddy"),
		bashRenderTemplate(t, "setup_app_route_text devlab dash.example.test 8080 18781 "+www+" 0"))
	out, ok := validate(edge)
	if ok {
		t.Fatalf("two applications on ONE hostname must not validate:\n%s", out)
	}
	if !strings.Contains(out, "ambiguous site definition") {
		t.Errorf("the collision must be caddy's ambiguous-site verdict, got:\n%s", out)
	}
	// Give the second one its own name and the very same pair validates — the hostname IS the separation.
	mustWrite(t, filepath.Join(conf, "apps", "zzdevlab.caddy"),
		bashRenderTemplate(t, "setup_app_route_text devlab devlab.example.test 8080 18781 "+www+" 0"))
	if out, ok := validate(edge); !ok {
		t.Fatalf("two applications under two hostnames must validate:\n%s", out)
	}
}

// mustWrite writes a file and the directories above it, or fails the test.
func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// THE MEASUREMENT THIS WHOLE CHANGE IS ABOUT, taken through the REAL caddy on a REAL listener: two root
// applications stand behind ONE socket, each owning the whole `/api/*` space, and the caller's HOSTNAME
// decides which one answers.
//
// What was measured on production on 2026-08-09, before this change: three different hostnames all got
// the same page, holistic's own API answered 404 through the edge while answering 200 directly, and
// nobody could log in to holistic because /api/auth/login reached DevLab. One site block accepted every
// name, and two `handle /api/*` fragments lay inside it, where devlab.caddy sorted before holistic.caddy.
//
// Every line below is one of the acceptance conditions, asked of the edge the shared templates build.
func TestTwoRootApplicationsAreToldApartByHostname(t *testing.T) {
	if _, err := exec.LookPath("caddy"); err != nil {
		t.Skip("caddy not installed — the template shape is proven by the seam tests above")
	}
	root := t.TempDir()
	conf := filepath.Join(root, "conf.d")
	dashWWW := filepath.Join(root, "opt", "holistic", "www")
	devlabWWW := filepath.Join(root, "opt", "devlab", "www")
	for _, d := range []string{filepath.Join(conf, "apps"), filepath.Join(conf, "services"), dashWWW, devlabWWW} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	mustWrite(t, filepath.Join(dashWWW, "index.html"), "<!doctype html><title>the dashboard</title>")
	mustWrite(t, filepath.Join(devlabWWW, "index.html"), "<!doctype html><title>devlab</title>")

	// Three upstreams: the dashboard's own API, DevLab's own API, and one uniform service.
	dashAPI := stubUpstream(t, "holistic")
	devlabAPI := stubUpstream(t, "devlab")
	prizmAPI := stubUpstream(t, "prizm")

	port := freePort(t)
	mustWrite(t, filepath.Join(conf, "services", "prizm.caddy"),
		bashRenderTemplate(t, fmt.Sprintf("setup_route_text prizm %d", prizmAPI)))
	mustWrite(t, filepath.Join(conf, "apps", "holistic.caddy"),
		bashRenderTemplate(t, fmt.Sprintf("setup_app_route_text holistic dash.example.test %d %d %s 1", port, dashAPI, dashWWW)))
	mustWrite(t, filepath.Join(conf, "apps", "devlab.caddy"),
		bashRenderTemplate(t, fmt.Sprintf("setup_app_route_text devlab devlab.example.test %d %d %s 0", port, devlabAPI, devlabWWW)))

	edge := bashRenderTemplate(t, fmt.Sprintf("SETUP_SERVICE_ROOT=%s; setup_edge_caddyfile_text %s 127.0.0.1:%d", root, conf, port))
	get := serveEdge(t, edge, port)

	for _, c := range []struct{ what, host, path, wantBody string }{
		// a) the dashboard's OWN api is reachable through the edge — it answered 404 before this change
		{"the dashboard's own API", "dash.example.test", "/api/instance", "holistic:/api/instance"},
		// b) …and its face is at the root of its name
		{"the dashboard's face", "dash.example.test", "/", "the dashboard"},
		// c) …and the uniform services hang under IT, because it carries the dashboard role
		{"a uniform service under the dashboard", "dash.example.test", "/api/services/prizm/x", "prizm:/api/services/prizm/x"},
		// d) DevLab's own API is reachable under DevLab's name — the whole /api/* space is its own
		{"DevLab's own API", "devlab.example.test", "/api/mercury/runs", "devlab:/api/mercury/runs"},
		// e) …and DevLab's face is at the root of DevLab's name
		{"DevLab's face", "devlab.example.test", "/", "devlab"},
		// a deep link is the application's start page, not a 404 (both are single-page applications)
		{"a dashboard deep link", "dash.example.test", "/mercury/todo", "the dashboard"},
	} {
		code, body := get(c.host, c.path)
		if code != http.StatusOK || !strings.Contains(body, c.wantBody) {
			t.Errorf("%s: GET %s with Host %s must answer 200 %q, got %d: %s", c.what, c.path, c.host, c.wantBody, code, body)
		}
	}

	// f) a name nobody claimed gets an honest refusal, not somebody else's page. This is the behaviour
	//    change an operator will notice: a health check on the bare address must send a Host header now.
	code, body := get("unbekannt.test", "/")
	if code != http.StatusNotFound {
		t.Errorf("an unclaimed hostname must be refused, got %d: %s", code, body)
	}
	if !strings.Contains(body, "no root application answering to the name") {
		t.Errorf("the refusal must say what is the case, got: %s", body)
	}

	// AND THE DEFECT ITSELF: prizm is reachable under the DASHBOARD's name and NOT under DevLab's. Under
	// DevLab's name the whole /api/* space belongs to DevLab — which is precisely what could not be true
	// while both lived in one site block.
	if _, body := get("devlab.example.test", "/api/services/prizm/x"); strings.Contains(body, "prizm:") {
		t.Errorf("a uniform service must NOT be reachable under an application that does not carry the dashboard role, got: %s", body)
	}
}

// stubUpstream starts a tiny HTTP server that echoes "<name>:<path>" and returns its port, so a test can
// tell WHICH daemon the edge reached — the whole question when two of them claim the same path space.
func stubUpstream(t *testing.T, name string) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("no free port for the %s stub: %v", name, err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "%s:%s", name, r.URL.Path)
	})}
	go func() { _ = srv.Serve(l) }()
	t.Cleanup(func() { _ = srv.Close() })
	return l.Addr().(*net.TCPAddr).Port
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
		t.Errorf("the refresh must take one service's directory out of the shell:\n%s", string(got))
	}
	// The refreshed shell serves NO application of its own any more: each root application brings its own
	// site block on its own hostname, from the apps shelf. What the shell still does is answer honestly
	// for a name nobody claimed — the bare address included, which is what an old health check hits.
	if !strings.Contains(string(got), "import "+filepath.Join(env["DEVLAB_CADDY_CONF"], "apps", "*.caddy")) {
		t.Errorf("the refreshed edge must import the apps shelf:\n%s", string(got))
	}
	if !strings.Contains(string(got), "has no root application answering to the name") {
		t.Errorf("the refreshed edge must answer honestly for a name nobody claims:\n%s", string(got))
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

// ── the layout migration: the moment the whole change is most dangerous ──────────────────────────────
// The new edge shell imports two shelves and NOTHING ELSE. A host whose delivered routes still lie FLAT
// in the route directory would, the instant that shell is written, stop importing every one of them and
// answer 404 for every service it carries. A half-performed move is worse than either state — so the move
// is CODE that runs in the same pass as the new shell, not a paragraph in a manual (Handarbeit kommt als
// Skript, and this is the part that must never be Handarbeit at all).
//
// The rule it follows, and the reason each case is what it is:
//   - a fragment carrying `handle /api/services/` is a uniform service's route → MOVED, byte for byte;
//   - a fragment carrying a naked `handle /api/*` is one of the two colliding blocks that caused all of
//     this → REMOVED, because it carries neither a hostname nor a serve root and so cannot be turned into
//     the site block that replaces it. Its successor is written at that application's next delivery;
//   - anything else is left where it is and NAMED. It is not ours to move.
//
// Everything it touches is backed up first, so the run's own rollback restores the host exactly.
func TestRefreshMigratesAFlatRouteDirectoryOntoTheShelves(t *testing.T) {
	env, _, _, _, caddyMain, _ := provisionEnv(t)
	conf := env["DEVLAB_CADDY_CONF"]
	if err := os.MkdirAll(filepath.Dir(caddyMain), 0o755); err != nil {
		t.Fatal(err)
	}
	// A host as it stands today: our own older shell, and every delivered route lying flat beside it.
	old := "# Managed by devlab-install-recv — the Holistic edge shell.\n" + testEdgeAddress +
		" {\n\timport " + conf + "/*.caddy\n\thandle {\n\t\troot * /var/lib/devlab/www\n\t\tfile_server\n\t}\n}\n"
	mustWrite(t, caddyMain, old)
	prizm := "handle /api/services/prizm/* {\n\treverse_proxy 127.0.0.1:18811\n}\n"
	presentr := "handle /api/services/presentr/* {\n\treverse_proxy 127.0.0.1:18812\n}\n"
	mustWrite(t, filepath.Join(conf, "prizm.caddy"), prizm)
	mustWrite(t, filepath.Join(conf, "presentr.caddy"), presentr)
	// …and the two blocks that collided: both claim the whole /api/*, and `import` expands alphabetically.
	mustWrite(t, filepath.Join(conf, "devlab.caddy"), "handle /api/* {\n\treverse_proxy 127.0.0.1:8781\n}\n")
	mustWrite(t, filepath.Join(conf, "holistic.caddy"), "handle /api/* {\n\treverse_proxy 127.0.0.1:8770\n}\n")
	// Something that is not ours at all.
	mustWrite(t, filepath.Join(conf, "zz-operators-own.caddy"), "# hand-written by the operator\n")

	res := runInstallRecv(t, env) // NO --provision: the plain receiver refresh, the one-line hand step
	if res.exit != 0 {
		t.Fatalf("the refresh must succeed (exit 0), got %d\n%s", res.exit, res.out)
	}

	// The service routes are on the services shelf, byte for byte, and gone from the flat directory.
	for name, want := range map[string]string{"prizm.caddy": prizm, "presentr.caddy": presentr} {
		got, err := os.ReadFile(filepath.Join(conf, "services", name))
		if err != nil {
			t.Fatalf("%s must be moved onto the services shelf: %v\n%s", name, err, res.out)
		}
		if string(got) != want {
			t.Errorf("%s must be moved UNCHANGED — only its shelf changes, got:\n%s", name, string(got))
		}
		if _, err := os.Stat(filepath.Join(conf, name)); err == nil {
			t.Errorf("%s must not be left behind in the flat directory too — it would be neither imported nor findable", name)
		}
	}
	// The colliding fragments are gone (and backed up), and neither became an app block: a fragment
	// carries no hostname, so there is nothing to turn it into.
	for _, name := range []string{"devlab.caddy", "holistic.caddy"} {
		if _, err := os.Stat(filepath.Join(conf, name)); err == nil {
			t.Errorf("the colliding fragment %s must be removed — it is what made two applications share one site block", name)
		}
		if _, err := os.Stat(filepath.Join(conf, "apps", name)); err == nil {
			t.Errorf("%s must NOT be invented into a site block: a fragment carries no hostname and no serve root", name)
		}
		if m, _ := filepath.Glob(filepath.Join(conf, name+".bak-*")); len(m) == 0 {
			t.Errorf("%s must be backed up before it is removed:\n%s", name, res.out)
		}
	}
	// A file that is not ours is left exactly where it is — and SAID, because it is no longer imported.
	if _, err := os.Stat(filepath.Join(conf, "zz-operators-own.caddy")); err != nil {
		t.Errorf("a file that is not ours must be left untouched: %v", err)
	}
	if !strings.Contains(res.out, "zz-operators-own.caddy") || !strings.Contains(res.out, "not ours to move") {
		t.Errorf("what is left behind must be NAMED, not silently orphaned:\n%s", res.out)
	}
	// What happened is stated in numbers an operator can check against the host.
	for _, want := range []string{"moved 2 delivered service route", "removed 2 colliding root-application fragment"} {
		if !strings.Contains(res.out, want) {
			t.Errorf("the move must report what it did (%q missing):\n%s", want, res.out)
		}
	}
	// And the edge that now stands imports the shelves the routes were moved onto.
	edge, _ := os.ReadFile(caddyMain)
	for _, shelf := range []string{"apps", "services"} {
		if !strings.Contains(string(edge), "import "+filepath.Join(conf, shelf, "*.caddy")) {
			t.Errorf("the new shell must import the %s shelf the migration filled:\n%s", shelf, string(edge))
		}
	}

	// IDEMPOTENT: running it again finds nothing left to move and changes nothing.
	res2 := runInstallRecv(t, env)
	if res2.exit != 0 {
		t.Fatalf("a second refresh must succeed, got %d\n%s", res2.exit, res2.out)
	}
	if strings.Contains(res2.out, "moved 1") || strings.Contains(res2.out, "moved 2") {
		t.Errorf("a host already laid out this way has nothing to move:\n%s", res2.out)
	}
	if got, _ := os.ReadFile(filepath.Join(conf, "services", "prizm.caddy")); string(got) != prizm {
		t.Errorf("a second run must leave the moved route exactly as it stands, got:\n%s", string(got))
	}
}

// The hostnames a host gives its root applications are established by the same one-line hand step, without
// re-provisioning: a host that is already a production target must not have to be provisioned again just
// to be given a name. And an unusable value is refused rather than written.
func TestEdgeHostFlagDeclaresNamesOnTheHost(t *testing.T) {
	env, _, _, _, caddyMain, _ := provisionEnv(t)
	mustWrite(t, caddyMain, "# Managed by devlab-install-recv — the Holistic edge shell.\n"+testEdgeAddress+
		" {\n\timport "+env["DEVLAB_CADDY_CONF"]+"/*.caddy\n}\n")

	res := runInstallRecv(t, env, "--edge-host", "holistic=dash.example.test", "--edge-host", "devlab=devlab.example.test")
	if res.exit != 0 {
		t.Fatalf("declaring hostnames must succeed without --provision, got %d\n%s", res.exit, res.out)
	}
	for id, want := range map[string]string{"holistic": "dash.example.test", "devlab": "devlab.example.test"} {
		b, err := os.ReadFile(filepath.Join(env["DEVLAB_EDGE_HOSTS_DIR"], id))
		if err != nil {
			t.Fatalf("the hostname of '%s' must be declared on the host: %v\n%s", id, err, res.out)
		}
		if !strings.Contains(string(b), want) {
			t.Errorf("the declaration of '%s' must hold %q, got:\n%s", id, want, string(b))
		}
	}
	// Re-declaring the same name changes nothing.
	res2 := runInstallRecv(t, env, "--edge-host", "holistic=dash.example.test")
	if res2.exit != 0 || !strings.Contains(res2.out, "already declared as 'dash.example.test'") {
		t.Errorf("re-declaring the same name must be idempotent and said so:\n%s", res2.out)
	}
	// A value that is not a hostname is refused — never written, never guessed into shape.
	for _, bad := range []string{"holistic=dashboard", "holistic=http://a.test", "holistic", "=a.test"} {
		r := runInstallRecv(t, env, "--edge-host", bad)
		if r.exit == 0 {
			t.Errorf("--edge-host %q must be refused:\n%s", bad, r.out)
		}
	}
	if b, _ := os.ReadFile(filepath.Join(env["DEVLAB_EDGE_HOSTS_DIR"], "holistic")); !strings.Contains(string(b), "dash.example.test") {
		t.Errorf("a refused value must leave the standing declaration untouched, got:\n%s", string(b))
	}
}

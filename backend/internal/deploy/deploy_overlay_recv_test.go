package deploy

// The transport the delivery chain reaches production over — a private WireGuard overlay — is the FIRST
// stage of a production target, not an accessory: DEVLAB_RUNS_PROD_TARGET names a host that only EXISTS
// once the overlay carries. These tests prove devlab-install-recv's overlay logic in its
// direct-invocation seam (DEVLAB_RECV_TEST=1, no root, fake wg/wg-quick/ping): --provision sets up THIS
// host's side and prints the ONE line that catches up the home side; --overlay-here catches the home
// side up and PROVES the tunnel with numbers, failing closed when the peer does not answer; a private
// key is never accepted; and the dead production-target twin is swept exactly once (Part 2).

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A valid WireGuard public key is 32 bytes base64 — 44 chars ending in '='.
const testWGPubKey = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="

// fakeWGTools writes fake wg / wg-quick / ping into a bin dir and returns the env pointing at them. The
// interface "up" state lives in a marker file so idempotency across runs is observable; a "down" call is
// recorded so a test can prove a standing link is NEVER torn down (Part 1.6). pingOK selects a peer that
// answers (0% loss, exit 0) from one that does not (100% loss, exit 1).
func fakeWGTools(t *testing.T, pingOK bool) (env map[string]string, stateDir string) {
	t.Helper()
	bin := t.TempDir()
	stateDir = t.TempDir()
	upMarker := filepath.Join(stateDir, "iface-up")
	downLog := filepath.Join(stateDir, "down-called")

	wg := "#!/usr/bin/env bash\n" +
		"case \"$1\" in\n" +
		"  genkey) echo 'PRIV0000000000000000000000000000000000000000=' ;;\n" +
		"  pubkey) cat >/dev/null; echo '" + testWGPubKey + "' ;;\n" +
		"  show)\n" +
		"    if [ \"$3\" = latest-handshakes ]; then echo 'PEERKEY 0'; exit 0; fi\n" +
		"    [ -f '" + upMarker + "' ] && exit 0 || exit 1 ;;\n" +
		"  syncconf) exit 0 ;;\n" +
		"esac\nexit 0\n"

	wgquick := "#!/usr/bin/env bash\n" +
		"case \"$1\" in\n" +
		"  up)   touch '" + upMarker + "' ;;\n" +
		"  down) echo down >> '" + downLog + "'; rm -f '" + upMarker + "' ;;\n" +
		"  strip) echo '[Interface]' ;;\n" +
		"esac\nexit 0\n"

	var ping string
	if pingOK {
		ping = "#!/usr/bin/env bash\necho '3 packets transmitted, 3 received, 0% packet loss'\necho 'rtt min/avg/max/mdev = 0.1/0.2/0.3/0.0 ms'\nexit 0\n"
	} else {
		ping = "#!/usr/bin/env bash\necho '3 packets transmitted, 0 received, 100% packet loss'\nexit 1\n"
	}

	for name, body := range map[string]string{"wg": wg, "wg-quick": wgquick, "ping": ping} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	env = map[string]string{
		"DEVLAB_WG_DIR":      filepath.Join(stateDir, "wg"),
		"DEVLAB_WG_BIN":      filepath.Join(bin, "wg"),
		"DEVLAB_WGQUICK_BIN": filepath.Join(bin, "wg-quick"),
		"DEVLAB_PING_BIN":    filepath.Join(bin, "ping"),
	}
	return env, stateDir
}

// mergeEnv overlays b onto a (b wins) into a fresh map.
func mergeEnv(a, b map[string]string) map[string]string {
	m := map[string]string{}
	for k, v := range a {
		m[k] = v
	}
	for k, v := range b {
		m[k] = v
	}
	return m
}

// A bare host provisioned WITH overlay arguments gets this host's side of the transport set up in the
// SAME run: the private key is generated locally, the config carries the interface + the peer, the
// self-check reports the overlay up, and the closing report prints this host's public key AND the exact
// one-liner that catches the home side up (Handarbeit kommt als Skript).
func TestProvisionSetsUpOverlay(t *testing.T) {
	base, _, _, _, _, _ := provisionEnv(t)
	wgEnv, wgState := fakeWGTools(t, true)
	env := mergeEnv(base, wgEnv)

	res := runInstallRecv(t, env, "--provision", "--deploy-pubkey", testDeployPubKey,
		"--overlay-address", "10.10.0.1/24", "--overlay-listen-port", "51820",
		"--overlay-peer-pubkey", testWGPubKey, "--overlay-peer-allowed", "10.10.0.2/32",
		"--overlay-endpoint", "prod.example.org:51820")
	if res.exit != 0 {
		t.Fatalf("provisioning with overlay must succeed (exit 0), got %d\n%s", res.exit, res.out)
	}

	conf := filepath.Join(wgState, "wg", "wg0.conf")
	cb, err := os.ReadFile(conf)
	if err != nil {
		t.Fatalf("overlay config not written: %v", err)
	}
	for _, want := range []string{"[Interface]", "Address = 10.10.0.1/24", "ListenPort = 51820",
		"PrivateKey = ", "[Peer]", "PublicKey = " + testWGPubKey, "AllowedIPs = 10.10.0.2/32"} {
		if !strings.Contains(string(cb), want) {
			t.Errorf("overlay config must contain %q:\n%s", want, string(cb))
		}
	}
	// The prod side lists NO peer endpoint (the home side roams to this listener).
	if strings.Contains(string(cb), "Endpoint =") {
		t.Errorf("the listening (prod) side must not pin a peer endpoint — the home side roams:\n%s", string(cb))
	}
	for _, want := range []string{
		"overlay config written",                          // self-check line
		"this host's overlay public key: " + testWGPubKey, // report prints our public key
		"--overlay-here",                                  // the home-side catch-up one-liner
		"--overlay-peer-pubkey '" + testWGPubKey + "'",    // …carrying THIS host's freshly-made key
		"--overlay-peer-endpoint prod.example.org:51820",  // …and how the home side dials this host
		"all self-checks passed",
	} {
		if !strings.Contains(res.out, want) {
			t.Errorf("the overlay report must mention %q:\n%s", want, res.out)
		}
	}
}

// A private key handed to --overlay-peer-pubkey is refused — a private overlay key never travels, in
// either direction (Part 1.2). A non-WireGuard value is refused too.
func TestProvisionRefusesBadOverlayPeerKey(t *testing.T) {
	cases := []struct{ name, key, want string }{
		{"private", "-----BEGIN OPENSSH PRIVATE KEY-----\nxxx\n-----END OPENSSH PRIVATE KEY-----", "looks like a PRIVATE key"},
		{"garbage", "not-a-key", "not a WireGuard public key"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			base, _, _, _, _, _ := provisionEnv(t)
			wgEnv, _ := fakeWGTools(t, true)
			env := mergeEnv(base, wgEnv)
			res := runInstallRecv(t, env, "--provision", "--deploy-pubkey", testDeployPubKey,
				"--overlay-address", "10.10.0.1/24", "--overlay-listen-port", "51820",
				"--overlay-peer-pubkey", c.key, "--overlay-peer-allowed", "10.10.0.2/32")
			if res.exit == 0 {
				t.Fatalf("a %s overlay peer key must be refused (exit != 0):\n%s", c.name, res.out)
			}
			if !strings.Contains(res.out, c.want) {
				t.Errorf("the refusal must name %q:\n%s", c.want, res.out)
			}
		})
	}
}

// A standing overlay is REPORTED, not rebuilt: a second identical provision keeps the private key, keeps
// the config, and NEVER tears the interface down (Part 1.6, test b).
func TestOverlayIdempotentDoesNotTearDown(t *testing.T) {
	base, _, _, _, _, _ := provisionEnv(t)
	wgEnv, wgState := fakeWGTools(t, true)
	env := mergeEnv(base, wgEnv)
	args := []string{"--provision", "--deploy-pubkey", testDeployPubKey,
		"--overlay-address", "10.10.0.1/24", "--overlay-listen-port", "51820",
		"--overlay-peer-pubkey", testWGPubKey, "--overlay-peer-allowed", "10.10.0.2/32"}

	if res := runInstallRecv(t, env, args...); res.exit != 0 {
		t.Fatalf("first overlay provision failed: %d\n%s", res.exit, res.out)
	}
	// The fake marks the interface up after the first `wg-quick up`; the second run sees it up.
	res := runInstallRecv(t, env, args...)
	if res.exit != 0 {
		t.Fatalf("the idempotent re-run must succeed, got %d\n%s", res.exit, res.out)
	}
	if !strings.Contains(res.out, "already up with the intended config") || !strings.Contains(res.out, "not interrupted") {
		t.Errorf("a standing overlay must be reported, not rebuilt:\n%s", res.out)
	}
	if !strings.Contains(res.out, "kept, not regenerated") {
		t.Errorf("the private key must be kept on a re-run, not regenerated:\n%s", res.out)
	}
	if _, err := os.Stat(filepath.Join(wgState, "down-called")); err == nil {
		t.Errorf("a standing overlay must NEVER be torn down (wg-quick down was called)")
	}
}

// The home side is caught up over the SAME script (no sibling): the far side's NEW public key is written
// as the peer, the endpoint + keepalive are set, and the tunnel is PROVEN with numbers (Part 1.3/1.4).
func TestOverlayHereCatchUpProves(t *testing.T) {
	wgEnv, wgState := fakeWGTools(t, true)
	env := mergeEnv(map[string]string{"DEVLAB_RECV_TEST": "1"}, wgEnv)
	res := runInstallRecv(t, env, "--overlay-here",
		"--overlay-address", "10.10.0.2/24", "--overlay-peer-pubkey", testWGPubKey,
		"--overlay-peer-allowed", "10.10.0.1/32", "--overlay-peer-endpoint", "prod.example.org:51820",
		"--overlay-keepalive", "25", "--overlay-verify-peer", "10.10.0.1")
	if res.exit != 0 {
		t.Fatalf("home-side catch-up with a reachable peer must succeed, got %d\n%s", res.exit, res.out)
	}
	conf := filepath.Join(wgState, "wg", "wg0.conf")
	cb, _ := os.ReadFile(conf)
	for _, want := range []string{"Address = 10.10.0.2/24", "PublicKey = " + testWGPubKey,
		"AllowedIPs = 10.10.0.1/32", "Endpoint = prod.example.org:51820", "PersistentKeepalive = 25"} {
		if !strings.Contains(string(cb), want) {
			t.Errorf("the home config must contain %q:\n%s", want, string(cb))
		}
	}
	// The home side roams, so it does NOT listen.
	if strings.Contains(string(cb), "ListenPort") {
		t.Errorf("the roaming home side must not pin a listen port:\n%s", string(cb))
	}
	for _, want := range []string{"packet loss", "the production overlay carries"} {
		if !strings.Contains(res.out, want) {
			t.Errorf("the proof must show %q with numbers:\n%s", want, res.out)
		}
	}
}

// The peer does not answer: the catch-up fails CLOSED with the real reason — and does NOT tear the
// correctly-built local side back down (rollback over an external condition would undo good config).
func TestOverlayHereUnreachableFailsClosed(t *testing.T) {
	wgEnv, wgState := fakeWGTools(t, false) // 100% loss
	env := mergeEnv(map[string]string{"DEVLAB_RECV_TEST": "1"}, wgEnv)
	res := runInstallRecv(t, env, "--overlay-here",
		"--overlay-address", "10.10.0.2/24", "--overlay-peer-pubkey", testWGPubKey,
		"--overlay-peer-allowed", "10.10.0.1/32", "--overlay-peer-endpoint", "prod.example.org:51820",
		"--overlay-verify-peer", "10.10.0.1")
	if res.exit == 0 {
		t.Fatalf("an unreachable peer must fail closed (exit != 0):\n%s", res.out)
	}
	if !strings.Contains(res.out, "did not answer") || !strings.Contains(res.out, "10.10.0.1") {
		t.Errorf("the failure must name the real reason and the peer:\n%s", res.out)
	}
	if !strings.Contains(res.out, "100% packet loss") {
		t.Errorf("the failure must show the numbers that prove it:\n%s", res.out)
	}
	// The local side is fully built — its config must remain, not be rolled back over the peer being down.
	if _, err := os.Stat(filepath.Join(wgState, "wg", "wg0.conf")); err != nil {
		t.Errorf("the correctly-built local overlay config must be kept, not rolled back: %v", err)
	}
}

// --overlay-here refuses to double as a provision (the home side is not a delivery target).
func TestOverlayHereRejectsProvision(t *testing.T) {
	wgEnv, _ := fakeWGTools(t, true)
	env := mergeEnv(map[string]string{"DEVLAB_RECV_TEST": "1"}, wgEnv)
	res := runInstallRecv(t, env, "--overlay-here", "--provision",
		"--overlay-address", "10.10.0.2/24", "--overlay-peer-pubkey", testWGPubKey,
		"--overlay-peer-allowed", "10.10.0.1/32")
	if res.exit == 0 {
		t.Fatalf("--overlay-here with --provision must be refused:\n%s", res.out)
	}
	if !strings.Contains(res.out, "do not combine it with --provision") {
		t.Errorf("the refusal must say why:\n%s", res.out)
	}
}

// A provision removes the dead production-target twin exactly once and never recreates it; the ONE
// source is the runtime environment (Part 2, test d). A second run reports its absence (idempotent).
func TestProvisionSweepsDeadProdTargetTwin(t *testing.T) {
	base, _, _, _, _, _ := provisionEnv(t)
	dead := filepath.Join(t.TempDir(), "prod-target")
	if err := os.WriteFile(dead, []byte("root@10.10.0.1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	env := mergeEnv(base, map[string]string{"DEVLAB_PROD_TARGET_FILE": dead})

	res := runInstallRecv(t, env, "--provision", "--deploy-pubkey", testDeployPubKey)
	if res.exit != 0 {
		t.Fatalf("provision must succeed, got %d\n%s", res.exit, res.out)
	}
	if _, err := os.Stat(dead); !os.IsNotExist(err) {
		t.Errorf("the dead production-target twin must be removed, still present: %v", err)
	}
	if !strings.Contains(res.out, "removed the dead production-target twin") {
		t.Errorf("the sweep must name the removed dead twin:\n%s", res.out)
	}
	if !strings.Contains(res.out, "no dead production-target twin") {
		t.Errorf("the self-check must confirm the target exists exactly once (no dead twin):\n%s", res.out)
	}
	// A second run finds nothing to remove and never recreates the file.
	res = runInstallRecv(t, env, "--provision", "--deploy-pubkey", testDeployPubKey)
	if res.exit != 0 {
		t.Fatalf("idempotent re-run must succeed, got %d\n%s", res.exit, res.out)
	}
	if _, err := os.Stat(dead); !os.IsNotExist(err) {
		t.Errorf("--provision must NEVER recreate the dead twin: %v", err)
	}
	if strings.Contains(res.out, "removed the dead production-target twin") {
		t.Errorf("a re-run must not claim to remove an already-absent twin:\n%s", res.out)
	}
}

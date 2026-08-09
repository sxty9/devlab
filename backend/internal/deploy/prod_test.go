package deploy

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"devlab/backend/internal/workspace"
)

// writeKey drops a fixture private key whose bytes carry a sentinel, so a test can prove the key
// MATERIAL never leaks into any argument or message (only the path is ever handled).
func writeKey(t *testing.T, sentinel string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "prod-deploy-key")
	if err := os.WriteFile(p, []byte("-----BEGIN KEY-----\n"+sentinel+"\n-----END KEY-----\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// WHAT-1 + WHAT-6(a): the file transport and the install trigger authenticate with the ONE
// configured key and the ONE durable known-hosts file — the same identity feeds both.
func TestProdBothCallsUseConfiguredKey(t *testing.T) {
	id := ProdIdentity{KeyFile: "/etc/somewhere/prod-key", KnownHostsFile: "/var/state/prod-known_hosts"}

	send := strings.Join(rsyncArgs(ProdConfig{Identity: id}, "src/", "dest/"), " ")
	trig := strings.Join(triggerCmdArgs(id, "user@host", "svc-a"), " ")

	for _, c := range []struct{ name, args string }{{"rsync", send}, {"trigger", trig}} {
		if !strings.Contains(c.args, id.KeyFile) {
			t.Errorf("%s call does not name the configured key: %s", c.name, c.args)
		}
		if !strings.Contains(c.args, "UserKnownHostsFile="+id.KnownHostsFile) {
			t.Errorf("%s call does not point at the durable known-hosts file: %s", c.name, c.args)
		}
	}
	// A local-directory fixture target (no key) must NOT inject an ssh transport. Check for the exact
	// "-e" ARGUMENT (a standalone element), not a substring — "--exclude=…" legitimately contains "-e".
	fixture := rsyncArgs(ProdConfig{}, "src/", "dest/")
	for i, a := range fixture {
		if a == "-e" {
			t.Errorf("the fixture path (no key) must not set an ssh transport: %v", fixture[i:])
		}
	}
}

// WHAT-3 + WHAT-6(b): a missing or unreadable key surfaces its OWN reason, not a masked
// "connection failed" from a later transport.
func TestProdMissingKeyNamesItsOwnReason(t *testing.T) {
	art := t.TempDir()
	if err := os.WriteFile(filepath.Join(art, "x"), []byte("y"), 0o644); err != nil {
		t.Fatal(err)
	}
	absent := filepath.Join(t.TempDir(), "no-such-key")
	cfg := ProdConfig{
		RsyncTarget: t.TempDir(),
		Identity:    ProdIdentity{KeyFile: absent, KnownHostsFile: filepath.Join(t.TempDir(), "kh")},
		Trigger:     func(context.Context, string) (string, error) { t.Fatal("trigger must never fire on an unusable key"); return "", nil },
	}
	_, err := SendProd(context.Background(), cfg, "svc-a", art)
	if err == nil {
		t.Fatal("a missing key must refuse")
	}
	if !strings.Contains(err.Error(), "deploy key") || !strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("error must name the missing key by its own reason, got: %v", err)
	}
	if strings.Contains(err.Error(), "send failed") || strings.Contains(err.Error(), "trigger failed") {
		t.Fatalf("a missing key must not read as a connection/transport failure, got: %v", err)
	}
}

func TestProdUnreadableKeyNamesItsOwnReason(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads every file regardless of mode; the permission gate cannot be exercised as root")
	}
	key := writeKey(t, "SECRET-KEY-MATERIAL")
	if err := os.Chmod(key, 0o000); err != nil {
		t.Fatal(err)
	}
	err := ProdIdentity{KeyFile: key, KnownHostsFile: "/tmp/kh"}.checkReadable()
	if err == nil {
		t.Fatal("an unreadable key must be refused")
	}
	if !strings.Contains(err.Error(), "not readable") {
		t.Fatalf("error must name the read failure, got: %v", err)
	}
	if strings.Contains(err.Error(), "SECRET-KEY-MATERIAL") {
		t.Fatalf("key material leaked into the error: %v", err)
	}
}

// WHAT-5 + WHAT-6(d): no key material appears in any argument or message; only the path is handled.
func TestProdNoKeyMaterialInOutput(t *testing.T) {
	const sentinel = "SECRET-KEY-MATERIAL"
	key := writeKey(t, sentinel)
	id := ProdIdentity{KeyFile: key, KnownHostsFile: filepath.Join(t.TempDir(), "kh")}

	surfaces := []string{
		strings.Join(id.sshOpts(), " "),
		strings.Join(rsyncArgs(ProdConfig{Identity: id}, "src/", "dest/"), " "),
		strings.Join(triggerCmdArgs(id, "user@host", "svc-a"), " "),
	}
	// A readable key passes checkReadable without ever reading its bytes.
	if err := id.checkReadable(); err != nil {
		t.Fatalf("a readable key must pass: %v", err)
	}
	for _, s := range surfaces {
		if strings.Contains(s, sentinel) {
			t.Fatalf("key material leaked into an argument surface: %s", s)
		}
	}
}

// The prod send runs ONLY against a fixture target in this phase: the artifact lands in the
// staging directory, and with a nil Trigger nothing is ever installed — the path is implemented
// but NOT armed.
func TestSendProdAgainstFixtureTarget(t *testing.T) {
	art := t.TempDir()
	if err := os.WriteFile(filepath.Join(art, "svc-ad"), []byte("prebuilt-bytes"), 0o755); err != nil {
		t.Fatal(err)
	}
	staging := t.TempDir() // the fixture rsync target — a local directory, never a live host

	if _, err := SendProd(context.Background(), ProdConfig{RsyncTarget: staging}, "svc-a", art); err != nil {
		t.Fatal(err)
	}

	got, err := os.ReadFile(filepath.Join(staging, "svc-a", "svc-ad"))
	if err != nil || string(got) != "prebuilt-bytes" {
		t.Fatalf("artifact not staged under <target>/<repo>/: %v %q", err, got)
	}
}

// Only FINISHED bytes travel: the production send ships the program and the setup/ product, but NOT a
// service's dashboard ui/ SOURCE — production never builds it, so it would be dead weight on the target.
// The exclusion is anchored to the artifact root, so only a TOP-LEVEL ui/ is dropped.
func TestSendProdExcludesUISource(t *testing.T) {
	art := t.TempDir()
	if err := os.WriteFile(filepath.Join(art, "svc-ad"), []byte("prebuilt-bytes"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(art, "setup"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(art, "setup", "svc-a.service"), []byte("[Service]\nUser=svc-a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(art, "ui", "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(art, "ui", "src", "App.tsx"), []byte("export const x = 1"), 0o644); err != nil {
		t.Fatal(err)
	}

	staging := t.TempDir()
	if _, err := SendProd(context.Background(), ProdConfig{RsyncTarget: staging}, "svc-a", art); err != nil {
		t.Fatal(err)
	}

	// The program and the setup product arrived…
	if _, err := os.Stat(filepath.Join(staging, "svc-a", "svc-ad")); err != nil {
		t.Fatalf("the program must reach production: %v", err)
	}
	if _, err := os.Stat(filepath.Join(staging, "svc-a", "setup", "svc-a.service")); err != nil {
		t.Fatalf("the setup product must reach production: %v", err)
	}
	// …but the ui SOURCE did not.
	if _, err := os.Stat(filepath.Join(staging, "svc-a", "ui")); !os.IsNotExist(err) {
		t.Fatalf("the ui/ source must NOT reach production (err=%v)", err)
	}
}

func TestSendProdTriggerSeam(t *testing.T) {
	art := t.TempDir()
	if err := os.WriteFile(filepath.Join(art, "x"), []byte("y"), 0o644); err != nil {
		t.Fatal(err)
	}
	fired := ""
	cfg := ProdConfig{
		RsyncTarget: t.TempDir(),
		Trigger:     func(_ context.Context, repo string) (string, error) { fired = repo; return "", nil },
	}
	if _, err := SendProd(context.Background(), cfg, "svc-a", art); err != nil {
		t.Fatal(err)
	}
	if fired != "svc-a" {
		t.Fatalf("trigger fired for %q, want svc-a", fired)
	}
}

func TestSendProdValidation(t *testing.T) {
	art := t.TempDir()
	// Repo grammar guards the remote path composition.
	if _, err := SendProd(context.Background(), ProdConfig{RsyncTarget: t.TempDir()}, "../evil", art); err == nil {
		t.Fatal("a traversal repo name must be rejected")
	}
	if _, err := SendProd(context.Background(), ProdConfig{RsyncTarget: t.TempDir()}, "UPPER", art); err == nil {
		t.Fatal("the name grammar is lowercase")
	}
	// The target comes from server-side config; an empty one is an honest refusal.
	if _, err := SendProd(context.Background(), ProdConfig{}, "svc-a", art); err == nil {
		t.Fatal("a missing prod target must refuse")
	}
	// No prebuilt artifact ⇒ nothing to ship.
	if _, err := SendProd(context.Background(), ProdConfig{RsyncTarget: t.TempDir()}, "svc-a",
		filepath.Join(t.TempDir(), "absent")); err == nil {
		t.Fatal("a missing artifact must refuse")
	}
}

// Build is a thin, honest delegation: it refuses outside per-user execution (root or the
// service user must never build agent code) and surfaces the toolchain's own words.
func TestBuildRequiresPerUserExecution(t *testing.T) {
	_, err := Build(context.Background(), workspace.Executor{User: "someone", PerUser: false}, t.TempDir())
	if err == nil {
		t.Fatal("a non-per-user build must be refused (root never builds)")
	}
}

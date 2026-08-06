package deploy

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A CHANGED host key is recognised from ssh's own banner (and only then), so a first contact — which
// accept-new records silently — is never mistaken for a changed key.
func TestHostKeyChangedDetection(t *testing.T) {
	changed := []string{
		"@@@ WARNING: REMOTE HOST IDENTIFICATION HAS CHANGED! @@@\nHost key verification failed.",
		"Host key verification failed.",
		"POSSIBLE DNS SPOOFING DETECTED",
		"Warning: the ECDSA host key for prod has changed",
	}
	for _, out := range changed {
		if !hostKeyChanged(out) {
			t.Errorf("must recognise a changed host key in %q", out)
		}
	}
	notChanged := []string{
		"", "kex_exchange_identification: Connection closed by remote host",
		"Permission denied (publickey).",
		"rsync: connection unexpectedly closed",
		"Warning: Permanently added 'prod' (ED25519) to the list of known hosts.", // first contact
	}
	for _, out := range notChanged {
		if hostKeyChanged(out) {
			t.Errorf("must NOT read a changed key from %q", out)
		}
	}
}

func TestHostFromTarget(t *testing.T) {
	for in, want := range map[string]string{
		"deploy@10.10.0.1:/srv/staging": "10.10.0.1",
		"deploy@prod.example":           "prod.example",
		"prod.example:/srv":             "prod.example",
		"prod.example":                  "prod.example",
		"":                              "",
	} {
		if got := hostFromTarget(in); got != want {
			t.Errorf("hostFromTarget(%q) = %q, want %q", in, got, want)
		}
	}
}

// asHostKeyChanged turns exactly the changed-key case into the distinct typed error, and leaves every
// other failure untouched so it keeps its own detail.
func TestAsHostKeyChanged(t *testing.T) {
	base := os.ErrClosed
	if got := asHostKeyChanged("deploy@prod.example", "some transient network error", base); got != base {
		t.Fatalf("a non-host-key failure must pass through unchanged, got %v", got)
	}
	got := asHostKeyChanged("deploy@prod.example", "REMOTE HOST IDENTIFICATION HAS CHANGED", base)
	var hk *HostKeyChangedError
	if !asType(got, &hk) {
		t.Fatalf("a changed host key must become *HostKeyChangedError, got %T", got)
	}
	if hk.Target != "prod.example" {
		t.Errorf("the error must name the host, got %q", hk.Target)
	}
	if !strings.Contains(hk.Error(), "CHANGED") || !strings.Contains(hk.Error(), "not a plain connection") && !strings.Contains(hk.Error(), "NOT a plain connection") {
		t.Errorf("the message must name the change distinctly, got %q", hk.Error())
	}
}

// asType is a tiny errors.As wrapper kept local to avoid importing errors in the test just for one call.
func asType(err error, target **HostKeyChangedError) bool {
	e, ok := err.(*HostKeyChangedError)
	if ok {
		*target = e
	}
	return ok
}

// Accept re-pins the known-hosts file ONLY when the host still presents the approved key; a key that
// changed again is refused, and nothing is written. The scan is seamed so no live host is needed.
func TestHostKeyManagerAcceptContentPin(t *testing.T) {
	kh := filepath.Join(t.TempDir(), "known_hosts")
	if err := os.WriteFile(kh, []byte("prod.example ssh-ed25519 OLDKEY\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	m := &HostKeyManager{Host: "prod.example", KnownHostsFile: kh, scan: func(context.Context, string) (HostKeyScan, error) {
		return HostKeyScan{Line: "prod.example ssh-ed25519 NEWKEY", Fingerprint: "SHA256:NEW"}, nil
	}}

	// Approving a fingerprint that no longer matches is refused; the file is left as it was.
	if err := m.Accept(context.Background(), "SHA256:STALE"); err == nil {
		t.Fatal("Accept must refuse a fingerprint that no longer matches the presented key")
	}
	if b, _ := os.ReadFile(kh); strings.Contains(string(b), "NEWKEY") {
		t.Fatal("a refused acceptance must not write the new key")
	}

	// Approving the fingerprint the host actually presents installs it (old entry dropped, new added).
	if err := m.Accept(context.Background(), "SHA256:NEW"); err != nil {
		t.Fatalf("Accept of the matching fingerprint must succeed: %v", err)
	}
	b, _ := os.ReadFile(kh)
	if !strings.Contains(string(b), "NEWKEY") {
		t.Errorf("the approved key must be recorded, got %q", string(b))
	}
	// An empty approved fingerprint is refused outright (nothing to verify against).
	if err := m.Accept(context.Background(), ""); err == nil {
		t.Fatal("Accept must refuse an empty approved fingerprint")
	}
}

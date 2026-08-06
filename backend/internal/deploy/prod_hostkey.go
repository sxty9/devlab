package deploy

// A freshly reinstalled production host presents a NEW ssh host key. The production send pins the
// target's key on first contact into <state>/prod-known_hosts and re-checks it on every later contact
// (StrictHostKeyChecking=accept-new — trust-on-first-use, never "trust everything"). So after a
// reinstall EVERY production delivery fails the host-key check — and ssh's own message ("connection
// closed", "verification failed") does NOT say WHY. This file gives that failure its OWN clear reason
// and the deliberate, verified way to accept the new key: it is never trusted silently and never
// overwritten automatically (a silently accepted new host key is exactly the attack the check exists
// to catch). The DECISION to accept is the human's, taken through the same Blocked/approval path the
// root-wrapper renewal uses; this file only DETECTS the change and, once approved, APPLIES it under a
// re-verification that the key still matches the one that was approved.

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// HostKeyChangedError is the DISTINCT reason a production send failed because the target's ssh host
// key no longer matches the one recorded in the known-hosts file — the machine was reinstalled, or
// something is intercepting the connection. It is never a masked "connection failed": the caller
// surfaces exactly this, and the chain HOLDS production until the new key is deliberately approved.
type HostKeyChangedError struct {
	Target string // the production host whose key changed (for the message and the approval)
	Detail string // the tail of ssh's own output, for the record
}

func (e *HostKeyChangedError) Error() string {
	return fmt.Sprintf("the ssh host key of the production target %q has CHANGED — the machine was "+
		"reinstalled, or something is intercepting the connection. This is NOT a plain connection "+
		"failure: production delivery is held until the new host key is deliberately approved, because a "+
		"silently accepted new key is exactly the interception this check guards against. %s",
		e.Target, strings.TrimSpace(e.Detail))
}

// hostKeyChanged reports whether ssh/rsync output shows the target's KNOWN host key no longer matches
// — the signature of a reinstalled (or intercepted) host, as opposed to a first contact (accept-new
// records an unknown key silently, so a first contact never lands here). ssh prints a fixed banner for
// this case regardless of locale of the surrounding text.
func hostKeyChanged(out string) bool {
	for _, marker := range []string{
		"REMOTE HOST IDENTIFICATION HAS CHANGED",
		"POSSIBLE DNS SPOOFING",
		"Host key verification failed",
		"host key for", // "... has changed and you have requested strict checking"
	} {
		if strings.Contains(out, marker) {
			// The last marker is broad; require it to co-occur with the change wording so a first-contact
			// prompt (which accept-new never raises) can never be mistaken for a changed key.
			if marker == "host key for" && !strings.Contains(out, "has changed") {
				continue
			}
			return true
		}
	}
	return false
}

// hostFromTarget reduces an ssh/rsync destination to the bare host: "user@host:path" → "host",
// "user@host" → "host", "host:path" → "host". Empty in → empty out.
func hostFromTarget(target string) string {
	t := strings.TrimSpace(target)
	if i := strings.IndexByte(t, '@'); i >= 0 {
		t = t[i+1:]
	}
	if i := strings.IndexByte(t, ':'); i >= 0 {
		t = t[:i]
	}
	return t
}

// asHostKeyChanged wraps a failed prod ssh/rsync call in a HostKeyChangedError when its output shows a
// changed host key; otherwise it returns the original error unchanged. target names the host for the
// message. This is the ONE place a raw ssh failure becomes the distinct host-key reason.
func asHostKeyChanged(target, out string, err error) error {
	if err == nil || !hostKeyChanged(out) {
		return err
	}
	return &HostKeyChangedError{Target: hostFromTarget(target), Detail: tail(strings.TrimSpace(out), 800)}
}

// HostKeyScan is the current host key a target presents, read WITHOUT trusting it: the ssh-keyscan
// line (to write into known-hosts on approval) and its fingerprint (to show the human and to pin the
// approval to). Reading a key is not trusting it — trust happens only when the human approves and the
// line is written.
type HostKeyScan struct {
	Line        string // the "host ssh-ed25519 AAAA..." line ssh-keyscan returned
	Fingerprint string // "SHA256:..." of that key, for the approval prompt and the pin
}

// HostKeyManager is the deliberate, verified accept path for a changed production host key. It owns
// the known-hosts file the production send checks against and the host it deploys to. It NEVER trusts
// a key on its own: Scan only READS the presented key; Accept writes it ONLY after re-confirming it
// still matches the fingerprint the human approved. The two external readers (ssh-keyscan, ssh-keygen)
// are seams so the logic is provable off a live host.
type HostKeyManager struct {
	Host           string // the production host (from the receiver destination), e.g. "10.10.0.1"
	KnownHostsFile string // the durable known-hosts the production send checks against
	// scan reads the key a host currently presents (default: ssh-keyscan + ssh-keygen -lf). A seam.
	scan func(ctx context.Context, host string) (HostKeyScan, error)
}

// NewHostKeyManager builds the manager for a production host and its known-hosts file, wired to the
// real ssh-keyscan reader.
func NewHostKeyManager(host, knownHosts string) *HostKeyManager {
	return &HostKeyManager{Host: hostFromTarget(host), KnownHostsFile: knownHosts, scan: scanHostKey}
}

// Target is the production host this manager governs — the dedup key for the approval and the record.
func (m *HostKeyManager) Target() string { return m.Host }

// ScanFingerprint reads the fingerprint of the key the production host CURRENTLY presents, for the
// approval prompt — the human compares it out-of-band before approving. It does not change trust.
func (m *HostKeyManager) ScanFingerprint(ctx context.Context) (string, error) {
	s, err := m.scan(ctx, m.Host)
	if err != nil {
		return "", err
	}
	return s.Fingerprint, nil
}

// Accept re-pins the known-hosts file to the host's CURRENT key — but only after re-confirming that
// key still hashes to approvedFingerprint, the exact fingerprint the human approved. If the key
// changed again since the approval (a second reinstall, or an interception that swapped in yet another
// key after the operator looked), Accept REFUSES: the approval was for a different key and must not
// carry over. This is the content-pin that makes the approval single-use for one specific key, the
// same shape the wrapper renewal uses (approve a sha, install only that sha).
func (m *HostKeyManager) Accept(ctx context.Context, approvedFingerprint string) error {
	if strings.TrimSpace(approvedFingerprint) == "" {
		return fmt.Errorf("deploy: refusing to accept a host key with no approved fingerprint to check it against")
	}
	s, err := m.scan(ctx, m.Host)
	if err != nil {
		return fmt.Errorf("deploy: could not read the current host key of %q to accept it: %w", m.Host, err)
	}
	if s.Fingerprint != approvedFingerprint {
		return fmt.Errorf("deploy: the production host %q now presents host key %s, but the approval was "+
			"for %s — the key changed AGAIN since it was approved. Refusing to trust it; a fresh approval "+
			"is required for the key now present", m.Host, s.Fingerprint, approvedFingerprint)
	}
	// Drop every stale entry for this host, then write the freshly confirmed key. Dropping first means a
	// leftover old line can never keep failing the check after the new key is trusted.
	if err := removeKnownHost(ctx, m.KnownHostsFile, m.Host); err != nil {
		return err
	}
	return appendKnownHostLine(m.KnownHostsFile, s.Line)
}

// scanHostKey is the real reader: ssh-keyscan fetches the host's current key line, ssh-keygen -lf
// derives its fingerprint. Neither trusts the key — they only read it.
func scanHostKey(ctx context.Context, host string) (HostKeyScan, error) {
	if strings.TrimSpace(host) == "" {
		return HostKeyScan{}, fmt.Errorf("deploy: no production host to scan a key from")
	}
	line, err := exec.CommandContext(ctx, "ssh-keyscan", "-T", "10", host).Output()
	if err != nil {
		return HostKeyScan{}, fmt.Errorf("deploy: ssh-keyscan %s: %w", host, err)
	}
	// ssh-keyscan may print several key types; keep the first non-comment line — one trusted key is
	// enough for the known-hosts check, and pinning ONE fingerprint keeps the approval unambiguous.
	first := ""
	for _, ln := range strings.Split(string(line), "\n") {
		ln = strings.TrimSpace(ln)
		if ln != "" && !strings.HasPrefix(ln, "#") {
			first = ln
			break
		}
	}
	if first == "" {
		return HostKeyScan{}, fmt.Errorf("deploy: ssh-keyscan returned no host key for %s", host)
	}
	fp, err := fingerprintOf(ctx, first)
	if err != nil {
		return HostKeyScan{}, err
	}
	return HostKeyScan{Line: first, Fingerprint: fp}, nil
}

// fingerprintOf derives the SHA256 fingerprint of a known-hosts key line via ssh-keygen -lf -.
func fingerprintOf(ctx context.Context, line string) (string, error) {
	cmd := exec.CommandContext(ctx, "ssh-keygen", "-lf", "-")
	cmd.Stdin = strings.NewReader(line + "\n")
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("deploy: ssh-keygen could not fingerprint the scanned host key: %w", err)
	}
	// "256 SHA256:abc… host (ED25519)" → the SHA256:… token.
	for _, f := range strings.Fields(string(out)) {
		if strings.HasPrefix(f, "SHA256:") {
			return f, nil
		}
	}
	return strings.TrimSpace(string(out)), nil
}

// removeKnownHost drops every entry for host from the known-hosts file (ssh-keygen -R), so no stale
// key lingers to keep failing the check. A missing file is fine — there is nothing to drop.
func removeKnownHost(ctx context.Context, knownHosts, host string) error {
	if _, err := os.Stat(knownHosts); os.IsNotExist(err) {
		return nil
	}
	if out, err := exec.CommandContext(ctx, "ssh-keygen", "-R", host, "-f", knownHosts).CombinedOutput(); err != nil {
		return fmt.Errorf("deploy: could not remove the old host key of %q from %s: %w: %s", host, knownHosts, err, tail(strings.TrimSpace(string(out)), 400))
	}
	return nil
}

// appendKnownHostLine writes one confirmed key line to the known-hosts file, creating it if absent.
func appendKnownHostLine(knownHosts, line string) error {
	f, err := os.OpenFile(knownHosts, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("deploy: could not open %s to write the approved host key: %w", knownHosts, err)
	}
	defer f.Close()
	if _, err := f.WriteString(strings.TrimRight(line, "\n") + "\n"); err != nil {
		return fmt.Errorf("deploy: could not write the approved host key to %s: %w", knownHosts, err)
	}
	return nil
}

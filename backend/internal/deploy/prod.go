package deploy

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
)

// prodRepoRe mirrors the wrapper's name grammar: nothing else ever reaches a path.
var prodRepoRe = regexp.MustCompile(`^[a-z][a-z0-9-]{2,30}$`)

// ProdIdentity is the ONE ssh access the production send authenticates with: the configured private
// key and the durable known-hosts file. BOTH prod calls use it — the rsync file transport and the
// forced-command install trigger — so there is exactly one key and one place it is named (E §7.4,
// Zugangspunkt wiederverwenden). It comes EXCLUSIVELY from server-side configuration, never a
// request. An empty KeyFile means unarmed: the local-directory fixture path used in tests.
type ProdIdentity struct {
	// KeyFile is the path to the deploy private key (from DEVLAB_RUNS_PROD_KEY). Only the path is
	// ever handled here; the key material itself is never read into this process (secret hygiene).
	KeyFile string
	// KnownHostsFile is where ssh records the target's host key on first contact and re-checks it
	// afterwards (from statepath.ProdKnownHosts). It must be a location the service user can write
	// and that survives a restart, because that user has no home ssh directory.
	KnownHostsFile string
}

// armed reports whether a real key is named — the production path, as opposed to the
// local-directory fixture used by tests (no ssh, no key).
func (id ProdIdentity) armed() bool { return strings.TrimSpace(id.KeyFile) != "" }

// sshOpts are the ssh options shared by the file transport and the install trigger, built ONCE from
// the identity so both authenticate identically:
//   - the configured key names the identity (IdentitiesOnly stops ssh from also offering agent or
//     default keys the service user does not have);
//   - BatchMode fails fast instead of hanging on a password prompt a misconfigured key would raise;
//   - the host key is recorded on first contact into the durable known-hosts file and CHECKED on
//     every later contact (accept-new is trust-on-first-use, never "trust everything").
func (id ProdIdentity) sshOpts() []string {
	return []string{
		"-i", id.KeyFile,
		"-o", "IdentitiesOnly=yes",
		"-o", "BatchMode=yes",
		"-o", "UserKnownHostsFile=" + id.KnownHostsFile,
		"-o", "StrictHostKeyChecking=accept-new",
	}
}

// checkReadable proves the service user can actually READ the configured key before either prod call
// runs, so a missing or unreadable key surfaces its OWN reason — not a downstream "connection
// failed" that hides the real cause (WHAT-3). It opens the file for reading and closes it at once;
// it never reads a byte of key material, so nothing secret can reach a message or log (WHAT-5).
func (id ProdIdentity) checkReadable() error {
	if !id.armed() {
		return fmt.Errorf("deploy: prod deploy key is not configured (server-side)")
	}
	f, err := os.Open(id.KeyFile)
	if err != nil {
		switch {
		case os.IsNotExist(err):
			return fmt.Errorf("deploy: prod deploy key %s does not exist — point DEVLAB_RUNS_PROD_KEY at the deploy private key", id.KeyFile)
		case os.IsPermission(err):
			return fmt.Errorf("deploy: prod deploy key %s is not readable by the service user — grant the service read access to it", id.KeyFile)
		default:
			return fmt.Errorf("deploy: prod deploy key %s cannot be opened: %w", id.KeyFile, err)
		}
	}
	return f.Close()
}

// ProdConfig configures the prod send. The target and the identity come EXCLUSIVELY from server-side
// configuration — never from a request — so a manipulated caller cannot redirect a prod deploy to a
// host it controls (E §7.4).
type ProdConfig struct {
	// RsyncTarget is the rsync destination root: "user@host:path" against the rrsync-confined
	// staging behind the forced-command receiver in production; a local directory in tests.
	RsyncTarget string
	// RsyncArgs overrides the transport flags (test seam; empty means the production set).
	RsyncArgs []string
	// Identity is the shared ssh key + known-hosts used by BOTH the rsync transport and the trigger.
	// Empty (no KeyFile) is the local-directory fixture path — no ssh, no key.
	Identity ProdIdentity
	// Trigger fires the install-only receiver on the target (the forced-command ssh call).
	// nil = NOT ARMED: the artifact is staged, nothing is installed — the state of this phase.
	Trigger func(ctx context.Context, repo string) error
}

// ProdSendExcludes names — at the ONE place, in code, never a silent rsync flag buried elsewhere —
// the artifact subtrees the production send does NOT ship, because production never installs them.
// `ui` is a service's dashboard SOURCE (TypeScript): the dashboard is BUILT on the dev host and served
// from there, never on the production host ("THIS HOST NEVER BUILDS — prod runs only prebuilt bytes"),
// so shipping the source would leave unused, unbuilt code on the target (measured: prizm 28 KB of
// source beside its 8.9 MB program). The production send carries only what production installs — the
// program and the `setup/` first-time product. Add an exclusion here, with its reason, never elsewhere.
var ProdSendExcludes = []string{"ui"}

// rsyncArgs assembles the full rsync argument list. When a key is armed it hands rsync its transport
// via `-e "ssh …"` carrying the SAME options as the trigger — so both calls authenticate with the
// one configured key. A local-directory target ignores `-e`, which keeps the fixture path unchanged.
// The ProdSendExcludes are anchored to the artifact root (a leading '/'), so only a TOP-LEVEL ui/ is
// dropped — never a ui/ that a service legitimately nests inside its own program tree.
func rsyncArgs(cfg ProdConfig, src, dest string) []string {
	args := cfg.RsyncArgs
	if args == nil {
		args = []string{"-az", "--delete"}
	}
	full := append([]string{}, args...)
	for _, ex := range ProdSendExcludes {
		full = append(full, "--exclude=/"+ex)
	}
	if cfg.Identity.armed() {
		full = append(full, "-e", "ssh "+strings.Join(cfg.Identity.sshOpts(), " "))
	}
	return append(full, src, dest)
}

// SendProd ships a PREBUILT artifact to the prod staging (install-only doctrine: this host built
// nothing as root, the target builds nothing at all). Both the file transport and the install
// trigger authenticate with the one configured deploy key; a missing or unreadable key is refused
// up front with its own reason (never a masked connection error). Prod is fed exclusively from the
// default branch — that policy sits with the caller of this function.
func SendProd(ctx context.Context, cfg ProdConfig, repo, artifactDir string) error {
	if !prodRepoRe.MatchString(repo) {
		return fmt.Errorf("deploy: invalid repo name %q", repo)
	}
	if strings.TrimSpace(cfg.RsyncTarget) == "" {
		return fmt.Errorf("deploy: prod target is not configured (server-side)")
	}
	if fi, err := os.Stat(artifactDir); err != nil || !fi.IsDir() {
		return fmt.Errorf("deploy: no prebuilt artifact at %s", artifactDir)
	}
	// A key is proven readable BEFORE any transport runs, so its absence names itself instead of
	// hiding behind a later "connection failed". The fixture path (no key) skips this.
	if cfg.Identity.armed() {
		if err := cfg.Identity.checkReadable(); err != nil {
			return err
		}
	}

	// Trailing slashes: ship the CONTENT of artifactDir into <target>/<repo>/.
	dest := strings.TrimRight(cfg.RsyncTarget, "/") + "/" + repo + "/"
	full := rsyncArgs(cfg, strings.TrimRight(artifactDir, "/")+"/", dest)
	if out, err := exec.CommandContext(ctx, "rsync", full...).CombinedOutput(); err != nil {
		return fmt.Errorf("deploy: prod send failed: %w: %s", err, tail(strings.TrimSpace(string(out)), 2000))
	}

	if cfg.Trigger == nil {
		return nil // staged only — no receiver fired (used by fixtures; the armed path always sets it)
	}
	if err := cfg.Trigger(ctx, repo); err != nil {
		return fmt.Errorf("deploy: prod install trigger failed: %w", err)
	}
	return nil
}

// triggerCmdArgs builds the trigger's ssh argument list: the shared identity options followed by the
// receiver and the repo. It uses the SAME sshOpts as the file transport, so the key that ships the
// files is the key that fires the install.
func triggerCmdArgs(id ProdIdentity, recv, repo string) []string {
	return append(append([]string{}, id.sshOpts()...), recv, repo)
}

// SSHTrigger fires the install-only receiver on the production host over the CONFIGURED deploy key:
// `ssh -i <key> … <recv> <repo>`. The receiver is the forced command behind that key, so the request
// reaches it as $SSH_ORIGINAL_COMMAND and it installs the staged artifact AND proves the service
// running on the target (the same honest gate the dev delivery uses, WHAT-2). A non-zero exit — a
// failed install or a service that did not come up — is therefore the production failure, surfaced
// by name. The identity supplies the key (BatchMode keeps a misconfigured key from hanging) and the
// durable known-hosts file (the host key is recorded on first contact and checked thereafter).
func SSHTrigger(recv string, id ProdIdentity) func(ctx context.Context, repo string) error {
	return func(ctx context.Context, repo string) error {
		if !prodRepoRe.MatchString(repo) {
			return fmt.Errorf("deploy: invalid repo name %q", repo)
		}
		cmd := exec.CommandContext(ctx, "ssh", triggerCmdArgs(id, recv, repo)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("%w: %s", err, tail(strings.TrimSpace(string(out)), 2000))
		}
		return nil
	}
}

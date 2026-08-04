package deploy

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// The self repo ships root-owned wrappers from deploy/ into the pinned sbin directory, but the
// autonomous chain delivers ONLY the daemon binary — root never installs a wrapper, because a
// service that could hand itself a fresh root script could turn "change the program" into "become
// root" (E §7.4). So a change that touches deploy/devlab-install or deploy/devlab-exec used to go
// live as a binary while its wrappers stayed stale under sbin, and the delivery reported green over
// half-shipped software (measured 31.07./01.08.2026).
//
// The chosen answer keeps the root boundary exactly where it is: the delivery does not replace the
// wrappers, it PROVES they are current. It reads the installed wrapper (world-readable, mode 0755)
// and the repository's deploy/<name>, compares their bytes, and — on any mismatch — FAILS the self
// delivery by name (K-4: an implemented change without its full delivery is a failure, never green)
// and hands a human with sudo the one command that makes the wrappers current. Nothing here writes
// a privileged path, so this probe adds no privilege-escalation surface: an attacker who rewrites
// deploy/devlab-install achieves nothing at run time except a named, refused delivery — the wrapper
// is still replaced only by a human with root, from a checkout they can review.

// wrapperInstallDir is where the pinned root wrappers live once installed (matches the installWrapper
// pin and the `service` CLI's install target). The default is the instance-neutral convention; the
// env override is a READ-ONLY test seam — it only moves what the drift probe COMPARES, it never
// grants privilege, so unlike the wrapper scripts' own env seams it needs no sudoers guard.
var wrapperInstallDir = envOr("DEVLAB_SBIN_DIR", "/usr/local/sbin")

// selfWrappers are the root-owned wrappers (and the shared setup library) the devlab (self) repository
// ships from deploy/ into wrapperInstallDir. This is the CANONICAL set: three places must name exactly
// it — the WRAPPERS array in the `service` CLI, the RENEWABLE list the root renewal tool
// (deploy/devlab-install) will act on, and this slice — and TestRenewableWrapperListsAgree fails if any
// of the three diverges (a comment used to be the only thing holding them together; that is not an
// assurance). The pins in deploy/devlab.sudoers / deploy/devlab-runs.sudoers are the SUBSET invoked via
// sudo (devlab-exec, devlab-mkworkspace, devlab-install); the same test proves every pinned wrapper is
// a member of this set, so a sudo grant can never name a wrapper the drift probe does not track.
// devlab-setup-lib.sh is a SOURCED fragment, not an executable wrapper, but it is installed beside them
// and BOTH devlab-install and devlab-exec source it, so a stale copy would silently change what a setup
// produces — the drift probe (and now the renewal tool) keeps it current like the rest.
// deploy/devlab-deploy-recv is deliberately absent — it lives on the PRODUCTION host, not in this host's
// sbin, and reaches production by hand (it is not installed to sbin by `service`).
var selfWrappers = []string{
	"devlab-exec",
	"devlab-install",
	"devlab-mkworkspace",
	"devlab-restart-when-free",
	"devlab-setup-lib.sh",
}

// WrapperDrift names one root wrapper whose installed copy no longer matches the repository's
// deploy/<name>: the wrapper the running daemon actually calls is not the one the delivered code
// expects.
type WrapperDrift struct {
	Name      string // wrapper name, e.g. "devlab-install"
	RepoPath  string // deploy/<name> in the checkout — the intended content
	Installed string // <sbin>/<name> — what runs today
	Reason    string // why it drifted ("not installed" | "installed copy differs …")

	// WantSHA and WantContent are the sha256 (hex) and bytes of the copy — stack-tip or
	// delivering-branch — that a renewal would install. Every drift the probes (StackTipWrapperDrift /
	// DeliveringBranchWrapperDrift, which the gate reuses) return now through driftAgainstInstalled
	// carries them.
	WantSHA     string
	WantContent []byte

	// Summary is a SHORT, human-readable description of what the renewal changes in this file (e.g.
	// "+12/-3 lines vs the installed script") — the difference summary a human reads to decide without
	// opening the branch, never the full content. Set by the renewal drift probes.
	Summary string
}

// ErrWrappersStale is the named refusal of a self delivery whose installed root wrappers no longer
// match the repository. It is NOT satisfied — the delivery genuinely did not complete.
var ErrWrappersStale = errors.New("delivery incomplete: installed root wrappers are stale")

// WrapperDriftError is the honest, named failure of a self delivery blocked on stale wrappers: it
// lists which wrappers are stale and the ONE line a human with sudo runs to make them current.
type WrapperDriftError struct {
	Drifts []WrapperDrift
	Fix    string // exactly one line, ready to run (Handarbeit kommt als Skript)
}

func (e *WrapperDriftError) Error() string {
	names := make([]string, len(e.Drifts))
	for i, d := range e.Drifts {
		names[i] = d.Name
	}
	// The FIRST line is self-contained: the motor records a failed stage by its first line only, so
	// which wrappers are stale AND the one-line fix must both live here (point 3 — name the stale
	// script and what to do). The itemized detail follows for anyone reading the whole error.
	var b strings.Builder
	fmt.Fprintf(&b, "delivery incomplete: root wrapper(s) %s are stale (installed under %s no longer match the repository) — the daemon binary was NOT installed, so nothing is half-shipped; run and then resume: %s",
		strings.Join(names, ", "), wrapperInstallDir, e.Fix)
	b.WriteString("\nThese wrappers run as root and are replaced only by a human with sudo, never by the chain:")
	for _, d := range e.Drifts {
		fmt.Fprintf(&b, "\n  - %s: %s (installed %s, expected deploy/%s)", d.Name, d.Reason, d.Installed, d.Name)
	}
	return b.String()
}

// Is lets callers match the sentinel through the concrete error (errors.Is).
func (e *WrapperDriftError) Is(target error) bool { return target == ErrWrappersStale }

// CheckWrapperDrift compares each root wrapper the self repo ships against the installed copy
// (<sbin>/<name>) by content, reading the intended content from the branch BEING DELIVERED
// (deliverBranch) — NOT the shared working tree, which on a resume sits on the standard branch and so
// would hide a run's own change (the false negative measured 2026-08-04). It is a READ-ONLY,
// unprivileged probe. A wrapper the delivering branch does not carry is skipped. Each drift carries the
// delivering-branch content and its sha256 (via DeliveringBranchWrapperDrift), so the same result also
// feeds the working-source renewal question; the gate itself needs only the names. The result is
// sorted by name so the report is stable.
func CheckWrapperDrift(wt, deliverBranch string) ([]WrapperDrift, error) {
	return DeliveringBranchWrapperDrift(wt, deliverBranch)
}

// GuardWrappersCurrent is the self-delivery gate: if any shipped root wrapper drifted from the branch
// BEING DELIVERED, it returns a WrapperDriftError (named, with the one-line fix) so the deliver-dev
// stage fails honestly instead of installing a new binary over stale root scripts. It measures the
// delivering branch (deliverBranch), so a run that changed a root script on its own branch is seen even
// when the shared working tree sits on the standard branch. wt is the checkout, so the fix points at
// that checkout's own `service` CLI — the existing install/repair path (reuse before build), run in one
// line by the human with sudo.
func GuardWrappersCurrent(wt, deliverBranch string) error {
	drifts, err := CheckWrapperDrift(wt, deliverBranch)
	if err != nil {
		return err
	}
	if len(drifts) == 0 {
		return nil
	}
	return &WrapperDriftError{
		Drifts: drifts,
		Fix:    "sudo " + filepath.Join(wt, "service") + " setup",
	}
}

func envOr(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}

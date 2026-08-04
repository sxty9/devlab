package deploy

// The WRITE half of the root-wrapper renewal (the read/prove half is wrappers.go). A change that
// touches one of the four root wrappers under /usr/local/sbin used to stall the whole chain until a
// human copied the file by hand — three times on 01./02.08.2026. This closes that gap WITHOUT moving
// the security boundary the wrappers themselves protect (E §7.4: the daemon runs AI-written code as
// an unprivileged user; could a run rewrite a root wrapper, a run could make itself root).
//
// The boundary is held by three facts, none of which a run can bend:
//
//  1. ONLY MERGED CONTENT IS A SOURCE. A renewal installs the wrapper exactly as it stands on the
//     repository's STANDARD BRANCH — content that traversed the full chain and was merged through a
//     protected pull request. MainWrapperDrift reads it from committed history (origin/<default>),
//     never from a branch, a working tree, or a file a run produced. A run cannot put bytes on the
//     standard branch by itself (it is protected), so it cannot choose what a renewal installs.
//
//  2. THE APPROVAL IS SINGLE-USE AND CONTENT-PINNED, AND A RUN CANNOT FORGE IT. The user approves a
//     named file with a named checksum. That approval lives in the daemon-owned question pool under
//     the state root (devlab:devlab, mode 0750); an agent run is a DIFFERENT OS account confined to
//     <state>/workspaces/<user>, so it can neither write the approval nor stage the grant the root
//     tool reads. The daemon renders the approved (name, sha) into a run-unwritable grant file next
//     to the merged content it re-read from the standard branch.
//
//  3. THE ROOT TOOL RE-VERIFIES EVERYTHING ITSELF. `devlab-install --renew-wrapper` (the only path
//     that writes /usr/local/sbin) trusts nothing the caller says: it re-checks that the name is one
//     of the four (the list lives in the tool), that the grant and content sit in a run-unwritable
//     place, that the content hashes to the approved checksum, and that the approval was not used
//     before — then it backs up the previous file and records who approved what. See deploy/devlab-install.
//
// This is the delta from the prior order, which built the ASK and REFUSED the write: back then the
// write looked like "let a run inject a root script". It is safe now because the content is pinned to
// merged history a run cannot author (1) and to an approval a run cannot forge (2), and root re-proves
// both (3) — so a run can neither pick the installed bytes nor grant itself the permission.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"devlab/backend/internal/git"
)

// WrapperContentAt yields the intended content of one renewable root wrapper (deploy/<name>) from a
// particular SOURCE, plus whether that source carries the wrapper at all. It is the ONE thing that
// differs between the two renewal sources — the merged (standard-branch) content and this run's own
// working-branch content — so the drift scan and the write path are each written once and reused for
// both (Keine Redundanz). label names the source in the human-readable drift reason.
type WrapperContentAt struct {
	label string
	read  func(name string) (content []byte, ok bool)
}

// MergedWrapperContent reads each wrapper from the repository's STANDARD BRANCH — the merged content,
// from committed history via origin/<default> (falling back to the local default branch). This is the
// source for a drifted-installed-script renewal: content that traversed the full chain and merged
// through a protected pull request, which a run cannot author by itself.
func MergedWrapperContent(wt string) WrapperContentAt {
	def := git.DefaultBranch(wt)
	return WrapperContentAt{
		label: "the standard branch",
		read:  func(name string) ([]byte, bool) { return mergedWrapperContent(wt, def, name) },
	}
}

// WorkingWrapperContent reads each wrapper from THIS run's own working branch (the working tree the
// run committed its change onto). This is the source when the run ITSELF changed a root wrapper: the
// content is not yet merged, so it is offered from the run's branch instead of the standard branch.
//
// SAFETY — why a run-authored source is admissible here (task point 3): unlike the merged source, a
// run CAN choose these bytes, so the human — never a merge — is the gate. That gate holds because the
// approval is bound to EXACTLY this content by its sha256 (RenewApprovedWrapper re-reads and refuses
// any other bytes), it is SINGLE-USE and lives in a daemon-owned place a run cannot write (the
// question pool and the staged grant dir), the root tool RE-VERIFIES name, sha, provenance and
// single-use itself before it writes, and a branch that changes AFTER the approval no longer hashes to
// the approved sha, so it installs nothing. Those four facts are why the human may approve one specific
// content the run wrote without opening a hole a run could widen on its own.
func WorkingWrapperContent(wt string) WrapperContentAt {
	return WrapperContentAt{
		label: "this run's working branch",
		read:  func(name string) ([]byte, bool) { return workingWrapperContent(wt, name) },
	}
}

// MainWrapperDrift compares each renewable root wrapper's INSTALLED copy (<sbin>/<name>,
// world-readable) against the copy on the repository's STANDARD BRANCH (the merged content). Every
// reported drift carries the merged content and its sha256, so the caller can offer EXACTLY that
// content for the user's approval. A read-only, unprivileged probe throughout.
func MainWrapperDrift(wt string) ([]WrapperDrift, error) {
	return driftAgainstInstalled(MergedWrapperContent(wt))
}

// WorkingWrapperDrift is the SIBLING probe for the case the chain lacked: the run itself changed a
// root wrapper, so the source of the renewal is THIS run's working branch, not the standard branch.
// It reports each installed wrapper that differs from the run's working-tree deploy/<name> and pins
// the offered content to the working-tree copy's sha256 — the exact (file, checksum) set the same
// wrapper-renewal question offers for approval. When the installed scripts already match the standard
// branch (so MainWrapperDrift is empty) but the run changed one, THIS is what turns a dead-end halt
// into an answerable approval question (task point 1).
func WorkingWrapperDrift(wt string) ([]WrapperDrift, error) {
	return driftAgainstInstalled(WorkingWrapperContent(wt))
}

// driftAgainstInstalled scans every renewable wrapper, comparing the source's intended content against
// the installed copy under <sbin>, and reports each mismatch with the intended content and its sha256.
// A wrapper the source does not carry is skipped (nothing to install from it). The result is sorted by
// name so the report is stable. It is the shared body of BOTH drift probes above.
func driftAgainstInstalled(src WrapperContentAt) ([]WrapperDrift, error) {
	var drifts []WrapperDrift
	for _, name := range selfWrappers {
		want, ok := src.read(name)
		if !ok {
			continue // this source does not carry the wrapper — nothing to renew from it
		}
		wantSHA := sha256hex(want)
		installedPath := filepath.Join(wrapperInstallDir, name)
		got, err := os.ReadFile(installedPath)
		if err != nil {
			if os.IsNotExist(err) {
				drifts = append(drifts, WrapperDrift{Name: name, Installed: installedPath,
					Reason: "not installed", WantSHA: wantSHA, WantContent: want,
					Summary: fmt.Sprintf("new script (%d lines) — not currently installed", lineCount(want))})
				continue
			}
			return nil, fmt.Errorf("read installed wrapper %s: %w", installedPath, err)
		}
		if sha256.Sum256(want) != sha256.Sum256(got) {
			drifts = append(drifts, WrapperDrift{Name: name, Installed: installedPath,
				Reason: "installed copy differs from " + src.label, WantSHA: wantSHA, WantContent: want,
				Summary: lineChangeSummary(got, want)})
		}
	}
	sort.Slice(drifts, func(i, j int) bool { return drifts[i].Name < drifts[j].Name })
	return drifts, nil
}

// mergedWrapperContent reads deploy/<name> from the standard branch, byte-for-byte. It tries the
// remote-tracking default branch first (the true merged stand), then the local default branch, so a
// checkout that has the branch but not the remote ref still resolves. The bytes equal the committed
// blob, so their sha256 equals that of a file installed verbatim from it.
func mergedWrapperContent(wt, def, name string) ([]byte, bool) {
	rel := "deploy/" + name
	for _, ref := range []string{"origin/" + def, def} {
		if b, ok := git.FileAtRefBytes(wt, ref, rel); ok {
			return b, true
		}
	}
	return nil, false
}

// workingWrapperContent reads deploy/<name> from the run's WORKING TREE — the branch the run committed
// its change onto, checked out at delivery time. These are the very bytes the drift probe compared
// against the installed copy, so the sha256 offered for approval equals what a renewal would install.
func workingWrapperContent(wt, name string) ([]byte, bool) {
	b, err := os.ReadFile(filepath.Join(wt, "deploy", name))
	if err != nil {
		return nil, false
	}
	return b, true
}

func sha256hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// lineCount reports how many lines a script has (a trailing newline does not count as an extra line).
func lineCount(b []byte) int {
	s := string(b)
	if s == "" {
		return 0
	}
	n := strings.Count(s, "\n")
	if !strings.HasSuffix(s, "\n") {
		n++
	}
	return n
}

// lineChangeSummary is the SHORT "+A/-R lines" description of the change from the installed script to
// the renewal's intended content — the difference summary a human reads to decide without opening the
// branch (task point 4), never the full content. It compares the two as multisets of lines: a line
// present more often in the new content counts as added, less often as removed. This over-counts a
// moved line (it shows as one add and one remove), but it is honest about scale and never leaks the
// content itself, which is exactly what a summary needs.
func lineChangeSummary(oldContent, newContent []byte) string {
	counts := map[string]int{}
	for _, l := range strings.Split(string(oldContent), "\n") {
		counts[l]--
	}
	for _, l := range strings.Split(string(newContent), "\n") {
		counts[l]++
	}
	added, removed := 0, 0
	for _, c := range counts {
		if c > 0 {
			added += c
		} else if c < 0 {
			removed += -c
		}
	}
	if added == 0 && removed == 0 {
		return "content differs (same lines, reordered) vs the installed script"
	}
	return fmt.Sprintf("+%d/-%d lines vs the installed script", added, removed)
}

// InstalledWrapperMatches reports whether the wrapper installed under <sbin>/<name> already hashes to
// sha (hex). The daemon uses it to make a renewal idempotent: a file already at the approved checksum
// is not renewed again, so a retry after a partial renewal never asks the root tool to spend a
// single-use approval a second time. A missing or unreadable installed file is not a match.
func InstalledWrapperMatches(name, sha string) bool {
	got, err := os.ReadFile(filepath.Join(wrapperInstallDir, name))
	if err != nil {
		return false
	}
	return sha256hex(got) == sha
}

// WrapperRenewer is the seam over the root write half — `sudo -n devlab-install --renew-wrapper
// <name> <content-file> <grant-file>`. The production implementation is SudoWrapperRenewer; tests
// substitute a fake that records the call.
type WrapperRenewer interface {
	Renew(ctx context.Context, name, contentPath, grantPath string) error
}

// RenewApprovedWrapper is the daemon side of ONE approved renewal. It re-reads the wrapper from the
// SAME source the approval was built from (src — merged standard branch or the run's working branch),
// refuses if that content no longer matches the sha the user approved (the approval is for exactly one
// content), stages the content and a run-unwritable grant under grantDir, and calls the root tool.
// grantDir MUST be a daemon-owned, run-unwritable directory (the caller passes
// <state>/mercury/wrapper-grants); the bytes a run could reach are never a source of what root writes.
//
// The re-read against src is what makes "a branch that changed after the approval installs nothing"
// hold for BOTH sources: whether the approved content came from the standard branch or the run's own
// working branch, a later change to that source no longer hashes to approvedSHA and is refused here —
// so a run can never swap the approved bytes for others after the human said yes (task point 3).
func RenewApprovedWrapper(ctx context.Context, r WrapperRenewer, src WrapperContentAt, grantDir, name, approvedSHA, approvalID, approvedBy, approvedAt string) error {
	want, ok := src.read(name)
	if !ok {
		return fmt.Errorf("renew %s: %s no longer carries deploy/%s — nothing to install", name, src.label, name)
	}
	if got := sha256hex(want); got != approvedSHA {
		// The source content moved since the user approved a specific checksum — the approval is no
		// longer for THIS content, so it installs nothing (task point 3). The next delivery re-asks.
		return fmt.Errorf("renew %s: %s content is now %s, not the approved %s — refusing (approval is for one content)", name, src.label, got, approvedSHA)
	}
	contentPath, grantPath, err := writeWrapperGrant(grantDir, name, approvedSHA, approvalID, approvedBy, approvedAt, want)
	if err != nil {
		return fmt.Errorf("renew %s: stage grant: %w", name, err)
	}
	if err := r.Renew(ctx, name, contentPath, grantPath); err != nil {
		return fmt.Errorf("renew %s: root install refused: %w", name, err)
	}
	return nil
}

// writeWrapperGrant stages the merged content and a minimal key=value grant the root tool reads. The
// grant is deliberately NOT the question JSON: the root tool must parse it without a JSON reader, and
// it must carry only what root verifies (the approved name + checksum + who/when). Both files are
// written 0640 under grantDir (daemon-owned, run-unwritable), each with a per-approval name so a
// second, concurrent renewal never overwrites another's staged content.
func writeWrapperGrant(dir, name, sha, approvalID, approvedBy, approvedAt string, content []byte) (contentPath, grantPath string, err error) {
	if err = os.MkdirAll(dir, 0o750); err != nil {
		return "", "", err
	}
	base := approvalID + "." + name
	contentPath = filepath.Join(dir, base+".content")
	grantPath = filepath.Join(dir, base+".grant")
	if err = os.WriteFile(contentPath, content, 0o640); err != nil {
		return "", "", err
	}
	grant := strings.Join([]string{
		"name=" + name,
		"sha256=" + sha,
		"approvalId=" + approvalID,
		"approvedBy=" + sanitizeGrantValue(approvedBy),
		"approvedAt=" + sanitizeGrantValue(approvedAt),
		"",
	}, "\n")
	if err = os.WriteFile(grantPath, []byte(grant), 0o640); err != nil {
		return "", "", err
	}
	return contentPath, grantPath, nil
}

// sanitizeGrantValue keeps a grant value on ONE line (the root tool reads line by line). Newlines
// would let a value forge a second key; they are replaced with spaces.
func sanitizeGrantValue(s string) string {
	return strings.NewReplacer("\n", " ", "\r", " ").Replace(strings.TrimSpace(s))
}

// SudoWrapperRenewer is the production WrapperRenewer: `sudo -n devlab-install --renew-wrapper …`.
// The sudoers grant already pins the devlab service to devlab-install (no arg constraint), and the
// daemon — never the agent's run shell — is what invokes it.
type SudoWrapperRenewer struct{}

func (SudoWrapperRenewer) Renew(ctx context.Context, name, contentPath, grantPath string) error {
	args := []string{"-n", installWrapper, "--renew-wrapper", name, contentPath, grantPath}
	out, err := exec.CommandContext(ctx, "sudo", args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w: %s", err, tail(strings.TrimSpace(string(out)), 2000))
	}
	return nil
}

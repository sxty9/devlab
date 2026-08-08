package deploy

// The WRITE half of the root-wrapper renewal (the read/prove half is wrappers.go). A change that
// touches one of the four root wrappers under /usr/local/sbin used to stall the whole chain until a
// human copied the file by hand — three times on 01./02.08.2026. This closes that gap WITHOUT moving
// the security boundary the wrappers themselves protect (E §7.4: the daemon runs AI-written code as
// an unprivileged user; could a run rewrite a root wrapper, a run could make itself root).
//
// The boundary is held by three facts, none of which a run can bend:
//
//  1. THE SOURCE IS COMMITTED HISTORY — the branch BEING DELIVERED (this run's own task branch), read
//     from its committed ref (never the shared working tree). It is the ONE reference the self-delivery
//     gate proves the install against, so an approval that installs it always resolves the gate — there
//     is deliberately no second stand to contradict it (an earlier design also measured the stack tip
//     and drove a pendulum; task point 2). The delivering branch is cut from the stack tip, so it also
//     carries any wrapper change an open delivery stacked before this run, whether or not the run itself
//     touched a root script. The run CAN author these bytes, so the human — not a merge — is the gate;
//     that is admissible under the four bindings spelled out below.
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

// DeliveringBranchContent reads each wrapper from the branch BEING DELIVERED — this run's own task
// branch tip, the tree that is actually shipped. It reads the committed branch ref (local first, since
// the run just committed onto it; origin/<branch> as the fallback for a freshly-opened checkout that
// only fetched it), and falls back to the working tree only when no branch ref resolves. This is what
// makes the gate measure the delivering branch instead of the shared working tree (which sits on the
// standard branch on a resume), so a run that changed a root script is SEEN (task point 1).
//
// SAFETY — why a run-authored source is admissible here (task point 3): unlike the stack-tip source, a
// run CAN choose these bytes, so the human — never a merge — is the gate. That gate holds because the
// approval is bound to EXACTLY this content by its sha256 (RenewApprovedWrapperSet re-reads and refuses
// any other bytes), it is SINGLE-USE and lives in a daemon-owned place a run cannot write (the
// question pool and the staged grant dir), the root tool RE-VERIFIES name, sha, provenance and
// single-use itself before it writes, and a branch that changes AFTER the approval no longer hashes to
// the approved sha, so it installs nothing. Those four facts are why the human may approve one specific
// content the run wrote without opening a hole a run could widen on its own.
func DeliveringBranchContent(wt, branch string) WrapperContentAt {
	ref := firstRef(wt, branch, "origin/"+branch)
	return WrapperContentAt{
		label: "this run's own branch",
		read: func(name string) ([]byte, bool) {
			if ref != "" {
				return refWrapper(wt, ref, name)
			}
			// No delivering-branch ref resolved — read the working tree, which in the normal path IS this
			// branch's checkout. This is the last resort, not the primary source; the branch ref above is
			// what makes the gate correct when the shared tree sits on the standard branch.
			b, err := os.ReadFile(filepath.Join(wt, "deploy", name))
			if err != nil {
				return nil, false
			}
			return b, true
		},
	}
}

// DeliveringBranchWrapperDrift compares each renewable root wrapper's INSTALLED copy against the
// branch BEING DELIVERED (this run's own task branch). It is the ONE drift probe of the delivery chain:
// both the self-delivery gate (GuardWrappersCurrent) and the wrapper-renewal question read it, so the
// gate and the question measure the SAME stand and an approval always resolves the gate (task point 2 —
// there is no second yardstick to contradict it). The delivering branch is cut from the stack tip, so
// it carries BOTH a wrapper the run itself changed AND one an open delivery stacked before it; reading
// its committed ref rather than the shared working tree is what lets the probe SEE a run's change even
// when the tree sits on the standard branch on a resume (the 2026-08-04 false negative). Every reported
// drift carries the branch content and its sha256, the exact set the question pins for approval.
func DeliveringBranchWrapperDrift(wt, branch string) ([]WrapperDrift, error) {
	return driftAgainstInstalled(DeliveringBranchContent(wt, branch))
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

// firstRef returns the first of the given git refs that resolves to a commit in wt, or "" when none
// does. It picks ONE stand up front so every wrapper of a source is read from the same ref (never a
// mix of a branch and the working tree), which is what keeps the offered checksums self-consistent.
func firstRef(wt string, refs ...string) string {
	for _, ref := range refs {
		if ref != "" && ref != "origin/" && git.RefExists(wt, ref) {
			return ref
		}
	}
	return ""
}

// refWrapper reads deploy/<name> from a committed git ref, byte-for-byte. The bytes equal the
// committed blob, so their sha256 equals that of a file installed verbatim from it. An empty ref (no
// stand resolved) carries no wrapper.
func refWrapper(wt, ref, name string) ([]byte, bool) {
	if ref == "" {
		return nil, false
	}
	return git.FileAtRefBytes(wt, ref, "deploy/"+name)
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

// ApprovedWrapper is ONE file of an approval: the renewable wrapper name and the sha256 the user
// approved for it. A single approval carries a SET of these, and the whole set is renewed as one unit.
type ApprovedWrapper struct {
	Name string
	SHA  string
}

// WrapperFile is ONE staged (name, content-path) pair the root tool installs — the name it validates
// against its own renewable list and the run-unwritable file holding the approved bytes.
type WrapperFile struct {
	Name        string
	ContentPath string
}

// WrapperRenewer is the seam over the root write half — `sudo -n devlab-install --renew-wrapper
// <grant-file> <name> <content-file> [<name> <content-file> ...]`. ONE call carries the WHOLE approval
// (all its files), so the root tool installs them atomically and spends the single-use approval exactly
// once; the old per-file seam let the first file's ledger entry poison the rest of its own approval
// (the 2026-08-08 half-renewal). The production implementation is SudoWrapperRenewer; tests substitute
// a fake that records the call.
type WrapperRenewer interface {
	Renew(ctx context.Context, grantPath string, files []WrapperFile) error
}

// RenewApprovedWrapperSet is the daemon side of ONE approved renewal that may cover SEVERAL files. It
// re-reads every wrapper from the SAME source the approval was built from (src — the delivering branch),
// refuses the WHOLE set if ANY file's content no longer matches the sha the user approved (the approval
// is for exactly one content per file, and it is all-or-none — task points 3/4), stages every content
// file plus one combined run-unwritable grant under grantDir, and calls the root tool ONCE. grantDir
// MUST be a daemon-owned, run-unwritable directory (the caller passes <state>/mercury/wrapper-grants);
// the bytes a run could reach are never a source of what root writes.
//
// The re-read against src is what makes "a branch that changed after the approval installs nothing" hold:
// a later change to the source no longer hashes to the approved sha and is refused here BEFORE any write,
// so a run can never swap the approved bytes for others after the human said yes (task point 3). Because
// the refusal is checked for every file before staging, a single changed file installs none of the set.
func RenewApprovedWrapperSet(ctx context.Context, r WrapperRenewer, src WrapperContentAt, grantDir, approvalID, approvedBy, approvedAt string, wrappers []ApprovedWrapper) error {
	if len(wrappers) == 0 {
		return nil // nothing approved to install
	}
	// Re-read and re-verify EVERY file first — any one that moved refuses the whole set (all-or-none).
	contents := make([][]byte, len(wrappers))
	for i, w := range wrappers {
		want, ok := src.read(w.Name)
		if !ok {
			return fmt.Errorf("renew %s: %s no longer carries deploy/%s — nothing to install (whole approval refused)", w.Name, src.label, w.Name)
		}
		if got := sha256hex(want); got != w.SHA {
			return fmt.Errorf("renew %s: %s content is now %s, not the approved %s — refusing the whole approval (approval is for one content)", w.Name, src.label, got, w.SHA)
		}
		contents[i] = want
	}
	files, grantPath, err := stageWrapperGrantSet(grantDir, approvalID, approvedBy, approvedAt, wrappers, contents)
	if err != nil {
		return fmt.Errorf("renew approval %s: stage grant: %w", approvalID, err)
	}
	if err := r.Renew(ctx, grantPath, files); err != nil {
		return fmt.Errorf("renew approval %s: root install refused: %w", approvalID, err)
	}
	return nil
}

// stageWrapperGrantSet stages the approved content of every file plus ONE combined key=value grant the
// root tool reads. The grant is deliberately NOT the question JSON: the root tool must parse it without a
// JSON reader, and it carries only what root verifies (the approvalId + who/when header, then one
// `sha256 <name> <sha>` line per file). Content files are written 0640 under grantDir (daemon-owned,
// run-unwritable), each named per (approval, wrapper) so a second, concurrent renewal never overwrites
// another's staged content; the grant is named per approval.
func stageWrapperGrantSet(dir, approvalID, approvedBy, approvedAt string, wrappers []ApprovedWrapper, contents [][]byte) (files []WrapperFile, grantPath string, err error) {
	if err = os.MkdirAll(dir, 0o750); err != nil {
		return nil, "", err
	}
	lines := []string{
		"approvalId=" + approvalID,
		"approvedBy=" + sanitizeGrantValue(approvedBy),
		"approvedAt=" + sanitizeGrantValue(approvedAt),
	}
	files = make([]WrapperFile, len(wrappers))
	for i, w := range wrappers {
		contentPath := filepath.Join(dir, approvalID+"."+w.Name+".content")
		if err = os.WriteFile(contentPath, contents[i], 0o640); err != nil {
			return nil, "", err
		}
		// name and sha are constrained tokens (no spaces), so a space-separated line parses safely in the
		// root tool's awk lookup. The content path is passed as a positional argument, never inside the
		// grant, so a path with spaces never reaches this line.
		lines = append(lines, "sha256 "+w.Name+" "+w.SHA)
		files[i] = WrapperFile{Name: w.Name, ContentPath: contentPath}
	}
	lines = append(lines, "")
	grantPath = filepath.Join(dir, approvalID+".grant")
	if err = os.WriteFile(grantPath, []byte(strings.Join(lines, "\n")), 0o640); err != nil {
		return nil, "", err
	}
	return files, grantPath, nil
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

func (SudoWrapperRenewer) Renew(ctx context.Context, grantPath string, files []WrapperFile) error {
	args := []string{"-n", installWrapper, "--renew-wrapper", grantPath}
	for _, f := range files {
		args = append(args, f.Name, f.ContentPath)
	}
	out, err := exec.CommandContext(ctx, "sudo", args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w: %s", err, tail(strings.TrimSpace(string(out)), 2000))
	}
	return nil
}

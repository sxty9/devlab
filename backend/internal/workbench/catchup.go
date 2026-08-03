// Catch-up (the "ein ruhender Zweig wird nachgezogen" axiom): a branch cut from an older layer of
// the stack is measured against the CURRENT tip and, when it sits behind, rebased onto it BEFORE
// more work is built on it. Measuring is read-only (StackPosition, Reaches); the rebase itself
// (CatchUpOnto) NEVER leaves a half-caught-up branch — a conflict, or any failure, is fully aborted
// and the branch is restored to exactly its previous commit.
package workbench

import (
	"context"
	"fmt"
	"strings"
)

// StackPosition measures where THIS bench's branch sits relative to tipBranch — the current stack
// tip (deliver.NextPRBase). It resolves the tip (origin/<tipBranch> when the remote carries it,
// else the local branch), the fork commit the branch and the tip still share, and how many commits
// the tip carries beyond that fork (the "distance behind the tip"). exists=false when the branch or
// the tip cannot be resolved — nothing to measure, never an error. Read-only: it mutates nothing and
// fetches nothing (the caller refreshed the remote refs already).
func (b *Bench) StackPosition(ctx context.Context, tipBranch string) (tipCommit, forkCommit string, behind int, exists bool, err error) {
	if !branchRe.MatchString(tipBranch) || strings.HasPrefix(tipBranch, "-") {
		return "", "", 0, false, nil
	}
	if !b.refExists(ctx, "refs/heads/"+b.branch) {
		return "", "", 0, false, nil // the branch carries no local work — nothing to measure
	}
	tipRef := ""
	switch {
	case b.refExists(ctx, "refs/remotes/origin/"+tipBranch):
		tipRef = "refs/remotes/origin/" + tipBranch
	case b.refExists(ctx, "refs/heads/"+tipBranch):
		tipRef = "refs/heads/" + tipBranch
	default:
		return "", "", 0, false, nil // the tip does not resolve — nothing to compare against
	}
	tip, err := b.ex.RevParse(ctx, b.repo, tipRef)
	if err != nil {
		return "", "", 0, false, err
	}
	fork, err := b.gitRO(ctx, "merge-base", tipRef, "refs/heads/"+b.branch)
	if err != nil {
		return "", "", 0, false, err
	}
	// behind = commits the tip carries beyond the branch (branch..tip) — how many layers landed since
	// the branch forked.
	behind, err = b.countAhead(ctx, "refs/heads/"+b.branch, tipRef)
	if err != nil {
		return "", "", 0, false, err
	}
	return strings.TrimSpace(tip), strings.TrimSpace(fork), behind, true, nil
}

// Reaches reports whether `commit` is contained in `ref` (an ancestor of, or equal to, it) — the
// read-only "did this delivery land in that layer?" probe. A non-commit value is (false, nil); a
// real failure (a bad object) is an error, never a guessed false.
func (b *Bench) Reaches(ctx context.Context, commit, ref string) (bool, error) {
	if strings.TrimSpace(commit) == "" || strings.HasPrefix(commit, "-") {
		return false, nil
	}
	if _, err := b.gitRO(ctx, "merge-base", "--is-ancestor", commit, ref); err != nil {
		// exit 1 = not an ancestor; anything else is a real failure. Tell them apart by proving the
		// object exists, so a bad ref never reads as "not contained".
		if _, cerr := b.gitRO(ctx, "cat-file", "-e", commit+"^{commit}"); cerr != nil {
			return false, fmt.Errorf("workbench: %s is not a commit in this repository: %w", commit, cerr)
		}
		return false, nil
	}
	return true, nil
}

// CatchUpReport names what a catch-up rebase did — honestly, per outcome.
type CatchUpReport struct {
	Rebased       bool     // the branch was replayed onto the tip (its head moved)
	Conflicted    bool     // the rebase conflicted; it was FULLY aborted, the branch is exactly as before
	ConflictFiles []string // best-effort names of the conflicting files
	OldHead       string   // the branch head before the catch-up
	NewHead       string   // the branch head after (equals OldHead on a no-op or an aborted conflict)
}

// CatchUpOnto rebases this bench's branch — its own commits — onto tipBranch, the current stack tip,
// so a branch cut from an older layer sits on the newest one. The bench must already be ON its
// branch (Prepare established that and left the working tree there). It NEVER leaves a
// half-caught-up branch: a conflict, or ANY failure during the rebase, is fully aborted and the
// branch is restored to EXACTLY its previous commit, with no rebase state left in the working tree.
// A branch that already contains the tip is a clean no-op (Rebased=false, head unchanged). Unlike a
// fold-in this DOES rewrite the branch's own commits onto the new base — that is the point of a
// catch-up — but only ever forward onto the tip, never a reset that drops work.
func (b *Bench) CatchUpOnto(ctx context.Context, tipBranch string) (CatchUpReport, error) {
	var rep CatchUpReport
	if !branchRe.MatchString(tipBranch) || strings.HasPrefix(tipBranch, "-") {
		return rep, fmt.Errorf("workbench: invalid catch-up tip %q", tipBranch)
	}
	// Fetch so the CURRENT tip is rebased onto, not a stale remote-tracking ref.
	if err := b.ex.Fetch(ctx, b.repo, b.token); err != nil {
		return rep, fmt.Errorf("workbench: fetch before catch-up: %w", err)
	}
	tipRef := ""
	switch {
	case b.refExists(ctx, "refs/remotes/origin/"+tipBranch):
		tipRef = "origin/" + tipBranch
	case b.refExists(ctx, "refs/heads/"+tipBranch):
		tipRef = tipBranch
	default:
		return rep, fmt.Errorf("workbench: catch-up tip %q exists neither locally nor on origin", tipBranch)
	}
	old, err := b.Head(ctx)
	if err != nil {
		return rep, err
	}
	rep.OldHead, rep.NewHead = old, old

	// Start from a clean, on-branch state: clear any leftover rebase state and uncommitted edits
	// (commits are untouched by CleanUntracked), then be sure the working tree is on the branch.
	_, _ = b.gitUser(ctx, "rebase", "--abort") // harmless when no rebase is in progress
	if err := b.CleanUntracked(ctx); err != nil {
		return rep, fmt.Errorf("workbench: clean before catch-up: %w", err)
	}
	if _, err := b.gitUser(ctx, "checkout", b.branch); err != nil {
		return rep, fmt.Errorf("workbench: switch to %s before catch-up: %w", b.branch, err)
	}

	if _, rerr := b.gitUser(ctx, "-c", "user.name=mercury-run", "-c", "user.email=mercury-run@local",
		"rebase", tipRef); rerr != nil {
		// A conflict (or any rebase failure): NAME the conflicting files BEFORE aborting (they vanish
		// with the abort), then take the branch fully back to where it was — no half-applied layer.
		rep.Conflicted = true
		rep.ConflictFiles = b.rebaseConflictNames(ctx)
		_, _ = b.gitUser(ctx, "rebase", "--abort")
		// Belt-and-braces: guarantee the exact-as-before invariant even if the abort hiccups.
		_, _ = b.gitUser(ctx, "reset", "--hard", old)
		_, _ = b.gitUser(ctx, "clean", "-fdx")
		if now, herr := b.Head(ctx); herr == nil {
			rep.NewHead = now
		}
		return rep, nil
	}
	now, err := b.Head(ctx)
	if err != nil {
		return rep, err
	}
	rep.NewHead = now
	rep.Rebased = now != old
	// A rebase rewrote the branch's OWN commits onto the new tip, so the remote (if the branch was
	// published in an earlier run) still carries the old history. A later fast-forward Publish would
	// then be REJECTED and fold the old commits back in — silently undoing the catch-up. So the task's
	// own branch is force-updated to match: this is a VORGANGS branch that no other work builds on,
	// never the protected default branch, and a rebase inherently moves it. After this the remote
	// equals the local rebased branch, so the ordinary Publish downstream is a clean no-op.
	if rep.Rebased && b.refExists(ctx, "refs/remotes/origin/"+b.branch) {
		if _, perr := b.ex.PushRefs(ctx, b.repo, b.token, true, b.branch); perr != nil {
			return rep, fmt.Errorf("workbench: publish the caught-up %s onto the tip: %w", b.branch, perr)
		}
	}
	return rep, nil
}

// rebaseConflictNames lists the unmerged files while a rebase sits paused on a conflict — best
// effort, so an empty list still reports the conflict, just without names.
func (b *Bench) rebaseConflictNames(ctx context.Context) []string {
	out, _ := b.gitRO(ctx, "diff", "--name-only", "--diff-filter=U")
	var names []string
	for _, l := range strings.Split(out, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			names = append(names, l)
		}
	}
	return names
}

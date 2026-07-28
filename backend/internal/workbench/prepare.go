// Prepare + hygiene: establishing the workbench NEVER discards committed work (REQ-023.1),
// a fold conflict is named without rolling anything back (REQ-023.2), and cleaning touches
// only unversioned remnants and uncommitted edits — never commits (REQ-023.4).
package workbench

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strconv"
	"strings"

	"devlab/backend/internal/workspace"
)

// PrepareResult reports what Prepare did — honestly, per dimension.
type PrepareResult struct {
	// Created: the workbench existed NEITHER locally nor on the remote and was created from
	// the default branch — the very first establishment in this repository (F8 semantics).
	Created bool
	// FoldedRemote: the pushed workbench state is contained in the local branch after this
	// call — folded into a kept local branch, or used as the creation base.
	FoldedRemote bool
	// FoldedDefault: the default branch state was incorporated BY PREPARE — only the
	// creation-from-default case. The running fold of the default branch is FoldInDefault's
	// own, separately reported act.
	FoldedDefault bool
	// Conflicted: folding the pushed state into the kept local branch conflicted. Nothing
	// was rolled back; the local state is exactly as before (REQ-023.2).
	Conflicted    bool
	ConflictFiles []string
	// Recovered counts committed-but-unpublished commits an interrupted predecessor left on
	// the local branch — retained here and republished by the next Publish (K-1; F8 req 5).
	Recovered int
	// Orphans are commits that were reachable from NO ref at all, secured under rescue refs
	// (REQ-023.5).
	Orphans []Orphan
	// Head is the workbench tip after preparation — the nameable state (REQ-022.3).
	Head string
}

// Prepare makes the workbench current WITHOUT ever discarding local work: a locally present
// branch STAYS and the remote is folded IN (merge); otherwise the branch comes from the
// remote, else from the default branch. NEVER `checkout -f -B`, NEVER a reset onto origin
// (REQ-023.1); a conflict is named, nothing is rolled back, and the service log records it.
//
// Getting onto the branch includes the CleanUntracked hygiene (an aborted run's uncommitted
// edits and untracked leftovers would block the switch and the fold) — committed work is
// untouched by it, per its own contract. Orphan rescue (REQ-023.5) runs as part of the
// preparation; its bookkeeping is best-effort and never sinks the run.
func (b *Bench) Prepare(ctx context.Context) (PrepareResult, error) {
	var res PrepareResult
	if err := b.ex.Fetch(ctx, b.repo, b.token); err != nil {
		return res, fmt.Errorf("fetch: %w", err)
	}
	def, err := b.defaultBranch()
	if err != nil {
		return res, err
	}
	// Hygiene BEFORE switching: drops only an aborted run's uncommitted leftovers — commits
	// are untouched (REQ-023.4), so nothing committed can be lost here.
	if err := b.CleanUntracked(ctx); err != nil {
		return res, fmt.Errorf("clean: %w", err)
	}

	local := b.refExists(ctx, "refs/heads/"+Branch)
	remote := b.refExists(ctx, "refs/remotes/origin/"+Branch)
	switch {
	case !local && remote:
		// No local branch — nothing committed here to protect. Grow onto the pushed state.
		if err := b.ex.CreateBranch(ctx, b.repo, Branch, "origin/"+Branch); err != nil {
			return res, fmt.Errorf("create %s from origin: %w", Branch, err)
		}
		res.FoldedRemote = true
	case !local && !remote:
		// The very first establishment: derive the workbench from the default branch.
		start := "origin/" + def
		if !b.refExists(ctx, "refs/remotes/origin/"+def) {
			start = def
		}
		if err := b.ex.CreateBranch(ctx, b.repo, Branch, start); err != nil {
			return res, fmt.Errorf("create %s from %s: %w", Branch, start, err)
		}
		res.Created, res.FoldedDefault = true, true
	default:
		// The local branch exists — it STAYS, whatever the remote says. An interrupted
		// predecessor may have left committed-but-unpublished work on it; that is exactly
		// what a reset onto origin would destroy, so no such reset exists here.
		if err := b.ex.Checkout(ctx, b.repo, Branch); err != nil {
			return res, fmt.Errorf("switch to %s: %w", Branch, err)
		}
		res.Recovered = b.unpublishedCount(ctx, def, remote)
		if res.Recovered > 0 {
			log.Printf("workbench: %s — recovered %d committed-but-unpublished commit(s) an interrupted run left on %s; retained and republishing", b.repo, res.Recovered, Branch)
		}
		if remote {
			// Fold the pushed state IN — never reset onto it. "Already up to date" (local is
			// ahead of or equal to the remote — the interrupted-run case) is a clean no-op
			// that keeps the local commits.
			if ferr := b.ex.FoldInBranch(ctx, b.repo, Branch); ferr != nil {
				if !errors.Is(ferr, workspace.ErrMergeConflict) {
					return res, fmt.Errorf("fold origin/%s: %w", Branch, ferr)
				}
				res.Conflicted = true
				res.ConflictFiles = b.conflictNames(ctx, "refs/heads/"+Branch, "refs/remotes/origin/"+Branch)
				log.Printf("workbench: %s — pushed %s did not fold cleanly into the local state (conflicts: %s); kept local, nothing rolled back", b.repo, Branch, nameList(res.ConflictFiles))
			} else {
				res.FoldedRemote = true
			}
		}
	}

	// Rescue commits no ref reaches anymore (REQ-023.5) — best-effort bookkeeping that never
	// sinks the preparation.
	if orphans, oerr := b.CollectOrphans(ctx); oerr != nil {
		log.Printf("workbench: %s — orphan sweep failed (non-fatal): %v", b.repo, oerr)
	} else {
		res.Orphans = orphans
	}
	if head, herr := b.Head(ctx); herr == nil {
		res.Head = head
	}
	return res, nil
}

// FoldReport names what a default-branch fold-in brought.
type FoldReport struct {
	Folded        bool     // the fold moved the tip (new commits arrived); an already-contained default is a clean no-op
	Conflicted    bool     // the fold conflicted; nothing was rolled back, the workbench is exactly as before
	ConflictFiles []string // best-effort names of the conflicted files
	Head          string   // the workbench tip after the call
}

// FoldInDefault folds the default branch INTO the workbench — the workbench only ever grows
// (REQ-022.2). It fetches first so the CURRENT default is folded, requires the working tree
// to be on the workbench (Prepare establishes that), and treats a conflict as non-fatal:
// named, logged, nothing rolled back — the run decides how to proceed (REQ-023.2).
func (b *Bench) FoldInDefault(ctx context.Context) (FoldReport, error) {
	var rep FoldReport
	if err := b.ex.Fetch(ctx, b.repo, b.token); err != nil {
		return rep, fmt.Errorf("fetch: %w", err)
	}
	def, err := b.defaultBranch()
	if err != nil {
		return rep, err
	}
	if cur := b.ex.CurrentBranch(ctx, b.repo); cur != Branch {
		return rep, fmt.Errorf("workbench not prepared: working tree is on %q, want %s", cur, Branch)
	}
	if !b.refExists(ctx, "refs/remotes/origin/"+def) {
		// No published default branch — nothing to fold; honest no-op.
		rep.Head, _ = b.Head(ctx)
		return rep, nil
	}
	before, _ := b.Head(ctx)
	if ferr := b.ex.FoldInBranch(ctx, b.repo, def); ferr != nil {
		if !errors.Is(ferr, workspace.ErrMergeConflict) {
			return rep, fmt.Errorf("fold origin/%s: %w", def, ferr)
		}
		rep.Conflicted = true
		rep.ConflictFiles = b.conflictNames(ctx, "refs/heads/"+Branch, "refs/remotes/origin/"+def)
		rep.Head = before
		log.Printf("workbench: %s — default branch %s does not fold cleanly (conflicts: %s); kept the workbench as-is, nothing rolled back", b.repo, def, nameList(rep.ConflictFiles))
		return rep, nil
	}
	rep.Head, _ = b.Head(ctx)
	rep.Folded = rep.Head != before
	return rep, nil
}

// CleanUntracked removes ONLY untracked/uncommitted files — NEVER commits (REQ-023.4): a
// best-effort merge abort (a killed fold leaves sequencer state), a hard reset TO HEAD
// (which by definition cannot move the branch off any commit) and a clean of untracked and
// ignored files (a stale build artefact can make an agent's verification pass for the wrong
// reason). The branch pointer and all committed history are untouched — this is hygiene,
// not a reset of the accumulated state.
func (b *Bench) CleanUntracked(ctx context.Context) error {
	_, _ = b.gitUser(ctx, "merge", "--abort") // harmless when no merge is in progress
	if _, err := b.gitUser(ctx, "reset", "--hard", "HEAD"); err != nil {
		return fmt.Errorf("reset to HEAD: %w", err)
	}
	if _, err := b.gitUser(ctx, "clean", "-fdx"); err != nil {
		return fmt.Errorf("clean: %w", err)
	}
	return nil
}

// unpublishedCount counts commits reachable from HEAD but from NEITHER the default branch
// NOR (when it exists) the pushed workbench — the committed work an interrupted run left
// that no remote yet carries. Best-effort: a git error yields 0 (the count only reports,
// never gates work).
func (b *Bench) unpublishedCount(ctx context.Context, def string, remoteDev bool) int {
	args := []string{"rev-list", "--count", "HEAD"}
	var not []string
	if b.refExists(ctx, "refs/remotes/origin/"+def) {
		not = append(not, "origin/"+def)
	} else if b.refExists(ctx, "refs/heads/"+def) {
		not = append(not, def)
	}
	if remoteDev {
		not = append(not, "origin/"+Branch)
	}
	if len(not) == 0 {
		return 0 // nothing to measure against — counting all of history would be a lie
	}
	args = append(args, "--not")
	args = append(args, not...)
	out, err := b.gitRO(ctx, args...)
	if err != nil {
		return 0
	}
	n, _ := strconv.Atoi(strings.TrimSpace(out))
	return n
}

// nameList renders a conflict-file list for the service log, honest about an empty one.
func nameList(names []string) string {
	if len(names) == 0 {
		return "unnamed"
	}
	return strings.Join(names, ", ")
}

// The explicit way back (REQ-022.4): discarding the workbench exists ONLY as a deliberate,
// visible human action behind the UI confirmation — the machinery never calls it. Even
// then, the discarded tip is first sheltered under a rescue ref: deliberate discard, zero
// silent loss.
package workbench

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"devlab/backend/internal/model"
)

// ResetToDefault discards the workbench in favor of the default branch. Reachable ONLY through
// the explicit UI path with its confirmation (REQ-022.4) — never called by the machinery, and
// it refuses any non-person actor outright. The previous tip is secured under a rescue ref
// before the branch moves, then the reset is published (the ONE forced push in the system).
func (b *Bench) ResetToDefault(ctx context.Context, by model.Actor) error {
	if by.User == "" || by.Autonomous {
		return errors.New("workbench: reset refused — the workbench is discarded only on a person's explicit request (REQ-022.4), never by the machinery")
	}
	if err := b.ex.Fetch(ctx, b.repo, b.token); err != nil {
		return fmt.Errorf("fetch: %w", err)
	}
	def, err := b.defaultBranch()
	if err != nil {
		return err
	}
	target := "origin/" + def
	if !b.refExists(ctx, "refs/remotes/origin/"+def) {
		target = def
		if !b.refExists(ctx, "refs/heads/"+def) {
			return fmt.Errorf("workbench: default branch %s not found — nothing to reset onto", def)
		}
	}
	targetSHA, err := b.ex.RevParse(ctx, b.repo, target)
	if err != nil {
		return fmt.Errorf("resolve %s: %w", target, err)
	}

	pushRefs := []string{Branch}
	if !b.refExists(ctx, "refs/heads/"+Branch) {
		// No workbench yet — the "reset" simply establishes it at the default tip.
		if err := b.CleanUntracked(ctx); err != nil {
			return fmt.Errorf("clean: %w", err)
		}
		if err := b.ex.CreateBranch(ctx, b.repo, Branch, target); err != nil {
			return fmt.Errorf("create %s at %s: %w", Branch, target, err)
		}
	} else {
		oldTip, err := b.ex.RevParse(ctx, b.repo, "refs/heads/"+Branch)
		if err != nil {
			return fmt.Errorf("resolve %s: %w", Branch, err)
		}
		// Shelter the discarded state first — unless it is already contained in the target
		// (then there is nothing to lose).
		if oldTip != targetSHA {
			if _, cerr := b.gitRO(ctx, "merge-base", "--is-ancestor", oldTip, targetSHA); cerr != nil {
				short := oldTip
				if len(short) > 8 {
					short = short[:8]
				}
				rescue := rescueRefPrefix + time.Now().UTC().Format("20060102-150405") + "-" + short
				if _, err := b.gitUser(ctx, "update-ref", rescue, oldTip); err != nil {
					return fmt.Errorf("rescue the discarded tip: %w", err)
				}
				pushRefs = append(pushRefs, rescue)
			}
		}
		// Get onto the workbench with a clean tree, then move it: the hard reset TO THE
		// RESOLVED COMMIT is the ONE deliberate discard in the system — explicit, actor-bound,
		// rescued above; the automated path never does this (K-1).
		if err := b.CleanUntracked(ctx); err != nil {
			return fmt.Errorf("clean: %w", err)
		}
		if err := b.ex.Checkout(ctx, b.repo, Branch); err != nil {
			return fmt.Errorf("switch to %s: %w", Branch, err)
		}
		if _, err := b.gitUser(ctx, "reset", "--hard", targetSHA); err != nil {
			return fmt.Errorf("move %s to %s: %w", Branch, target, err)
		}
		if _, err := b.gitUser(ctx, "clean", "-fdx"); err != nil {
			return fmt.Errorf("clean after move: %w", err)
		}
	}

	// Publish the reset (and the rescue) atomically — forced, because discarding IS this
	// action's stated purpose; everywhere else the workbench pushes fast-forward only.
	if _, err := b.ex.PushRefs(ctx, b.repo, b.token, true, pushRefs...); err != nil {
		return fmt.Errorf("publish the reset: %w", err)
	}
	log.Printf("workbench: %s — %s reset to %s (%.8s) by %s; previous state sheltered under %s", b.repo, Branch, def, targetSHA, by.User, rescueRefPrefix+"*")
	return nil
}

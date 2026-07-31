// The examined stand of one repository, joined for the chain motor (BEFUND 3): the passive pool
// (runs.AxiomChecks) holds WHICH COMMIT a repository was last examined against per axiom, the
// constitution store holds what those axioms are CALLED, and the ONE renderer
// (mercury.RepoScopeSection) turns both into the prompt section. Nothing here decides anything — the
// join lives at the composition root because that is where both pools are owned.
//
// Without this join the pool had no consumer at all: every prompt fell back to "never examined ⇒
// examine the whole repository", every night, at maximum reasoning effort.
package api

import (
	"context"
	"errors"
	"log"
	"time"

	"devlab/backend/internal/mercury"
	"devlab/backend/internal/runs"
)

// AxiomScope renders the per-repository examined stand for this run's axioms (executor.Deps).
func (d *ChainDeps) AxiomScope(ctx context.Context, repo string, run runs.Run) string {
	if d == nil || d.s == nil || len(run.AxiomIDs) == 0 {
		return "" // a ToDo names no axiom — there is no incremental scope to speak of
	}
	checked, err := d.s.axiomChecks.ForRepo(axiomScopeKey(repo))
	return axiomScopeSection(d.scopeAxioms(ctx, run), checked, err)
}

// RecordAxiomScope notes the stand this repository was examined against (executor.Deps). The pool
// reports damage it had to set aside; the motor logs that and proceeds, so an unwritable pool costs
// the next run its incrementality and nothing else.
func (d *ChainDeps) RecordAxiomScope(repo string, run runs.Run, commit string, at time.Time) error {
	if d == nil || d.s == nil || len(run.AxiomIDs) == 0 {
		return nil
	}
	return d.s.axiomChecks.Record(axiomScopeKey(repo), run.AxiomIDs, commit, at)
}

// axiomScopeKey is the pool's key for one repository: the run target's repo id, the form the
// execution document carries from the first stage to the last. One spelling, so the stand a run
// records is the stand the next run finds — the ledger's full-name form is a different pool's key.
func axiomScopeKey(repo string) string { return repoShort(repo) }

// scopeAxioms resolves the run's axiom ids to records WITH their titles. An unreadable constitution
// store is not fatal here: the ids alone still name the axioms (mercury renders the id when there is
// no title), so the scope stays truthful and only becomes terser.
func (d *ChainDeps) scopeAxioms(ctx context.Context, run runs.Run) []mercury.RunAxiom {
	if cat, _, err := d.s.runCatalog(ctx, ""); err == nil {
		if named := cat.AxiomsFor(run.AxiomIDs); len(named) == len(run.AxiomIDs) {
			return named
		}
	}
	out := make([]mercury.RunAxiom, 0, len(run.AxiomIDs))
	for _, id := range run.AxiomIDs {
		out = append(out, mercury.RunAxiom{ID: id})
	}
	return out
}

// axiomScopeSection is the join itself, kept a free function over its three inputs so the one
// property that matters most is testable without a server: a pool that could not be READ must never
// turn into the claim "this repository was never examined". poolErr is passed through by name.
func axiomScopeSection(axioms []mercury.RunAxiom, checked map[string]runs.AxiomCheck, poolErr error) string {
	unread := ""
	if poolErr != nil {
		unread = poolErr.Error()
		if errors.Is(poolErr, runs.ErrAxiomChecksUnreadable) {
			// Logged once here: the prompt names the gap to the agent, the operator sees the file.
			log.Printf("devlabd: the examined-stand pool could not be read; the prompt names the gap: %v", poolErr)
		}
	}
	last := make(map[string]mercury.LastCheck, len(checked))
	for id, c := range checked {
		at := ""
		if !c.At.IsZero() {
			at = c.At.UTC().Format(time.RFC3339)
		}
		last[id] = mercury.LastCheck{Commit: c.Commit, At: at}
	}
	return mercury.RepoScopeSection(axioms, last, unread)
}

package api

import (
	"context"
	"log"

	"devlab/backend/internal/runs"
)

// A write to the axiom store leaves ONE derived artefact behind that must catch up with it in
// the SAME request — the one place the cookie-bearing axiom-store session exists: every run's
// prompt snapshot (automatic run and ToDo alike, because both carry the constitution in full
// wording, REQ-002.1). There is NO rollout anymore (REQ-002): the constitution reaches repos
// through the prompt; the CLAUDE.md reference block travels with the chain's implement/publish
// stage instead.
//
// The whole reconcile is best-effort: the write has already committed, so a reconcile failure
// is logged and skipped, never turned into an error the user sees (a stale snapshot self-heals
// on the next write).

// reconcileAfterWrite recomposes the affected runs (REQ-003: EVERY write path recomposes). The
// catalog comes from the ONE store scan (runCatalog): a second scanner here would be a second place
// deciding what belongs to the constitution, and the two would drift apart.
func (s *Server) reconcileAfterWrite(ctx context.Context, cookie string) {
	cat, _, err := s.runCatalog(ctx, cookie)
	if err != nil {
		log.Printf("devlabd: mercury reconcile scan failed (skipped): %v", err)
		return
	}
	s.recomposeAffectedRuns(cat)
}

// recomposeAffectedRuns re-snapshots exactly the runs whose composed inputs changed — detected in
// the composition core (runs.RecomposeDrifted), which owns the knowledge of what a prompt is made
// of; a copy of that knowledge here would be a second composition path. Unaffected runs keep their
// snapshot untouched. It patches (no history snapshot): a recompose is a derived-state refresh, not
// a user config edit.
func (s *Server) recomposeAffectedRuns(cat runs.Catalog) {
	if s.runs == nil {
		return
	}
	if _, err := s.runs.Patch(func(cur []runs.Run) ([]runs.Run, error) {
		return runs.RecomposeDrifted(cur, cat), nil
	}); err != nil {
		log.Printf("devlabd: mercury recompose after write failed (skipped): %v", err)
	}
}

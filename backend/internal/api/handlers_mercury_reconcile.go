package api

import (
	"context"
	"log"
	"strings"

	"devlab/backend/internal/mercury"
	"devlab/backend/internal/runs"
)

// A write to the axiom store leaves ONE derived artefact behind that must catch up with it in
// the SAME request — the one place the cookie-bearing axiom-store session exists: every
// automatic run's prompt snapshot (composed from its axioms + all run rules). There is NO
// rollout anymore (REQ-002): the constitution reaches repos through the prompt; the CLAUDE.md
// reference block travels with the chain's implement/publish stage instead.
//
// The whole reconcile is best-effort: the write has already committed, so a reconcile failure
// is logged and skipped, never turned into an error the user sees (a stale snapshot self-heals
// on the next write).

// reconcileAfterWrite recomposes the affected runs (REQ-003: EVERY write path recomposes).
func (s *Server) reconcileAfterWrite(ctx context.Context, cookie string) {
	cat, err := s.scanForReconcile(ctx, cookie)
	if err != nil {
		log.Printf("devlabd: mercury reconcile scan failed (skipped): %v", err)
		return
	}
	s.recomposeAffectedRuns(cat)
}

// scanForReconcile reads the store once into the composition catalog. Reuses fetchRecord — the
// same read primitive every other Mercury handler uses.
func (s *Server) scanForReconcile(ctx context.Context, cookie string) (runs.Catalog, error) {
	paths, err := s.axioms.List(ctx, "")
	if err != nil {
		return runs.Catalog{}, err
	}
	cat := runs.Catalog{ByID: map[string]mercury.RunAxiom{}}
	for _, p := range paths {
		if !strings.HasSuffix(p, ".md") {
			continue
		}
		switch {
		case strings.HasPrefix(p, mercury.NsAxiome+"/"):
			if rec, ok := s.fetchRecord(ctx, cookie, p); ok && rec.Axiom.ID != "" {
				cat.ByID[rec.Axiom.ID] = mercury.RunAxiom{ID: rec.Axiom.ID, Titel: rec.Axiom.Titel, Body: rec.Axiom.Body}
			}
		case strings.HasPrefix(p, mercury.NsLaeufe+"/"):
			if rec, ok := s.fetchRecord(ctx, cookie, p); ok {
				cat.Laufregeln = append(cat.Laufregeln, mercury.RunAxiom{ID: rec.Axiom.ID, Titel: rec.Axiom.Titel, Body: rec.Axiom.Body})
			}
		}
	}
	return cat, nil
}

// recomposeAffectedRuns re-snapshots exactly the auto runs whose composed inputs changed —
// detected by comparing each run's stored input hash to a freshly computed one (which folds in
// the record titles, so a pure rename counts; REQ-003). Unaffected runs keep their snapshot
// untouched. It patches (no history snapshot): a recompose is a derived-state refresh, not a
// user config edit.
func (s *Server) recomposeAffectedRuns(cat runs.Catalog) {
	if s.runs == nil {
		return
	}
	if _, err := s.runs.Patch(func(cur []runs.Run) ([]runs.Run, error) {
		for i := range cur {
			if cur[i].Kind != "" && cur[i].Kind != "auto" {
				continue // a todo's prompt is its task; axioms/run rules do not fold in
			}
			want := mercury.RunInputsHash(cat.AxiomsFor(cur[i].AxiomIDs), cat.Laufregeln)
			if cur[i].PromptInputHash == want {
				continue // unaffected — leave this run as it is
			}
			runs.ComposeInto(&cur[i], cat)
		}
		return cur, nil
	}); err != nil {
		log.Printf("devlabd: mercury recompose after write failed (skipped): %v", err)
	}
}

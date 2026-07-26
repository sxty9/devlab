package api

import (
	"context"
	"log"
	"strings"
	"time"

	"devlab/backend/internal/mercury"
	"devlab/backend/internal/runs"
)

// A write to the axiom store leaves two derived artefacts behind that must catch up with it in the SAME
// request — the one place the cookie-bearing axiom-store session exists:
//
//  1. every automatic run's prompt snapshot (composed from its axioms + all Laufregeln), and
//  2. when the change touched the CLAUDE.md surface (axiome/regeln), the rolled-out constitution block.
//
// Both reuse the existing composition — composeInto for the run prompts, RenderBlock for the block — so
// no second store path is opened. One store scan feeds both. The whole reconcile is best-effort: the
// write has already committed, so a reconcile failure is logged and skipped, never turned into an error
// the user sees (a stale snapshot is self-heals on the next write, and the rollout is retried likewise).

// reconcileAfterWrite recomposes the affected runs and, when touchedClaudeMd is set, hands the freshly
// rendered block to the debounced auto-rollout. Callers pass touchedClaudeMd=true exactly when the write
// created/changed/removed a record under axiome/ or regeln/ (the two namespaces the CLAUDE.md carries).
func (s *Server) reconcileAfterWrite(ctx context.Context, cookie string, touchedClaudeMd bool) {
	byID, laufregeln, axiome, regeln, err := s.scanForReconcile(ctx, cookie)
	if err != nil {
		log.Printf("devlabd: mercury reconcile scan failed (skipped): %v", err)
		return
	}
	s.recomposeAffectedRuns(byID, laufregeln)
	if touchedClaudeMd && s.autoRollout != nil {
		// Render here, where the session exists, and hand the target block to the background worker.
		s.autoRollout.enqueue(mercury.RenderBlock(axiome, regeln))
	}
}

// scanForReconcile reads the store once and buckets every record into the shapes the two consumers need:
// axioms as RunAxioms (byID, keyed by stable id) and Laufregeln for run composition, plus axiome/regeln
// as Records for the rollout block. NsAxiome records feed both forms from a single fetch. Reuses
// fetchRecord — the same read primitive every other Mercury handler uses.
func (s *Server) scanForReconcile(ctx context.Context, cookie string) (byID map[string]mercury.RunAxiom, laufregeln []mercury.RunAxiom, axiome, regeln []mercury.Record, err error) {
	paths, err := s.axioms.List(ctx, "")
	if err != nil {
		return nil, nil, nil, nil, err
	}
	byID = map[string]mercury.RunAxiom{}
	for _, p := range paths {
		if !strings.HasSuffix(p, ".md") {
			continue
		}
		switch {
		case strings.HasPrefix(p, mercury.NsAxiome+"/"):
			if rec, ok := s.fetchRecord(ctx, cookie, p); ok {
				axiome = append(axiome, rec)
				if rec.Axiom.ID != "" {
					byID[rec.Axiom.ID] = mercury.RunAxiom{ID: rec.Axiom.ID, Titel: rec.Axiom.Titel, Body: rec.Axiom.Body}
				}
			}
		case strings.HasPrefix(p, mercury.NsRegeln+"/"):
			if rec, ok := s.fetchRecord(ctx, cookie, p); ok {
				regeln = append(regeln, rec)
			}
		case strings.HasPrefix(p, mercury.NsLaeufe+"/"):
			if rec, ok := s.fetchRecord(ctx, cookie, p); ok {
				laufregeln = append(laufregeln, mercury.RunAxiom{ID: rec.Axiom.ID, Titel: rec.Axiom.Titel, Body: rec.Axiom.Body})
			}
		}
	}
	return byID, laufregeln, axiome, regeln, nil
}

// recomposeAffectedRuns re-snapshots exactly the auto runs whose composed inputs changed — detected by
// comparing each run's stored input hash to a freshly computed one (which now folds in the record titles,
// so a pure rename counts). Unaffected runs keep their snapshot and UpdatedAt untouched. It patches (no
// history snapshot): a recompose is a derived-state refresh, not a user config edit. Because every write
// runs this, a run's snapshot never lingers out of date — the "veraltet" state is structurally gone.
func (s *Server) recomposeAffectedRuns(byID map[string]mercury.RunAxiom, laufregeln []mercury.RunAxiom) {
	if s.runs == nil {
		return
	}
	now := time.Now().UTC()
	if _, err := s.runs.Patch(func(cur []runs.Run) ([]runs.Run, error) {
		for i := range cur {
			if cur[i].IsTodo() {
				continue // a ToDo's prompt is its task; axioms/Laufregeln do not fold in
			}
			want := mercury.RunInputsHash(axiomsFor(cur[i].AxiomIDs, byID), laufregeln)
			if cur[i].PromptHash == want {
				continue // unaffected — leave this run (and its UpdatedAt) as it is
			}
			composeInto(&cur[i], byID, laufregeln, now)
			cur[i].UpdatedAt = now
		}
		return cur, nil
	}); err != nil {
		log.Printf("devlabd: mercury recompose after write failed (skipped): %v", err)
	}
}

// touchesClaudeMd reports whether any of the given record/category paths lies under a namespace the
// rolled-out CLAUDE.md carries (axiome or regeln) — i.e. whether the write should trigger a rollout. A
// move is checked on both its source and destination, so leaving or entering the surface both count.
func touchesClaudeMd(paths ...string) bool {
	for _, p := range paths {
		p = strings.TrimSpace(p)
		if strings.HasPrefix(p, mercury.NsAxiome+"/") || strings.HasPrefix(p, mercury.NsRegeln+"/") {
			return true
		}
	}
	return false
}

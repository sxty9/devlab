// Completion and listing selectors — PURE functions (REQ-011.2, REQ-037.1, B-8). Nothing here
// is ever stored: whether an execution or run counts as completed, and how the open list and
// the history split, are derivations over the passive pools.
package runs

import "devlab/backend/internal/model"

// ExecutionCompleted reports whether an execution is history-ready. HISTORISED IS STRICT (WHAT-4):
// the whole chain ran through — up to AND including the production delivery — with no intervention
// and no approval needed. Anything short of that is not a history entry but a CURRENT state, and
// belongs in the tab that holds that state.
//
// The old rule asked only "did it end, and does an unsettled delivery hang on it". That let an
// execution which blocked or failed on an EARLY stage count as history: it produced no delivery, so
// nothing hung on it, so it slipped in — even though its chain never ran through (the vacuous-truth
// hole). The positive proof the chain DID run through is MergedAt: SettleExecution stamps it only
// once EVERY delivery of the execution is settled through production (B-8 + WHAT-1), and the startup
// reconciliation stamps it on a synthetic check-off once the work verifiably arrived in the default
// branch. A blocked or failed execution never earns that stamp.
//
// So an execution historises iff it ended AND either it is a frozen archive record (Legacy — a
// closed past, display-only, with no live tab to move to), or it carries the MergedAt stamp AND the
// ledger agrees every delivery of it is settled (belt-and-suspenders: the stamp and the ledger are
// written together, so they never disagree, but the surface must never show an un-settled delivery
// as history). The client mirror (views/mercury/tasks/select.ts executionCompleted) asks exactly
// this, so the two never drift.
func ExecutionCompleted(res Result, deliveries []Delivery) bool {
	if res.EndedAt == nil {
		return false
	}
	if res.Legacy {
		return true // a frozen archive record is a closed past — history by construction.
	}
	if res.MergedAt == nil {
		return false // no production-settled stamp ⇒ the chain did not run through: a current state.
	}
	for _, d := range deliveries {
		if d.ExecutionID == res.ID && !d.Settled() {
			return false
		}
	}
	return true
}

// RunCompleted reports whether a run (todo) counts as done: at least one execution completed
// per ExecutionCompleted covering all its targets. An auto run recurs by definition and is
// never "done"; it stays in its list forever while its executions historize individually.
func RunCompleted(r Run, results []Result, deliveries []Delivery) bool {
	if r.Kind != model.KindTodo {
		return false
	}
	for _, res := range results {
		if res.RunID != r.ID || !ExecutionCompleted(res, deliveries) {
			continue
		}
		if executionCoversTargets(res, r.Targets) {
			return true
		}
	}
	return false
}

// executionCoversTargets reports whether one execution successfully worked EVERY target of the
// run: each target repo appears in the server stage array with a finished, succeeded pipeline.
// A target-less (legacy) record is covered by any completed execution.
func executionCoversTargets(res Result, targets []Target) bool {
	if len(targets) == 0 {
		return true
	}
	byRepo := make(map[string][]model.StageView, len(res.Repos))
	for _, rp := range res.Repos {
		byRepo[rp.Repo] = rp.Stages
	}
	for _, t := range targets {
		stages, ok := byRepo[t.Repo]
		if !ok {
			return false
		}
		done, succeeded := model.PipelineSucceeded(stages)
		if !done || !succeeded {
			return false
		}
	}
	return true
}

// SplitOpenHistory splits runs into the open list and the history so that open ∩ history = ∅
// (REQ-011.2): a run appears in the history only through completed executions, and an open run
// never shows completed-execution rows as its own state. Slices are never nil (JSON null would
// blank the UI lists).
func SplitOpenHistory(runs []Run, results []Result, deliveries []Delivery) (open []Run, history []Result) {
	open, history = []Run{}, []Result{}
	for _, r := range runs {
		if !RunCompleted(r, results, deliveries) {
			open = append(open, r)
		}
	}
	for _, res := range results {
		if ExecutionCompleted(res, deliveries) {
			history = append(history, res)
		}
	}
	return open, history
}

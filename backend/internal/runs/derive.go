// Completion and listing selectors — PURE functions (REQ-011.2, REQ-037.1, B-8). Nothing here
// is ever stored: whether an execution or run counts as completed, and how the open list and
// the history split, are derivations over the passive pools.
package runs

import "devlab/backend/internal/model"

// ExecutionCompleted reports whether an execution is history-ready: it ended AND every delivery
// that arose from it is SETTLED (B-8 + WHAT-1). A delivery is settled when it is closed, rolled
// back, or merged AND its production step has succeeded (or is proven not applicable). A merged
// delivery whose work is not yet running in production keeps its execution — and thus its ToDo —
// out of the history: production is the last step of the chain, and "done" means proven live there,
// not merely merged.
func ExecutionCompleted(res Result, deliveries []Delivery) bool {
	if res.EndedAt == nil {
		return false
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

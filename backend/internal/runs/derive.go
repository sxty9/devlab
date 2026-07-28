// Completion and listing selectors — PURE functions (REQ-011.2, REQ-037.1, B-8). Nothing here
// is ever stored: whether an execution or run counts as completed, and how the open list and
// the history split, are derivations over the passive pools.
package runs

// ExecutionCompleted reports whether an execution is history-ready: it ended AND every delivery
// that arose from it is merged, rolled back, or closed with a reason (B-8). Until then the
// execution stays in the open list.
func ExecutionCompleted(res Result, deliveries []Delivery) bool {
	panic("TODO(B8)")
}

// RunCompleted reports whether a run (todo) counts as done: at least one execution completed
// per ExecutionCompleted covering all its targets.
func RunCompleted(r Run, results []Result, deliveries []Delivery) bool {
	panic("TODO(B8)")
}

// SplitOpenHistory splits runs into the open list and the history so that open ∩ history = ∅
// (REQ-011.2): a run appears in the history only through completed executions, and an open run
// never shows completed-execution rows as its own state.
func SplitOpenHistory(runs []Run, results []Result, deliveries []Delivery) (open []Run, history []Result) {
	panic("TODO(B8)")
}

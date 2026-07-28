// Resume planning (C F1) — a PURE decision: given the persisted execution documents, results,
// open deliveries and an optionally adopted open PR, decide whether a start resumes the existing
// execution (same id, REQ-019.1), starts fresh, or adopts an open PR's state. No I/O here; the
// scheduler calls this inside its one atomic Submit.
package runs

import (
	"time"

	"devlab/backend/internal/execstate"
	"devlab/backend/internal/model"
)

// ResumePlan is the decision Submit acts on.
type ResumePlan struct {
	// Action is "resume" | "fresh" | "adopt-pr".
	Action      string
	ExecutionID string
	Reason      string
}

// PlanResume classifies the situation: a live document within the resume window resumes under
// its OWN id; a document older than resumeWindow counts as reliably abandoned; an open,
// run-created PR is adopted rather than duplicated (REQ-019.5). An explicit fresh start is the
// caller's decision and marks the old document discarded — not decided here.
func PlanResume(docs []execstate.Doc, results []Result, open []Delivery, openPR *model.PRRef, resumeWindow time.Duration, now time.Time) ResumePlan {
	panic("TODO(B2)")
}

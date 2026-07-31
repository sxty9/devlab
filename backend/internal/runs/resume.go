// Resume planning (C F1) — a PURE decision: given the persisted execution documents, results,
// open deliveries and an optionally adopted open PR, decide whether a start resumes the existing
// execution (same id, REQ-019.1), starts fresh, or adopts an open PR's state. No I/O here; the
// scheduler calls this inside its one atomic Submit.
package runs

import (
	"fmt"
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

// Resume plan actions.
const (
	ResumeActionResume  = "resume"
	ResumeActionFresh   = "fresh"
	ResumeActionAdoptPR = "adopt-pr"
)

// PlanResume classifies the situation: a live document within the resume window resumes under
// its OWN id; a document older than resumeWindow counts as reliably abandoned; an open,
// run-created PR is adopted rather than duplicated (REQ-019.5). An explicit fresh start is the
// caller's decision and marks the old document discarded — not decided here.
func PlanResume(docs []execstate.Doc, results []Result, open []Delivery, openPR *model.PRRef, resumeWindow time.Duration, now time.Time) ResumePlan {
	// The newest resumable live document wins (per run there is at most one live document —
	// the store invariant; over a mixed doc set the newest is the honest anchor).
	var cand *execstate.Doc
	for i := range docs {
		d := docs[i]
		switch d.Phase {
		case model.PhasePaused, model.PhaseBlocked, model.PhaseInterrupted:
			if cand == nil || !d.CreatedAt.Before(cand.CreatedAt) {
				cand = &docs[i]
			}
		}
	}
	if cand != nil {
		age := now.Sub(lastTouched(*cand))
		if resumeWindow > 0 && age > resumeWindow {
			return ResumePlan{
				Action:      ResumeActionFresh,
				ExecutionID: cand.ID,
				Reason: fmt.Sprintf("previous execution %s counts as reliably abandoned (untouched for %s, resume window %s)",
					cand.ID, age.Round(time.Minute), resumeWindow),
			}
		}
		reason := fmt.Sprintf("continuing execution %s (%s", cand.ID, cand.Phase)
		if cand.Continuation != nil {
			reason += fmt.Sprintf(" at %s/%s", cand.Continuation.Repo, cand.Continuation.Stage)
		}
		reason += ") — finished repositories stay finished"
		if d := progressNote(*cand); d != "" {
			reason += "; " + d
		}
		return ResumePlan{Action: ResumeActionResume, ExecutionID: cand.ID, Reason: reason}
	}

	// No resumable document. Work that already reached a PR is ADOPTED, never duplicated:
	// either the caller found an open PR by head, or the ledger still holds an open
	// execution-created delivery.
	if openPR != nil {
		return ResumePlan{
			Action: ResumeActionAdoptPR,
			Reason: fmt.Sprintf("open pull request #%d (%s) already carries this work — adopting it instead of duplicating", openPR.Number, openPR.HeadBranch),
		}
	}
	for _, d := range open {
		if d.ExecutionID != "" && d.OpenState() {
			return ResumePlan{
				Action:      ResumeActionAdoptPR,
				ExecutionID: d.ExecutionID,
				Reason:      fmt.Sprintf("open delivery %s at %s already carries this work — adopting its branch instead of duplicating", d.ID, d.Repo),
			}
		}
	}
	return ResumePlan{Action: ResumeActionFresh, Reason: "no unfinished execution to continue — starting fresh"}
}

func lastTouched(d execstate.Doc) time.Time {
	if d.UpdatedAt != nil {
		return *d.UpdatedAt
	}
	return d.CreatedAt
}

func progressNote(d execstate.Doc) string {
	done := 0
	for _, r := range d.Repos {
		if r.State == execstate.RepoDone {
			done++
		}
	}
	if done == 0 {
		return ""
	}
	return fmt.Sprintf("%d of %d repositories already finished", done, len(d.Repos))
}

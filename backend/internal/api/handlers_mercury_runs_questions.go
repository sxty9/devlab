// The Blocked surface (REQ: autonomy-level questions as blockers): every run that stopped and asked
// stands at ONE place, the answer resumes it, and a raised question reaches the user the same way a
// disturbance does. This file is the request+delivery half; the run-side halt lives in the executor,
// the pool in package runs.
package api

import (
	"errors"
	"log"
	"net/http"
	"strings"

	"devlab/backend/internal/live"
	"devlab/backend/internal/runs"
	"devlab/backend/internal/sched"
)

// StartQuestionDelivery arms the outward delivery of a raised question: it installs the question
// pool's on-new hook so every new question is recorded as a DISTURBANCE notice — which the notice
// delivery hook (StartNoticeDelivery) then pushes to the run owner AND surfaces on the dashboard
// badge. Reusing the notice path means there is exactly one way a fault reaches the user, never two.
// Without a notice pool nothing is recorded; the question still stands on the Blocked surface.
func (s *Server) StartQuestionDelivery() {
	if s.runQuestions == nil {
		log.Printf("devlabd: question delivery OFF — question pool unavailable")
		return
	}
	s.runQuestions.SetOnNew(func(q runs.Question) {
		if s.runNotices != nil {
			text := "A run stopped and needs your decision: " + firstLineOf(q.Question)
			if q.QKind == runs.QuestionWrapperRenewal {
				text = "A run asks to renew the root wrapper scripts and needs your explicit approval: " + firstLineOf(q.Question)
			}
			if _, err := s.runNotices.Coalesce(runs.Notice{
				Kind: runs.NoticeQuestion, Repo: q.Repo, Text: text,
				NextStep: "Answer it in the Blocked tab — the run continues with your answer.",
			}); err != nil {
				log.Printf("devlabd: question notice not recorded: %v", err)
			}
			s.publish(live.TopicNotices)
		}
		s.publish(live.TopicQuestions)
	})
	log.Printf("devlabd: question delivery ENABLED (disturbance notice per new question)")
}

// runsQuestionsList returns the open and answered questions the Blocked surface renders, newest
// first. It is the ONE read path for the Blocked tab.
func (s *Server) runsQuestionsList(w http.ResponseWriter, _ *http.Request) {
	if s.runQuestions == nil {
		writeJSON(w, http.StatusOK, map[string]any{"questions": []runs.Question{}})
		return
	}
	list, err := s.runQuestions.List()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "Could not read the questions")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"questions": list})
}

// runsQuestionAnswer records the user's answer on one open question and resumes its run — the run
// continues from where it stopped, it does not start over. approve is the single-use green light a
// guarded handle (the wrapper renewal) needs; for a plain decision it is simply stored.
func (s *Server) runsQuestionAnswer(w http.ResponseWriter, r *http.Request) {
	if s.runQuestions == nil {
		writeErr(w, http.StatusServiceUnavailable, "The question pool is unavailable")
		return
	}
	var body struct {
		ID      string `json:"id"`
		Answer  string `json:"answer"`
		Approve bool   `json:"approve"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	body.ID = strings.TrimSpace(body.ID)
	body.Answer = strings.TrimSpace(body.Answer)
	if body.ID == "" {
		writeErr(w, http.StatusBadRequest, "id is required")
		return
	}
	if body.Answer == "" {
		writeErr(w, http.StatusBadRequest, "An answer is required — the run continues with it")
		return
	}
	q, err := s.runQuestions.Answer(body.ID, body.Answer, body.Approve, actorFrom(r))
	if errors.Is(err, runs.ErrNotFound) {
		writeErr(w, http.StatusNotFound, "No such question")
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "Could not record the answer")
		return
	}
	s.publish(live.TopicQuestions)
	s.publish(live.TopicNotices)

	// Drive the run that raised the question forward with the answer. Two cases, because an answer
	// belongs to the ORDER, not to the one execution that asked:
	//   - the raising execution is still live (paused/blocked/interrupted) → RESUME it in place;
	//   - its execution has ENDED (the run has no live execution — the very trap this fixes: the
	//     approval could be given but never redeemed) → START a fresh execution, which redeems the
	//     answer by run id (AnsweredForRun) exactly as a manual restart would.
	// A missing scheduler (dev) leaves the answer recorded for the run's next admission; it is never
	// an error for the answer itself.
	resumed := false
	if s.scheduler != nil && q.RunID != "" {
		switch rerr := s.scheduler.Resume(q.RunID, actorFrom(r)); {
		case rerr == nil:
			resumed = true
		case errors.Is(rerr, sched.ErrNotActive):
			// No live execution to resume — start a fresh one so the recorded answer is redeemed
			// instead of lingering unread. The restart runs the SAME order: an already-implemented
			// target takes the rest path (no new agent work), and the wrapper renewal installs under
			// the approval just answered.
			if _, serr := s.scheduler.Submit(r.Context(), sched.StartRequest{RunID: q.RunID, By: actorFrom(r)}); serr != nil {
				log.Printf("devlabd: restart run %s to redeem its answer: %v", q.RunID, serr)
			} else {
				resumed = true
			}
		case errors.Is(rerr, sched.ErrNotPaused), errors.Is(rerr, sched.ErrNotRunning), errors.Is(rerr, sched.ErrUnknownRun):
			// The answer stands; the run simply has no resumable execution right now.
		default:
			log.Printf("devlabd: resume run %s after answer: %v", q.RunID, rerr)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"question": q, "resumed": resumed})
}

// firstLineOf is the notice-text condenser: the first line of a possibly multi-line question.
func firstLineOf(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = strings.TrimSpace(s[:i])
	}
	const max = 200
	if len(s) > max {
		return s[:max] + "…"
	}
	return s
}

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
	"time"

	"devlab/backend/internal/live"
	"devlab/backend/internal/model"
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
	// Retire at boot any dead question — one whose run vanished, or whose order has since finished —
	// while the daemon was down, so it never holds a repository the moment the service comes back.
	s.sweepDeadQuestions()
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

// sweepDeadQuestions retires every open question that can no longer take effect — the ONE place a
// question is closed for a reason outside the user's answer. Two families, both drawn from the
// authoritative stores (never a flag the question keeps about itself):
//
//   - MOOT: the question's RUN no longer exists (an order removed while its question stood open). It
//     cannot be approved (no branch is carried on) nor rejected-with-effect (no run to end), yet as an
//     open question it would hold its repository against every new order — the deadlock that froze
//     production.
//   - ENDED: the run still exists but its ORDER has FINISHED — its latest execution ended completed or
//     discarded and none is live, so no execution will act on the question again and answering it
//     resumes nothing (Selbst prüfen statt fragen: "Endet der Vorgang, zu dem eine Frage gehört, kann
//     ihre Beantwortung keine Wirkung mehr entfalten"). A run merely WAITING on its question is not
//     ended — its latest execution is failed-with-a-blocked-repo or interrupted (auto-resumes), never
//     completed/discarded — so a live waiting question is left exactly where it is.
//
// The owner decides existence and endedness here (the run and execution stores are authoritative) and
// hands the pool the sets; the pool, being passive, only closes the ids it is told. Best-effort: an
// unreadable store closes nothing (a set must be authoritative, never partial). Called on every Blocked
// read, at boot, and before the branch-halt consults an open question, so a dead question never blocks
// anyone.
func (s *Server) sweepDeadQuestions() {
	if s.runQuestions == nil || s.runs == nil {
		return
	}
	all, err := s.runs.List()
	if err != nil {
		log.Printf("devlabd: question sweep skipped (run store unreadable): %v", err)
		return
	}
	existing := make(map[string]bool, len(all))
	for _, r := range all {
		existing[r.ID] = true
	}
	changed := false
	closed, err := s.runQuestions.CloseMoot(existing, "the run no longer exists — the question is moot and blocks nothing")
	if err != nil {
		log.Printf("devlabd: moot-question sweep failed: %v", err)
	}
	for _, q := range closed {
		log.Printf("devlabd: closed moot question %s (run %s no longer exists) — it holds nothing anymore", q.ID, q.RunID)
		changed = true
	}
	ended, eerr := s.endedRunIDs()
	if eerr != nil {
		log.Printf("devlabd: ended-question sweep skipped (execution store unreadable): %v", eerr)
	} else if len(ended) > 0 {
		endedClosed, err := s.runQuestions.CloseEnded(ended, "the order has finished — its execution ended, so the question can no longer take effect")
		if err != nil {
			log.Printf("devlabd: ended-question sweep failed: %v", err)
		}
		for _, q := range endedClosed {
			log.Printf("devlabd: closed ended question %s (order %s has finished) — its answer can no longer take effect", q.ID, q.RunID)
			changed = true
		}
	}
	if changed {
		s.publish(live.TopicQuestions)
	}
}

// endedRunIDs is the set of orders whose latest execution has ENDED without leaving the run resumable:
// no execution is live (queued|running|paused|blocked|interrupted) and the most recent one finalized as
// completed or discarded. A run whose latest execution is FAILED is deliberately excluded — that is the
// state a run stands in while it WAITS on its own blocking question (a repo blocked, the question open),
// and such a question must stay open until it is answered or rejected. An interrupted run auto-resumes,
// so it is not ended either. The determination reads the execution documents (the authoritative record),
// never a flag on the question. Nil docs store → empty set (nothing is ended), never a wrong closure.
func (s *Server) endedRunIDs() (map[string]bool, error) {
	if s.docs == nil {
		return map[string]bool{}, nil
	}
	docs, err := s.docs.List()
	if err != nil {
		return nil, err
	}
	type agg struct {
		latest  model.ExecPhase
		at      time.Time
		hasLive bool
	}
	byRun := map[string]*agg{}
	for _, d := range docs {
		a := byRun[d.RunID]
		if a == nil {
			a = &agg{}
			byRun[d.RunID] = a
		}
		if d.Live() {
			a.hasLive = true
		}
		if a.at.IsZero() || d.CreatedAt.After(a.at) {
			a.at = d.CreatedAt
			a.latest = d.Phase
		}
	}
	ended := map[string]bool{}
	for runID, a := range byRun {
		if a.hasLive {
			continue
		}
		if a.latest == model.PhaseCompleted || a.latest == model.PhaseDiscarded {
			ended[runID] = true
		}
	}
	return ended, nil
}

// runsQuestionsList returns the open and answered questions the Blocked surface renders, newest
// first. It is the ONE read path for the Blocked tab. It first retires any dead question (its run is
// gone, or its order has finished), so the surface never shows — and a repository is never held by — a
// question nobody can act on.
func (s *Server) runsQuestionsList(w http.ResponseWriter, _ *http.Request) {
	if s.runQuestions == nil {
		writeJSON(w, http.StatusOK, map[string]any{"questions": []runs.Question{}})
		return
	}
	s.sweepDeadQuestions()
	list, err := s.runQuestions.List()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "Could not read the questions")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"questions": list})
}

// runsQuestionAnswer is the ONE access point for deciding an open question — the two co-equal
// outcomes share it (no parallel path). With decline=false it records the user's answer and resumes
// its run, which continues from where it stopped rather than starting over; approve is the single-use
// green light a guarded handle (the wrapper renewal) needs. With decline=true it REJECTS the question
// — the always-available "no" — which ends the run and frees its slot (see runsQuestionDecline).
func (s *Server) runsQuestionAnswer(w http.ResponseWriter, r *http.Request) {
	if s.runQuestions == nil {
		writeErr(w, http.StatusServiceUnavailable, "The question pool is unavailable")
		return
	}
	var body struct {
		ID      string `json:"id"`
		Answer  string `json:"answer"`
		Approve bool   `json:"approve"`
		Decline bool   `json:"decline"`
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
	// The rejection is a full answer, not a missing one: it needs no text and never approves.
	if body.Decline {
		s.runsQuestionDecline(w, r, body.ID, body.Answer)
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

// runsQuestionDecline carries out the REJECTION of one open question — the co-equal "no" that ends
// its run. The order is deliberate and idempotent:
//  1. record the rejection on the question (resolved+declined) so it holds no repository and drops
//     off the surface; a note carries the user's optional words;
//  2. retire ANY other still-open question the same run left for the same repository, so the rejection
//     leaves no second lingering blocker;
//  3. end the run: its live execution is marked FAILED with the rejection as the reason and its slot
//     is freed. A question mints no delivery, so there is no hanging delivery tip to unwind, and
//     nothing was installed, changed or transferred — the state before the question stands unchanged.
//
// The rejection is the safe exit and the way a blocked stack tip is released: after it, another order
// on the same repository starts at once (no open question holds it). An unknown id is a 404; an
// already-closed question is a no-op that still reconciles the run, so a double click never errors.
func (s *Server) runsQuestionDecline(w http.ResponseWriter, r *http.Request, id, note string) {
	by := actorFrom(r)
	q, err := s.runQuestions.Decline(id, note, by)
	if errors.Is(err, runs.ErrNotFound) {
		writeErr(w, http.StatusNotFound, "No such question")
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "Could not record the rejection")
		return
	}
	if q.RunID != "" {
		// No second question of this run may keep holding the repository after the run is ended.
		if _, werr := s.runQuestions.WithdrawForRun(q.RunID, q.Repo, ""); werr != nil {
			log.Printf("devlabd: retire the declined run's other questions (%s): %v", q.RunID, werr)
		}
		if s.scheduler != nil {
			if ferr := s.scheduler.FailForDeclinedQuestion(q.RunID, by); ferr != nil {
				log.Printf("devlabd: end run %s after its question was declined: %v", q.RunID, ferr)
			}
		}
	}
	s.publish(live.TopicQuestions)
	s.publish(live.TopicNotices)
	writeJSON(w, http.StatusOK, map[string]any{"question": q, "declined": true})
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

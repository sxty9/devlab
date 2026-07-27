package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"devlab/backend/internal/runs"
)

// SlotOverview is a portioned snapshot of the execution floor: how many standing slots exist, how many are
// occupied, how many free, how many temporary overloads run past the cap, and which runs are deferred
// (stood down to free a slot) with where each will resume. Read-only projection assembled outside the
// scheduler (the passive-pool rule: evaluation lives outside the store).
type SlotOverview struct {
	Capacity int            `json:"capacity"`
	Used     int            `json:"used"`     // standing (non-overload) slots occupied
	Free     int            `json:"free"`     // capacity − used, floored at 0 (0 while an exclusive run holds the floor)
	Overload int            `json:"overload"` // runs beyond the cap — temporary, self-healing
	Deferred []DeferredView `json:"deferred"`
}

// DeferredView is one deferred run and its continuation point, so the overview shows exactly what was
// stood down and where it will pick up.
type DeferredView struct {
	RunID       string `json:"runId"`
	RunName     string `json:"runName,omitempty"`
	ResultID    string `json:"resultId,omitempty"`
	Done        int    `json:"done"`
	Total       int    `json:"total,omitempty"` // 0 = an auto sweep (unknown ahead of time)
	ResumePoint string `json:"resumePoint"`
}

// StartDecision is returned when a start is blocked because the floor is full: which block, which ways
// forward (queue / defer / overload — overload only for a cap block), a reasoned defer suggestion, and the
// current slot picture.
type StartDecision struct {
	Blocked    string           `json:"blocked"`
	Options    []string         `json:"options"`
	Suggestion *DeferSuggestion `json:"suggestion,omitempty"`
	Slots      SlotOverview     `json:"slots"`
}

// DeferSuggestion names the run the system recommends deferring, and why (loses-least / easiest-to-resume).
type DeferSuggestion struct {
	RunID   string `json:"runId"`
	RunName string `json:"runName,omitempty"`
	Reason  string `json:"reason"`
}

// slotOverview assembles the floor snapshot from the live activities + the deferred runs in the store.
func (s *Server) slotOverview(active []runs.Activity) SlotOverview {
	ov := SlotOverview{Deferred: []DeferredView{}}
	if s.scheduler != nil {
		ov.Capacity = s.scheduler.Capacity()
	}
	exclusive := false
	for _, a := range active {
		if a.Overload {
			ov.Overload++
		}
		if a.Exclusive {
			exclusive = true
		}
	}
	ov.Used = len(active) - ov.Overload
	ov.Free = ov.Capacity - ov.Used
	if ov.Free < 0 {
		ov.Free = 0
	}
	if exclusive {
		ov.Free = 0 // a whole-floor exclusive run leaves no slot truly free, however few are nominally used
	}
	ov.Deferred = s.deferredRuns(active)
	return ov
}

// deferredRuns lists the runs currently stood down as a deferred suspension (excluding any that are
// already resuming, which show up in the active list), each with its continuation point.
func (s *Server) deferredRuns(active []runs.Activity) []DeferredView {
	out := []DeferredView{}
	if s.runs == nil {
		return out
	}
	live := make(map[string]bool, len(active))
	for _, a := range active {
		live[a.RunID] = true
	}
	all, err := s.runs.List()
	if err != nil {
		return out
	}
	for _, r := range all {
		if r.Suspended == nil || !r.Suspended.IsDeferred() || live[r.ID] {
			continue
		}
		done, _, _ := s.runProgress(r.ID, r.Suspended.ResultID)
		total := todoRepoTotal(r)
		out = append(out, DeferredView{
			RunID: r.ID, RunName: r.Name, ResultID: r.Suspended.ResultID,
			Done: done, Total: total, ResumePoint: resumePointText(done, total),
		})
	}
	return out
}

// runProgress reads a run's live result document once: how many repos it has finished, and the repo it is
// mid-way through (if any). Feeds both the deferred-list resume points and the defer suggestion's
// loses-least scoring.
func (s *Server) runProgress(runID, resultID string) (done int, liveRepo string, ok bool) {
	if s.runResults == nil || resultID == "" {
		return 0, "", false
	}
	res, found, err := s.runResults.Get(runID, resultID)
	if err != nil || !found {
		return 0, "", false
	}
	if res.Live != nil {
		liveRepo = res.Live.Repo
	}
	return len(res.Repos), liveRepo, true
}

func resumePointText(done, total int) string {
	if total > 0 {
		return fmt.Sprintf("%d von %d Repos erledigt — setzt am nächsten freien Platz fort", done, total)
	}
	return fmt.Sprintf("%d Repos erledigt — setzt am nächsten freien Platz fort", done)
}

// buildStartDecision offers the ways forward for a blocked start. Overload is offered ONLY for a cap block
// — a repo that is genuinely busy or an exclusive floor cannot be crossed by a temporary extra slot.
func (s *Server) buildStartDecision(block runs.AdmitBlock) StartDecision {
	d := StartDecision{Blocked: block.Reason, Options: []string{"queue", "defer"}}
	if s.scheduler != nil {
		d.Slots = s.slotOverview(s.scheduler.Active())
	}
	if block.Reason == runs.AdmitCap {
		d.Options = append(d.Options, "overload")
	}
	d.Suggestion = s.suggestDefer(block)
	return d
}

// suggestDefer recommends WHICH run to defer to clear the way — the one that loses the least and is
// easiest to resume. For a repo/exclusive block only the conflicting runs can free the way; for a cap
// block any active run frees a slot. Ties break toward the YOUNGER run (least effort invested).
func (s *Server) suggestDefer(block runs.AdmitBlock) *DeferSuggestion {
	if s.scheduler == nil {
		return nil
	}
	active := s.scheduler.Active()
	byID := make(map[string]runs.Activity, len(active))
	for _, a := range active {
		byID[a.RunID] = a
	}
	candidates := block.Conflicts
	if block.Reason == runs.AdmitCap {
		candidates = nil
		for _, a := range active {
			candidates = append(candidates, a.RunID)
		}
	}

	best := ""
	bestScore := 0
	bestReason := ""
	var bestStarted time.Time
	for _, id := range candidates {
		a, ok := byID[id]
		if !ok {
			continue // no longer active
		}
		score, reason := s.deferScore(a, block)
		if best == "" || score < bestScore || (score == bestScore && a.StartedAt.After(bestStarted)) {
			best, bestScore, bestReason, bestStarted = id, score, reason, a.StartedAt
		}
	}
	if best == "" {
		return nil
	}
	return &DeferSuggestion{RunID: best, RunName: byID[best].RunName, Reason: bestReason}
}

// deferScore ranks a candidate for deferral — LOWER is a better target. A run between repos loses nothing
// (0); a run mid-repo would discard that repo's in-flight work (1000, always worse). A conflicting
// exclusive/repo-busy run carries its own justification.
func (s *Server) deferScore(a runs.Activity, block runs.AdmitBlock) (int, string) {
	done, liveRepo, _ := s.runProgress(a.RunID, a.ResultID)
	switch block.Reason {
	case runs.AdmitExclusive:
		return 0, "Belegt als Rundumlauf alle Plätze — nur sein Zurückstellen gibt einen Platz frei."
	case runs.AdmitRepoBusy:
		return 0, "Belegt ein Repository, das dieses ToDo braucht — Zurückstellen macht es frei."
	}
	if liveRepo != "" {
		return 1000, fmt.Sprintf("Ist gerade mitten in „%s“ — hier ginge beim Zurückstellen laufende Arbeit verloren.", liveRepo)
	}
	reason := "Läuft gerade zwischen zwei Repositorys — Zurückstellen verliert keine laufende Arbeit"
	if done > 0 {
		reason += fmt.Sprintf("; %d Repos sind fertig und bleiben es", done)
	}
	return 0, reason + "."
}

// markRunDueNow marks a run due right now so the scheduler starts it at the next free slot (the "queue"
// strategy). Patch, not Mutate — it is runtime scheduling state, not a config edit.
func (s *Server) markRunDueNow(id string) {
	if s.runs == nil {
		return
	}
	now := time.Now()
	_, _ = s.runs.Patch(func(cur []runs.Run) ([]runs.Run, error) {
		for i := range cur {
			if cur[i].ID != id {
				continue
			}
			t := now
			if cur[i].IsTodo() {
				cur[i].DueAt = &t
			} else {
				cur[i].NextFireAt = &t
			}
		}
		return cur, nil
	})
}

// runDefer stands a specific run down to free its slot (first-class, independent of starting anything).
func (s *Server) runDefer(w http.ResponseWriter, r *http.Request) {
	if s.scheduler == nil {
		writeErr(w, http.StatusServiceUnavailable, "Ausführung ist nicht konfiguriert")
		return
	}
	if !s.scheduler.Defer(r.PathValue("id")) {
		writeErr(w, http.StatusConflict, "Dieser Lauf ist nicht aktiv")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// decodeRunNowBody leniently reads the optional {strategy, deferRunId, fresh} body of a run trigger. An
// absent or empty body means the ordinary start — so a decode error is not fatal, it falls back to zero
// values.
func decodeRunNowBody(r *http.Request) (strategy, deferRunID string, fresh bool) {
	var body struct {
		Strategy   string `json:"strategy"`
		DeferRunID string `json:"deferRunId"`
		Fresh      bool   `json:"fresh"`
	}
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&body)
	}
	return body.Strategy, body.DeferRunID, body.Fresh
}

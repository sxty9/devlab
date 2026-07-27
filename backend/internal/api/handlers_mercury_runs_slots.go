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
	for _, a := range active {
		if a.Overload {
			ov.Overload++
		}
	}
	ov.Used = len(active) - ov.Overload
	ov.Free = ov.Capacity - ov.Used
	if ov.Free < 0 {
		ov.Free = 0
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
// (0); a run mid-repo would discard that repo's in-flight work (1000, always worse). A run holding a repo
// this ToDo needs carries its own justification.
func (s *Server) deferScore(a runs.Activity, block runs.AdmitBlock) (int, string) {
	done, liveRepo, _ := s.runProgress(a.RunID, a.ResultID)
	if block.Reason == runs.AdmitRepoBusy {
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

// runConfig reports the runs subsystem's live configuration: the number of execution slots in effect and
// the env/default seed (so the UI can show what a reset would fall back to). This is the service's config
// interface (req 13) — the setting belongs to the central configuration, not the env of the host.
func (s *Server) runConfig(w http.ResponseWriter, r *http.Request) {
	effective := 0
	if s.scheduler != nil {
		effective = s.scheduler.Capacity()
	}
	configured := 0
	if s.runSettings != nil {
		if rs, err := s.runSettings.Get(); err == nil {
			configured = rs.MaxConcurrent
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"maxConcurrent":     effective,          // slots currently in effect
		"maxConcurrentSeed": maxConcurrentSeed(), // the env/default fallback when nothing is configured
		"configured":        configured != 0,     // whether a UI value is set (vs. running on the seed)
	})
}

// runSetConfig sets the number of execution slots from the UI (req 13). It persists the value AND applies
// it to the running scheduler at once — a raise starts waiting runs immediately, a lower drains naturally
// — without a restart. maxConcurrent<=0 clears the setting, reverting to the env/default seed.
func (s *Server) runSetConfig(w http.ResponseWriter, r *http.Request) {
	if s.runSettings == nil {
		writeErr(w, http.StatusServiceUnavailable, "Einstellungen sind nicht verfügbar")
		return
	}
	var body struct {
		MaxConcurrent int `json:"maxConcurrent"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if body.MaxConcurrent > runMaxSlots {
		writeErr(w, http.StatusBadRequest, "die Zahl der Plätze ist zu hoch")
		return
	}
	if _, err := s.runSettings.Set(func(rs *runs.RunSettings) {
		if body.MaxConcurrent < 1 {
			rs.MaxConcurrent = 0 // clear → fall back to the env/default seed
		} else {
			rs.MaxConcurrent = body.MaxConcurrent
		}
	}); err != nil {
		writeErr(w, http.StatusInternalServerError, "Einstellung konnte nicht gespeichert werden")
		return
	}
	// Apply live. A cleared value reverts to the seed.
	effective := body.MaxConcurrent
	if effective < 1 {
		effective = maxConcurrentSeed()
	}
	if s.scheduler != nil {
		s.scheduler.SetCapacity(effective)
	}
	writeJSON(w, http.StatusOK, map[string]any{"maxConcurrent": effective})
}

// runMaxSlots is a sanity ceiling on the configured slot count — high enough to never bind in practice,
// low enough to reject a fat-fingered value that would spawn thousands of runs.
const runMaxSlots = 64

// maxConcurrentSeed is the env/default fallback for the slot count when nothing is configured in the UI.
func maxConcurrentSeed() int {
	if n := maxConcurrentRuns(); n > 0 {
		return n
	}
	return runs.DefaultMaxConcurrent
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

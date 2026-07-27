package api

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"time"

	"devlab/backend/internal/runs"
)

// Slot management — the read-model and the start-decision that let an urgent ToDo get a slot when all of
// them are taken, instead of a bare refusal (task points 2, 3, 6). It sits in the api layer, not the runs
// package: the automatic suggestion is an EVALUATION over the passive results pool, which by axiom lives
// outside the pool. The scheduler owns admission (the live coordinator); here we read Active()/Capacity()/
// Admissibility() plus the result documents and portion them for the UI and the decision.

// SlotOverview is the portioned "what is happening with the slots right now" view (task point 6): the
// standing capacity and how much of it is used/free, how many runs overload beyond the cap, every run in
// flight, and the deferred runs waiting for a free slot with their resume point. Occupied/free counts and
// the active list are cheap (no file reads); only the few deferred runs cost one result read each.
type SlotOverview struct {
	Capacity int             `json:"capacity"`
	Used     int             `json:"used"`     // standing (non-overload) slots occupied
	Free     int             `json:"free"`     // capacity − used, floored at 0
	Overload int             `json:"overload"` // runs beyond the cap right now (temporary, self-healing)
	Active   []runs.Activity `json:"active"`   // each with its exclusive/overload flags
	Deferred []DeferredView  `json:"deferred"`
}

// DeferredView is one run that gave up its slot and is waiting to resume — with its resume point so the
// overview shows, portioned, exactly where it will pick up (never a raw log).
type DeferredView struct {
	RunID       string `json:"runId"`
	RunName     string `json:"runName,omitempty"`
	ResultID    string `json:"resultId,omitempty"`
	Done        int    `json:"done"`            // repos already completed (kept across the defer)
	Total       int    `json:"total,omitempty"` // known for a ToDo; 0 = unknown (an auto sweep)
	ResumePoint string `json:"resumePoint"`     // human "3 von 8 Repos erledigt — setzt am nächsten freien Platz fort"
}

// StartDecision is returned when a ToDo cannot start because the slots are full: it names WHY, the ways
// forward (task point 2), and the automatic suggestion (task point 3), together with the current slot
// state so the dialog needs no second request.
type StartDecision struct {
	Blocked    string           `json:"blocked"` // runs.Admit* reason
	Options    []string         `json:"options"` // subset of "queue" | "defer" | "overload"
	Suggestion *DeferSuggestion `json:"suggestion,omitempty"`
	Slots      SlotOverview     `json:"slots"`
}

// DeferSuggestion is the system's own pick of which run to stand down — the one whose interruption loses
// the least and is easiest to resume — with a plain-language justification, acceptable in one step.
type DeferSuggestion struct {
	RunID   string `json:"runId"`
	RunName string `json:"runName,omitempty"`
	Reason  string `json:"reason"`
}

// slotOverview assembles the current slot picture. active/capacity are read from the scheduler; the
// deferred list is the stored runs paused with reason "deferred" that are NOT currently resuming.
func (s *Server) slotOverview() SlotOverview {
	ov := SlotOverview{Active: []runs.Activity{}, Deferred: []DeferredView{}}
	if s.scheduler == nil {
		return ov
	}
	active := s.scheduler.Active()
	ov.Active = active
	ov.Capacity = s.scheduler.Capacity()
	live := map[string]bool{}
	exclusive := false
	for _, a := range active {
		live[a.RunID] = true
		if a.Overload {
			ov.Overload++
		}
		if a.Exclusive {
			exclusive = true
		}
	}
	ov.Used = len(active) - ov.Overload
	if ov.Free = ov.Capacity - ov.Used; ov.Free < 0 {
		ov.Free = 0
	}
	// A Rundumlauf holds the WHOLE floor — no other run can start beside it, so no slot is truly free
	// however few are nominally occupied. Report it honestly rather than dangling an unusable "frei".
	if exclusive {
		ov.Free = 0
	}

	all, err := s.runs.List()
	if err != nil {
		return ov
	}
	for _, r := range all {
		// A deferred run that is currently resuming still carries its Suspended pointer until it finishes;
		// it already shows in Active, so exclude it here — the deferred list is only the ones standing by.
		if !r.Suspended.IsDeferred() || live[r.ID] {
			continue
		}
		d := DeferredView{RunID: r.ID, RunName: r.Name, ResultID: r.Suspended.ResultID}
		if r.IsTodo() {
			d.Total = len(r.TodoTargets())
		}
		if done, _, ok := s.runProgress(r.ID, r.Suspended.ResultID); ok {
			d.Done = done
		}
		d.ResumePoint = resumePointText(d.Done, d.Total)
		ov.Deferred = append(ov.Deferred, d)
	}
	sort.Slice(ov.Deferred, func(i, j int) bool { return ov.Deferred[i].RunID < ov.Deferred[j].RunID })
	return ov
}

// resumePointText renders a deferred run's resume point, portioned. total 0 (an auto sweep) omits the
// denominator.
func resumePointText(done, total int) string {
	if total > 0 {
		return fmt.Sprintf("%d von %d Repos erledigt — setzt am nächsten freien Platz fort", done, total)
	}
	return fmt.Sprintf("%d Repos erledigt — setzt am nächsten freien Platz fort", done)
}

// runProgress reads a run's live/partial result once: how many repos it has completed and which repo (if
// any) it is currently working — the input to the deferred resume point and to the defer suggestion. ok
// is false when there is no such result yet (a run that only just started).
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

// buildStartDecision turns an admission block into the choices offered and the automatic suggestion.
// Overload is offered ONLY for a plain cap block — never when a repo is busy or an exclusive run holds the
// floor, because overload must not cross those two limits (task point 5).
func (s *Server) buildStartDecision(run runs.Run, block runs.AdmitBlock) StartDecision {
	d := StartDecision{Blocked: block.Reason, Slots: s.slotOverview(), Options: []string{"queue", "defer"}}
	if block.Reason == runs.AdmitCap {
		d.Options = append(d.Options, "overload")
	}
	d.Suggestion = s.suggestDefer(block)
	return d
}

// suggestDefer picks the run to stand down (task point 3): among the runs that would actually unblock the
// start — any active run for a cap block, or exactly the conflicting/holding runs for a repo-busy or
// exclusive block — the one that loses the least and is easiest to resume (lowest deferScore). The pick is
// returned with a plain-language reason. nil when there is nothing to suggest.
func (s *Server) suggestDefer(block runs.AdmitBlock) *DeferSuggestion {
	candidates := block.Conflicts // repo-busy / exclusive: only these free the way
	if block.Reason == runs.AdmitCap {
		candidates = nil // any active run frees a slot
		for _, a := range s.scheduler.Active() {
			candidates = append(candidates, a.RunID)
		}
	}
	byID := map[string]runs.Activity{}
	for _, a := range s.scheduler.Active() {
		byID[a.RunID] = a
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
		// Lower score wins; ties break toward the YOUNGER run (later StartedAt) — least effort invested.
		if best == "" || score < bestScore || (score == bestScore && a.StartedAt.After(bestStarted)) {
			best, bestScore, bestReason, bestStarted = id, score, reason, a.StartedAt
		}
	}
	if best == "" {
		return nil
	}
	return &DeferSuggestion{RunID: best, RunName: byID[best].RunName, Reason: bestReason}
}

// deferScore ranks an active run as a defer target — LOWER is a better candidate. Task point 3's two
// criteria coincide: a run cleanly BETWEEN repos discards nothing and resumes trivially (score 0), while a
// run mid-repo discards that repo's partial work on defer (a large penalty that grows with how far in it
// is). The exclusive/repo-busy blocks carry their own justification (only that run can free the way). The
// reason returned is the one shown to the user.
func (s *Server) deferScore(a runs.Activity, block runs.AdmitBlock) (int, string) {
	done, liveRepo, _ := s.runProgress(a.RunID, a.ResultID)

	switch block.Reason {
	case runs.AdmitExclusive:
		return 0, "Belegt als Rundumlauf alle Plätze — nur sein Zurückstellen gibt einen Platz frei."
	case runs.AdmitRepoBusy:
		return 0, "Belegt ein Repository, das dieses ToDo braucht — Zurückstellen macht es frei."
	}

	// Cap block: choose by how little is lost.
	if liveRepo != "" {
		// Mid-repo: the in-flight repo's work is what a defer discards. We don't re-read step depth here
		// (the overview already read the result); the presence of a live repo is the signal that this run
		// would lose work, so it ranks WORSE than any between-repos run.
		return 1000, fmt.Sprintf("Ist gerade mitten in „%s“ — hier ginge beim Zurückstellen laufende Arbeit verloren.", liveRepo)
	}
	reason := "Läuft gerade zwischen zwei Repositorys — Zurückstellen verliert keine laufende Arbeit"
	if done > 0 {
		reason += fmt.Sprintf("; %d Repos sind fertig und bleiben es", done)
	}
	return 0, reason + "."
}

// markRunDueNow makes a run due immediately so the scheduler admits it at the next free slot — the "queue"
// half of the start decision, and the handoff after deferring another run. A ToDo becomes due via DueAt;
// an auto run via NextFireAt. Patch (not Mutate): this is runtime scheduling state, not a config edit.
func (s *Server) markRunDueNow(id string) {
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

// runActive reports the full slot overview (task point 6) — capacity, used/free slots, running overloads,
// every active run (with its exclusive/overload flags), and the deferred runs with their resume points. It
// is the single source the UI reads on mount (so running/deferred state survives a reload) and polls to
// follow live. Empty/all-free after a restart, since Active mirrors actually-alive goroutines.
func (s *Server) runActive(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.slotOverview())
}

// runNow starts a run immediately, detached from this request. With no body it attempts a plain start and,
// if the slots are full, returns the start DECISION (200, started:false) — the ways forward plus the
// automatic suggestion — rather than a bare refusal. With a body it carries out the chosen strategy:
//
//	queue    — mark it due so it takes the next free slot (task point 2);
//	overload — run it in a temporary extra slot past the cap (task point 2), refused if a repo is busy or
//	           an exclusive run holds the floor (task point 5);
//	defer    — stand a named run (deferRunId) down to free its slot, then queue this one for it.
//
// 503 when unconfigured, 404 unknown id, 409 when a strategy cannot be carried out.
func (s *Server) runNow(w http.ResponseWriter, r *http.Request) {
	if s.scheduler == nil {
		writeErr(w, http.StatusServiceUnavailable, "Ausführung ist nicht konfiguriert (DEVLAB_RUNS_MODE/DEVLAB_RUNS_USER)")
		return
	}
	id := r.PathValue("id")
	run, ok, err := s.runs.Get(id)
	if err != nil || !ok {
		writeErr(w, http.StatusNotFound, "Kein Lauf mit dieser id")
		return
	}
	// Optional body — a bare "Jetzt ausführen" sends none, so tolerate EOF.
	var body struct {
		Strategy   string `json:"strategy"`
		DeferRunID string `json:"deferRunId"`
	}
	_ = json.NewDecoder(io.LimitReader(r.Body, maxBodyBytes)).Decode(&body)
	_ = r.Body.Close()

	switch body.Strategy {
	case "", "start":
		if s.scheduler.FireNow(id, actor(r)) {
			writeJSON(w, http.StatusOK, map[string]any{"started": true})
			return
		}
		block := s.scheduler.Admissibility(run)
		if block.Reason == runs.AdmitRunning {
			writeErr(w, http.StatusConflict, "Dieser Lauf läuft bereits")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"started": false, "decision": s.buildStartDecision(run, block)})
	case "queue":
		if s.scheduler.FireNow(id, actor(r)) { // a slot may be free after all
			writeJSON(w, http.StatusOK, map[string]any{"started": true})
			return
		}
		s.markRunDueNow(id)
		writeJSON(w, http.StatusOK, map[string]any{"queued": true})
	case "overload":
		if !s.scheduler.StartOverload(id, actor(r)) {
			writeErr(w, http.StatusConflict, "Überladen nicht möglich — ein Ziel-Repository ist belegt oder ein Rundumlauf hält alle Plätze")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"started": true, "overloaded": true})
	case "defer":
		if body.DeferRunID == "" {
			writeErr(w, http.StatusBadRequest, "deferRunId fehlt")
			return
		}
		if !s.scheduler.Defer(body.DeferRunID) {
			writeErr(w, http.StatusConflict, "Der zurückzustellende Lauf ist nicht aktiv")
			return
		}
		// The freed slot opens once the deferred run winds down; queue this one so it takes it. Try an
		// immediate start too, for the rare case a slot is already free.
		if s.scheduler.FireNow(id, actor(r)) {
			writeJSON(w, http.StatusOK, map[string]any{"started": true, "deferred": body.DeferRunID})
			return
		}
		s.markRunDueNow(id)
		writeJSON(w, http.StatusOK, map[string]any{"queued": true, "deferred": body.DeferRunID})
	default:
		writeErr(w, http.StatusBadRequest, "Unbekannte Strategie")
	}
}

// runDefer stands a specific in-flight run down to free its slot (task point 1), as a first-class action
// (the "Zurückstellen" control on the active-runs overview) independent of starting anything else. The run
// keeps its progress and resumes the same execution at the next free slot. 409 if it is not active.
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

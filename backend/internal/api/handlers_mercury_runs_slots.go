package api

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"devlab/backend/internal/runs"
)

// Slot management (TEIL B) — the read-model and the start-decision that let an urgent ToDo get a slot
// when all of them are taken, instead of a bare refusal (task points 5, 6, 8). It sits in the api layer,
// not the runs package: the automatic suggestion is an EVALUATION over the passive results pool, which by
// axiom lives outside the pool. The scheduler owns admission (the live coordinator); here we read
// Active()/Capacity()/Admissibility() plus the result documents and portion them for the UI and decision.

// SlotOverview is the portioned "what is happening with the slots right now" view (task point 8): the
// standing capacity and how much of it is used/free, how many runs overload beyond the cap, and — as one
// enriched, non-redundant list — every run the system is working (executing, plus suspended and deferred
// runs with their resume point). The occupied/free counts and the active list are cheap; the inflight
// enrichment costs one result read per in-flight run.
type SlotOverview struct {
	Capacity int             `json:"capacity"`
	Used     int             `json:"used"`     // standing (non-overload) slots occupied
	Free     int             `json:"free"`     // capacity − used, floored at 0
	Overload int             `json:"overload"` // runs beyond the cap right now (temporary, self-healing)
	Active   []runs.Activity `json:"active"`   // each with its exclusive/overload flags (slot occupancy)
	Inflight []inFlightRun   `json:"inflight"` // executing + suspended + deferred, enriched for the overview
}

// StartDecision is returned when a run cannot start because the slots are full: it names WHY, the ways
// forward (task point 5), and the automatic suggestion (task point 6), together with the current slot
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

// slotOverview assembles the current slot picture: the live runs and capacity from the scheduler, the
// used/free/overload portioning, and the enriched inflight list (which already carries suspended and
// deferred runs, so the deferred ones are visible with their resume point without a second, redundant
// list). A Rundumlauf (auto run) holds the whole floor, so no slot is truly free while it runs.
func (s *Server) slotOverview() SlotOverview {
	ov := SlotOverview{Active: []runs.Activity{}, Inflight: []inFlightRun{}}
	if s.scheduler == nil {
		return ov
	}
	active := s.scheduler.Active()
	ov.Active = active
	ov.Capacity = s.scheduler.Capacity()
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
	if ov.Free = ov.Capacity - ov.Used; ov.Free < 0 {
		ov.Free = 0
	}
	// A Rundumlauf holds the WHOLE floor — no other run can start beside it, so no slot is truly free
	// however few are nominally occupied. Report it honestly rather than dangling an unusable "frei".
	if exclusive {
		ov.Free = 0
	}
	ov.Inflight = s.assembleInFlight(active)
	return ov
}

// runProgress reads a run's live/partial result once: how many repos it has completed and which repo (if
// any) it is currently working — the input to the defer suggestion. ok is false when there is no such
// result yet (a run that only just started).
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
// floor, because overload must not cross those two limits (task point 7).
func (s *Server) buildStartDecision(run runs.Run, block runs.AdmitBlock) StartDecision {
	d := StartDecision{Blocked: block.Reason, Slots: s.slotOverview(), Options: []string{"queue", "defer"}}
	if block.Reason == runs.AdmitCap {
		d.Options = append(d.Options, "overload")
	}
	d.Suggestion = s.suggestDefer(block)
	return d
}

// suggestDefer picks the run to stand down (task point 6): among the runs that would actually unblock the
// start — any active run for a cap block, or exactly the conflicting/holding runs for a repo-busy or
// exclusive block — the one that loses the least and is easiest to resume (lowest deferScore). The pick is
// returned with a plain-language reason. nil when there is nothing to suggest.
func (s *Server) suggestDefer(block runs.AdmitBlock) *DeferSuggestion {
	if s.scheduler == nil {
		return nil
	}
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

// deferScore ranks an active run as a defer target — LOWER is a better candidate. Task point 6's two
// criteria coincide: a run cleanly BETWEEN repos discards nothing and resumes trivially (score 0), while a
// run mid-repo discards that repo's partial work on defer (a large penalty). The exclusive/repo-busy
// blocks carry their own justification (only that run can free the way). The reason returned is shown.
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

// runActive reports the full slot overview (task point 8) — capacity, used/free slots, running overloads,
// every active run (with its exclusive/overload flags), and the enriched inflight list (executing +
// suspended + deferred, each with its resume point). It is the single source the UI reads on mount (so
// running/deferred state survives a reload) and polls to follow live. Empty/all-free after a restart,
// since Active mirrors actually-alive goroutines.
func (s *Server) runActive(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.slotOverview())
}

// runNow starts a run immediately, detached from this request. With no strategy it attempts a plain start
// and, if the slots are full, returns the start DECISION (200, started:false) — the ways forward plus the
// automatic suggestion — rather than a bare refusal. With a strategy it carries out the chosen way:
//
//	queue    — mark it due so it takes the next free slot (task point 5);
//	overload — run it in a temporary extra slot past the cap (task point 5), refused if a repo is busy or
//	           an exclusive run holds the floor (task point 7);
//	defer    — stand a named run (deferRunId) down to free its slot, then queue this one for it.
//
// `fresh` (body or ?fresh=1) forces a fresh start over an interrupted execution; on an immediate start the
// returned `plan` says whether it continued or began anew (and why). 503 unconfigured, 404 unknown id,
// 409 when a strategy cannot be carried out.
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
		Fresh      bool   `json:"fresh"`
	}
	_ = json.NewDecoder(io.LimitReader(r.Body, maxBodyBytes)).Decode(&body)
	_ = r.Body.Close()
	// ?fresh=1 stays supported alongside the body flag (the primary trigger uses it).
	if q := r.URL.Query().Get("fresh"); q == "1" || q == "true" || q == "TRUE" {
		body.Fresh = true
	}
	fresh := body.Fresh

	switch body.Strategy {
	case "", "start":
		if plan, started := s.scheduler.FireNow(id, actor(r), fresh); started {
			writeJSON(w, http.StatusOK, map[string]any{"started": true, "plan": plan})
			return
		}
		block := s.scheduler.Admissibility(run)
		if block.Reason == runs.AdmitRunning {
			writeErr(w, http.StatusConflict, "Dieser Lauf läuft bereits")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"started": false, "decision": s.buildStartDecision(run, block)})
	case "queue":
		if plan, started := s.scheduler.FireNow(id, actor(r), fresh); started { // a slot may be free after all
			writeJSON(w, http.StatusOK, map[string]any{"started": true, "plan": plan})
			return
		}
		if fresh {
			s.scheduler.DiscardResumable(id) // preserve the fresh intent to the actual fire
		}
		s.markRunDueNow(id)
		writeJSON(w, http.StatusOK, map[string]any{"queued": true})
	case "overload":
		plan, started := s.scheduler.StartOverload(id, actor(r), fresh)
		if !started {
			writeErr(w, http.StatusConflict, "Überladen nicht möglich — ein Ziel-Repository ist belegt oder ein Rundumlauf hält alle Plätze")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"started": true, "overloaded": true, "plan": plan})
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
		if plan, started := s.scheduler.FireNow(id, actor(r), fresh); started {
			writeJSON(w, http.StatusOK, map[string]any{"started": true, "deferred": body.DeferRunID, "plan": plan})
			return
		}
		if fresh {
			s.scheduler.DiscardResumable(id)
		}
		s.markRunDueNow(id)
		writeJSON(w, http.StatusOK, map[string]any{"queued": true, "deferred": body.DeferRunID})
	default:
		writeErr(w, http.StatusBadRequest, "Unbekannte Strategie")
	}
}

// runDefer stands a specific in-flight run down to free its slot (task point 4), as a first-class action
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

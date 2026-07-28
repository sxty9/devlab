// Execution & slot handlers (S7) — thin adapters over sched.Scheduler. B2 fills the bodies;
// until then every route answers 501 with a named reason (never a fake state).
package api

import "net/http"

// notYet answers a not-yet-wired execution route honestly.
func (s *Server) notYet(w http.ResponseWriter, what string) {
	writeErr(w, http.StatusNotImplemented, what+" is not wired yet (execution machinery, Welle 1)")
}

// runActive returns the ACTIVE executions as a LIST plus the restart state — read once on
// mount, then SSE-driven; NEVER polled (REQ-034).
func (s *Server) runActive(w http.ResponseWriter, _ *http.Request) {
	if s.scheduler == nil {
		s.notYet(w, "GET /api/mercury/runs/active")
		return
	}
	overview, err := s.scheduler.SlotOverview()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "Could not derive the active list")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"active": overview.Active, "restart": s.scheduler.RestartState()})
}

// runSlots returns the slot overview (capacity, occupancy, deferred, queued starts).
func (s *Server) runSlots(w http.ResponseWriter, _ *http.Request) {
	if s.scheduler == nil {
		s.notYet(w, "GET /api/mercury/runs/slots")
		return
	}
	overview, err := s.scheduler.SlotOverview()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "Could not derive the slot overview")
		return
	}
	writeJSON(w, http.StatusOK, overview)
}

// runNow submits a start — the ONE atomic admission point (sched.Submit): restart gate,
// admission, B-4 preflight gate, resume-vs-fresh.
func (s *Server) runNow(w http.ResponseWriter, r *http.Request) {
	if s.scheduler == nil {
		s.notYet(w, "POST /api/mercury/runs/{id}/run")
		return
	}
	panic("TODO(B2)") // parse placement/fresh → sched.Submit → StartOutcome
}

// runCancel aborts exactly one run's live execution (REQ-013.5).
func (s *Server) runCancel(w http.ResponseWriter, r *http.Request) {
	if s.scheduler == nil {
		s.notYet(w, "POST /api/mercury/runs/{id}/cancel")
		return
	}
	panic("TODO(B2)")
}

// runDefer frees the slot, keeping progress (paused, deferred-by-user).
func (s *Server) runDefer(w http.ResponseWriter, r *http.Request) {
	if s.scheduler == nil {
		s.notYet(w, "POST /api/mercury/runs/{id}/defer")
		return
	}
	panic("TODO(B2)")
}

// runResume resumes a deferred or blocked execution.
func (s *Server) runResume(w http.ResponseWriter, r *http.Request) {
	if s.scheduler == nil {
		s.notYet(w, "POST /api/mercury/runs/{id}/resume")
		return
	}
	panic("TODO(B2)")
}

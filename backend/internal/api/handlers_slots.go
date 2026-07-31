// Execution & slot handlers (S7) — thin adapters over sched.Scheduler: parse → Submit/Defer/
// Resume/Cancel → answer. The scheduler publishes the SSE topics itself (it owns the
// transitions); nothing here polls.
package api

import (
	"encoding/json"
	"errors"
	"net/http"

	"devlab/backend/internal/model"
	"devlab/backend/internal/sched"
)

// notYet answers a not-yet-wired execution route honestly.
func (s *Server) notYet(w http.ResponseWriter, what string) {
	writeErr(w, http.StatusNotImplemented, what+" is not wired yet (execution machinery, Welle 1)")
}

// actorFrom names the caller for the transition protocol (REQ-041).
func actorFrom(r *http.Request) model.Actor {
	if u := userFrom(r); u != nil {
		return model.Actor{User: u.Username}
	}
	return model.Actor{}
}

// runActive returns the ACTIVE executions as a LIST plus the restart state — read once on
// mount, then SSE-driven; NEVER polled (REQ-034).
func (s *Server) runActive(w http.ResponseWriter, _ *http.Request) {
	if s.scheduler == nil {
		s.notYet(w, "GET /api/mercury/runs/active")
		return
	}
	active, err := s.scheduler.ActiveList()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "Could not derive the active list")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"active": active, "restart": s.scheduler.RestartState()})
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
// admission, B-4 preflight gate, resume-vs-fresh. The outcome is always honest: started,
// queued, resumed, or a named reason with the per-target evidence.
func (s *Server) runNow(w http.ResponseWriter, r *http.Request) {
	if s.scheduler == nil {
		s.notYet(w, "POST /api/mercury/runs/{id}/run")
		return
	}
	var body struct {
		Fresh     bool `json:"fresh"`
		Placement *struct {
			Kind             string `json:"kind"`
			DeferExecutionID string `json:"deferExecutionId"`
		} `json:"placement"`
	}
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&body) // an empty body is a plain start
	}
	req := sched.StartRequest{RunID: r.PathValue("id"), By: actorFrom(r), Fresh: body.Fresh}
	if body.Placement != nil {
		req.Placement = &sched.Placement{Kind: body.Placement.Kind, DeferExecutionID: body.Placement.DeferExecutionID}
	}
	out, err := s.scheduler.Submit(r.Context(), req)
	if err != nil {
		if errors.Is(err, sched.ErrUnknownRun) {
			writeErr(w, http.StatusNotFound, "Run not found")
			return
		}
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// runCancel aborts exactly one run's live execution (REQ-013.5).
func (s *Server) runCancel(w http.ResponseWriter, r *http.Request) {
	if s.scheduler == nil {
		s.notYet(w, "POST /api/mercury/runs/{id}/cancel")
		return
	}
	s.execAction(w, r, s.scheduler.Cancel)
}

// runDefer frees the slot, keeping progress (paused, deferred-by-user).
func (s *Server) runDefer(w http.ResponseWriter, r *http.Request) {
	if s.scheduler == nil {
		s.notYet(w, "POST /api/mercury/runs/{id}/defer")
		return
	}
	s.execAction(w, r, s.scheduler.Defer)
}

// runResume resumes a deferred or blocked execution.
func (s *Server) runResume(w http.ResponseWriter, r *http.Request) {
	if s.scheduler == nil {
		s.notYet(w, "POST /api/mercury/runs/{id}/resume")
		return
	}
	s.execAction(w, r, s.scheduler.Resume)
}

// execAction runs one per-run scheduler action and maps its named conditions onto status
// codes: unknown run → 404, wrong state → 409, else 204.
func (s *Server) execAction(w http.ResponseWriter, r *http.Request, act func(runID string, by model.Actor) error) {
	err := act(r.PathValue("id"), actorFrom(r))
	switch {
	case err == nil:
		w.WriteHeader(http.StatusNoContent)
	case errors.Is(err, sched.ErrNotActive):
		writeErr(w, http.StatusConflict, "The run has no live execution")
	case errors.Is(err, sched.ErrNotRunning), errors.Is(err, sched.ErrNotPaused):
		writeErr(w, http.StatusConflict, err.Error())
	case errors.Is(err, sched.ErrUnknownRun):
		writeErr(w, http.StatusNotFound, "Run not found")
	default:
		writeErr(w, http.StatusInternalServerError, err.Error())
	}
}

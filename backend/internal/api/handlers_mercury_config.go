package api

import (
	"net/http"
	"strings"

	"devlab/backend/internal/runs"
)

// Service-level configuration for the Mercury runner — the central config surface the axioms require:
// values that are the SERVICE'S default rather than any single run's choice live here, apart from the
// per-run tuning in the run CRUD. Today that is one value, the default per-repo time budget every run
// follows when it made no own choice; the shape is a struct so further service config joins it without a
// second endpoint or store.

// withEffectiveDefaults fills any blank service default with the value that actually applies, so the
// surface always shows a concrete number (the built-in three hours, or a legacy env override) instead of
// a blank that a reader would have to interpret. Blank stays the persisted meaning ("follow the built-in
// default"); this only projects it for display and echo.
func (s *Server) withEffectiveDefaults(c runs.Config) runs.Config {
	if strings.TrimSpace(c.DefaultTimeBudget) == "" {
		c.DefaultTimeBudget = humanBudget(serviceDefaultBudget(s.settings))
	}
	return c
}

// mercuryConfigGet returns the current service configuration, with blanks resolved to the value in force.
func (s *Server) mercuryConfigGet(w http.ResponseWriter, r *http.Request) {
	if s.settings == nil {
		writeErr(w, http.StatusServiceUnavailable, "settings store unavailable")
		return
	}
	writeJSON(w, http.StatusOK, s.withEffectiveDefaults(s.settings.Get()))
}

// mercuryConfigSet persists a new service configuration. The default time budget is guarded exactly like
// a run's own budget (validateTimeBudget): a non-negative Go duration, "0" for a deliberate no-cap
// default, or "" to reset to the built-in. Returns the same resolved shape GET does.
func (s *Server) mercuryConfigSet(w http.ResponseWriter, r *http.Request) {
	if s.settings == nil {
		writeErr(w, http.StatusServiceUnavailable, "settings store unavailable")
		return
	}
	var body runs.Config
	if !decodeJSON(w, r, &body) {
		return
	}
	tb, code, msg := validateTimeBudget(body.DefaultTimeBudget)
	if code != 0 {
		writeErr(w, code, msg)
		return
	}
	body.DefaultTimeBudget = tb
	if err := s.settings.Set(body); err != nil {
		writeErr(w, http.StatusInternalServerError, "configuration could not be saved")
		return
	}
	writeJSON(w, http.StatusOK, s.withEffectiveDefaults(s.settings.Get()))
}

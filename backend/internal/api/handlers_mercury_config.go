package api

import (
	"net/http"
	"os"
	"time"

	"devlab/backend/internal/runs"
)

// Service-level run configuration: the settings that apply to every run unless a run overrides them.
// Today that is the default time budget. This is the seam a central configuration surface reads and
// writes — the default belongs to the service, apart from the per-run/todo choice.

// defaultTimeBudget resolves the service-wide default agent time budget: the configured value if set,
// else the DEVLAB_RUNS_AGENT_TIMEOUT env (the pre-config bootstrap the cap first shipped with), else the
// built-in fallback (three hours). It returns the duration (0 = no cap) and its display label.
// Resolution is policy and lives OUTSIDE the passive config pool, which only holds the raw string.
func (s *Server) defaultTimeBudget() (time.Duration, string) {
	if s.config != nil {
		if c, err := s.config.Get(); err == nil {
			if d, ok := parseBudget(c.DefaultTimeBudget); ok {
				return d, budgetLabel(d)
			}
		}
	}
	if d, ok := parseBudget(os.Getenv("DEVLAB_RUNS_AGENT_TIMEOUT")); ok {
		return d, budgetLabel(d)
	}
	return runs.DefaultTimeBudgetFallback, budgetLabel(runs.DefaultTimeBudgetFallback)
}

type configBody struct {
	// DefaultTimeBudget is the service default agent time budget: a duration ("3h", "90m"), "0" for no
	// cap, or "" to reset to the built-in default. Same grammar as a per-run budget.
	DefaultTimeBudget string `json:"defaultTimeBudget"`
}

// mercuryConfigGet returns the resolved service configuration — the effective default time budget as a
// label, so the surface can show "Default (3h)" even when nothing is explicitly configured.
func (s *Server) mercuryConfigGet(w http.ResponseWriter, r *http.Request) {
	if s.config == nil {
		writeErr(w, http.StatusServiceUnavailable, "Konfiguration nicht verfügbar")
		return
	}
	_, label := s.defaultTimeBudget()
	writeJSON(w, http.StatusOK, map[string]any{"defaultTimeBudget": label})
}

// mercuryConfigSet stores the service default time budget. It passes the value through the SAME
// normalizer a per-run budget uses (normalizeTimeBudget), so a service default and a per-run override
// are validated and spelled identically — one gate, never a second, divergent one. Returns the newly
// resolved configuration.
func (s *Server) mercuryConfigSet(w http.ResponseWriter, r *http.Request) {
	if s.config == nil {
		writeErr(w, http.StatusServiceUnavailable, "Konfiguration nicht verfügbar")
		return
	}
	var body configBody
	if !decodeJSON(w, r, &body) {
		return
	}
	tb, code, msg := normalizeTimeBudget(body.DefaultTimeBudget)
	if code != 0 {
		writeErr(w, code, msg)
		return
	}
	if err := s.config.Set(runs.Config{DefaultTimeBudget: tb}); err != nil {
		writeErr(w, http.StatusInternalServerError, "Konfiguration konnte nicht gespeichert werden")
		return
	}
	_, label := s.defaultTimeBudget()
	writeJSON(w, http.StatusOK, map[string]any{"defaultTimeBudget": label})
}

// Service interfaces (cross-cutting 5; the /api/service/* convention fixed once, B-14):
// configuration as a complete interface (maintained centrally, not in the service tab),
// load/storage/AI-usage telemetry — reported, never judged. The handlers stay thin: the
// configuration IS the settings pool (one source of truth, no second place to maintain slot
// capacity, the default time budget or the automerge window), and the telemetry answers are
// derived on the spot, never stored.
package api

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"devlab/backend/internal/live"
	"devlab/backend/internal/model"
	"devlab/backend/internal/runs"
	"devlab/backend/internal/telemetry"
)

// maxSlotCapacity bounds a configured slot count. It is a sanity bound against a typo, not a
// policy: the requirement asks for double-digit capacities to actually work.
const maxSlotCapacity = 64

// defaultUsageWindow is the window an AI-usage report covers when the caller names none.
const defaultUsageWindow = 24 * time.Hour

// serviceConfigGet returns the service configuration (model.ServiceConfig).
func (s *Server) serviceConfigGet(w http.ResponseWriter, _ *http.Request) {
	if s.settings == nil {
		writeErr(w, http.StatusNotImplemented, "GET /api/service/config is not wired yet (Welle 1)")
		return
	}
	set, err := s.settings.Get()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "Could not read the service configuration")
		return
	}
	writeJSON(w, http.StatusOK, configOf(set))
}

// serviceConfigPut replaces the service configuration; it takes effect immediately (a capacity
// change reaches the slot overview at once, because the scheduler reads the same pool on every
// admission — there is no cached copy to invalidate).
func (s *Server) serviceConfigPut(w http.ResponseWriter, r *http.Request) {
	if s.settings == nil {
		writeErr(w, http.StatusNotImplemented, "PUT /api/service/config is not wired yet (Welle 1)")
		return
	}
	var body model.ServiceConfig
	if !decodeJSON(w, r, &body) {
		return
	}
	if body.MaxConcurrency < 1 || body.MaxConcurrency > maxSlotCapacity {
		writeErr(w, http.StatusBadRequest, "maxConcurrency must be between 1 and "+strconv.Itoa(maxSlotCapacity))
		return
	}
	if body.DefaultTimeBudget < 0 {
		writeErr(w, http.StatusBadRequest, "defaultTimeBudget cannot be negative (0 means no budget)")
		return
	}
	if body.AutomergeWindow < 0 {
		writeErr(w, http.StatusBadRequest, "automergeWindow cannot be negative")
		return
	}
	set := runs.Settings{
		MaxConcurrency:    body.MaxConcurrency,
		DefaultTimeBudget: time.Duration(body.DefaultTimeBudget),
		AutomergeWindow:   time.Duration(body.AutomergeWindow),
	}
	if err := s.settings.Put(set, actorFrom(r)); err != nil {
		writeErr(w, http.StatusInternalServerError, "Could not save the service configuration")
		return
	}
	// The capacity is part of the slot picture and the budget part of every referring run, so
	// both surfaces are told — after the successful write (the pool stays passive).
	s.publish(live.TopicSlots)
	s.publish(live.TopicRuns)
	writeJSON(w, http.StatusOK, configOf(set))
}

// configOf renders the settings pool as the configuration interface's shape.
func configOf(set runs.Settings) model.ServiceConfig {
	return model.ServiceConfig{
		MaxConcurrency:    set.MaxConcurrency,
		DefaultTimeBudget: model.Duration(set.DefaultTimeBudget),
		AutomergeWindow:   model.Duration(set.AutomergeWindow),
	}
}

// serviceTelemetry reports the process's own load (model.LoadView).
func (s *Server) serviceTelemetry(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, telemetry.Load())
}

// serviceStorage reports the portioned pool occupancy (model.StorageView).
func (s *Server) serviceStorage(w http.ResponseWriter, _ *http.Request) {
	view, err := telemetry.Storage(s.paths)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "Could not report the storage use")
		return
	}
	writeJSON(w, http.StatusOK, view)
}

// serviceAiUsage reports the aggregated AI usage (model.AiUsageView) over a window — ?hours=N,
// default 24, 0 for the whole pool.
func (s *Server) serviceAiUsage(w http.ResponseWriter, r *http.Request) {
	window := defaultUsageWindow
	if raw := strings.TrimSpace(r.URL.Query().Get("hours")); raw != "" {
		h, err := strconv.Atoi(raw)
		if err != nil || h < 0 {
			writeErr(w, http.StatusBadRequest, "hours must be a non-negative number (0 = the whole pool)")
			return
		}
		window = time.Duration(h) * time.Hour
	}
	view, err := s.usage.Aggregate(window)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "Could not report the AI usage")
		return
	}
	writeJSON(w, http.StatusOK, view)
}

// RecordAiUsage is the EXPORTED entry point of the ONE AI-usage pool (cross-cutting 5) — the one
// the executor chain's Deps is wired to from cmd/devlabd, alongside the in-package AI handlers.
// It records through the single implementation (recordAiUsage) rather than repeating it, so the
// service keeps exactly one truth — and one write path — for its AI consumption.
func (s *Server) RecordAiUsage(u telemetry.UsageSample) { s.recordAiUsage(u) }

// Service interfaces (cross-cutting 5; the /api/service/* convention fixed once, B-14):
// configuration as a complete interface (maintained centrally, not in the service tab),
// load/storage/AI-usage telemetry — reported, never judged. B10 fills the bodies over the
// frozen SettingsStore/telemetry contracts.
package api

import "net/http"

// serviceConfigGet returns the service configuration (model.ServiceConfig).
func (s *Server) serviceConfigGet(w http.ResponseWriter, _ *http.Request) {
	if s.settings == nil {
		writeErr(w, http.StatusNotImplemented, "GET /api/service/config is not wired yet (Welle 1)")
		return
	}
	panic("TODO(B10)")
}

// serviceConfigPut replaces the service configuration; it takes effect immediately
// (capacity changes reach the slot overview at once).
func (s *Server) serviceConfigPut(w http.ResponseWriter, r *http.Request) {
	if s.settings == nil {
		writeErr(w, http.StatusNotImplemented, "PUT /api/service/config is not wired yet (Welle 1)")
		return
	}
	panic("TODO(B10)")
}

// serviceTelemetry reports the process's own load (model.LoadView).
func (s *Server) serviceTelemetry(w http.ResponseWriter, _ *http.Request) {
	panic("TODO(B10)")
}

// serviceStorage reports the portioned pool occupancy (model.StorageView).
func (s *Server) serviceStorage(w http.ResponseWriter, _ *http.Request) {
	panic("TODO(B10)")
}

// serviceAiUsage reports the aggregated AI usage (model.AiUsageView).
func (s *Server) serviceAiUsage(w http.ResponseWriter, _ *http.Request) {
	panic("TODO(B10)")
}

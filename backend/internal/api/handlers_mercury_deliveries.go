// Delivery handlers (S10) — thin adapters over the delivery ledger and package deliver. The
// list is a pure ledger read (available now); rollback and the deliberate repo reset go
// through deliver.Rollback / workbench.ResetToDefault — B4 fills those bodies.
package api

import (
	"net/http"

	"devlab/backend/internal/model"
)

// runDeliveriesList returns the delivery ledger as the wire view (REQ-024/F12).
func (s *Server) runDeliveriesList(w http.ResponseWriter, _ *http.Request) {
	if s.deliveries == nil {
		writeJSON(w, http.StatusOK, map[string]any{"deliveries": []model.Delivery{}})
		return
	}
	all, err := s.deliveries.All()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "Could not read the delivery ledger")
		return
	}
	out := make([]model.Delivery, 0, len(all))
	for _, d := range all {
		out = append(out, model.Delivery{
			ID: d.ID, Repo: d.Repo, Branch: d.Branch,
			FromCommit: d.FromCommit, ToCommit: d.ToCommit,
			PRNumber: d.PRNumber, PRURL: d.PRURL,
			CreatedAt: d.CreatedAt, MergedAt: d.MergedAt, ReversalOf: d.ReversalOf,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"deliveries": out})
}

// runDeliveryRollback counter-books one delivery (REQ-025) via deliver.Rollback.
func (s *Server) runDeliveryRollback(w http.ResponseWriter, r *http.Request) {
	writeErr(w, http.StatusNotImplemented, "Rollback is not wired yet (delivery chain, Welle 1)")
}

// runRepoReset is the DELIBERATE dev reset (REQ-022.4) — only ever behind the UI confirmation;
// it calls workbench.ResetToDefault.
func (s *Server) runRepoReset(w http.ResponseWriter, r *http.Request) {
	writeErr(w, http.StatusNotImplemented, "Repo reset is not wired yet (workbench, Welle 1)")
}

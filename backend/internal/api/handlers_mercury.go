package api

import (
	"net/http"

	"devlab/backend/internal/aigentic"
	"devlab/backend/internal/mercury"
)

// Mercury is the axiom-management surface. It reads and writes the constitution through aigentic's
// scheme-backed graveyard (owning no store of its own), forwarding the caller's session so aigentic
// resolves the same identity and enforces its own rights. Read endpoints are guard; writes are the
// next stage.

// mercuryTree returns the whole axiom model — the three namespaces (axiome, regeln, laeufe) as
// arbitrarily deep category trees, derived purely from the record paths.
func (s *Server) mercuryTree(w http.ResponseWriter, r *http.Request) {
	paths, status, err := aigentic.GraveList(r.Context(), r.Header.Get("Cookie"), "")
	if err != nil {
		mercuryError(w, status, err)
		return
	}
	writeJSON(w, http.StatusOK, mercury.Build(paths))
}

// mercuryItem returns one record's parsed content (front-matter + body) by its path.
func (s *Server) mercuryItem(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Query().Get("path")
	if path == "" {
		writeErr(w, http.StatusBadRequest, "path is required")
		return
	}
	data, found, status, err := aigentic.GraveGet(r.Context(), r.Header.Get("Cookie"), path)
	if err != nil {
		mercuryError(w, status, err)
		return
	}
	if !found {
		writeErr(w, http.StatusNotFound, "No axiom at that path")
		return
	}
	ax := mercury.ParseAxiom(string(data))
	writeJSON(w, http.StatusOK, map[string]any{"path": path, "axiom": ax})
}

// mercuryError maps aigentic's status onto DevLab's, translating the store's absence into an
// actionable message rather than a bare 502.
func mercuryError(w http.ResponseWriter, status int, err error) {
	switch status {
	case http.StatusServiceUnavailable:
		writeErr(w, http.StatusServiceUnavailable, "Mercury's store is unavailable — aigentic must run with the scheme graveyard")
	case http.StatusForbidden:
		writeErr(w, http.StatusForbidden, "Mercury needs the aigentic 'run' right (hp_aigentic_run)")
	default:
		writeErr(w, http.StatusBadGateway, "Mercury store error")
	}
}

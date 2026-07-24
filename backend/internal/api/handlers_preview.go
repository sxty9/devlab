package api

import (
	"net/http"
	"strings"

	"devlab/backend/internal/preview"
)

// previewCreate spins up a per-user, passphrase-gated preview of the caller's branch. Needs push
// (don't spend a public subdomain on a branch the caller can't ship). The workspace is ensured so
// the branch commit + .sxgate/preview.conf exist locally for the as-user checkout. The lock is
// released before the (possibly slow) build so it doesn't block the user's other ops on the repo.
func (s *Server) previewCreate(w http.ResponseWriter, r *http.Request) {
	if !s.requireRealGitHub(w, "Previews") {
		return
	}
	wc, unlock, ok := s.mutateCtx(w, r, true)
	if !ok {
		return
	}
	var body struct {
		Branch string `json:"branch"`
	}
	if !decodeJSON(w, r, &body) {
		unlock()
		return
	}
	branch := strings.TrimSpace(body.Branch)
	if branch == "" {
		branch = wc.exec.CurrentBranch(r.Context(), wc.wt)
	}
	unlock()
	if branch == "" {
		writeErr(w, http.StatusBadRequest, "No branch to preview")
		return
	}
	p, err := preview.Up(r.Context(), wc.user, r.PathValue("id"), branch)
	if err != nil {
		writeErr(w, http.StatusBadGateway, "Preview failed: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, p)
}

// previewList returns the caller's previews for this repo (filtered by owner, so no cross-user leak).
func (s *Server) previewList(w http.ResponseWriter, r *http.Request) {
	u := userFrom(r)
	if u == nil {
		writeErr(w, http.StatusForbidden, "Not authorized")
		return
	}
	list := preview.List(u.Username, r.PathValue("id"))
	if list == nil {
		list = []preview.Preview{}
	}
	writeJSON(w, http.StatusOK, list)
}

// previewDelete tears down one of the caller's previews (the wrapper re-checks ownership).
func (s *Server) previewDelete(w http.ResponseWriter, r *http.Request) {
	if !s.requireRealGitHub(w, "Previews") {
		return
	}
	u := userFrom(r)
	if err := preview.Down(r.Context(), u.Username, r.PathValue("slug")); err != nil {
		writeErr(w, http.StatusBadGateway, "Could not remove preview: "+err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

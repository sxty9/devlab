package api

import (
	"net/http"

	"devlab/backend/internal/atlas"
	"devlab/backend/internal/model"
)

// atlasGraph returns the deployed Holistic landscape: the services this host actually runs, derived
// from their rights manifests and Caddy routes, plus the inconsistencies between them.
//
// Read-only and host-scoped — it reads nothing of the caller's, and touches no workspace. Their repo
// set is used only to link a deployed service back to a repo they can open, so a GitHub outage
// degrades the graph rather than failing it.
func (s *Server) atlasGraph(w http.ResponseWriter, r *http.Request) {
	repos, ok := s.userRepos(r)
	writeJSON(w, http.StatusOK, atlas.GraphFor(repoIDs(repos), ok))
}

func repoIDs(repos []model.Repo) []string {
	ids := make([]string, 0, len(repos))
	for _, r := range repos {
		ids = append(ids, r.ID)
	}
	return ids
}

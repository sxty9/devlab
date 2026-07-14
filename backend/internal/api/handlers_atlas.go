package api

import (
	"net/http"

	"devlab/backend/internal/atlas"
	"devlab/backend/internal/discover"
	"devlab/backend/internal/model"
)

// atlasGraph returns the deployed Holistic landscape: the services this host actually runs, derived
// from their rights manifests and Caddy routes, plus the inconsistencies between them.
//
// Read-only and host-scoped — it reads nothing of the caller's, and touches no workspace. The
// caller's repo set is used only to link a deployed service back to a repo they can open.
func (s *Server) atlasGraph(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, atlas.GraphFor(repoIDs(s.visibleRepos(r))))
}

// visibleRepos resolves the caller's repo set, degrading to an empty set rather than failing: Atlas
// is about the host, and a GitHub outage must not take the landscape down with it.
func (s *Server) visibleRepos(r *http.Request) []model.Repo {
	if s.v.DevBypass() {
		return discover.Repos(s.reposBase)
	}
	u := userFrom(r)
	token, err := s.userToken(u)
	if err != nil {
		return nil
	}
	repos, err := discover.ReposForUser(r.Context(), u.Username, token)
	if err != nil {
		return nil
	}
	return repos
}

func repoIDs(repos []model.Repo) []string {
	ids := make([]string, 0, len(repos))
	for _, r := range repos {
		ids = append(ids, r.ID)
	}
	return ids
}

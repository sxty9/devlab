package api

import (
	"net/http"
	"strconv"
	"strings"

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

// atlasPorts returns the central port ledger: which service holds which routed port, and which ports
// in the managed band are free. Read-only — the same passive projection as the graph, scoped to ports,
// so what the dashboard shows can never drift from what is actually deployed.
func (s *Server) atlasPorts(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, atlas.AllocationNow())
}

// atlasProposePort answers "which port should service <id> take?" — the endpoint setup consults so a
// new service draws a free port from the central allocation instead of copying a template default. It
// never resolves silently: when the desired port is taken, the response names the holder and grants a
// free port instead, so setup can report the clash rather than install a service that will not start.
func (s *Server) atlasProposePort(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	id := strings.TrimSpace(q.Get("id"))
	if id == "" || len(id) > 40 {
		writeErr(w, http.StatusBadRequest, "id required")
		return
	}
	desired := 0
	if d := strings.TrimSpace(q.Get("desired")); d != "" {
		n, err := strconv.Atoi(d)
		if err != nil || n < 0 || n > 65535 {
			writeErr(w, http.StatusBadRequest, "desired must be a port number")
			return
		}
		desired = n
	}
	writeJSON(w, http.StatusOK, atlas.Propose(id, desired))
}

func repoIDs(repos []model.Repo) []string {
	ids := make([]string, 0, len(repos))
	for _, r := range repos {
		ids = append(ids, r.ID)
	}
	return ids
}

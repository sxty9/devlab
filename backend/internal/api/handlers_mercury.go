package api

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"sync"

	"devlab/backend/internal/axiomrepo"
	"devlab/backend/internal/mercury"
)

// Mercury is the axiom-management surface. It reads and writes the constitution through a single
// access point — the axiomrepo store, a working copy of the dedicated constitution Git repository.
// There is no second path to the same data.
//
// The one thing the record layout cannot express is a manual sibling order (paths sort by name), so
// Mercury keeps a small display-only order file beside the clone — a global preference, since the
// constitution is global. orderMu guards its load-modify-save.
var orderMu sync.Mutex

// mercuryOrderPath resolves the manual-order file, matching the other DevLab store conventions.
func mercuryOrderPath() string {
	if p := os.Getenv("DEVLAB_MERCURY_ORDER"); p != "" {
		return p
	}
	return filepath.Join("/var/lib/devlab/mercury", "order.json")
}

// mercuryTree returns the whole axiom model — the three namespaces (axiome, regeln, laeufe) as
// arbitrarily deep category trees, derived from the record paths and the user's manual sibling order.
func (s *Server) mercuryTree(w http.ResponseWriter, r *http.Request) {
	paths, err := s.axioms.List(r.Context(), "")
	if err != nil {
		mercuryError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, mercury.Build(paths, mercury.LoadOrder(mercuryOrderPath())))
}

// mercuryReorder sets the manual order of one category's immediate children. The body is the
// complete ordered list of child names (subcategory names and ".md" leaf names) — the same
// full-array shape the mail service uses, so a partial list can't silently drop entries.
func (s *Server) mercuryReorder(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Category string   `json:"category"`
		Order    []string `json:"order"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if body.Category == "" || body.Order == nil {
		writeErr(w, http.StatusBadRequest, "category and order are required")
		return
	}
	orderMu.Lock()
	defer orderMu.Unlock()
	path := mercuryOrderPath()
	o := mercury.LoadOrder(path)
	o[body.Category] = body.Order
	if err := mercury.SaveOrder(path, o); err != nil {
		writeErr(w, http.StatusInternalServerError, "Reihenfolge konnte nicht gespeichert werden")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// mercuryItem returns one record's parsed content (front-matter + body) by its path.
func (s *Server) mercuryItem(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Query().Get("path")
	if path == "" {
		writeErr(w, http.StatusBadRequest, "path is required")
		return
	}
	data, found, err := s.axioms.Get(r.Context(), path)
	if err != nil {
		mercuryError(w, err)
		return
	}
	if !found {
		writeErr(w, http.StatusNotFound, "No axiom at that path")
		return
	}
	ax := mercury.ParseAxiom(string(data))
	resp := map[string]any{"path": path, "axiom": ax}
	// Join the local authorship pool by the stable id. Absent ⇒ the client shows the author as unknown.
	if a, ok := s.axiomAuthors.Get(ax.ID); ok {
		resp["author"] = a
	}
	writeJSON(w, http.StatusOK, resp)
}

// mercuryError maps a Mercury backend failure onto an HTTP response that is honest about WHY it could
// not be served. The two store-outage sentinels get distinct, actionable 503s — and, deliberately,
// neither is ever an empty success: a constitution that cannot be reached must never read as "there
// are no axioms". Anything else (a run-store slip, an unexpected git error) falls through to a plain
// 502 carrying the underlying detail.
func mercuryError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, axiomrepo.ErrNoToken):
		writeErr(w, http.StatusServiceUnavailable,
			"The constitution is unavailable: no GitHub account is linked to reach the axioms repository. "+
				"Link the runner account, then retry — this is a connection problem, not an empty constitution.")
	case errors.Is(err, axiomrepo.ErrUnavailable):
		writeErr(w, http.StatusServiceUnavailable,
			"The constitution is unavailable: the axioms repository could not be reached (no network, or the "+
				"clone failed). This is a read error, not an empty constitution — no axioms have been lost.")
	default:
		writeErr(w, http.StatusBadGateway, "Mercury backend error: "+err.Error())
	}
}

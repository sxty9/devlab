package api

import (
	"net/http"
	"os"
	"path/filepath"
	"sync"

	"devlab/backend/internal/aigentic"
	"devlab/backend/internal/mercury"
)

// Mercury is the axiom-management surface. It reads and writes the constitution through aigentic's
// scheme-backed graveyard (owning no store of its own), forwarding the caller's session so aigentic
// resolves the same identity and enforces its own rights.
//
// The one thing scheme cannot express is a manual sibling order (it sorts by name), so Mercury keeps
// a small display-only order file beside it — a global preference, since the constitution is global.
// orderMu guards its load-modify-save.
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
	paths, status, err := aigentic.GraveList(r.Context(), r.Header.Get("Cookie"), "")
	if err != nil {
		mercuryError(w, status, err)
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

package api

import (
	"net/http"
	"strconv"
	"strings"

	"devlab/backend/internal/aigentic"
	"devlab/backend/internal/mercury"
)

// The edit surface lets a user own the tree by hand: change an axiom's text, re-file it into another
// category, rename it, or delete it — and rename a whole category. Everything rides the graveyard
// primitives (put/move/delete): scheme carries a record's content with its path on a move, so a
// re-file or rename never rewrites the axiom, and the front-matter id stays stable across it.

// editAxiom replaces an axiom's title and body, preserving its id and quelle. The leaf slug is
// re-derived from the new title so the heading always matches the path — a title change renames the
// record within its category; a body-only edit leaves the path untouched. A re-file into another
// category is a move (below).
func (s *Server) editAxiom(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Path  string `json:"path"`
		Titel string `json:"titel"`
		Body  string `json:"body"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	body.Path = strings.TrimSpace(body.Path)
	body.Titel = strings.TrimSpace(body.Titel)
	body.Body = strings.TrimSpace(body.Body)
	if body.Path == "" || body.Titel == "" || body.Body == "" {
		writeErr(w, http.StatusBadRequest, "path, titel and body are required")
		return
	}

	cookie := r.Header.Get("Cookie")
	csrf := csrfFrom(r)

	// Read the existing record so the stable id and quelle survive the edit.
	data, found, status, err := aigentic.GraveGet(r.Context(), cookie, body.Path)
	if err != nil {
		mercuryError(w, status, err)
		return
	}
	if !found {
		writeErr(w, http.StatusNotFound, "No axiom at that path")
		return
	}
	prev := mercury.ParseAxiom(string(data))

	ax := mercury.Axiom{ID: prev.ID, Titel: body.Titel, Quelle: prev.Quelle, Body: body.Body}
	if ax.ID == "" {
		ax.ID = mintID() // a record that predates ids gets one now
	}
	content := []byte(mercury.Render(ax))

	// Keep the heading matched to the path: if the new title slugs to a different leaf, rename the
	// record (same category) before writing. A body-only edit leaves the slug unchanged and never moves.
	newPath := reslugLeaf(body.Path, body.Titel)
	if newPath != body.Path {
		if status, err := aigentic.GraveMove(r.Context(), cookie, csrf, body.Path, newPath); err != nil {
			if status == http.StatusConflict {
				writeErr(w, http.StatusConflict, "In dieser Kategorie existiert bereits ein Axiom mit diesem Titel")
				return
			}
			mercuryError(w, status, err)
			return
		}
	}
	if _, status, err := aigentic.GravePut(r.Context(), cookie, csrf, newPath, firstLine(body.Body), content, true); err != nil {
		mercuryError(w, status, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"path": newPath, "axiom": ax})
}

// moveAxiom re-files or renames an axiom: from and to are full record paths. A category change
// re-files it; a slug change renames it. The destination must be free (aigentic returns 409).
func (s *Server) moveAxiom(w http.ResponseWriter, r *http.Request) {
	var body struct {
		From string `json:"from"`
		To   string `json:"to"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	body.From, body.To = strings.TrimSpace(body.From), strings.TrimSpace(body.To)
	if !mercury.ValidRecordPath(body.To) {
		writeErr(w, http.StatusBadRequest, "Ungültiger Zielpfad — erwartet z. B. axiome/kategorie/name.md (Kleinbuchstaben, Bindestriche)")
		return
	}
	if body.From == body.To {
		writeJSON(w, http.StatusOK, map[string]string{"path": body.To})
		return
	}
	status, err := aigentic.GraveMove(r.Context(), r.Header.Get("Cookie"), csrfFrom(r), body.From, body.To)
	if err != nil {
		if status == http.StatusConflict {
			writeErr(w, http.StatusConflict, "Am Zielpfad liegt bereits ein Axiom")
			return
		}
		mercuryError(w, status, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"path": body.To})
}

// deleteAxiom removes an axiom. Idempotent.
func (s *Server) deleteAxiom(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Query().Get("path")
	if !mercury.ValidRecordPath(path) {
		writeErr(w, http.StatusBadRequest, "Ungültiger Pfad")
		return
	}
	if status, err := aigentic.GraveDelete(r.Context(), r.Header.Get("Cookie"), csrfFrom(r), path); err != nil {
		mercuryError(w, status, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// moveCategory renames or re-homes a whole category: every record under `from/` is moved to the
// same suffix under `to/`. Because scheme has no empty folders, moving the leaves IS renaming the
// category. Best-effort per record; the first hard failure stops and is reported.
func (s *Server) moveCategory(w http.ResponseWriter, r *http.Request) {
	var body struct {
		From string `json:"from"`
		To   string `json:"to"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	body.From, body.To = strings.TrimSpace(body.From), strings.TrimSpace(body.To)
	if !mercury.ValidCategory(body.From) || !mercury.ValidCategory(body.To) {
		writeErr(w, http.StatusBadRequest, "Ungültige Kategorie — erwartet z. B. axiome/architektur/uniformitaet")
		return
	}
	if body.From == body.To {
		writeJSON(w, http.StatusOK, map[string]int{"moved": 0})
		return
	}

	cookie := r.Header.Get("Cookie")
	csrf := csrfFrom(r)
	// List by the bare prefix (scheme's list rejects a trailing slash), then keep only records
	// genuinely UNDER this category — "axiome/ui" must not sweep up a sibling "axiome/ui-native".
	all, status, err := aigentic.GraveList(r.Context(), cookie, body.From)
	if err != nil {
		mercuryError(w, status, err)
		return
	}
	prefix := body.From + "/"
	moved := 0
	for _, leaf := range all {
		if !strings.HasPrefix(leaf, prefix) {
			continue
		}
		dest := body.To + strings.TrimPrefix(leaf, body.From)
		if _, err := aigentic.GraveMove(r.Context(), cookie, csrf, leaf, dest); err != nil {
			writeErr(w, http.StatusConflict, "Konnte "+leaf+" nicht verschieben (Ziel belegt?); "+strconv.Itoa(moved)+" bereits verschoben")
			return
		}
		moved++
	}
	writeJSON(w, http.StatusOK, map[string]int{"moved": moved})
}

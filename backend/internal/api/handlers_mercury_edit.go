package api

import (
	"errors"

	"devlab/backend/internal/axiomrepo"
	"net/http"
	"strconv"
	"strings"
	"time"

	"devlab/backend/internal/axiomauthors"
	"devlab/backend/internal/live"
	"devlab/backend/internal/mercury"
)

// The edit surface lets a user own the tree by hand: change an axiom's text, re-file it into another
// category, rename it, or delete it — and rename a whole category. Everything rides the store
// primitives (put/move/delete): a move carries a record's content with its path, so a re-file or
// rename never rewrites the axiom, and the front-matter id stays stable across it.

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

	// Read the existing record so the stable id and quelle survive the edit.
	data, found, err := s.axioms.Get(r.Context(), body.Path)
	if err != nil {
		mercuryError(w, err)
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
		if err := s.axioms.Move(r.Context(), body.Path, newPath, "Rename axiom: "+body.Titel, actor(r)); err != nil {
			if errors.Is(err, axiomrepo.ErrExists) {
				writeErr(w, http.StatusConflict, "An axiom with this title already exists in this category")
				return
			}
			mercuryError(w, err)
			return
		}
	}
	if err := s.axioms.Put(r.Context(), newPath, string(content), firstLine(body.Body), actor(r), true); err != nil {
		mercuryError(w, err)
		return
	}
	s.reconcileAfterWrite(r.Context(), cookie)
	s.publish(live.TopicAxioms)
	// Record the editor in DevLab's local pool. An axiom that predates authorship tracking keeps an
	// empty (unknown) creator — only the editor is stamped, never a back-filled creator.
	now := time.Now().UTC()
	who := actor(r)
	s.axiomAuthors.Mutate(ax.ID, func(a axiomauthors.Author) axiomauthors.Author {
		a.UpdatedBy, a.UpdatedAt = who, now
		return a
	})
	writeJSON(w, http.StatusOK, map[string]any{"path": newPath, "axiom": ax})
}

// moveAxiom re-files or renames an axiom: from and to are full record paths. A category change
// re-files it; a slug change renames it. The destination must be free (the store returns ErrExists).
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
		writeErr(w, http.StatusBadRequest, "Invalid target path — expected e.g. axiome/category/name.md (lowercase, hyphens)")
		return
	}
	if body.From == body.To {
		writeJSON(w, http.StatusOK, map[string]string{"path": body.To})
		return
	}
	cookie := r.Header.Get("Cookie")
	err := s.axioms.Move(r.Context(), body.From, body.To, "Move: "+body.From+" → "+body.To, actor(r))
	if err != nil {
		if errors.Is(err, axiomrepo.ErrExists) {
			writeErr(w, http.StatusConflict, "An axiom already exists at the target path")
			return
		}
		mercuryError(w, err)
		return
	}
	s.reconcileAfterWrite(r.Context(), cookie)
	s.publish(live.TopicAxioms)
	writeJSON(w, http.StatusOK, map[string]string{"path": body.To})
}

// deleteAxiom removes an axiom. Idempotent.
func (s *Server) deleteAxiom(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Query().Get("path")
	if !mercury.ValidRecordPath(path) {
		writeErr(w, http.StatusBadRequest, "Invalid path")
		return
	}
	cookie := r.Header.Get("Cookie")
	if err := s.axioms.Delete(r.Context(), path, "Delete: "+path, actor(r)); err != nil {
		mercuryError(w, err)
		return
	}
	s.reconcileAfterWrite(r.Context(), cookie)
	s.publish(live.TopicAxioms)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// moveCategory renames or re-homes a whole category: every record under `from/` is moved to the
// same suffix under `to/`. Because git tracks no empty folders, moving the leaves IS renaming the
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
		writeErr(w, http.StatusBadRequest, "Invalid category — expected e.g. axiome/architecture/uniformity")
		return
	}
	if body.From == body.To {
		writeJSON(w, http.StatusOK, map[string]int{"moved": 0})
		return
	}

	cookie := r.Header.Get("Cookie")
	// List by the bare prefix, then keep only records genuinely UNDER this category — the prefix match
	// alone would let "axiome/ui" sweep up a sibling "axiome/ui-native", so re-check the "/" boundary.
	all, err := s.axioms.List(r.Context(), body.From)
	if err != nil {
		mercuryError(w, err)
		return
	}
	prefix := body.From + "/"
	moved := 0
	for _, leaf := range all {
		if !strings.HasPrefix(leaf, prefix) {
			continue
		}
		dest := body.To + strings.TrimPrefix(leaf, body.From)
		if err := s.axioms.Move(r.Context(), leaf, dest, "Move category: "+leaf+" → "+dest, actor(r)); err != nil {
			writeErr(w, http.StatusConflict, "Could not move "+leaf+" (target occupied?); "+strconv.Itoa(moved)+" already moved")
			return
		}
		moved++
	}
	if moved > 0 {
		s.reconcileAfterWrite(r.Context(), cookie)
		s.publish(live.TopicAxioms)
	}
	writeJSON(w, http.StatusOK, map[string]int{"moved": moved})
}

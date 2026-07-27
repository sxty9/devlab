package api

import (
	"fmt"
	"net/http"

	"devlab/backend/internal/live"
	"devlab/backend/internal/mercury"
)

// migrateResult reports where one decomposed atom was (or would be) filed.
type migrateResult struct {
	Titel      string `json:"titel"`
	Quelle     string `json:"quelle"`
	Path       string `json:"path"`
	Classified bool   `json:"classified"`
}

// mercuryMigrate performs the one-time constitution migration: it decomposes the original axiom
// document into fine-grained atoms and files each at a path that MIRRORS the source structure —
// axiome/<section>/<maxim>/<atom>. This is deterministic and faithful: the constitution's own
// sections (architektur, prozess, gesetzbuecher, umgebung, mobile) are a coherent taxonomy, so a
// structural migration beats an AI guess that cold-starts by dumping everything into one category.
// (The AI classifier is for NEW axioms added later, where there is no known section.) Dry-run by
// default; only ?apply=true writes. Intended to run ONCE against a clean store.
func (s *Server) mercuryMigrate(w http.ResponseWriter, r *http.Request) {
	apply := r.URL.Query().Get("apply") == "true"
	cookie := r.Header.Get("Cookie")
	csrf := csrfFrom(r)

	atoms := mercury.Decompose()
	results := make([]migrateResult, 0, len(atoms))
	used := map[string]bool{} // guard against duplicate slugs within a category before the store sees them

	for _, atom := range atoms {
		path := migratePath(atom, used)
		used[path] = true
		mr := migrateResult{Titel: atom.SeedTitle, Quelle: atom.Quelle, Path: path, Classified: true}

		if apply {
			ax := mercury.Axiom{ID: mintID(), Titel: atom.SeedTitle, Quelle: atom.Quelle, Body: atom.Body}
			placed, perr := s.putAxiom(r.Context(), cookie, csrf, path, firstLine(atom.Body), mercury.Render(ax))
			if perr != nil {
				mr.Path = "FEHLER: " + perr.Error()
			} else {
				mr.Path = placed
			}
		}
		results = append(results, mr)
	}

	if apply {
		s.publish(live.TopicAxioms)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"applied": apply,
		"atoms":   len(atoms),
		"results": results,
	})
}

// migratePath builds the structural path for an atom, disambiguating a slug clash within its
// category with a numeric suffix so no two atoms collide.
func migratePath(atom mercury.Atom, used map[string]bool) string {
	dir := "axiome/" + atom.SuggestedCategory
	if atom.MaximSlug != "" {
		dir += "/" + atom.MaximSlug
	}
	base := mercury.Slug(atom.SeedTitle)
	if base == "" {
		base = "axiom"
	}
	path := dir + "/" + base + ".md"
	for n := 2; used[path]; n++ {
		path = fmt.Sprintf("%s/%s-%d.md", dir, base, n)
	}
	return path
}

// firstLine is the record's mandatory scheme description: the first line of the body, trimmed to a
// sane length.
func firstLine(body string) string {
	line := body
	if i := indexNewline(body); i >= 0 {
		line = body[:i]
	}
	if len(line) > 160 {
		line = line[:160]
	}
	if line == "" {
		return "Axiom"
	}
	return line
}

func indexNewline(s string) int {
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			return i
		}
	}
	return -1
}

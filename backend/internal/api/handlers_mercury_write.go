package api

import (
	"context"
	"crypto/rand"
	"encoding/base32"
	"fmt"
	"net/http"
	"strings"

	"devlab/backend/internal/aigentic"
	"devlab/backend/internal/mercury"
)

const classifyAttempts = 3

// addAxiom files a new axiom into the tree. aigentic (claude-cli) proposes the category path; the
// placement is validated and retried with a correction on any contract violation; after three
// failures the axiom is parked under axiome/unsortiert/ rather than guessed — visible, never lost.
// A stable id is minted into the axiom's front-matter (survives a later re-file), and the write
// refuses to clobber (409 → the slug is disambiguated and retried once).
func (s *Server) addAxiom(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Titel string `json:"titel"`
		Body  string `json:"body"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	body.Titel = strings.TrimSpace(body.Titel)
	body.Body = strings.TrimSpace(body.Body)
	if body.Titel == "" || body.Body == "" {
		writeErr(w, http.StatusBadRequest, "titel and body are required")
		return
	}

	cookie := r.Header.Get("Cookie")
	csrf := csrfFrom(r)

	paths, status, err := aigentic.GraveList(r.Context(), cookie, "")
	if err != nil {
		mercuryError(w, status, err)
		return
	}
	categories := mercury.Categories(paths)

	placement, ok := s.classify(r.Context(), cookie, csrf, categories, body.Titel, body.Body)
	titel := body.Titel
	var path, desc string
	if ok {
		path, desc, titel = placement.Pfad, placement.Beschreibung, firstNonEmpty(placement.Titel, body.Titel)
	} else {
		// Could not classify after retries: park it, still described, still findable.
		path = "axiome/unsortiert/" + slug(body.Titel) + ".md"
		desc = "unsortiert: " + body.Titel
	}

	ax := mercury.Axiom{ID: mintID(), Titel: titel, Body: body.Body}
	content := mercury.Render(ax)

	placed, err := s.putAxiom(r.Context(), cookie, csrf, path, desc, content)
	if err != nil {
		mercuryError(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"path":       placed,
		"id":         ax.ID,
		"classified": ok,
	})
}

// classify runs the classification loop: prompt aigentic (claude-cli), validate, and retry with a
// correction line on any contract violation, up to classifyAttempts. ok is false when every attempt
// failed (the caller parks the axiom).
func (s *Server) classify(ctx context.Context, cookie, csrf string, categories []string, titel, body string) (mercury.Placement, bool) {
	correction := ""
	for i := 0; i < classifyAttempts; i++ {
		prompt := mercury.ClassifyPrompt(categories, titel, body, correction)
		result, _, err := aigentic.Run(ctx, cookie, csrf, "claude-cli", aigentic.Request{Prompt: prompt, OutputFormat: "text"})
		if err != nil {
			return mercury.Placement{}, false // aigentic unreachable / no subscription: park it
		}
		p, perr := mercury.ParsePlacement(result.Output, categories)
		if perr == nil {
			return p, true
		}
		correction = perr.Error()
	}
	return mercury.Placement{}, false
}

// putAxiom writes the record, disambiguating a slug clash once (409 → append -2) so a new axiom can
// never silently overwrite an existing one.
func (s *Server) putAxiom(ctx context.Context, cookie, csrf, path, desc, content string) (string, error) {
	_, status, err := aigentic.GravePut(ctx, cookie, csrf, path, desc, []byte(content), false)
	if err == nil {
		return path, nil
	}
	if status == http.StatusConflict {
		alt := strings.TrimSuffix(path, ".md") + "-2.md"
		if _, _, err2 := aigentic.GravePut(ctx, cookie, csrf, alt, desc, []byte(content), false); err2 == nil {
			return alt, nil
		}
	}
	return "", err
}

func csrfFrom(r *http.Request) string {
	if c, err := r.Cookie(csrfCookie); err == nil {
		return c.Value
	}
	return ""
}

func firstNonEmpty(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return a
	}
	return b
}

// mintID mints a stable, unguessable axiom id. It lives in the record's front-matter, not its path,
// so it survives a re-file (scheme moves the content with the path).
func mintID() string {
	var b [10]byte
	_, _ = rand.Read(b[:])
	return "ax_" + strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b[:]))
}

// slug reduces a title to a lowercase kebab path segment (ascii-only, matching the classifier's
// path rule), for the parked-axiom fallback.
func slug(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	repl := strings.NewReplacer("ä", "ae", "ö", "oe", "ü", "ue", "ß", "ss")
	s = repl.Replace(s)
	var b strings.Builder
	prevDash := false
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			prevDash = false
		default:
			if !prevDash {
				b.WriteByte('-')
				prevDash = true
			}
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		out = fmt.Sprintf("axiom-%s", mintID()[3:9])
	}
	return out
}

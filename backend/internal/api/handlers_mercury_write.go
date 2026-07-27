package api

import (
	"context"
	"crypto/rand"
	"encoding/base32"
	"net/http"
	"strings"

	"devlab/backend/internal/aigentic"
	"devlab/backend/internal/mercury"
)

const classifyAttempts = 3

// addAxiom files a new record into one of the three namespaces (section = axiome | regeln | laeufe,
// default axiome). The same mechanism serves all three — a symmetric design: aigentic (claude-cli)
// proposes the category path within that namespace; the placement is validated and retried with a
// correction on any contract violation; after three failures the record is parked under
// <ns>/unsortiert/ rather than guessed — visible, never lost. A stable id is minted into the
// front-matter (survives a later re-file), and the write refuses to clobber (409 → the slug is
// disambiguated and retried once).
func (s *Server) addAxiom(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Titel   string `json:"titel"`
		Body    string `json:"body"`
		Section string `json:"section"`
		Force   bool   `json:"force"` // create even though a similar record exists (user chose "trotzdem neu")
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
	ns := strings.TrimSpace(body.Section)
	if ns == "" {
		ns = mercury.NsAxiome
	}
	if ns != mercury.NsAxiome && ns != mercury.NsRegeln && ns != mercury.NsLaeufe && ns != mercury.NsMeta {
		writeErr(w, http.StatusBadRequest, "section must be axiome, regeln, laeufe or meta")
		return
	}

	cookie := r.Header.Get("Cookie")
	csrf := csrfFrom(r)

	paths, status, err := aigentic.GraveList(r.Context(), cookie, "")
	if err != nil {
		mercuryError(w, status, err)
		return
	}
	categories, members := s.classifyContext(r.Context(), cookie, ns, paths)

	placement, ok := s.classify(r.Context(), cookie, csrf, categories, members, body.Titel, body.Body, "", ns)
	titel := body.Titel
	var path, desc string
	if ok {
		path, desc, titel = placement.Pfad, placement.Beschreibung, firstNonEmpty(placement.Titel, body.Titel)
		// Duplicate guard: the classifier flags an existing record with the same content. Write
		// nothing — hand the decision back (extend it, adjust it, or create anyway with force).
		if !body.Force && strings.TrimSpace(placement.DuplikatVon) != "" {
			if dup, found := s.fetchRecord(r.Context(), cookie, strings.TrimSpace(placement.DuplikatVon)); found {
				writeJSON(w, http.StatusOK, map[string]any{
					"duplicate": map[string]any{"path": dup.Path, "axiom": dup.Axiom},
					"proposed":  map[string]any{"titel": titel, "body": body.Body, "path": reslugLeaf(path, titel)},
				})
				return
			}
		}
	} else {
		// Could not classify after retries: park it, still described, still findable.
		path = ns + "/unsortiert/" + slug(body.Titel) + ".md"
		desc = "unsortiert: " + body.Titel
	}

	// Strict conformance gate: an axiom MUST satisfy every meta-axiom. On a violation write nothing and
	// hand the decision back (accept the AI correction, adjust it, or create anyway with force). Only
	// axioms are gated — meta-axioms have no meta-meta level — and a checker outage fails open.
	if ns == mercury.NsAxiome && !body.Force {
		if metas := s.metaAxioms(r.Context(), cookie, paths); len(metas) > 0 {
			if c, cok := s.conform(r.Context(), cookie, csrf, titel, body.Body, metas); cok && !c.Conforms {
				writeJSON(w, http.StatusOK, map[string]any{
					"nonconform": map[string]any{
						"violations": orEmptyViolations(c.Violations),
						"proposed":   map[string]any{"titel": c.Titel, "body": c.Body, "path": reslugLeaf(path, c.Titel)},
					},
				})
				return
			}
		}
	}

	// The heading always matches the path: force the leaf slug from the final title, keeping the
	// category the classifier chose. Same invariant the edit path enforces.
	path = reslugLeaf(path, titel)

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

// aigenticRetry runs the shared aigentic claude-cli parse loop: it builds a prompt (folding the
// previous attempt's parse error in as a correction line), runs it, and re-parses — up to
// classifyAttempts times. It returns the first successful parse; the transport error verbatim if
// aigentic is unreachable (no retry); or the last parse error once every attempt has failed. classify,
// conform and planRuns are all this one loop with a different prompt builder and parser.
func aigenticRetry[T any](ctx context.Context, cookie, csrf string, build func(correction string) string, parse func(output string) (T, error)) (T, error) {
	var zero T
	correction := ""
	var lastErr error
	for i := 0; i < classifyAttempts; i++ {
		result, _, err := aigentic.Run(ctx, cookie, csrf, "claude-cli", aigentic.Request{Prompt: build(correction), OutputFormat: "text"})
		if err != nil {
			return zero, err
		}
		v, perr := parse(result.Output)
		if perr == nil {
			return v, nil
		}
		correction, lastErr = perr.Error(), perr
	}
	return zero, lastErr
}

// classify runs the classification loop for namespace ns: prompt aigentic (claude-cli), validate,
// and retry with a correction line on any contract violation, up to classifyAttempts. ok is false
// when every attempt failed (the caller parks the record).
func (s *Server) classify(ctx context.Context, cookie, csrf string, categories []string, members map[string][]string, titel, body, hint, ns string) (mercury.Placement, bool) {
	p, err := aigenticRetry(ctx, cookie, csrf,
		func(correction string) string {
			return mercury.ClassifyPrompt(categories, members, titel, body, hint, correction, ns)
		},
		func(out string) (mercury.Placement, error) { return mercury.ParsePlacement(out, categories, ns) })
	if err != nil {
		return mercury.Placement{}, false // aigentic unreachable or unparseable after retries: park it
	}
	return p, true
}

// optimizeRecord polishes a record's title+body via aigentic (orthography, generalization, brevity)
// WITHOUT persisting — the "Mit KI optimieren" button and the auto-optimize on add both use it. On
// repeated model failure it returns the input unchanged (never worse than the original).
func (s *Server) optimizeRecord(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Titel   string `json:"titel"`
		Body    string `json:"body"`
		Section string `json:"section"`
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
	ns := strings.TrimSpace(body.Section)
	if ns == "" {
		ns = mercury.NsAxiome
	}
	cookie, csrf := r.Header.Get("Cookie"), csrfFrom(r)
	// For an axiom, fold the meta-axioms into the polish so it comes out already conforming.
	var metas []mercury.RunAxiom
	if ns == mercury.NsAxiome {
		if paths, _, err := aigentic.GraveList(r.Context(), cookie, ""); err == nil {
			metas = s.metaAxioms(r.Context(), cookie, paths)
		}
	}
	for i := 0; i < classifyAttempts; i++ {
		result, _, err := aigentic.Run(r.Context(), cookie, csrf, "claude-cli",
			aigentic.Request{Prompt: mercury.OptimizePrompt(body.Titel, body.Body, ns, metas), OutputFormat: "text"})
		if err != nil {
			mercuryError(w, http.StatusBadGateway, err)
			return
		}
		if opt, perr := mercury.ParseOptimized(result.Output); perr == nil {
			writeJSON(w, http.StatusOK, opt)
			return
		}
	}
	writeJSON(w, http.StatusOK, mercury.Optimized{Titel: body.Titel, Body: body.Body})
}

// conformRecord checks an axiom strictly against every meta-axiom for the "Konformität"-panel. No
// meta-axioms (or a model outage) ⇒ conforms:true (fail open) so the surface never blocks on infra.
func (s *Server) conformRecord(w http.ResponseWriter, r *http.Request) {
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
	cookie, csrf := r.Header.Get("Cookie"), csrfFrom(r)
	paths, status, err := aigentic.GraveList(r.Context(), cookie, "")
	if err != nil {
		mercuryError(w, status, err)
		return
	}
	metas := s.metaAxioms(r.Context(), cookie, paths)
	if len(metas) == 0 {
		writeJSON(w, http.StatusOK, map[string]any{"conforms": true, "violations": []mercury.Violation{}, "metaCount": 0})
		return
	}
	c, ok := s.conform(r.Context(), cookie, csrf, body.Titel, body.Body, metas)
	if !ok {
		writeJSON(w, http.StatusOK, map[string]any{"conforms": true, "violations": []mercury.Violation{}, "metaCount": len(metas), "unavailable": true})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"conforms":   c.Conforms,
		"violations": orEmptyViolations(c.Violations),
		"proposed":   map[string]any{"titel": c.Titel, "body": c.Body},
		"metaCount":  len(metas),
	})
}

// metaAxioms gathers all meta-axioms (the meta/ records) as RunAxioms — the binding requirements every
// axiom must satisfy. Reuses fetchRecord, like classifyContext.
func (s *Server) metaAxioms(ctx context.Context, cookie string, paths []string) []mercury.RunAxiom {
	var metas []mercury.RunAxiom
	for _, p := range paths {
		if !strings.HasPrefix(p, mercury.NsMeta+"/") || !strings.HasSuffix(p, ".md") {
			continue
		}
		if rec, ok := s.fetchRecord(ctx, cookie, p); ok {
			metas = append(metas, mercury.RunAxiom{ID: rec.Axiom.ID, Titel: rec.Axiom.Titel, Body: rec.Axiom.Body})
		}
	}
	return metas
}

// conform runs the conformance loop: prompt aigentic, validate, retry with a correction line, up to
// classifyAttempts. ok is false when every attempt failed (model unreachable / unparseable) — the
// caller then fails open (never block adding an axiom because the checker is down).
func (s *Server) conform(ctx context.Context, cookie, csrf, titel, body string, metas []mercury.RunAxiom) (mercury.Conformance, bool) {
	c, err := aigenticRetry(ctx, cookie, csrf,
		func(correction string) string { return mercury.ConformancePrompt(titel, body, metas, correction) },
		mercury.ParseConformance)
	if err != nil {
		return mercury.Conformance{}, false
	}
	return c, true
}

func orEmptyViolations(v []mercury.Violation) []mercury.Violation {
	if v == nil {
		return []mercury.Violation{}
	}
	return v
}

// classifyContext builds the classifier's category context: the existing category list plus a map of
// category → the titles of its records, so the model reads each category's THEME (not just its name)
// and files by content. Reuses fetchRecord; N fetches per add, acceptable for an interactive action.
func (s *Server) classifyContext(ctx context.Context, cookie, ns string, paths []string) ([]string, map[string][]string) {
	categories := mercury.Categories(paths, ns)
	members := map[string][]string{}
	for _, p := range paths {
		if !strings.HasPrefix(p, ns+"/") || !strings.HasSuffix(p, ".md") {
			continue
		}
		rec, ok := s.fetchRecord(ctx, cookie, p)
		if !ok {
			continue
		}
		parent := p[:strings.LastIndex(p, "/")]
		title := rec.Axiom.Titel
		if title == "" {
			title = strings.TrimSuffix(p[strings.LastIndex(p, "/")+1:], ".md")
		}
		// Path + title + a content snippet: the path so duplikat_von can name an entry exactly, the
		// snippet so a CONTENT duplicate is recognisable even when the wording differs.
		members[parent] = append(members[parent], p+" — "+title+": "+mercury.Snippet(rec.Axiom.Body, 160))
	}
	return categories, members
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

// slug reduces a title to a lowercase kebab path segment via mercury.Slug — the single
// transliteration used everywhere, so there is no second, drifting sibling — with an empty-title
// fallback for the parked-axiom case.
func slug(s string) string {
	out := mercury.Slug(s)
	if out == "" {
		out = "axiom-" + mintID()[3:9]
	}
	return out
}

// reslugLeaf keeps a record's category but forces its leaf filename to slug(titel), so an axiom's
// heading always matches its path. Both write paths — add and edit — run through it.
func reslugLeaf(path, titel string) string {
	i := strings.LastIndex(path, "/")
	if i < 0 {
		return path
	}
	return path[:i+1] + slug(titel) + ".md"
}

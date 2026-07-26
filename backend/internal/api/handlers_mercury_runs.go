package api

import (
	"context"
	"errors"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"devlab/backend/internal/aigentic"
	"devlab/backend/internal/mercury"
	"devlab/backend/internal/runs"
)

// Automatische Läufe — run instances. A run bundles Axioms (only axioms, never Implementierungsregeln)
// and a recurring schedule; its Claude prompt is composed from those axioms + all global Laufregeln
// and stored as a snapshot. This file is the management layer (CRUD, coverage, prompt preview, the two
// AI-planning buttons with review/apply, config history). Execution (the scheduler) is a separate,
// gated phase; the stored results are read here but written there.

// runView is a run plus derived state the store itself does not hold: a staleness flag (its snapshot no
// longer matches the scheme inputs) and the run's still-open pull requests awaiting their merge to main.
// The pending-PR list is what keeps a ToDo visibly IN the active list until the main-merge is through:
// it is not "erledigt" the instant a PR opens — it is "in Arbeit, wartet auf Merge" until the PR lands.
type runView struct {
	runs.Run
	Stale      bool            `json:"stale"`
	PendingPRs []pendingPRView `json:"pendingPrs,omitempty"`
}

// pendingPRView is one of a run's open PRs (repo + number + URL) surfaced to the UI so it can show the
// awaiting-merge state and link straight to the PR.
type pendingPRView struct {
	Repo   string `json:"repo"`
	Number int    `json:"number"`
	URL    string `json:"url"`
}

// pendingPRsByRun groups the tracked (not-yet-merged) PRs by their run id, so a run/ToDo can be shown as
// awaiting its merge. A passive read of the PR pool — no evaluation lives in the pool itself. Empty when
// the store is unavailable.
func (s *Server) pendingPRsByRun() map[string][]pendingPRView {
	out := map[string][]pendingPRView{}
	if s.runPRs == nil {
		return out
	}
	prs, err := s.runPRs.List()
	if err != nil {
		return out
	}
	for _, p := range prs {
		out[p.RunID] = append(out[p.RunID], pendingPRView{Repo: p.Repo, Number: p.Number, URL: p.URL})
	}
	return out
}

// runCatalog scans the scheme store once: every axiom by its stable id (as the RunAxiom shape used for
// composition), the id→path index (coverage badges), and all global Laufregeln. Mirrors
// buildRolloutBlock's scan; reuses fetchRecord.
func (s *Server) runCatalog(ctx context.Context, cookie string) (byID map[string]mercury.RunAxiom, idPath map[string]string, laufregeln []mercury.RunAxiom, status int, err error) {
	paths, status, err := aigentic.GraveList(ctx, cookie, "")
	if err != nil {
		return nil, nil, nil, status, err
	}
	byID = map[string]mercury.RunAxiom{}
	idPath = map[string]string{}
	for _, p := range paths {
		switch {
		case strings.HasPrefix(p, mercury.NsAxiome+"/") && strings.HasSuffix(p, ".md"):
			if rec, ok := s.fetchRecord(ctx, cookie, p); ok && rec.Axiom.ID != "" {
				byID[rec.Axiom.ID] = mercury.RunAxiom{ID: rec.Axiom.ID, Titel: rec.Axiom.Titel, Body: rec.Axiom.Body}
				idPath[rec.Axiom.ID] = p
			}
		case strings.HasPrefix(p, mercury.NsLaeufe+"/") && strings.HasSuffix(p, ".md"):
			if rec, ok := s.fetchRecord(ctx, cookie, p); ok {
				laufregeln = append(laufregeln, mercury.RunAxiom{ID: rec.Axiom.ID, Titel: rec.Axiom.Titel, Body: rec.Axiom.Body})
			}
		}
	}
	return byID, idPath, laufregeln, http.StatusOK, nil
}

// todoAttachmentDescriptors maps a ToDo's stored attachment metadata to the lean descriptors the prompt
// references (filename + type, never bytes). Shared by snapshot composition (composeInto) and the media
// handlers so the prompt's view of the attachments has one definition.
func todoAttachmentDescriptors(atts []runs.Attachment) []mercury.TodoAttachment {
	out := make([]mercury.TodoAttachment, 0, len(atts))
	for _, a := range atts {
		out = append(out, mercury.TodoAttachment{Filename: a.Filename, MIME: a.MIME})
	}
	return out
}

func axiomsFor(ids []string, byID map[string]mercury.RunAxiom) []mercury.RunAxiom {
	out := make([]mercury.RunAxiom, 0, len(ids))
	for _, id := range ids {
		if a, ok := byID[id]; ok {
			out = append(out, a)
		}
	}
	return out
}

func titleLegend(byID map[string]mercury.RunAxiom) map[string]string {
	m := make(map[string]string, len(byID))
	for id, a := range byID {
		m[id] = mercury.RunAxiomTitle(a)
	}
	return m
}

// composeInto (re)composes a run's prompt snapshot. An auto run folds in its axioms + all Laufregeln;
// a ToDo's prompt is just its task (the constitution reaches the agent via the repo's CLAUDE.md), so
// it has no scheme inputs and therefore no staleness.
func composeInto(run *runs.Run, byID map[string]mercury.RunAxiom, laufregeln []mercury.RunAxiom, now time.Time) {
	run.PromptAt = now
	if run.IsTodo() {
		// The stored snapshot is the target-agnostic base (the task itself plus its attachments, which are
		// the same across targets). A ToDo can reach several repos at once, some newly created, so the
		// "this repo is new — scaffold it" note is per-target and is composed at execution time (see the
		// executor), not baked into one shared snapshot.
		run.Prompt = mercury.ComposeTodoPrompt(run.Name, run.Task, "", todoAttachmentDescriptors(run.Attachments))
		run.PromptHash = ""
		return
	}
	axs := axiomsFor(run.AxiomIDs, byID)
	run.Prompt = mercury.ComposeRunPrompt(run.Name, axs, laufregeln)
	run.PromptHash = mercury.RunInputsHash(axs, laufregeln)
}

// upsertPlannedRun folds one planned auto run into out: if an auto run of the same name (case-insensitive)
// already exists, its axioms are extended (deduped), its snapshot recomposed and its schedule re-armed;
// otherwise np is appended as a new run. It returns the resulting list, a copy of the affected run (so the
// caller can name it / link to it) and whether a new run was created. Only auto runs are matched — a ToDo
// that happens to share a name is never turned into an axiom carrier. Shared by the interactive
// fill-apply and the background auto-assigner so the merge semantics have a single definition.
func upsertPlannedRun(out []runs.Run, np runs.Run, byID map[string]mercury.RunAxiom, laufregeln []mercury.RunAxiom, now time.Time) ([]runs.Run, runs.Run, bool) {
	for i := range out {
		if out[i].IsTodo() || !strings.EqualFold(out[i].Name, np.Name) {
			continue
		}
		out[i].AxiomIDs = dedupStrings(append(out[i].AxiomIDs, np.AxiomIDs...))
		out[i].UpdatedAt = now
		composeInto(&out[i], byID, laufregeln, now)
		if out[i].Enabled {
			if nf, e := out[i].Schedule.Next(now); e == nil {
				out[i].NextFireAt = &nf
			}
		}
		return out, out[i], false
	}
	return append(out, np), np, true
}

// runsList returns every run with a staleness flag, plus an id→title legend for the axioms in play.
func (s *Server) runsList(w http.ResponseWriter, r *http.Request) {
	if s.runs == nil {
		writeErr(w, http.StatusServiceUnavailable, "Läufe-Store nicht verfügbar")
		return
	}
	cookie := r.Header.Get("Cookie")
	byID, _, laufregeln, status, err := s.runCatalog(r.Context(), cookie)
	if err != nil {
		mercuryError(w, status, err)
		return
	}
	all, err := s.runs.List()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "Läufe konnten nicht gelesen werden")
		return
	}
	prsByRun := s.pendingPRsByRun()
	views := make([]runView, 0, len(all))
	for _, run := range all {
		cur := mercury.RunInputsHash(axiomsFor(run.AxiomIDs, byID), laufregeln)
		views = append(views, runView{
			Run:        run,
			Stale:      run.PromptHash != "" && run.PromptHash != cur,
			PendingPRs: prsByRun[run.ID],
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"runs": views, "axioms": titleLegend(byID)})
}

// runsCoverage reports which axioms are already backed by a run (badges in the picker) + the id→path
// index and an id→title legend.
func (s *Server) runsCoverage(w http.ResponseWriter, r *http.Request) {
	if s.runs == nil {
		writeErr(w, http.StatusServiceUnavailable, "Läufe-Store nicht verfügbar")
		return
	}
	byID, idPath, _, status, err := s.runCatalog(r.Context(), r.Header.Get("Cookie"))
	if err != nil {
		mercuryError(w, status, err)
		return
	}
	all, err := s.runs.List()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "Läufe konnten nicht gelesen werden")
		return
	}
	covered := map[string][]string{}
	for _, run := range all {
		for _, id := range run.AxiomIDs {
			covered[id] = append(covered[id], run.ID)
		}
	}
	// pending: an automatic assignment is scheduled or running, so a currently-uncovered axiom is only
	// TEMPORARILY uncovered. Coverage stays honest (the axiom is reported uncovered until the assignment
	// actually lands); pending merely lets the UI show the state as transient rather than permanent.
	writeJSON(w, http.StatusOK, map[string]any{
		"covered": covered, "index": idPath, "axioms": titleLegend(byID), "pending": s.assignPending(),
	})
}

func (s *Server) runGet(w http.ResponseWriter, r *http.Request) {
	if s.runs == nil {
		writeErr(w, http.StatusServiceUnavailable, "Läufe-Store nicht verfügbar")
		return
	}
	run, ok, err := s.runs.Get(r.PathValue("id"))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "Lauf konnte nicht gelesen werden")
		return
	}
	if !ok {
		writeErr(w, http.StatusNotFound, "Kein Lauf mit dieser id")
		return
	}
	byID, _, laufregeln, status, err := s.runCatalog(r.Context(), r.Header.Get("Cookie"))
	if err != nil {
		mercuryError(w, status, err)
		return
	}
	cur := mercury.RunInputsHash(axiomsFor(run.AxiomIDs, byID), laufregeln)
	writeJSON(w, http.StatusOK, map[string]any{
		"run":    runView{Run: run, Stale: run.PromptHash != "" && run.PromptHash != cur, PendingPRs: s.pendingPRsByRun()[run.ID]},
		"axioms": titleLegend(byID),
	})
}

// runPromptPreview composes the prompt LIVE from the current scheme (does not persist) — the "so
// würde der Lauf aussehen"-Vorschau.
func (s *Server) runPromptPreview(w http.ResponseWriter, r *http.Request) {
	if s.runs == nil {
		writeErr(w, http.StatusServiceUnavailable, "Läufe-Store nicht verfügbar")
		return
	}
	run, ok, err := s.runs.Get(r.PathValue("id"))
	if err != nil || !ok {
		writeErr(w, http.StatusNotFound, "Kein Lauf mit dieser id")
		return
	}
	byID, _, laufregeln, status, err := s.runCatalog(r.Context(), r.Header.Get("Cookie"))
	if err != nil {
		mercuryError(w, status, err)
		return
	}
	axs := axiomsFor(run.AxiomIDs, byID)
	writeJSON(w, http.StatusOK, map[string]any{"prompt": mercury.ComposeRunPrompt(run.Name, axs, laufregeln)})
}

type runBody struct {
	Name     string        `json:"name"`
	Type     runs.Type     `json:"type"` // "" = auto
	Enabled  bool          `json:"enabled"`
	Schedule runs.Schedule `json:"schedule"`
	AxiomIDs []string      `json:"axiomIds"`
	// todo only
	Task    string        `json:"task"`
	Targets []runs.Target `json:"targets"`
	DueAt   *time.Time    `json:"dueAt"`
	// Deprecated: a single existing/new repo. Folded into Targets when a client still sends it, so an
	// older ToDo form keeps working.
	Repo    string `json:"repo"`
	NewRepo string `json:"newRepo"`
}

// repoNameRe bounds a new repo's name (it becomes a GitHub repo and a deploy-allowlist key).
var repoNameRe = regexp.MustCompile(`^[A-Za-z0-9._-]{1,100}$`)

// normalizeTargets validates and cleans a ToDo's target list: at least one target, each being exactly
// one existing repo OR one new-repo name (bounded like a GitHub repo), with duplicates collapsed and
// order preserved. On success it returns the cleaned list and a zero status; otherwise an HTTP status
// and a German message. It is the single gate a ToDo's destinations pass through.
func normalizeTargets(in []runs.Target) ([]runs.Target, int, string) {
	if len(in) == 0 {
		return nil, http.StatusBadRequest, "ein ToDo braucht mindestens ein Ziel: ein vorhandenes Repo oder ein neues"
	}
	out := make([]runs.Target, 0, len(in))
	seen := map[string]bool{}
	for _, t := range in {
		repo := strings.TrimSpace(t.Repo)
		newRepo := strings.TrimSpace(t.NewRepo)
		if (repo == "") == (newRepo == "") {
			return nil, http.StatusBadRequest, "jedes Ziel ist genau ein vorhandenes Repo ODER ein neues"
		}
		if newRepo != "" && !repoNameRe.MatchString(newRepo) {
			return nil, http.StatusBadRequest, "ungültiger Repo-Name (erlaubt: Buchstaben, Ziffern, Punkt, Unterstrich, Bindestrich)"
		}
		key := "r:" + repo
		if newRepo != "" {
			key = "n:" + newRepo
		}
		if seen[key] {
			continue // the same repo listed twice reaches it once
		}
		seen[key] = true
		out = append(out, runs.Target{Repo: repo, NewRepo: newRepo})
	}
	return out, 0, ""
}

func validateRunBody(b *runBody, byID map[string]mercury.RunAxiom) (int, string) {
	b.Name = strings.TrimSpace(b.Name)
	if b.Name == "" {
		return http.StatusBadRequest, "name ist erforderlich"
	}
	if b.Type == "" {
		b.Type = runs.TypeAuto
	}
	switch b.Type {
	case runs.TypeTodo:
		b.Task = strings.TrimSpace(b.Task)
		if b.Task == "" {
			return http.StatusBadRequest, "ein ToDo braucht eine Aufgabenbeschreibung"
		}
		// Fold a legacy single-target payload (repo/newRepo) into the target list so an older client
		// keeps working; from here on Targets is the single source of truth.
		if len(b.Targets) == 0 && (strings.TrimSpace(b.Repo) != "" || strings.TrimSpace(b.NewRepo) != "") {
			b.Targets = []runs.Target{{Repo: b.Repo, NewRepo: b.NewRepo}}
		}
		b.Repo, b.NewRepo = "", ""
		targets, code, msg := normalizeTargets(b.Targets)
		if code != 0 {
			return code, msg
		}
		b.Targets = targets
		return 0, ""
	case runs.TypeAuto:
		if err := b.Schedule.Valid(); err != nil {
			return http.StatusBadRequest, "Zeitplan ungültig: " + err.Error()
		}
		b.AxiomIDs = dedupStrings(b.AxiomIDs)
		if len(b.AxiomIDs) == 0 {
			return http.StatusBadRequest, "ein Lauf braucht mindestens ein Axiom"
		}
		for _, id := range b.AxiomIDs {
			if _, ok := byID[id]; !ok {
				return http.StatusBadRequest, "unbekanntes Axiom: " + id
			}
		}
		return 0, ""
	default:
		return http.StatusBadRequest, "type muss auto oder todo sein"
	}
}

func (s *Server) runCreate(w http.ResponseWriter, r *http.Request) {
	if s.runs == nil {
		writeErr(w, http.StatusServiceUnavailable, "Läufe-Store nicht verfügbar")
		return
	}
	var body runBody
	if !decodeJSON(w, r, &body) {
		return
	}
	cookie := r.Header.Get("Cookie")
	byID, _, laufregeln, status, err := s.runCatalog(r.Context(), cookie)
	if err != nil {
		mercuryError(w, status, err)
		return
	}
	if code, msg := validateRunBody(&body, byID); code != 0 {
		writeErr(w, code, msg)
		return
	}
	now := time.Now()
	run := runs.Run{
		ID: runs.NewID(), Name: body.Name, Type: body.Type, Enabled: body.Enabled, Schedule: body.Schedule,
		AxiomIDs: body.AxiomIDs, Task: body.Task, Targets: body.Targets, DueAt: body.DueAt,
		CreatedAt: now.UTC(), UpdatedAt: now.UTC(),
	}
	composeInto(&run, byID, laufregeln, now.UTC())
	// Optimise the branch description with AI only when the raw name is too thin to slugify; best-effort,
	// so a miss just leaves the executor to slug the name itself. Done here (outside Mutate) so the store
	// lock is never held across the aigentic round-trip.
	run.BranchDesc = s.aiBranchDesc(r.Context(), cookie, csrfFrom(r), run.Name, run.Task)
	// Only a recurring auto run has a NextFireAt; a ToDo fires once at its optional DueAt (or manually).
	if run.Enabled && !run.IsTodo() {
		if nf, err := run.Schedule.Next(now); err == nil {
			run.NextFireAt = &nf
		}
	}
	if _, err := s.runs.Mutate("create", actor(r), func(cur []runs.Run) ([]runs.Run, error) {
		return append(cur, run), nil
	}); err != nil {
		writeErr(w, http.StatusInternalServerError, "Lauf konnte nicht gespeichert werden")
		return
	}
	writeJSON(w, http.StatusOK, run)
}

func (s *Server) runUpdate(w http.ResponseWriter, r *http.Request) {
	if s.runs == nil {
		writeErr(w, http.StatusServiceUnavailable, "Läufe-Store nicht verfügbar")
		return
	}
	id := r.PathValue("id")
	var body runBody
	if !decodeJSON(w, r, &body) {
		return
	}
	cookie := r.Header.Get("Cookie")
	byID, _, laufregeln, status, err := s.runCatalog(r.Context(), cookie)
	if err != nil {
		mercuryError(w, status, err)
		return
	}
	if code, msg := validateRunBody(&body, byID); code != 0 {
		writeErr(w, code, msg)
		return
	}
	now := time.Now()
	// Recompute the AI-optimized branch description for the edited name (outside Mutate so the store lock
	// is never held across the aigentic round-trip). Recomputing on every edit keeps it consistent: a name
	// that is now good enough clears it (""), a still-thin one refreshes it.
	branchDesc := s.aiBranchDesc(r.Context(), cookie, csrfFrom(r), body.Name, body.Task)
	var updated runs.Run
	if _, err := s.runs.Mutate("update", actor(r), func(cur []runs.Run) ([]runs.Run, error) {
		idx := indexOfRun(cur, id)
		if idx < 0 {
			return nil, runs.ErrNotFound // abort before any write; no spurious save/snapshot
		}
		cur[idx].Name = body.Name
		cur[idx].Type = body.Type
		cur[idx].Enabled = body.Enabled
		cur[idx].Schedule = body.Schedule
		cur[idx].AxiomIDs = body.AxiomIDs
		cur[idx].Task = body.Task
		cur[idx].Targets = body.Targets
		cur[idx].BranchDesc = branchDesc
		// Drop any legacy single-target fields so a record edited after the upgrade keeps Targets as its
		// only source of truth (TodoTargets prefers Targets, but leaving stale fields is untidy).
		cur[idx].Repo, cur[idx].NewRepo = "", ""
		cur[idx].DueAt = body.DueAt
		cur[idx].UpdatedAt = now.UTC()
		composeInto(&cur[idx], byID, laufregeln, now.UTC())
		if cur[idx].Enabled && !cur[idx].IsTodo() {
			if nf, e := cur[idx].Schedule.Next(now); e == nil {
				cur[idx].NextFireAt = &nf
			}
		} else {
			cur[idx].NextFireAt = nil // ToDos fire once at DueAt, never on a recurring schedule
		}
		updated = cur[idx]
		return cur, nil
	}); err != nil {
		if errors.Is(err, runs.ErrNotFound) {
			writeErr(w, http.StatusNotFound, "Kein Lauf mit dieser id")
			return
		}
		writeErr(w, http.StatusInternalServerError, "Lauf konnte nicht gespeichert werden")
		return
	}
	// Editing a run may have removed axioms from it, leaving them uncovered; re-cover them in the background.
	s.kickAutoAssign(r)
	writeJSON(w, http.StatusOK, updated)
}

func (s *Server) runDelete(w http.ResponseWriter, r *http.Request) {
	if s.runs == nil {
		writeErr(w, http.StatusServiceUnavailable, "Läufe-Store nicht verfügbar")
		return
	}
	id := r.PathValue("id")
	// Results/logs are intentionally NOT deleted — they are historized and persist past the run.
	if _, err := s.runs.Mutate("delete", actor(r), func(cur []runs.Run) ([]runs.Run, error) {
		if indexOfRun(cur, id) < 0 {
			return nil, runs.ErrNotFound
		}
		out := make([]runs.Run, 0, len(cur)-1)
		for _, run := range cur {
			if run.ID != id {
				out = append(out, run)
			}
		}
		return out, nil
	}); err != nil {
		if errors.Is(err, runs.ErrNotFound) {
			writeErr(w, http.StatusNotFound, "Kein Lauf mit dieser id")
			return
		}
		writeErr(w, http.StatusInternalServerError, "Lauf konnte nicht gelöscht werden")
		return
	}
	// The ToDo is gone; drop its attachment blobs too so the passive pool keeps no orphans.
	if s.attachments != nil {
		_ = s.attachments.DeleteAll(id)
	}
	// Deleting a run may have orphaned its axioms; re-cover them in the background.
	s.kickAutoAssign(r)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) runRecompose(w http.ResponseWriter, r *http.Request) {
	if s.runs == nil {
		writeErr(w, http.StatusServiceUnavailable, "Läufe-Store nicht verfügbar")
		return
	}
	id := r.PathValue("id")
	byID, _, laufregeln, status, err := s.runCatalog(r.Context(), r.Header.Get("Cookie"))
	if err != nil {
		mercuryError(w, status, err)
		return
	}
	now := time.Now().UTC()
	// Patch, not Mutate: recomposing only refreshes the derived prompt snapshot from the current
	// scheme — it is not a user config edit, so it must not flood the restore history.
	if _, err := s.runs.Patch(func(cur []runs.Run) ([]runs.Run, error) {
		idx := indexOfRun(cur, id)
		if idx < 0 {
			return nil, runs.ErrNotFound
		}
		composeInto(&cur[idx], byID, laufregeln, now)
		cur[idx].UpdatedAt = now
		return cur, nil
	}); err != nil {
		if errors.Is(err, runs.ErrNotFound) {
			writeErr(w, http.StatusNotFound, "Kein Lauf mit dieser id")
			return
		}
		writeErr(w, http.StatusInternalServerError, "Lauf konnte nicht aktualisiert werden")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// runsAiFill proposes runs for the not-yet-covered axioms (reviewable; writes nothing).
func (s *Server) runsAiFill(w http.ResponseWriter, r *http.Request) {
	if s.runs == nil {
		writeErr(w, http.StatusServiceUnavailable, "Läufe-Store nicht verfügbar")
		return
	}
	cookie, csrf := r.Header.Get("Cookie"), csrfFrom(r)
	byID, _, _, status, err := s.runCatalog(r.Context(), cookie)
	if err != nil {
		mercuryError(w, status, err)
		return
	}
	all, err := s.runs.List()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "Läufe konnten nicht gelesen werden")
		return
	}
	covered := map[string]bool{}
	names := make([]string, 0, len(all))
	for _, run := range all {
		names = append(names, run.Name)
		for _, id := range run.AxiomIDs {
			covered[id] = true
		}
	}
	var uncovered []mercury.RunAxiom
	for id, a := range byID {
		if !covered[id] {
			uncovered = append(uncovered, a)
		}
	}
	if len(uncovered) == 0 {
		writeErr(w, http.StatusBadRequest, "Alle Axiome sind bereits durch einen Lauf abgedeckt")
		return
	}
	known := keysOf(byID)
	plan, err := s.planRuns(r.Context(), cookie, csrf, known, func(correction string) string {
		return mercury.RunPlanFillPrompt(uncovered, names, correction)
	})
	if err != nil {
		mercuryError(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"proposal": plan, "axioms": titleLegend(byID)})
}

// runsAiFinetune proposes a cleaner regrouping of all current runs (reviewable; writes nothing).
func (s *Server) runsAiFinetune(w http.ResponseWriter, r *http.Request) {
	if s.runs == nil {
		writeErr(w, http.StatusServiceUnavailable, "Läufe-Store nicht verfügbar")
		return
	}
	cookie, csrf := r.Header.Get("Cookie"), csrfFrom(r)
	byID, _, _, status, err := s.runCatalog(r.Context(), cookie)
	if err != nil {
		mercuryError(w, status, err)
		return
	}
	all, err := s.runs.List()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "Läufe konnten nicht gelesen werden")
		return
	}
	if len(all) == 0 {
		writeErr(w, http.StatusBadRequest, "Keine Läufe zum Fine-tunen — zuerst welche anlegen oder „Mit KI auffüllen“")
		return
	}
	current := plannedRunsFrom(all)
	catalog := catalogSlice(byID)
	known := keysOf(byID)
	plan, err := s.planRuns(r.Context(), cookie, csrf, known, func(correction string) string {
		return mercury.RunPlanFinetunePrompt(catalog, current, correction)
	})
	if err != nil {
		mercuryError(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"proposal": plan, "axioms": titleLegend(byID)})
}

// planRuns runs the aigentic classification loop for a run plan (mirrors s.classify).
func (s *Server) planRuns(ctx context.Context, cookie, csrf string, knownIDs []string, build func(correction string) string) (mercury.RunPlan, error) {
	correction := ""
	var lastErr error
	for i := 0; i < classifyAttempts; i++ {
		result, _, err := aigentic.Run(ctx, cookie, csrf, "claude-cli", aigentic.Request{Prompt: build(correction), OutputFormat: "text"})
		if err != nil {
			return mercury.RunPlan{}, err
		}
		plan, perr := mercury.ParseRunPlan(result.Output, knownIDs)
		if perr == nil {
			return plan, nil
		}
		correction, lastErr = perr.Error(), perr
	}
	if lastErr == nil {
		lastErr = mercury.ErrNoJSON
	}
	return mercury.RunPlan{}, lastErr
}

// aiBranchDesc supplies the "otherwise optimise with ai" half of the run-branch naming convention: when a
// run's own name is too thin to slugify into a readable branch (see runs.BranchDescGoodEnough), it asks
// aigentic for a concise English description instead. It is deliberately best-effort and non-blocking —
// a short timeout and ANY error (aigentic down, missing right/key, junk output) yield "", so creating or
// editing a run never hangs on or fails because of the AI service; the executor then slugifies the name
// (or its "change" fallback) directly. It returns "" without spending a round-trip when the name is
// already good enough, and only ever runs in a request handler, where the caller's cookie authorises the
// aigentic proxy — the background executor has no session and so relies on the deterministic slug.
func (s *Server) aiBranchDesc(ctx context.Context, cookie, csrf, name, task string) string {
	if runs.BranchDescGoodEnough(name) {
		return ""
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	prompt := "Name a git branch for the following software task. Reply with ONLY a concise English " +
		"description of 2 to 5 words, all lowercase, words separated by single spaces, no punctuation and " +
		"no surrounding quotes.\n\nTask title: " + name + "\n"
	if strings.TrimSpace(task) != "" {
		prompt += "Task detail: " + task + "\n"
	}
	result, _, err := aigentic.Run(ctx, cookie, csrf, "claude-cli", aigentic.Request{Prompt: prompt, OutputFormat: "text"})
	if err != nil || result == nil {
		return ""
	}
	desc := strings.TrimSpace(result.Output)
	if i := strings.IndexByte(desc, '\n'); i >= 0 {
		desc = strings.TrimSpace(desc[:i]) // keep only the first line if the model added prose
	}
	if !runs.BranchDescGoodEnough(desc) {
		return "" // unusable answer — fall back to the deterministic name slug
	}
	return desc
}

// runsApplyProposal persists a (possibly user-edited) reviewed proposal. mode "fill" creates/extends
// named runs; mode "replace" replaces the whole set (fine-tune).
func (s *Server) runsApplyProposal(w http.ResponseWriter, r *http.Request) {
	if s.runs == nil {
		writeErr(w, http.StatusServiceUnavailable, "Läufe-Store nicht verfügbar")
		return
	}
	var body struct {
		Mode string          `json:"mode"`
		Plan mercury.RunPlan `json:"plan"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if body.Mode != "fill" && body.Mode != "replace" {
		writeErr(w, http.StatusBadRequest, "mode muss fill oder replace sein")
		return
	}
	cookie := r.Header.Get("Cookie")
	byID, _, laufregeln, status, err := s.runCatalog(r.Context(), cookie)
	if err != nil {
		mercuryError(w, status, err)
		return
	}
	// Re-validate the (possibly user-edited) plan against the current axiom set.
	if err := mercury.ValidateRunPlan(&body.Plan, keysOf(byID)); err != nil {
		writeErr(w, http.StatusBadRequest, "Vorschlag ungültig: "+err.Error())
		return
	}
	now := time.Now()
	planned := make([]runs.Run, 0, len(body.Plan.Runs))
	for _, pr := range body.Plan.Runs {
		run := runs.Run{
			ID: runs.NewID(), Name: pr.Name, Enabled: true, Schedule: toSchedule(pr.Schedule),
			AxiomIDs: dedupStrings(pr.AxiomIDs), CreatedAt: now.UTC(), UpdatedAt: now.UTC(),
		}
		composeInto(&run, byID, laufregeln, now.UTC())
		if nf, e := run.Schedule.Next(now); e == nil {
			run.NextFireAt = &nf
		}
		planned = append(planned, run)
	}
	action := "apply-" + body.Mode
	if _, err := s.runs.Mutate(action, actor(r), func(cur []runs.Run) ([]runs.Run, error) {
		if body.Mode == "replace" {
			return planned, nil
		}
		// fill: extend an auto run of the same name (append axioms), else append the new run.
		out := append([]runs.Run(nil), cur...)
		for _, np := range planned {
			out, _, _ = upsertPlannedRun(out, np, byID, laufregeln, now.UTC())
		}
		return out, nil
	}); err != nil {
		writeErr(w, http.StatusInternalServerError, "Vorschlag konnte nicht gespeichert werden")
		return
	}
	// A replace can strip coverage from axioms the new set omits; cover them again in the background.
	s.kickAutoAssign(r)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) runsHistoryList(w http.ResponseWriter, r *http.Request) {
	if s.runs == nil {
		writeErr(w, http.StatusServiceUnavailable, "Läufe-Store nicht verfügbar")
		return
	}
	snaps, err := s.runs.History().List()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "Verlauf konnte nicht gelesen werden")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"snapshots": snaps})
}

func (s *Server) runsHistoryRestore(w http.ResponseWriter, r *http.Request) {
	if s.runs == nil {
		writeErr(w, http.StatusServiceUnavailable, "Läufe-Store nicht verfügbar")
		return
	}
	var body struct {
		TS string `json:"ts"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if !runHistoryTSRe.MatchString(body.TS) {
		writeErr(w, http.StatusBadRequest, "ungültiger Zeitstempel")
		return
	}
	snap, ok, err := s.runs.History().Get(body.TS)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "Snapshot konnte nicht gelesen werden")
		return
	}
	if !ok {
		writeErr(w, http.StatusNotFound, "Kein Snapshot mit diesem Zeitstempel")
		return
	}
	now := time.Now()
	restored := make([]runs.Run, len(snap.Runs))
	copy(restored, snap.Runs)
	for i := range restored { // recompute NextFireAt forward so a restore doesn't fire the past immediately
		if restored[i].Enabled {
			if nf, e := restored[i].Schedule.Next(now); e == nil {
				restored[i].NextFireAt = &nf
			}
		} else {
			restored[i].NextFireAt = nil
		}
	}
	if _, err := s.runs.Mutate("restore", actor(r), func([]runs.Run) ([]runs.Run, error) {
		return restored, nil
	}); err != nil {
		writeErr(w, http.StatusInternalServerError, "Wiederherstellung fehlgeschlagen")
		return
	}
	// Restoring an older set can leave axioms added since then uncovered; re-cover them in the background.
	s.kickAutoAssign(r)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "runs": len(restored)})
}

var resultIDRe = regexp.MustCompile(`^[0-9A-Za-z_.:-]{1,64}$`)

// runHistoryTSRe pins a restore timestamp to the RFC3339Nano shape List() emits, so a crafted `ts`
// cannot traverse out of the history directory (stem() does not escape "/" or "..").
var runHistoryTSRe = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(\.\d+)?Z$`)

func (s *Server) runResultsList(w http.ResponseWriter, r *http.Request) {
	if s.runResults == nil {
		writeJSON(w, http.StatusOK, map[string]any{"results": []any{}})
		return
	}
	refs, err := s.runResults.ListForRun(r.PathValue("id"))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "Ergebnisse konnten nicht gelesen werden")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"results": refs})
}

func (s *Server) runResultGet(w http.ResponseWriter, r *http.Request) {
	if s.runResults == nil {
		writeErr(w, http.StatusNotFound, "Kein Ergebnis")
		return
	}
	rid := r.PathValue("rid")
	if !resultIDRe.MatchString(rid) {
		writeErr(w, http.StatusBadRequest, "ungültige Ergebnis-id")
		return
	}
	res, ok, err := s.runResults.Get(r.PathValue("id"), rid)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "Ergebnis konnte nicht gelesen werden")
		return
	}
	if !ok {
		writeErr(w, http.StatusNotFound, "Kein Ergebnis mit dieser id")
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// runsCalendar returns the upcoming run occurrences over a window (default 30 days) — the data for
// the auto-updating Laufkalender, sorted chronologically.
func (s *Server) runsCalendar(w http.ResponseWriter, r *http.Request) {
	if s.runs == nil {
		writeErr(w, http.StatusServiceUnavailable, "Läufe-Store nicht verfügbar")
		return
	}
	days := 30
	if d := r.URL.Query().Get("days"); d != "" {
		if n, err := strconv.Atoi(d); err == nil && n > 0 && n <= 366 {
			days = n
		}
	}
	all, err := s.runs.List()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "Läufe konnten nicht gelesen werden")
		return
	}
	// ?type=auto|todo (default: both) — the Läufe and ToDos sections each show their own calendar,
	// the global one unites them and tells them apart by `type`.
	want := strings.TrimSpace(r.URL.Query().Get("type"))
	now := time.Now()
	horizon := now.Add(time.Duration(days) * 24 * time.Hour)
	type occ struct {
		RunID    string    `json:"runId"`
		RunName  string    `json:"runName"`
		Type     runs.Type `json:"type"` // auto | todo — drives the colour separation
		At       time.Time `json:"at"`
		Schedule string    `json:"schedule"`
	}
	occs := []occ{} // never nil: a nil slice marshals to JSON null and hangs the UI
	for _, run := range all {
		kind := runs.TypeAuto
		if run.IsTodo() {
			kind = runs.TypeTodo
		}
		if !run.Enabled || (want != "" && want != "all" && string(kind) != want) {
			continue
		}
		if run.IsTodo() {
			// A ToDo is a single point in time; an overdue-but-unfinished one still belongs on the
			// calendar. Without a due date it only ever runs manually — nothing to show.
			if run.Done || run.DueAt == nil || run.DueAt.After(horizon) {
				continue
			}
			occs = append(occs, occ{RunID: run.ID, RunName: run.Name, Type: kind, At: *run.DueAt, Schedule: "einmalig"})
			continue
		}
		t := now
		for i := 0; i < 400; i++ { // cap: a daily run over a year is ~366
			next, err := run.Schedule.Next(t)
			if err != nil || next.After(horizon) {
				break
			}
			occs = append(occs, occ{RunID: run.ID, RunName: run.Name, Type: kind, At: next, Schedule: scheduleSummary(run.Schedule)})
			t = next
		}
	}
	sort.Slice(occs, func(i, j int) bool { return occs[i].At.Before(occs[j].At) })
	writeJSON(w, http.StatusOK, map[string]any{"from": now, "to": horizon, "occurrences": occs})
}

// runsExecutions returns completed executions (newest first, with token/cost) — the execution history.
// Includes executions of runs that were since deleted (persistent logs). ?type=auto|todo scopes it to
// one surface (the Läufe and ToDos tabs each show their own history, symmetrically); default/all is the
// global log. Each execution's kind is resolved from the stamp on the result, falling back to the live
// run's kind for older (unstamped) results, and to auto for an orphan with neither (the "" == auto
// convention). The resolved kind is returned so the caller sees a definite auto|todo.
func (s *Server) runsExecutions(w http.ResponseWriter, r *http.Request) {
	if s.runResults == nil {
		writeJSON(w, http.StatusOK, map[string]any{"executions": []any{}})
		return
	}
	execs, err := s.runResults.All()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "Ausführungs-History konnte nicht gelesen werden")
		return
	}
	// runID → kind, so an unstamped result inherits the type of its still-existing run.
	kindByRun := map[string]runs.Type{}
	if s.runs != nil {
		if all, listErr := s.runs.List(); listErr == nil {
			for _, run := range all {
				kindByRun[run.ID] = runs.NormalizeType(run.Type)
			}
		}
	}
	resolve := func(e runs.ExecutionSummary) runs.Type {
		if e.Type != "" {
			return runs.NormalizeType(e.Type)
		}
		if k, ok := kindByRun[e.RunID]; ok {
			return k
		}
		return runs.TypeAuto
	}
	want := strings.TrimSpace(r.URL.Query().Get("type"))
	out := make([]runs.ExecutionSummary, 0, len(execs))
	for _, e := range execs {
		kind := resolve(e)
		if want != "" && want != "all" && string(kind) != want {
			continue
		}
		e.Type = kind // hand back the resolved kind, never an empty one
		out = append(out, e)
	}
	writeJSON(w, http.StatusOK, map[string]any{"executions": out})
}

// mercuryChat is the Mercury-WIDE assistant (not run-specific): it sees the axioms, the
// implementation rules, the Laufregeln, the runs and the ToDos, answers questions about any of them,
// and — only when asked to create/change runs — embeds a run-plan the caller surfaces as a reviewable
// proposal.
func (s *Server) mercuryChat(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Messages []mercury.ChatMessage `json:"messages"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if len(body.Messages) == 0 {
		writeErr(w, http.StatusBadRequest, "messages ist erforderlich")
		return
	}
	cookie, csrf := r.Header.Get("Cookie"), csrfFrom(r)
	ctx, status, err := s.chatContext(r.Context(), cookie)
	if err != nil {
		mercuryError(w, status, err)
		return
	}
	prompt := mercury.MercuryChatPrompt(ctx, body.Messages)
	result, _, err := aigentic.Run(r.Context(), cookie, csrf, "claude-cli", aigentic.Request{Prompt: prompt, OutputFormat: "text"})
	if err != nil {
		mercuryError(w, http.StatusBadGateway, err)
		return
	}
	known := make([]string, 0, len(ctx.Axiome))
	for _, a := range ctx.Axiome {
		known = append(known, a.ID)
	}
	plan, cleaned, ok := mercury.ExtractRunPlan(result.Output, known)
	resp := map[string]any{"reply": cleaned}
	if ok {
		resp["proposal"] = plan
	}
	writeJSON(w, http.StatusOK, resp)
}

// chatContext gathers everything the Mercury-wide assistant knows: one scheme scan (axioms, rules,
// Laufregeln) plus the runs and ToDos.
func (s *Server) chatContext(ctx context.Context, cookie string) (mercury.ChatContext, int, error) {
	paths, status, err := aigentic.GraveList(ctx, cookie, "")
	if err != nil {
		return mercury.ChatContext{}, status, err
	}
	var c mercury.ChatContext
	for _, p := range paths {
		if !strings.HasSuffix(p, ".md") {
			continue
		}
		rec, ok := s.fetchRecord(ctx, cookie, p)
		if !ok {
			continue
		}
		a := mercury.RunAxiom{ID: rec.Axiom.ID, Titel: rec.Axiom.Titel, Body: rec.Axiom.Body}
		switch {
		case strings.HasPrefix(p, mercury.NsAxiome+"/"):
			c.Axiome = append(c.Axiome, a)
		case strings.HasPrefix(p, mercury.NsRegeln+"/"):
			c.Regeln = append(c.Regeln, a)
		case strings.HasPrefix(p, mercury.NsLaeufe+"/"):
			c.Laufregeln = append(c.Laufregeln, a)
		}
	}
	if s.runs != nil {
		if all, err := s.runs.List(); err == nil {
			for _, run := range all {
				if run.IsTodo() {
					c.Todos = append(c.Todos, run.Name+" — "+targetsLabel(run)+" — "+mercury.Snippet(run.Task, 120))
					continue
				}
				c.Runs = append(c.Runs, mercury.PlannedRun{Name: run.Name, AxiomIDs: run.AxiomIDs, Schedule: fromSchedule(run.Schedule)})
			}
		}
	}
	return c, http.StatusOK, nil
}

// --- small helpers -----------------------------------------------------------

func plannedRunsFrom(all []runs.Run) []mercury.PlannedRun {
	out := make([]mercury.PlannedRun, 0, len(all))
	for _, run := range all {
		out = append(out, mercury.PlannedRun{Name: run.Name, AxiomIDs: run.AxiomIDs, Schedule: fromSchedule(run.Schedule)})
	}
	return out
}

func catalogSlice(byID map[string]mercury.RunAxiom) []mercury.RunAxiom {
	out := make([]mercury.RunAxiom, 0, len(byID))
	for _, a := range byID {
		out = append(out, a)
	}
	return out
}

// scheduleSummary is a compact German label for a schedule ("täglich 03:00", "wöchentlich Mo,Do 03:00").
func scheduleSummary(sc runs.Schedule) string {
	switch sc.Kind {
	case runs.Daily:
		return "täglich " + sc.TimeOfDay
	case runs.Weekly:
		names := []string{"So", "Mo", "Di", "Mi", "Do", "Fr", "Sa"}
		var days []string
		for _, d := range sc.Weekdays {
			if int(d) >= 0 && int(d) < 7 {
				days = append(days, names[d])
			}
		}
		return "wöchentlich " + strings.Join(days, ",") + " " + sc.TimeOfDay
	default:
		return sc.TimeOfDay
	}
}

// targetsLabel renders a ToDo's destinations for the Mercury-wide assistant context — each target as
// its repo id, a to-be-created one prefixed "neu:", joined by commas.
func targetsLabel(run runs.Run) string {
	targets := run.TodoTargets()
	parts := make([]string, 0, len(targets))
	for _, t := range targets {
		if t.NewRepo != "" {
			parts = append(parts, "neu: "+t.NewRepo)
			continue
		}
		parts = append(parts, t.Repo)
	}
	return strings.Join(parts, ", ")
}

func actor(r *http.Request) string {
	if u := userFrom(r); u != nil {
		return u.Username
	}
	return "?"
}

func indexOfRun(list []runs.Run, id string) int {
	for i := range list {
		if list[i].ID == id {
			return i
		}
	}
	return -1
}

func dedupStrings(in []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

func keysOf(m map[string]mercury.RunAxiom) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func toSchedule(ps mercury.PlanSchedule) runs.Schedule {
	wd := make([]time.Weekday, 0, len(ps.Weekdays))
	for _, d := range ps.Weekdays {
		wd = append(wd, time.Weekday(d))
	}
	return runs.Schedule{Kind: runs.Kind(ps.Kind), TimeOfDay: ps.TimeOfDay, Weekdays: wd}
}

func fromSchedule(s runs.Schedule) mercury.PlanSchedule {
	wd := make([]int, 0, len(s.Weekdays))
	for _, d := range s.Weekdays {
		wd = append(wd, int(d))
	}
	return mercury.PlanSchedule{Kind: string(s.Kind), TimeOfDay: s.TimeOfDay, Weekdays: wd}
}

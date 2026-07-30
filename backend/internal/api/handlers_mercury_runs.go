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
	"devlab/backend/internal/live"
	"devlab/backend/internal/mercury"
	"devlab/backend/internal/model"
	"devlab/backend/internal/runs"
)

// Tasks & runs — the THIN management layer (B 1.4): parse → validate → the runs pools and the
// planning core in package runs → answer. Both kinds (auto runs and todos) ride ONE machinery.
// Execution belongs to sched/executor (handlers_slots.go); results are read here, written
// there.

// runCatalog scans the constitution store once — the ONE scan behind every composition: every axiom
// by its stable id (as the RunAxiom composition shape), the id→path index (coverage badges), every
// Implementierungsregel and every global Laufregel. The Implementierungsregeln are part of the
// binding wording every execution prompt carries (REQ-002.1), so they are read here, not somewhere
// else.
func (s *Server) runCatalog(ctx context.Context, cookie string) (cat runs.Catalog, idPath map[string]string, err error) {
	paths, err := s.axioms.List(ctx, "")
	if err != nil {
		return runs.Catalog{}, nil, err
	}
	cat.ByID = map[string]mercury.RunAxiom{}
	idPath = map[string]string{}
	for _, p := range paths {
		if !strings.HasSuffix(p, ".md") {
			continue
		}
		rule := func() (mercury.RunAxiom, bool) {
			rec, ok := s.fetchRecord(ctx, cookie, p)
			return mercury.RunAxiom{ID: rec.Axiom.ID, Titel: rec.Axiom.Titel, Body: rec.Axiom.Body}, ok
		}
		switch {
		case strings.HasPrefix(p, mercury.NsAxiome+"/"):
			if rec, ok := s.fetchRecord(ctx, cookie, p); ok && rec.Axiom.ID != "" {
				cat.ByID[rec.Axiom.ID] = mercury.RunAxiom{ID: rec.Axiom.ID, Titel: rec.Axiom.Titel, Body: rec.Axiom.Body}
				idPath[rec.Axiom.ID] = p
			}
		case strings.HasPrefix(p, mercury.NsRegeln+"/"):
			if r, ok := rule(); ok {
				cat.Regeln = append(cat.Regeln, r)
			}
		case strings.HasPrefix(p, mercury.NsLaeufe+"/"):
			if r, ok := rule(); ok {
				cat.Laufregeln = append(cat.Laufregeln, r)
			}
		}
	}
	return cat, idPath, nil
}

func titleLegend(cat runs.Catalog) map[string]string {
	m := make(map[string]string, len(cat.ByID))
	for id, a := range cat.ByID {
		m[id] = mercury.RunAxiomTitle(a)
	}
	return m
}

// runsList returns every run plus an id→title legend for the axioms in play. Snapshots are
// kept current by every constitution write (reconcileAfterWrite).
func (s *Server) runsList(w http.ResponseWriter, r *http.Request) {
	if s.runs == nil {
		writeErr(w, http.StatusServiceUnavailable, "Run store unavailable")
		return
	}
	cat, _, err := s.runCatalog(r.Context(), r.Header.Get("Cookie"))
	if err != nil {
		mercuryError(w, err)
		return
	}
	all, err := s.runs.List()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "Could not read runs")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"runs": orEmptyRuns(all), "axioms": titleLegend(cat)})
}

// runsCoverage reports which axioms are already backed by a run (badges in the picker) + the
// id→path index and an id→title legend. `pending` marks a scheduled/running auto-assignment.
func (s *Server) runsCoverage(w http.ResponseWriter, r *http.Request) {
	if s.runs == nil {
		writeErr(w, http.StatusServiceUnavailable, "Run store unavailable")
		return
	}
	cat, idPath, err := s.runCatalog(r.Context(), r.Header.Get("Cookie"))
	if err != nil {
		mercuryError(w, err)
		return
	}
	all, err := s.runs.List()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "Could not read runs")
		return
	}
	covered := map[string][]string{}
	for _, run := range all {
		for _, id := range run.AxiomIDs {
			covered[id] = append(covered[id], run.ID)
		}
	}
	writeJSON(w, http.StatusOK, model.RunCoverage{
		Covered: covered, Index: idPath, Axioms: titleLegend(cat), Pending: s.assignPending(),
	})
}

func (s *Server) runGet(w http.ResponseWriter, r *http.Request) {
	if s.runs == nil {
		writeErr(w, http.StatusServiceUnavailable, "Run store unavailable")
		return
	}
	run, ok, err := s.runs.Get(r.PathValue("id"))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "Could not read the run")
		return
	}
	if !ok {
		writeErr(w, http.StatusNotFound, "No run with this id")
		return
	}
	cat, _, err := s.runCatalog(r.Context(), r.Header.Get("Cookie"))
	if err != nil {
		mercuryError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"run": run, "axioms": titleLegend(cat)})
}

// runPromptPreview composes the prompt LIVE from the current constitution (does not persist).
func (s *Server) runPromptPreview(w http.ResponseWriter, r *http.Request) {
	if s.runs == nil {
		writeErr(w, http.StatusServiceUnavailable, "Run store unavailable")
		return
	}
	run, ok, err := s.runs.Get(r.PathValue("id"))
	if err != nil || !ok {
		writeErr(w, http.StatusNotFound, "No run with this id")
		return
	}
	cat, _, err := s.runCatalog(r.Context(), r.Header.Get("Cookie"))
	if err != nil {
		mercuryError(w, err)
		return
	}
	preview := run
	runs.ComposeInto(&preview, cat)
	writeJSON(w, http.StatusOK, map[string]any{"prompt": preview.PromptSnapshot})
}

// repoNameRe bounds a new repo's name (it becomes a GitHub repo and an install-allowlist key).
var repoNameRe = regexp.MustCompile(`^[A-Za-z0-9._-]{1,100}$`)

// runEffortAllowed is the effort ladder a run/todo may pick: the native claude CLI --effort
// levels (the very set the AI tab uses) plus "ultracode", the maximal tier.
var runEffortAllowed = func() map[string]bool {
	m := map[string]bool{"ultracode": true}
	for k := range effortAllowed {
		m[k] = true
	}
	return m
}()

// runModelRe bounds a selected model to the shape a CLI model alias or id takes, so a stored
// value can never smuggle extra arguments into the executor's CLI call.
var runModelRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// validateTuning trims and guards the tuning a run/todo carries (all optional; empty REFERS to
// the service default, REQ-010.2). The time budget is the third tunable at the same access
// point: absent = the default reference, 0 = explicitly "no budget" (REQ-010.3), negative is
// refused rather than silently corrected.
func validateTuning(t *runs.Tuning) (int, string) {
	t.Model = strings.TrimSpace(t.Model)
	if t.Model != "" && !runModelRe.MatchString(t.Model) {
		return http.StatusBadRequest, "Invalid model"
	}
	t.ModelVersion = strings.TrimSpace(t.ModelVersion)
	if t.ModelVersion != "" && !runModelRe.MatchString(t.ModelVersion) {
		return http.StatusBadRequest, "Invalid model version"
	}
	t.Effort = strings.TrimSpace(t.Effort)
	if t.Effort != "" && !runEffortAllowed[t.Effort] {
		return http.StatusBadRequest, "Invalid effort (allowed: low, medium, high, xhigh, max, ultracode)"
	}
	if t.TimeBudget != nil && *t.TimeBudget < 0 {
		return http.StatusBadRequest, "Invalid time budget (0 means no budget; omit it to use the default)"
	}
	return 0, ""
}

// normalizeTargets validates and cleans a todo's target list: at least one target, each naming
// a repo (Create marks a repo to be created first, REQ-006.2), duplicates collapsed, order
// preserved.
func normalizeTargets(in []runs.Target) ([]runs.Target, int, string) {
	if len(in) == 0 {
		return nil, http.StatusBadRequest, "A todo needs at least one target repository"
	}
	out := make([]runs.Target, 0, len(in))
	seen := map[string]bool{}
	for _, t := range in {
		repo := strings.TrimSpace(t.Repo)
		if repo == "" {
			return nil, http.StatusBadRequest, "Each target names exactly one repository"
		}
		if t.Create && !repoNameRe.MatchString(repo) {
			return nil, http.StatusBadRequest, "Invalid new-repository name (allowed: letters, digits, dot, underscore, hyphen)"
		}
		if seen[repo] {
			continue
		}
		seen[repo] = true
		out = append(out, runs.Target{Repo: repo, Create: t.Create})
	}
	return out, 0, ""
}

// validateRunInput normalizes and validates a create/update payload against the axiom catalog.
func validateRunInput(b *runs.RunInput, cat runs.Catalog) (int, string) {
	b.Title = strings.TrimSpace(b.Title)
	if b.Title == "" {
		return http.StatusBadRequest, "title is required"
	}
	if b.Tuning != nil {
		if code, msg := validateTuning(b.Tuning); code != 0 {
			return code, msg
		}
	}
	if b.Kind == "" {
		b.Kind = model.KindAuto
	}
	switch b.Kind {
	case model.KindTodo:
		b.Task = strings.TrimSpace(b.Task)
		if b.Task == "" {
			return http.StatusBadRequest, "A todo needs a task description"
		}
		targets, code, msg := normalizeTargets(b.Targets)
		if code != 0 {
			return code, msg
		}
		b.Targets = targets
		return 0, ""
	case model.KindAuto:
		if b.Schedule == nil {
			return http.StatusBadRequest, "An auto run needs a schedule"
		}
		if err := b.Schedule.Valid(); err != nil {
			return http.StatusBadRequest, "Invalid schedule: " + err.Error()
		}
		b.AxiomIDs = runs.DedupStrings(b.AxiomIDs)
		if len(b.AxiomIDs) == 0 {
			return http.StatusBadRequest, "An auto run needs at least one axiom"
		}
		for _, id := range b.AxiomIDs {
			if _, ok := cat.ByID[id]; !ok {
				return http.StatusBadRequest, "Unknown axiom: " + id
			}
		}
		return 0, ""
	default:
		return http.StatusBadRequest, "kind must be auto or todo"
	}
}

// applyInput maps a validated input onto a run.
func applyInput(run *runs.Run, b runs.RunInput) {
	run.Kind = b.Kind
	run.Title = b.Title
	run.Task = b.Task
	run.AxiomIDs = b.AxiomIDs
	run.Schedule = b.Schedule
	if b.Active != nil {
		run.Active = *b.Active
	}
	run.Targets = b.Targets
	run.DueAt = b.DueAt
	if b.Tuning != nil {
		run.Tuning = *b.Tuning
	}
}

func (s *Server) runCreate(w http.ResponseWriter, r *http.Request) {
	if s.runs == nil {
		writeErr(w, http.StatusServiceUnavailable, "Run store unavailable")
		return
	}
	var body runs.RunInput
	if !decodeJSON(w, r, &body) {
		return
	}
	cat, _, err := s.runCatalog(r.Context(), r.Header.Get("Cookie"))
	if err != nil {
		mercuryError(w, err)
		return
	}
	if code, msg := validateRunInput(&body, cat); code != 0 {
		writeErr(w, code, msg)
		return
	}
	now := time.Now().UTC()
	who := actorOf(r)
	run := runs.Run{ID: runs.NewID(), Authorship: model.Authorship{Created: who, CreatedAt: now, Updated: who, UpdatedAt: now}}
	if body.Active == nil {
		run.Active = true
	}
	applyInput(&run, body)
	runs.ComposeInto(&run, cat)
	if err := s.runs.Put(run); err != nil {
		writeErr(w, http.StatusInternalServerError, "Could not save the run")
		return
	}
	s.publish(live.TopicRuns)
	writeJSON(w, http.StatusOK, run)
}

func (s *Server) runUpdate(w http.ResponseWriter, r *http.Request) {
	if s.runs == nil {
		writeErr(w, http.StatusServiceUnavailable, "Run store unavailable")
		return
	}
	var body runs.RunInput
	if !decodeJSON(w, r, &body) {
		return
	}
	run, ok, err := s.runs.Get(r.PathValue("id"))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "Could not read the run")
		return
	}
	if !ok {
		writeErr(w, http.StatusNotFound, "No run with this id")
		return
	}
	cat, _, err := s.runCatalog(r.Context(), r.Header.Get("Cookie"))
	if err != nil {
		mercuryError(w, err)
		return
	}
	if code, msg := validateRunInput(&body, cat); code != 0 {
		writeErr(w, code, msg)
		return
	}
	applyInput(&run, body)
	// The last editor changes; the original creator stays visible (REQ-041).
	run.Authorship.Updated = actorOf(r)
	run.Authorship.UpdatedAt = time.Now().UTC()
	runs.ComposeInto(&run, cat)
	if err := s.runs.Put(run); err != nil {
		writeErr(w, http.StatusInternalServerError, "Could not save the run")
		return
	}
	// Editing may have removed axioms, leaving them uncovered; re-cover in the background.
	s.kickAutoAssign(r)
	s.publish(live.TopicRuns)
	writeJSON(w, http.StatusOK, run)
}

func (s *Server) runDelete(w http.ResponseWriter, r *http.Request) {
	if s.runs == nil {
		writeErr(w, http.StatusServiceUnavailable, "Run store unavailable")
		return
	}
	id := r.PathValue("id")
	// Results/logs are intentionally NOT deleted — they are historized and persist past the run.
	if err := s.runs.Delete(id); err != nil {
		if errors.Is(err, runs.ErrNotFound) {
			writeErr(w, http.StatusNotFound, "No run with this id")
			return
		}
		writeErr(w, http.StatusInternalServerError, "Could not delete the run")
		return
	}
	// The todo is gone; drop its attachment blobs too so the passive pool keeps no orphans.
	if s.attachments != nil {
		_ = s.attachments.DeleteAll(id)
	}
	s.kickAutoAssign(r)
	s.publish(live.TopicRuns)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// runsAiFill proposes runs for the not-yet-covered axioms (reviewable; writes nothing).
func (s *Server) runsAiFill(w http.ResponseWriter, r *http.Request) {
	if s.runs == nil {
		writeErr(w, http.StatusServiceUnavailable, "Run store unavailable")
		return
	}
	cookie, csrf := r.Header.Get("Cookie"), csrfFrom(r)
	cat, _, err := s.runCatalog(r.Context(), cookie)
	if err != nil {
		mercuryError(w, err)
		return
	}
	all, err := s.runs.List()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "Could not read runs")
		return
	}
	covered := map[string]bool{}
	names := make([]string, 0, len(all))
	for _, run := range all {
		names = append(names, run.Title)
		for _, id := range run.AxiomIDs {
			covered[id] = true
		}
	}
	var uncovered []mercury.RunAxiom
	for id, a := range cat.ByID {
		if !covered[id] {
			uncovered = append(uncovered, a)
		}
	}
	if len(uncovered) == 0 {
		writeErr(w, http.StatusBadRequest, "Every axiom is already covered by a run")
		return
	}
	plan, err := s.planRuns(r.Context(), cookie, csrf, keysOf(cat.ByID), func(correction string) string {
		return mercury.RunPlanFillPrompt(uncovered, names, correction)
	})
	if err != nil {
		mercuryError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"proposal": plan, "axioms": titleLegend(cat)})
}

// runsAiFinetune proposes a cleaner regrouping of all current runs (reviewable; writes nothing).
func (s *Server) runsAiFinetune(w http.ResponseWriter, r *http.Request) {
	if s.runs == nil {
		writeErr(w, http.StatusServiceUnavailable, "Run store unavailable")
		return
	}
	cookie, csrf := r.Header.Get("Cookie"), csrfFrom(r)
	cat, _, err := s.runCatalog(r.Context(), cookie)
	if err != nil {
		mercuryError(w, err)
		return
	}
	all, err := s.runs.List()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "Could not read runs")
		return
	}
	if len(all) == 0 {
		writeErr(w, http.StatusBadRequest, "No runs to fine-tune — create some first or use AI fill")
		return
	}
	plan, err := s.planRuns(r.Context(), cookie, csrf, keysOf(cat.ByID), func(correction string) string {
		return mercury.RunPlanFinetunePrompt(catalogSlice(cat), plannedRunsFrom(all), correction)
	})
	if err != nil {
		mercuryError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"proposal": plan, "axioms": titleLegend(cat)})
}

// planRuns runs the aigentic classification loop for a run plan.
func (s *Server) planRuns(ctx context.Context, cookie, csrf string, knownIDs []string, build func(correction string) string) (mercury.RunPlan, error) {
	return aigenticRetry(ctx, cookie, csrf, build,
		func(out string) (mercury.RunPlan, error) { return mercury.ParseRunPlan(out, knownIDs) })
}

// runsApplyProposal persists a (possibly user-edited) reviewed proposal. mode "fill"
// creates/extends named runs; mode "replace" replaces the whole set.
func (s *Server) runsApplyProposal(w http.ResponseWriter, r *http.Request) {
	if s.runs == nil {
		writeErr(w, http.StatusServiceUnavailable, "Run store unavailable")
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
		writeErr(w, http.StatusBadRequest, "mode must be fill or replace")
		return
	}
	cat, _, err := s.runCatalog(r.Context(), r.Header.Get("Cookie"))
	if err != nil {
		mercuryError(w, err)
		return
	}
	if err := mercury.ValidateRunPlan(&body.Plan, keysOf(cat.ByID)); err != nil {
		writeErr(w, http.StatusBadRequest, "Invalid proposal: "+err.Error())
		return
	}
	now := time.Now().UTC()
	who := actorOf(r)
	planned := make([]runs.Run, 0, len(body.Plan.Runs))
	for _, pr := range body.Plan.Runs {
		run := runs.Run{
			ID: runs.NewID(), Kind: model.KindAuto, Title: pr.Name, Active: true,
			Schedule: toSchedule(pr.Schedule), AxiomIDs: runs.DedupStrings(pr.AxiomIDs),
			Authorship: model.Authorship{Created: who, CreatedAt: now, Updated: who, UpdatedAt: now},
		}
		runs.ComposeInto(&run, cat)
		planned = append(planned, run)
	}
	cur, err := s.runs.List()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "Could not read runs")
		return
	}
	next := planned
	if body.Mode == "fill" {
		next = append([]runs.Run(nil), cur...)
		for _, np := range planned {
			next, _, _ = runs.UpsertPlannedRun(next, np, cat, now, who)
		}
	}
	if err := s.runs.ReplaceAll(next, "apply-"+body.Mode, who); err != nil {
		writeErr(w, http.StatusInternalServerError, "Could not save the proposal")
		return
	}
	s.kickAutoAssign(r)
	s.publish(live.TopicRuns)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) runsHistoryList(w http.ResponseWriter, r *http.Request) {
	if s.runs == nil {
		writeErr(w, http.StatusServiceUnavailable, "Run store unavailable")
		return
	}
	snaps, err := s.runs.History().List()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "Could not read the history")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"snapshots": snaps})
}

// runHistoryTSRe pins a restore timestamp to the RFC3339Nano shape List() emits, so a crafted
// ts cannot traverse out of the history directory.
var runHistoryTSRe = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(\.\d+)?Z$`)

func (s *Server) runsHistoryRestore(w http.ResponseWriter, r *http.Request) {
	if s.runs == nil {
		writeErr(w, http.StatusServiceUnavailable, "Run store unavailable")
		return
	}
	var body struct {
		TS string `json:"ts"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if !runHistoryTSRe.MatchString(body.TS) {
		writeErr(w, http.StatusBadRequest, "Invalid timestamp")
		return
	}
	snap, ok, err := s.runs.History().Get(body.TS)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "Could not read the snapshot")
		return
	}
	if !ok {
		writeErr(w, http.StatusNotFound, "No snapshot with this timestamp")
		return
	}
	// Recompose from the CURRENT constitution so a restored config comes back correct, never
	// stale (REQ-003).
	cat, _, err := s.runCatalog(r.Context(), r.Header.Get("Cookie"))
	if err != nil {
		mercuryError(w, err)
		return
	}
	restored := make([]runs.Run, len(snap.Runs))
	copy(restored, snap.Runs)
	for i := range restored {
		runs.ComposeInto(&restored[i], cat)
	}
	if err := s.runs.ReplaceAll(restored, "restore", actorOf(r)); err != nil {
		writeErr(w, http.StatusInternalServerError, "Restore failed")
		return
	}
	s.kickAutoAssign(r)
	s.publish(live.TopicRuns)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "runs": len(restored)})
}

var resultIDRe = regexp.MustCompile(`^[0-9A-Za-z_.:-]{1,64}$`)

func (s *Server) runResultsList(w http.ResponseWriter, r *http.Request) {
	if s.results == nil {
		writeJSON(w, http.StatusOK, map[string]any{"results": []any{}})
		return
	}
	list, err := s.results.ForRun(r.PathValue("id"))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "Could not read the results")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"results": list})
}

func (s *Server) runResultGet(w http.ResponseWriter, r *http.Request) {
	if s.results == nil {
		writeErr(w, http.StatusNotFound, "No result")
		return
	}
	rid := r.PathValue("rid")
	if !resultIDRe.MatchString(rid) {
		writeErr(w, http.StatusBadRequest, "Invalid result id")
		return
	}
	res, ok, err := s.results.Get(rid)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "Could not read the result")
		return
	}
	if !ok {
		writeErr(w, http.StatusNotFound, "No result with this id")
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// runsCalendar returns the calendar window — the union of past executions and upcoming
// firings, chronological. The one calendar access point behind every calendar surface
// (REQ-012); ?type=auto|todo scopes it.
func (s *Server) runsCalendar(w http.ResponseWriter, r *http.Request) {
	if s.runs == nil {
		writeErr(w, http.StatusServiceUnavailable, "Run store unavailable")
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
		writeErr(w, http.StatusInternalServerError, "Could not read runs")
		return
	}
	want := strings.TrimSpace(r.URL.Query().Get("type"))
	now := time.Now()
	horizon := now.Add(time.Duration(days) * 24 * time.Hour)
	occs := []model.RunOccurrence{} // never nil: null would blank the UI list
	for _, run := range all {
		if want != "" && want != "all" && string(run.Kind) != want {
			continue
		}
		if run.Kind == model.KindTodo {
			// A todo is a single point in time; overdue-but-open still belongs on the calendar.
			if run.DueAt == nil || run.DueAt.After(horizon) {
				continue
			}
			occs = append(occs, model.RunOccurrence{
				RunID: run.ID, RunTitle: run.Title, Kind: run.Kind,
				At: run.DueAt.Format(time.RFC3339), Schedule: "once",
			})
			continue
		}
		if !run.Active || run.Schedule == nil {
			continue
		}
		t := now
		for i := 0; i < 400; i++ { // cap: a daily run over a year is ~366
			next := runs.NextFire(*run.Schedule, t)
			if next.IsZero() || next.After(horizon) {
				break
			}
			occs = append(occs, model.RunOccurrence{
				RunID: run.ID, RunTitle: run.Title, Kind: run.Kind,
				At: next.Format(time.RFC3339), Schedule: scheduleSummary(*run.Schedule),
			})
			t = next
		}
	}
	// Past side: every stored execution, kind-resolved exactly like the history (the shared
	// source) so calendar and history can never diverge.
	from := now
	for _, e := range s.resolvedExecutions(want) {
		at := e.StartedAt
		succeeded := resultSucceeded(e)
		occs = append(occs, model.RunOccurrence{
			RunID: e.RunID, RunTitle: e.RunTitle, Kind: e.Kind,
			At: at.Format(time.RFC3339), ResultID: e.ID, Succeeded: &succeeded,
		})
		if at.Before(from) {
			from = at
		}
	}
	sort.Slice(occs, func(i, j int) bool { return occs[i].At < occs[j].At })
	writeJSON(w, http.StatusOK, model.RunCalendar{
		From: from.Format(time.RFC3339), To: horizon.Format(time.RFC3339), Occurrences: occs,
	})
}

// resultSucceeded reads success off the server stage array — every repo pipeline done and
// succeeded (the client never derives stages, B-17; neither does this projection invent them).
func resultSucceeded(res runs.Result) bool {
	if len(res.Repos) == 0 {
		return false
	}
	for _, rp := range res.Repos {
		done, ok := model.PipelineSucceeded(rp.Stages)
		if !done || !ok {
			return false
		}
	}
	return true
}

// runsExecutions returns stored executions (newest first) — the execution history, including
// runs since deleted. ?type=auto|todo scopes it per surface.
func (s *Server) runsExecutions(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"executions": s.resolvedExecutions(r.URL.Query().Get("type"))})
}

// resolvedExecutions returns every stored execution (newest first) with its kind resolved — a
// stamped result is trusted, an unstamped one falls back to its still-existing run's kind. The
// single source of past-execution truth shared by history and calendar.
func (s *Server) resolvedExecutions(typeParam string) []runs.Result {
	if s.results == nil {
		return []runs.Result{}
	}
	execs, err := s.results.List()
	if err != nil {
		return []runs.Result{}
	}
	kindByRun := map[string]model.RunKind{}
	if s.runs != nil {
		if all, listErr := s.runs.List(); listErr == nil {
			for _, run := range all {
				kindByRun[run.ID] = run.Kind
			}
		}
	}
	want := strings.TrimSpace(typeParam)
	out := make([]runs.Result, 0, len(execs))
	for _, e := range execs {
		if e.Kind == "" {
			if k, ok := kindByRun[e.RunID]; ok {
				e.Kind = k
			} else {
				e.Kind = model.KindAuto
			}
		}
		if want != "" && want != "all" && string(e.Kind) != want {
			continue
		}
		out = append(out, e)
	}
	return out
}

// mercuryChat is the Mercury-WIDE assistant: it sees the axioms, the implementation rules, the
// run rules, the meta-axioms, the runs and the todos, and may embed ONE reviewable action.
func (s *Server) mercuryChat(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Messages []mercury.ChatMessage `json:"messages"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if len(body.Messages) == 0 {
		writeErr(w, http.StatusBadRequest, "messages is required")
		return
	}
	cookie, csrf := r.Header.Get("Cookie"), csrfFrom(r)
	cctx, err := s.chatContext(r, cookie)
	if err != nil {
		mercuryError(w, err)
		return
	}
	prompt := mercury.MercuryChatPrompt(cctx, body.Messages)
	result, _, err := aigentic.Run(r.Context(), cookie, csrf, "claude-cli", aigentic.Request{Prompt: prompt, OutputFormat: "text"})
	if err != nil {
		mercuryError(w, err)
		return
	}
	action, cleaned, ok := mercury.ExtractChatAction(result.Output, cctx.ActionContext())
	resp := map[string]any{"reply": cleaned, "model": result.Model}
	if ok {
		resp["action"] = action
	}
	writeJSON(w, http.StatusOK, resp)
}

// chatContext gathers everything the Mercury-wide assistant knows.
func (s *Server) chatContext(r *http.Request, cookie string) (mercury.ChatContext, error) {
	ctx := r.Context()
	paths, err := s.axioms.List(ctx, "")
	if err != nil {
		return mercury.ChatContext{}, err
	}
	var c mercury.ChatContext
	for _, p := range paths {
		if !strings.HasSuffix(p, ".md") {
			continue
		}
		section := chatSection(p)
		if section == "" {
			continue
		}
		rec, ok := s.fetchRecord(ctx, cookie, p)
		if !ok {
			continue
		}
		c.Records = append(c.Records, mercury.ChatRecord{
			Section: section, Path: p, ID: rec.Axiom.ID, Titel: rec.Axiom.Titel, Body: rec.Axiom.Body,
		})
	}
	if s.runs != nil {
		if all, err := s.runs.List(); err == nil {
			for _, run := range all {
				cr := mercury.ChatRun{ID: run.ID, Name: run.Title, Todo: run.Kind == model.KindTodo}
				if run.Kind == model.KindTodo {
					cr.Task = run.Task
					cr.Targets = chatTargets(run.Targets)
				} else {
					if run.Schedule != nil {
						cr.Schedule = fromSchedule(*run.Schedule)
					}
					cr.AxiomIDs = run.AxiomIDs
				}
				c.Runs = append(c.Runs, cr)
			}
		}
	}
	// Existing repos as possible todo targets — best-effort: a GitHub hiccup must not break chat.
	if repos, ok := s.userRepos(r); ok {
		for _, rp := range repos {
			c.Repos = append(c.Repos, mercury.ChatRepo{ID: rp.ID, Name: rp.Name})
		}
	}
	return c, nil
}

// chatSection maps a record path to its Mercury section, or "" outside the known namespaces.
func chatSection(path string) string {
	for _, ns := range []string{mercury.NsAxiome, mercury.NsRegeln, mercury.NsLaeufe, mercury.NsMeta} {
		if strings.HasPrefix(path, ns+"/") {
			return ns
		}
	}
	return ""
}

// chatTargets converts stored targets to the chat action shape.
func chatTargets(ts []runs.Target) []mercury.ActionTarget {
	out := make([]mercury.ActionTarget, 0, len(ts))
	for _, t := range ts {
		at := mercury.ActionTarget{Repo: t.Repo}
		if t.Create {
			at.NewRepo, at.Repo = t.Repo, ""
		}
		out = append(out, at)
	}
	return out
}

// --- small helpers -----------------------------------------------------------

func plannedRunsFrom(all []runs.Run) []mercury.PlannedRun {
	out := make([]mercury.PlannedRun, 0, len(all))
	for _, run := range all {
		pr := mercury.PlannedRun{Name: run.Title, AxiomIDs: run.AxiomIDs}
		if run.Schedule != nil {
			pr.Schedule = fromSchedule(*run.Schedule)
		}
		out = append(out, pr)
	}
	return out
}

func catalogSlice(cat runs.Catalog) []mercury.RunAxiom {
	out := make([]mercury.RunAxiom, 0, len(cat.ByID))
	for _, a := range cat.ByID {
		out = append(out, a)
	}
	return out
}

// scheduleSummary is a compact label for a schedule ("daily 03:00", "weekly Mon,Thu 03:00").
func scheduleSummary(sc runs.ScheduleSpec) string {
	switch sc.Kind {
	case runs.Daily:
		return "daily " + sc.TimeOfDay
	case runs.Weekly:
		names := []string{"Sun", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat"}
		var days []string
		for _, d := range sc.Weekdays {
			if int(d) >= 0 && int(d) < 7 {
				days = append(days, names[d])
			}
		}
		return "weekly " + strings.Join(days, ",") + " " + sc.TimeOfDay
	default:
		return sc.TimeOfDay
	}
}

// actorOf is the ONE mapping from the request to an authorship actor (REQ-041): a label,
// never a barrier. No user on the request reads as unattributed, never fabricated.
func actorOf(r *http.Request) model.Actor {
	return model.Actor{User: strings.TrimSuffix(actor(r), "?")}
}

func indexOfRun(list []runs.Run, id string) int {
	for i := range list {
		if list[i].ID == id {
			return i
		}
	}
	return -1
}

// orEmptyRuns guarantees a non-nil slice (nil marshals to JSON null and blanks the list).
func orEmptyRuns(r []runs.Run) []runs.Run {
	if r == nil {
		return []runs.Run{}
	}
	return r
}

func keysOf(m map[string]mercury.RunAxiom) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func toSchedule(ps mercury.PlanSchedule) *runs.ScheduleSpec {
	wd := make([]time.Weekday, 0, len(ps.Weekdays))
	for _, d := range ps.Weekdays {
		wd = append(wd, time.Weekday(d))
	}
	return &runs.ScheduleSpec{Kind: runs.ScheduleKind(ps.Kind), TimeOfDay: ps.TimeOfDay, Weekdays: wd}
}

func fromSchedule(s runs.ScheduleSpec) mercury.PlanSchedule {
	wd := make([]int, 0, len(s.Weekdays))
	for _, d := range s.Weekdays {
		wd = append(wd, int(d))
	}
	return mercury.PlanSchedule{Kind: string(s.Kind), TimeOfDay: s.TimeOfDay, Weekdays: wd}
}

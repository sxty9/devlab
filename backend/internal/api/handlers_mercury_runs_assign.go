package api

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"devlab/backend/internal/mercury"
	"devlab/backend/internal/runs"
)

// A newly created axiom (and any axiom left uncovered by a run edit, delete or history restore) must be
// enforced without a manual step, so it is assigned to a run AUTOMATICALLY. This is the background half
// of that guarantee: the write handler only *kicks* the assigner and returns at once; the assigner
// coalesces bursts of kicks, then runs ONE assignment pass on the caller's behalf.
//
// It adds no second planning path: it reuses the very machinery behind the "Mit KI auffüllen" button —
// s.runCatalog (the axiom set), s.planRuns + mercury.RunPlanFillPrompt (the plan, validated against the
// known axiom ids), composeInto (the snapshot) and upsertPlannedRun (extend-or-create) — the difference
// being only that it runs detached and debounced instead of returning a reviewable proposal. The plan
// prefers extending an existing, thematically fitting run and creates a new one only when none fits.
//
// The pass is idempotent: it acts only on the CURRENTLY uncovered axioms (re-checked under the store
// lock), so an already-covered axiom is never reassigned and overlapping passes never double-assign.
// Every outcome — success or failure — is recorded as a portioned notice so the user is informed and can
// adjust the assignment afterwards. A failure (e.g. the KI-Planung is unreachable) leaves the axiom
// uncovered and visible; it blocks neither the axiom write nor the running scheduler.
type autoAssigner struct {
	s     *Server
	delay time.Duration

	// Network seams (the only I/O), injected so tests drive the pass deterministically without aigentic.
	// catalog returns the current axiom set (by id) and all Laufregeln; plan turns the uncovered axioms
	// into a validated run plan.
	catalog func(ctx context.Context, cookie string) (map[string]mercury.RunAxiom, []mercury.RunAxiom, error)
	plan    func(ctx context.Context, cookie, csrf string, uncovered []mercury.RunAxiom, existing, known []string) (mercury.RunPlan, error)

	mu      sync.Mutex
	cookie  string // credentials + actor of the most recent kick — the pass runs as that user
	csrf    string
	actor   string
	timer   *time.Timer
	running bool
	pending bool // a kick arrived while a pass was scheduled/running → run once more afterwards
}

// assignPassTimeout caps one background pass. Generous: a plan may retry the model a few times (each
// aigentic call is itself bounded), so this is only a safety net against a wedged call.
const assignPassTimeout = 10 * time.Minute

// newAutoAssigner builds the assigner with the production seams wired to the reused machinery.
func newAutoAssigner(s *Server) *autoAssigner {
	a := &autoAssigner{s: s, delay: assignDelay()}
	a.catalog = func(ctx context.Context, cookie string) (map[string]mercury.RunAxiom, []mercury.RunAxiom, error) {
		byID, _, laufregeln, err := s.runCatalog(ctx, cookie)
		return byID, laufregeln, err
	}
	a.plan = func(ctx context.Context, cookie, csrf string, uncovered []mercury.RunAxiom, existing, known []string) (mercury.RunPlan, error) {
		return s.planRuns(ctx, cookie, csrf, known, func(correction string) string {
			return mercury.RunPlanFillPrompt(uncovered, existing, correction)
		})
	}
	return a
}

// assignDelay is the debounce window: kicks within it coalesce into one bundled pass. Short by default so
// a new axiom is covered promptly; overridable (mainly for tests) via DEVLAB_MERCURY_ASSIGN_DELAY.
func assignDelay() time.Duration {
	if d := strings.TrimSpace(os.Getenv("DEVLAB_MERCURY_ASSIGN_DELAY")); d != "" {
		if pd, err := time.ParseDuration(d); err == nil && pd >= 0 {
			return pd
		}
	}
	return 2 * time.Second
}

// kick records the latest credentials and (re)arms the debounce. Cheap and non-blocking — the write
// handler calls it and returns immediately.
func (a *autoAssigner) kick(cookie, csrf, actor string) {
	if a == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.cookie, a.csrf, a.actor = cookie, csrf, actor
	a.schedule()
}

// schedule arms (or pushes out) the debounce timer. The caller holds a.mu. While a pass runs, it only
// flags that another is wanted; the running pass re-arms the timer when it finishes.
func (a *autoAssigner) schedule() {
	if a.running {
		a.pending = true
		return
	}
	if a.timer != nil {
		a.timer.Reset(a.delay)
		return
	}
	a.timer = time.AfterFunc(a.delay, a.fire)
}

// fire runs one pass with the freshest captured credentials, then re-arms if a kick arrived meanwhile.
func (a *autoAssigner) fire() {
	a.mu.Lock()
	a.timer = nil
	if a.running { // a pass is already running — let it pick up this firing when it finishes
		a.pending = true
		a.mu.Unlock()
		return
	}
	a.running = true
	cookie, csrf, actor := a.cookie, a.csrf, a.actor
	a.mu.Unlock()

	a.runPass(cookie, csrf, actor)

	a.mu.Lock()
	a.running = false
	rerun := a.pending
	a.pending = false
	if rerun {
		a.schedule()
	}
	a.mu.Unlock()
}

// Pending reports whether an assignment is scheduled or running — so an uncovered axiom can be shown as
// only temporarily uncovered rather than permanently.
func (a *autoAssigner) Pending() bool {
	if a == nil {
		return false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.running || a.timer != nil
}

// runPass performs one assignment pass: recompute the uncovered set from the source of truth, plan it,
// apply it, and record notices. Contained against panics — an autonomous background task must never take
// devlabd down.
func (a *autoAssigner) runPass(cookie, csrf, actor string) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("devlabd: auto-assign panicked (contained): %v", r)
		}
	}()
	if a.s == nil || a.s.runs == nil {
		return
	}
	if strings.TrimSpace(cookie) == "" {
		return // no session to act as → aigentic is unreachable; nothing to attempt (e.g. dev-bypass)
	}
	ctx, cancel := context.WithTimeout(context.Background(), assignPassTimeout)
	defer cancel()

	byID, laufregeln, err := a.catalog(ctx, cookie)
	if err != nil {
		// The scheme store is unreachable: the whole Mercury surface is down and we cannot even tell which
		// axioms exist. Nothing known to assign — stay quiet (the outage is visible elsewhere) and retry on
		// the next kick.
		log.Printf("devlabd: auto-assign catalog unavailable: %v", err)
		return
	}
	all, err := a.s.runs.List()
	if err != nil {
		log.Printf("devlabd: auto-assign runs list: %v", err)
		return
	}
	covered := map[string]bool{}
	existing := make([]string, 0, len(all))
	for _, run := range all {
		if !run.IsTodo() {
			existing = append(existing, run.Name) // only auto runs carry axioms and may be extended
		}
		for _, id := range run.AxiomIDs {
			covered[id] = true
		}
	}
	uncovered := make([]mercury.RunAxiom, 0)
	for id, ax := range byID {
		if !covered[id] {
			uncovered = append(uncovered, ax)
		}
	}
	if len(uncovered) == 0 {
		return // idempotent: everything is already covered
	}
	// Map iteration is random; sort for a stable prompt and stable notices.
	sort.Slice(uncovered, func(i, j int) bool { return uncovered[i].ID < uncovered[j].ID })

	plan, err := a.plan(ctx, cookie, csrf, uncovered, existing, keysOf(byID))
	if err != nil {
		log.Printf("devlabd: auto-assign planning failed: %v", err)
		a.recordFailure(uncovered, "KI-Planung nicht erreichbar")
		return
	}
	a.applyPlan(actor, plan, byID, laufregeln, uncovered)
}

// errNoAssignment aborts the apply Mutate when the plan, re-filtered against the live covered set, leaves
// nothing to write — so no snapshot is recorded for a no-op.
var errNoAssignment = errors.New("auto-assign: nothing to assign")

// assignOutcome captures one run's share of a pass for the notice feed.
type assignOutcome struct {
	runID    string
	runName  string
	newRun   bool
	axiomIDs []string
	titles   []string
}

// applyPlan writes the plan into the run store: for each planned run it keeps only the axioms that are
// STILL uncovered and known (re-checked under the lock), then extends a matching auto run or creates a
// new one, recomposing the affected snapshot in the same step. On success it records one notice per
// affected run; on a store error it records a single failure notice.
func (a *autoAssigner) applyPlan(actor string, plan mercury.RunPlan, byID map[string]mercury.RunAxiom, laufregeln []mercury.RunAxiom, tried []mercury.RunAxiom) {
	now := time.Now().UTC()
	var outcomes []assignOutcome
	_, err := a.s.runs.Mutate("auto-assign", actor, func(cur []runs.Run) ([]runs.Run, error) {
		covered := map[string]bool{}
		for _, run := range cur {
			for _, id := range run.AxiomIDs {
				covered[id] = true
			}
		}
		out := append([]runs.Run(nil), cur...)
		var oc []assignOutcome
		for _, pr := range plan.Runs {
			var ids []string
			for _, id := range pr.AxiomIDs {
				if _, known := byID[id]; known && !covered[id] {
					ids = append(ids, id)
				}
			}
			ids = dedupStrings(ids)
			if len(ids) == 0 {
				continue
			}
			np := runs.Run{
				ID: runs.NewID(), Name: strings.TrimSpace(pr.Name), Enabled: true,
				Schedule: toSchedule(pr.Schedule), AxiomIDs: ids,
				CreatedAt: now, UpdatedAt: now,
			}
			composeInto(&np, byID, laufregeln, now)
			if nf, e := np.Schedule.Next(now); e == nil {
				np.NextFireAt = &nf
			}
			var affected runs.Run
			var created bool
			out, affected, created = upsertPlannedRun(out, np, byID, laufregeln, now)
			titles := make([]string, 0, len(ids))
			for _, id := range ids {
				titles = append(titles, mercury.RunAxiomTitle(byID[id]))
				covered[id] = true // a later plan entry must not re-grab the same axiom
			}
			oc = append(oc, assignOutcome{runID: affected.ID, runName: affected.Name, newRun: created, axiomIDs: ids, titles: titles})
		}
		if len(oc) == 0 {
			return nil, errNoAssignment
		}
		outcomes = oc
		return out, nil
	})
	if errors.Is(err, errNoAssignment) {
		return // the plan named only already-covered axioms → genuine no-op, no notice
	}
	if err != nil {
		log.Printf("devlabd: auto-assign save failed: %v", err)
		a.recordFailure(tried, "Zuordnung konnte nicht gespeichert werden")
		return
	}
	for _, o := range outcomes {
		a.record(runs.Notice{
			Kind: runs.NoticeAssigned, RunID: o.runID, RunName: o.runName, NewRun: o.newRun,
			AxiomIDs: o.axiomIDs, Axioms: o.titles,
		})
	}
}

// recordFailure notes that a set of axioms could not be assigned, with a short reason (never a raw log).
func (a *autoAssigner) recordFailure(axioms []mercury.RunAxiom, reason string) {
	ids := make([]string, 0, len(axioms))
	titles := make([]string, 0, len(axioms))
	for _, ax := range axioms {
		ids = append(ids, ax.ID)
		titles = append(titles, mercury.RunAxiomTitle(ax))
	}
	a.record(runs.Notice{Kind: runs.NoticeFailed, AxiomIDs: ids, Axioms: titles, Reason: reason})
}

func (a *autoAssigner) record(n runs.Notice) {
	if a.s == nil || a.s.runNotices == nil {
		return
	}
	if err := a.s.runNotices.Add(n); err != nil {
		log.Printf("devlabd: auto-assign notice write failed: %v", err)
	}
}

// --- server-side glue ---------------------------------------------------------

// kickAutoAssign hands the assigner the caller's session (cookie + CSRF) and identity, then returns at
// once. The pass runs on the user's behalf, exactly as the interactive "Mit KI auffüllen" does.
func (s *Server) kickAutoAssign(r *http.Request) {
	if s.assigner == nil {
		return
	}
	s.assigner.kick(r.Header.Get("Cookie"), csrfFrom(r), actor(r))
}

// assignPending reports whether an automatic assignment is scheduled or in flight.
func (s *Server) assignPending() bool { return s.assigner.Pending() }

// runsNoticesList returns the auto-assignment feed (newest first), portioned by the store's own cap.
func (s *Server) runsNoticesList(w http.ResponseWriter, _ *http.Request) {
	if s.runNotices == nil {
		writeJSON(w, http.StatusOK, map[string]any{"notices": []runs.Notice{}})
		return
	}
	list, err := s.runNotices.List()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "Hinweise konnten nicht gelesen werden")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"notices": list})
}

// runsNoticeDismiss drops one notice the user has acknowledged.
func (s *Server) runsNoticeDismiss(w http.ResponseWriter, r *http.Request) {
	if s.runNotices == nil {
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
		return
	}
	var body struct {
		ID string `json:"id"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if strings.TrimSpace(body.ID) == "" {
		writeErr(w, http.StatusBadRequest, "id ist erforderlich")
		return
	}
	if err := s.runNotices.Dismiss(body.ID); err != nil {
		writeErr(w, http.StatusInternalServerError, "Hinweis konnte nicht entfernt werden")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// runsNoticesClear empties the feed.
func (s *Server) runsNoticesClear(w http.ResponseWriter, _ *http.Request) {
	if s.runNotices == nil {
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
		return
	}
	if err := s.runNotices.Clear(); err != nil {
		writeErr(w, http.StatusInternalServerError, "Hinweise konnten nicht entfernt werden")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

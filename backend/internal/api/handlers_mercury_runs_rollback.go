package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"devlab/backend/internal/discover"
	"devlab/backend/internal/github"
	"devlab/backend/internal/model"
	"devlab/backend/internal/runs"
	"devlab/backend/internal/workspace"
)

// Rolling back a delivery is a COUNTER-BOOKING, never a deletion (req 10): its changes are nullified by a
// new reversing commit on the dev branch — history is never rewritten and no branch is force-overwritten,
// so the running dev state and everything built on it stays valid; dev is then re-delivered. It works in
// both directions (req 11): an OPEN PR is closed with a justification, a MERGED one gets a reversing PR
// that runs the same stacked chain. Conflicts are never guessed (req 12): when later work built on the
// delivery, a concrete ToDo is raised to counter-book by hand instead of committing a mangled revert.

// errDeliveryNotFound is returned when a rollback names a delivery the ledger doesn't have.
var errDeliveryNotFound = errors.New("delivery not found")

// counterBookResult is what the git side of a rollback reports back to the orchestration: whether the
// reverse conflicted, whether it actually changed anything (false = the effect was already gone, an
// idempotent no-op), the dev tip before/after the counter-booking commit (the reversal's range), the
// repo's default branch (so a reversing PR can be based without a second lookup), and the dev-deploy.
type counterBookResult struct {
	conflicted    bool
	changed       bool
	before, after string
	defaultBranch string
	deployed      bool
	deployLog     string
}

// RollbackOutcome is the API-visible result of a rollback.
type RollbackOutcome struct {
	DeliveryID         string `json:"deliveryId"`
	Reverted           bool   `json:"reverted"`
	AlreadyReverted    bool   `json:"alreadyReverted,omitempty"`
	Conflict           bool   `json:"conflict,omitempty"`
	NoChange           bool   `json:"noChange,omitempty"` // the delivery's effect was already absent (idempotent no-op)
	TodoID             string `json:"todoId,omitempty"`
	LaterOpen          int    `json:"laterOpen,omitempty"`
	ClosedPR           int    `json:"closedPr,omitempty"`
	ReversalPRUrl      string `json:"reversalPrUrl,omitempty"`
	ReversalDeliveryID string `json:"reversalDeliveryId,omitempty"`
	Deployed           bool   `json:"deployed,omitempty"`
}

// ─── Seam dispatchers (real impl unless a test injected a fake) ──────────────

func (x *runExecutor) counterBook(ctx context.Context, token string, d runs.Delivery, reversalBranch string) (counterBookResult, error) {
	if x.counterBookFn != nil {
		return x.counterBookFn(ctx, token, d, reversalBranch)
	}
	return x.counterBookReal(ctx, token, d, reversalBranch)
}

func (x *runExecutor) createPR(ctx context.Context, token, fullName, head, base, title, body string) (github.PullRequest, error) {
	if x.createPRFn != nil {
		return x.createPRFn(ctx, token, fullName, head, base, title, body)
	}
	return github.CreatePullRequest(ctx, token, fullName, head, base, title, body)
}

func (x *runExecutor) closePR(ctx context.Context, token, fullName string, number int) error {
	if x.closePRFn != nil {
		return x.closePRFn(ctx, token, fullName, number)
	}
	return github.ClosePullRequest(ctx, token, fullName, number)
}

func (x *runExecutor) commentPR(ctx context.Context, token, fullName string, number int, body string) error {
	if x.commentPRFn != nil {
		return x.commentPRFn(ctx, token, fullName, number, body)
	}
	return github.AddPullRequestComment(ctx, token, fullName, number, body)
}

func (x *runExecutor) resetRepo(ctx context.Context, token string, repo model.Repo) (string, error) {
	if x.resetRepoFn != nil {
		return x.resetRepoFn(ctx, token, repo)
	}
	return x.resetRepoReal(ctx, token, repo)
}

// ─── Rollback orchestration ─────────────────────────────────────────────────

// RollbackDelivery counter-books one delivery. The git side (counterBook) either reports a conflict — in
// which case a ToDo is raised and nothing is discarded — or leaves a clean reversing commit on the dev
// branch. On a clean revert: a MERGED delivery gets a stacked reversing PR (so main, and hence prod on
// merge, is reversed too), while an OPEN one has its PR closed with a justification (the dev counter-book
// already withdrew it). The delivery is then marked reverted; the original record is never destroyed.
func (x *runExecutor) RollbackDelivery(ctx context.Context, deliveryID, actor string) (RollbackOutcome, error) {
	out := RollbackOutcome{DeliveryID: deliveryID}
	d, ok, err := x.s.deliveries.Get(deliveryID)
	if err != nil {
		return out, err
	}
	if !ok {
		return out, errDeliveryNotFound
	}
	if d.Status == runs.DeliveryReverted {
		out.AlreadyReverted = true
		return out, nil
	}
	token, err := x.token()
	if err != nil {
		return out, err
	}

	// Authoritative merged/open state: Maintain mirrors it onto the ledger, but a merge can land between
	// ticks, so confirm from GitHub when there is a PR (fall back to the ledger status if the fetch fails).
	merged := d.Status == runs.DeliveryMerged
	if d.PRNumber != 0 {
		if pr, ferr := x.fetchPR(ctx, token, d.Repo, d.PRNumber); ferr == nil {
			merged = pr.Merged
		}
	}

	// A merged delivery's counter-booking is itself a delivery with a stacked reversing PR; snapshot its
	// branch during counterBook so the reversal is addressable like any other change. The identifiers are
	// DETERMINISTIC (derived from the delivery id) so a retried rollback reuses the same branch/record
	// instead of proliferating duplicates.
	reversalBranch, reversalID := "", ""
	if merged {
		reversalID = "dlv_rev_" + strings.TrimPrefix(d.ID, "dlv_")
		reversalBranch = "mercury-run/" + d.RunID + "/revert-" + d.ID
	}

	cb, err := x.counterBook(ctx, token, d, reversalBranch)
	if err != nil {
		return out, err
	}
	if cb.conflicted {
		// Make no guess (req 12): later work built on this delivery and the reverse doesn't apply cleanly.
		// Raise a concrete ToDo that does the counter-booking by hand, instead of silently discarding it.
		later, _ := x.s.deliveries.LaterOpenDeliveries(d.Repo, d.ID)
		todo := buildRollbackTodo(d, later, actor)
		if _, err := x.s.runs.Mutate("rollback-conflict", actor, func(cur []runs.Run) ([]runs.Run, error) {
			return append(cur, todo), nil
		}); err != nil {
			return out, err
		}
		out.Conflict, out.TodoID, out.LaterOpen = true, todo.ID, len(later)
		log.Printf("devlabd: rollback of delivery %s conflicts with %d later delivery/ies — raised ToDo %s", d.ID, len(later), todo.ID)
		return out, nil
	}
	out.Deployed = cb.deployed
	now := time.Now().UTC()

	if !cb.changed {
		// Nothing to counter-book: the delivery's effect is already absent from the dev state (a repeated
		// rollback, or later work removed it). Mark it reverted idempotently and, if its PR is still open,
		// close it. A MERGED delivery whose effect is already gone from dev is logged — dev is correct, but
		// the merged default branch may still carry it, which a fresh rollback (once un-marked) would catch.
		out.NoChange = true
		if !merged && d.PRNumber != 0 {
			_ = x.commentPR(ctx, token, d.Repo, d.PRNumber, rollbackCloseReason(d, actor))
			if err := x.closePR(ctx, token, d.Repo, d.PRNumber); err != nil {
				return out, fmt.Errorf("close PR: %w", err)
			}
			out.ClosedPR = d.PRNumber
		} else if merged {
			log.Printf("devlabd: rollback of delivery %s — dev already lacks its effect; the merged default branch may still carry it", d.ID)
		}
		_ = x.s.deliveries.Update(d.ID, func(dv *runs.Delivery) {
			dv.Status = runs.DeliveryReverted
			dv.RevertedAt = &now
			dv.RevertedBy = actor
		})
		out.Reverted = true
		return out, nil
	}

	if merged {
		// The reversing PR stacks like any other change (req 11): on the latest open delivery, else default.
		prBase := cb.defaultBranch
		if prev, ok, _ := x.s.deliveries.LatestOpen(d.Repo); ok && prev.Branch != "" {
			prBase = prev.Branch
		}
		pr, err := x.createPR(ctx, token, d.Repo, reversalBranch, prBase, "Revert: "+d.RunName, reversalPRBody(d, actor))
		if err != nil {
			return out, fmt.Errorf("reversing PR: %w", err)
		}
		_ = x.s.runPRs.Add(runs.PendingPR{
			Repo: d.Repo, Number: pr.Number, URL: pr.HTMLURL, RunID: d.RunID,
			CreatedAt: now, MergeBy: now.Add(x.autoMergeAfter),
		})
		_ = x.s.deliveries.Add(runs.Delivery{
			ID: reversalID, RunID: d.RunID, ResultID: d.ResultID, RunName: "Revert: " + d.RunName, Repo: d.Repo,
			Branch: reversalBranch, DevBranch: devBranchName(), BaseBranch: prBase,
			FromCommit: cb.before, ToCommit: cb.after, PRNumber: pr.Number, PRUrl: pr.HTMLURL,
			CreatedAt: now, Status: runs.DeliveryOpen, RevertOf: d.ID,
		})
		out.ReversalPRUrl, out.ReversalDeliveryID = pr.HTMLURL, reversalID
	} else if d.PRNumber != 0 {
		// Open PR → close with a justification (the dev counter-book already removed its effect).
		_ = x.commentPR(ctx, token, d.Repo, d.PRNumber, rollbackCloseReason(d, actor))
		if err := x.closePR(ctx, token, d.Repo, d.PRNumber); err != nil {
			return out, fmt.Errorf("close PR: %w", err)
		}
		out.ClosedPR = d.PRNumber
	}

	_ = x.s.deliveries.Update(d.ID, func(dv *runs.Delivery) {
		dv.Status = runs.DeliveryReverted
		dv.RevertedAt = &now
		dv.RevertedBy = actor
	})
	out.Reverted = true
	log.Printf("devlabd: rolled back delivery %s (%s, merged=%v) by %s", d.ID, d.Repo, merged, actor)
	return out, nil
}

// counterBookReal performs the git side of a rollback against the real per-user workspace: prepare the
// dev branch (grow, not reset), revert the delivery's range as one counter-booking commit, and — on a
// clean revert — push dev (plus the reversal snapshot branch when given) and re-deliver dev.
func (x *runExecutor) counterBookReal(ctx context.Context, token string, d runs.Delivery, reversalBranch string) (counterBookResult, error) {
	name := repoNameOf(d.Repo)
	repo := model.Repo{ID: name, Name: name, FullName: d.Repo}
	devBranch := devBranchName()

	if _, err := x.s.workspaces.Ensure(ctx, x.user, repo.ID, repo.FullName, token, true); err != nil {
		return counterBookResult{}, fmt.Errorf("workspace: %w", err)
	}
	unlock, err := x.s.workspaces.Lock(x.user, repo.ID)
	if err != nil {
		return counterBookResult{}, fmt.Errorf("lock: %w", err)
	}
	defer unlock()
	branch, err := github.DefaultBranch(ctx, token, repo.FullName)
	if err != nil || branch == "" {
		return counterBookResult{}, fmt.Errorf("default branch: %v", err)
	}
	wt, err := x.s.workspaces.Path(x.user, repo.ID)
	if err != nil {
		return counterBookResult{}, fmt.Errorf("workspace path: %w", err)
	}
	ex := workspace.Executor{User: x.user, PerUser: true}
	if _, err := ex.EnsureDevBranch(ctx, wt, token, branch, devBranch); err != nil {
		return counterBookResult{}, fmt.Errorf("dev branch: %w", err)
	}
	if err := ex.CleanWorktree(ctx, wt); err != nil {
		return counterBookResult{}, fmt.Errorf("clean: %w", err)
	}
	before, err := ex.RevParse(ctx, wt, "HEAD")
	if err != nil {
		return counterBookResult{}, fmt.Errorf("dev tip: %w", err)
	}
	ghLogin, ghID := x.runnerIdentity()
	conflicted, changed, err := ex.RevertRange(ctx, wt, d.FromCommit, d.ToCommit, ghLogin, ghID, counterBookMessage(d))
	if err != nil {
		return counterBookResult{}, fmt.Errorf("counter-booking: %w", err)
	}
	if conflicted {
		return counterBookResult{conflicted: true, defaultBranch: branch}, nil
	}
	if !changed {
		// The delivery's effect is already absent (a repeated rollback, or later work removed it). No new
		// commit, nothing to push — an idempotent no-op. The dev tip is unchanged.
		return counterBookResult{changed: false, before: before, after: before, defaultBranch: branch}, nil
	}
	after, err := ex.RevParse(ctx, wt, "HEAD")
	if err != nil {
		return counterBookResult{}, fmt.Errorf("dev tip: %w", err)
	}
	pushRefs := []string{devBranch}
	if reversalBranch != "" {
		if err := ex.BranchAt(ctx, wt, reversalBranch, "HEAD"); err != nil {
			return counterBookResult{}, fmt.Errorf("reversal branch: %w", err)
		}
		pushRefs = append(pushRefs, reversalBranch)
	}
	if _, err := ex.PushRefs(ctx, wt, token, false, pushRefs...); err != nil {
		return counterBookResult{}, fmt.Errorf("push: %w", err)
	}
	res := counterBookResult{changed: true, before: before, after: after, defaultBranch: branch}
	// Re-deliver dev (req 10): the dev state changed, so dev must be re-shipped (full mode only).
	if x.mode == "full" && x.shouldDevDeploy(repo.ID) && x.deployable(repo.Name) {
		if artifactDir, berr := x.buildArtifact(ctx, wt, repo); berr == nil {
			if depLog, derr := x.deploy(ctx, repo, artifactDir, "dev"); derr == nil {
				res.deployed, res.deployLog = true, depLog
			}
		}
	}
	return res, nil
}

// resetRepoReal is the EXPLICIT way back (req 5): move the dev branch back onto the default branch and
// force-publish it, then re-deliver dev. Destructive and explicit — never a side effect of a run.
func (x *runExecutor) resetRepoReal(ctx context.Context, token string, repo model.Repo) (string, error) {
	devBranch := devBranchName()
	branch, err := github.DefaultBranch(ctx, token, repo.FullName)
	if err != nil || branch == "" {
		return "", fmt.Errorf("default branch: %v", err)
	}
	if _, err := x.s.workspaces.Ensure(ctx, x.user, repo.ID, repo.FullName, token, true); err != nil {
		return "", fmt.Errorf("workspace: %w", err)
	}
	unlock, err := x.s.workspaces.Lock(x.user, repo.ID)
	if err != nil {
		return "", fmt.Errorf("lock: %w", err)
	}
	defer unlock()
	wt, err := x.s.workspaces.Path(x.user, repo.ID)
	if err != nil {
		return "", fmt.Errorf("workspace path: %w", err)
	}
	ex := workspace.Executor{User: x.user, PerUser: true}
	if err := ex.ResetDevToDefault(ctx, wt, token, branch, devBranch); err != nil {
		return "", fmt.Errorf("reset: %w", err)
	}
	if _, err := ex.PushRefs(ctx, wt, token, true, devBranch); err != nil { // force — the deliberate reset
		return "", fmt.Errorf("push: %w", err)
	}
	logLine := "dev branch " + devBranch + " reset to " + branch
	if x.mode == "full" && x.shouldDevDeploy(repo.ID) && x.deployable(repo.Name) {
		if artifactDir, berr := x.buildArtifact(ctx, wt, repo); berr == nil {
			if depLog, derr := x.deploy(ctx, repo, artifactDir, "dev"); derr == nil {
				logLine += "\n" + depLog
			}
		}
	}
	return logLine, nil
}

// ResetRepo resets one repo's dev branch to its default branch (the explicit way back).
func (x *runExecutor) ResetRepo(ctx context.Context, repoFullName, actor string) (string, error) {
	token, err := x.token()
	if err != nil {
		return "", err
	}
	name := repoNameOf(repoFullName)
	repo := model.Repo{ID: name, Name: name, FullName: repoFullName}
	out, err := x.resetRepo(ctx, token, repo)
	if err != nil {
		return "", err
	}
	log.Printf("devlabd: dev branch of %s reset to default by %s", repoFullName, actor)
	return out, nil
}

// ─── ToDo / message builders ────────────────────────────────────────────────

// buildRollbackTodo composes the concrete ToDo raised when a counter-booking conflicts with later work
// (req 12). Its task is an English instruction to the agent (the resolution is an implementation task):
// undo exactly this delivery on the dev branch while preserving the later deliveries that build on it. It
// is created due-now so the runner picks it up, and never rewrites history.
func buildRollbackTodo(d runs.Delivery, later []runs.Delivery, actor string) runs.Run {
	repoName := repoNameOf(d.Repo)
	var b strings.Builder
	fmt.Fprintf(&b, "Roll back Mercury delivery %s in %s (run %q", d.ID, repoName, d.RunName)
	if d.PRNumber != 0 {
		fmt.Fprintf(&b, ", PR #%d", d.PRNumber)
	}
	b.WriteString(").\n\n")
	fmt.Fprintf(&b, "Counter-book its changes on the dev branch (%s): add a NEW reverting commit that "+
		"nullifies exactly the effect of commits %s..%s. Do NOT rewrite history and do NOT force-push — "+
		"the running dev state and everything built on it must stay valid.\n\n", d.DevBranch, short(d.FromCommit), short(d.ToCommit))
	b.WriteString("A plain revert conflicts because later work builds on this delivery. Resolve the " +
		"conflicts by hand, preserving that later work:\n")
	for _, l := range later {
		fmt.Fprintf(&b, "  - delivery %s (run %q", l.ID, l.RunName)
		if l.PRNumber != 0 {
			fmt.Fprintf(&b, ", PR #%d", l.PRNumber)
		}
		b.WriteString(")\n")
	}
	b.WriteString("\nWhen done, the dev state must no longer contain this delivery's changes while every " +
		"later delivery keeps working. Commit the counter-booking so this run opens its own (stacked) PR.")

	now := time.Now().UTC()
	due := now
	return runs.Run{
		ID: runs.NewID(), Name: "Rollback: " + d.RunName + " (" + repoName + ")",
		Type: runs.TypeTodo, Enabled: true, Task: b.String(),
		Targets:   []runs.Target{{Repo: repoName}},
		CreatedAt: now, UpdatedAt: now, DueAt: &due,
	}
}

func counterBookMessage(d runs.Delivery) string {
	return "Revert Mercury delivery " + d.ID + " (" + d.RunName + ")\n\n" +
		"Counter-booking of " + short(d.FromCommit) + ".." + short(d.ToCommit) + " — history preserved.\n\n🤖 Mercury"
}

func reversalPRBody(d runs.Delivery, actor string) string {
	return "Gegenbuchung der bereits zusammengeführten Lieferung **" + d.ID + "** (Lauf _" + d.RunName +
		"_) durch " + actorLabel(actor) + ". Dieser PR hebt die Wirkung der Lieferung durch einen neuen, " +
		"umkehrenden Commit auf — ohne die Historie umzuschreiben — und durchläuft dieselbe Kette wie jede " +
		"andere Änderung.\n\n🤖 Mercury"
}

func rollbackCloseReason(d runs.Delivery, actor string) string {
	return "Zurückgerollt durch " + actorLabel(actor) + ": Die Lieferung wurde auf dem dev-Stand gegengebucht " +
		"(umkehrender Commit, Historie erhalten). Da dieser PR noch nicht zusammengeführt war, wird er mit " +
		"dieser Begründung geschlossen.\n\n🤖 Mercury"
}

func actorLabel(actor string) string {
	if strings.TrimSpace(actor) == "" {
		return "Mercury"
	}
	return actor
}

// ─── HTTP handlers ──────────────────────────────────────────────────────────

// rollbackReady reports whether rollback/reset can run: the scheduler executor is armed and NOT in
// read-only report mode (report never produces deliveries or deploys).
func (s *Server) rollbackReady() bool {
	return s.runExec != nil && s.runExec.mode != "" && s.runExec.mode != "report"
}

// runDeliveriesList returns the delivery ledger (optionally filtered by ?repo=), newest first — the
// addressable record of what each run built and what dev serves.
func (s *Server) runDeliveriesList(w http.ResponseWriter, r *http.Request) {
	if s.deliveries == nil {
		writeJSON(w, http.StatusOK, map[string]any{"deliveries": []runs.Delivery{}})
		return
	}
	var (
		list []runs.Delivery
		err  error
	)
	if repo := strings.TrimSpace(r.URL.Query().Get("repo")); repo != "" {
		list, err = s.deliveries.ListForRepo(repo)
	} else {
		list, err = s.deliveries.List()
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "Lieferungen konnten nicht gelesen werden")
		return
	}
	// Newest first for display.
	out := make([]runs.Delivery, len(list))
	for i := range list {
		out[i] = list[len(list)-1-i]
	}
	writeJSON(w, http.StatusOK, map[string]any{"deliveries": out})
}

// runDeliveryRollback counter-books a delivery (kill-switch for shipped work). 503 when execution is
// unconfigured, 404 for an unknown delivery.
func (s *Server) runDeliveryRollback(w http.ResponseWriter, r *http.Request) {
	if !s.rollbackReady() {
		writeErr(w, http.StatusServiceUnavailable, "Rückrollen ist nicht konfiguriert (DEVLAB_RUNS_MODE=pr|full)")
		return
	}
	id := r.PathValue("id")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()
	out, err := s.runExec.RollbackDelivery(ctx, id, actor(r))
	if errors.Is(err, errDeliveryNotFound) {
		writeErr(w, http.StatusNotFound, "Keine Lieferung mit dieser id")
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "Rückrollen fehlgeschlagen: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// runRepoReset resets a repo's dev branch to its default branch — the explicit way back (req 5). Body:
// {"repo":"owner/name"} (a bare name is resolved under the configured owner).
func (s *Server) runRepoReset(w http.ResponseWriter, r *http.Request) {
	if !s.rollbackReady() {
		writeErr(w, http.StatusServiceUnavailable, "Zurücksetzen ist nicht konfiguriert (DEVLAB_RUNS_MODE=pr|full)")
		return
	}
	var body struct {
		Repo string `json:"repo"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "Ungültiger Request-Body")
		return
	}
	full := strings.TrimSpace(body.Repo)
	if full == "" {
		writeErr(w, http.StatusBadRequest, "repo fehlt")
		return
	}
	if !strings.Contains(full, "/") {
		full = discover.Owner() + "/" + full
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()
	logLine, err := s.runExec.ResetRepo(ctx, full, actor(r))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "Zurücksetzen fehlgeschlagen: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "log": logLine})
}

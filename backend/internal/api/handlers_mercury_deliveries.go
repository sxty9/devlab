// Delivery handlers (S10) — thin adapters over the delivery ledger and package deliver. The
// list is a pure ledger read; rollback runs deliver.Rollback (the counter-booking) over the
// runner's workbench; the deliberate repo reset goes through workbench.ResetToDefault
// (REQ-022.4). This file also composes the periodic delivery maintenance for cmd/devlabd.
package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"devlab/backend/internal/deliver"
	"devlab/backend/internal/deploy"
	"devlab/backend/internal/discover"
	"devlab/backend/internal/executor"
	"devlab/backend/internal/github"
	"devlab/backend/internal/live"
	"devlab/backend/internal/model"
	"devlab/backend/internal/runs"
	"devlab/backend/internal/workbench"
	"devlab/backend/internal/workspace"
)

// runsUser is the Linux account the autonomous chain executes as (workspace owner).
func runsUser() string { return strings.TrimSpace(os.Getenv("DEVLAB_RUNS_USER")) }

// runsTokenUser is the linked account whose token the chain acts with — the OS identity that
// executes and the GitHub identity that pushes are separate concerns.
func runsTokenUser() string {
	if v := strings.TrimSpace(os.Getenv("DEVLAB_RUNS_TOKEN_USER")); v != "" {
		return v
	}
	return runsUser()
}

// runnerToken resolves the chain's GitHub token from the link store.
func (s *Server) runnerToken() (string, error) {
	if s.links == nil {
		return "", errors.New("no GitHub link store configured")
	}
	u := runsTokenUser()
	if u == "" {
		return "", errors.New("no runner account configured")
	}
	return s.links.Token(u)
}

// deliverOps is the production GitHubOps+GitSide of the delivery chain. Tests substitute the
// seam with a fixture.
var deliverOps = func(s *Server, token string) deliver.GitHubOps {
	return runnerGitSide{GitHubOps: deliver.NewGitHub(token), s: s, token: token}
}

// runDeliveriesList returns the delivery ledger as the wire view (REQ-024/F12).
func (s *Server) runDeliveriesList(w http.ResponseWriter, _ *http.Request) {
	if s.deliveries == nil {
		writeJSON(w, http.StatusOK, map[string]any{"deliveries": []model.Delivery{}})
		return
	}
	all, err := s.deliveries.All()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "Could not read the delivery ledger")
		return
	}
	reversed := map[string]bool{}
	for _, d := range all {
		if d.ReversalOf != "" {
			reversed[d.ReversalOf] = true
		}
	}
	out := make([]model.Delivery, 0, len(all))
	for _, d := range all {
		out = append(out, model.Delivery{
			ID: d.ID, Repo: d.Repo, Branch: d.Branch,
			FromCommit: d.FromCommit, ToCommit: d.ToCommit,
			PRNumber: d.PRNumber, PRURL: d.PRURL,
			CreatedAt: d.CreatedAt, MergedAt: d.MergedAt, ReversalOf: d.ReversalOf,
			Stage: deliveryStage(d, reversed[d.ID]),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"deliveries": out})
}

// deliveryStage derives the ledger lifecycle stage for the deliveries view (F12):
// open → merged/closed; a counter-booked delivery reads "reverted" whatever it was before.
func deliveryStage(d runs.Delivery, hasReversal bool) string {
	switch {
	case hasReversal || (d.ClosedAt != nil && strings.HasPrefix(d.ClosedReason, "rolled back by ")):
		return "reverted"
	case d.MergedAt != nil:
		return "merged"
	case d.ClosedAt != nil:
		return "closed"
	default:
		return "open"
	}
}

// runDeliveryRollback counter-books one delivery (REQ-025) via deliver.Rollback.
func (s *Server) runDeliveryRollback(w http.ResponseWriter, r *http.Request) {
	if s.deliveries == nil {
		writeErr(w, http.StatusServiceUnavailable, "The delivery ledger is not available")
		return
	}
	id := r.PathValue("id")
	token, err := s.runnerToken()
	if err != nil {
		writeErr(w, http.StatusBadRequest, "Rollback needs the linked runner account: "+err.Error())
		return
	}
	actor := model.Actor{User: userFrom(r).Username}
	out, err := deliver.Rollback(r.Context(), deliverOps(s, token), s.deliveries, s.runs, id, actor)
	if err != nil {
		if errors.Is(err, deliver.ErrDeliveryNotFound) {
			writeErr(w, http.StatusNotFound, "No such delivery")
			return
		}
		writeErr(w, http.StatusBadGateway, "Rollback failed: "+err.Error())
		return
	}

	// A reversal PR runs the same chain — track it for the auto-merge window.
	if out.ReversalPR != nil && out.ReversalDeliveryID != "" && s.runPRs != nil {
		if d, ok, _ := s.deliveries.ByID(out.ReversalDeliveryID); ok {
			now := time.Now().UTC()
			_ = s.runPRs.Add(runs.PendingPR{
				Repo: d.Repo, Number: out.ReversalPR.Number, URL: out.ReversalPR.URL,
				DeliveryID: d.ID, CreatedAt: now, MergeBy: now.Add(s.automergeWindow()),
			})
		}
	}
	// A closed original may have completed its execution (B-8).
	if d, ok, _ := s.deliveries.ByID(id); ok && d.ExecutionID != "" {
		_ = deliver.SettleExecution(s.deliveries, s.results, d.ExecutionID)
	}
	s.publish(live.TopicDeliveries)
	if out.ConflictTodoID != "" {
		s.publish(live.TopicRuns)
	}
	resp := map[string]any{"outcome": rollbackOutcomeText(out)}
	if out.ConflictTodoID != "" {
		resp["todoId"] = out.ConflictTodoID
	}
	writeJSON(w, http.StatusOK, resp)
}

// rollbackOutcomeText renders the outcome as the one human-readable line the surface shows.
func rollbackOutcomeText(out deliver.RollbackOutcome) string {
	parts := []string{}
	if out.ConflictTodoID != "" {
		parts = append(parts, "conflict — a todo for the manual counter-booking was raised")
	}
	if out.ClosedPR != nil {
		parts = append(parts, fmt.Sprintf("closed PR #%d with the justification", out.ClosedPR.Number))
	}
	if out.ReversalPR != nil {
		parts = append(parts, fmt.Sprintf("opened reversal PR #%d through the delivery chain", out.ReversalPR.Number))
	}
	if out.Detail != "" {
		parts = append(parts, out.Detail)
	}
	if len(parts) == 0 {
		parts = append(parts, "rolled back")
	}
	return strings.Join(parts, "; ")
}

// automergeWindow resolves the auto-merge window from the service settings (runtime wins),
// with the conservative default when no settings store is wired.
func (s *Server) automergeWindow() time.Duration {
	if s.settings != nil {
		if set, err := s.settings.Get(); err == nil && set.AutomergeWindow > 0 {
			return set.AutomergeWindow
		}
	}
	return 720 * time.Hour
}

// runRepoReset is the DELIBERATE dev reset (REQ-022.4) — only ever behind the UI confirmation;
// it calls workbench.ResetToDefault (the ONE reset, never on the automated path).
func (s *Server) runRepoReset(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Repo string `json:"repo"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	repoID := strings.TrimSpace(body.Repo)
	if repoID == "" {
		writeErr(w, http.StatusBadRequest, "Which repository should be reset?")
		return
	}
	token, err := s.runnerToken()
	if err != nil {
		writeErr(w, http.StatusBadRequest, "The reset needs the linked runner account: "+err.Error())
		return
	}
	user := runsUser()
	if user == "" {
		writeErr(w, http.StatusBadRequest, "No runner account configured")
		return
	}
	full, ok := s.resolveRunnerRepo(r.Context(), token, repoID)
	if !ok {
		writeErr(w, http.StatusNotFound, errRepoNotFound)
		return
	}
	actor := model.Actor{User: userFrom(r).Username}
	bench, _, unlock, err := s.runnerBench(r.Context(), user, token, repoID, full)
	if err != nil {
		writeErr(w, http.StatusBadGateway, "Could not prepare the runner workspace: "+err.Error())
		return
	}
	defer unlock()
	if err := bench.ResetToDefault(r.Context(), actor); err != nil {
		writeErr(w, http.StatusBadGateway, "Reset failed: "+err.Error())
		return
	}
	s.publish(live.TopicDeliveries)
	writeJSON(w, http.StatusOK, map[string]any{"reset": true, "repo": repoID})
}

// resolveRunnerRepo maps a repo id onto its full name within the instance's repo set.
func (s *Server) resolveRunnerRepo(ctx context.Context, token, repoID string) (string, bool) {
	repos, err := discover.ReposForUser(ctx, runsTokenUser(), token)
	if err != nil {
		return "", false
	}
	for _, r := range repos {
		if r.ID == repoID || r.Name == repoID {
			return r.FullName, true
		}
	}
	return "", false
}

// runnerBench prepares the runner's workspace of one repo and opens a workbench on it.
func (s *Server) runnerBench(ctx context.Context, user, token, repoID, full string) (*workbench.Bench, string, func(), error) {
	if s.workspaces == nil {
		return nil, "", nil, errors.New("no workspace manager")
	}
	if _, err := s.workspaces.Ensure(ctx, user, repoID, full, token, true); err != nil {
		return nil, "", nil, err
	}
	unlock, err := s.workspaces.Lock(user, repoID)
	if err != nil {
		return nil, "", nil, err
	}
	wt, err := s.workspaces.Path(user, repoID)
	if err != nil {
		unlock()
		return nil, "", nil, err
	}
	ex := workspace.Executor{User: user, PerUser: true}
	bench, err := workbench.New(&ex, wt)
	if err != nil {
		unlock()
		return nil, "", nil, err
	}
	return bench, wt, unlock, nil
}

// ── The production git side of a rollback ────────────────────────────────────────────────

// runnerGitSide implements deliver.GitSide over the runner's workbench: prepare (fold in,
// never reset), one counter-booking commit via RevertRange, snapshot + atomic push.
type runnerGitSide struct {
	deliver.GitHubOps
	s     *Server
	token string
}

func (g runnerGitSide) CounterBook(ctx context.Context, d runs.Delivery, reversalBranch string) (deliver.CounterBookResult, error) {
	res := deliver.CounterBookResult{}
	user := runsUser()
	if user == "" {
		return res, errors.New("no runner account configured (DEVLAB_RUNS_USER)")
	}
	repoID := repoShort(d.Repo)
	bench, wt, unlock, err := g.s.runnerBench(ctx, user, g.token, repoID, d.Repo)
	if err != nil {
		return res, err
	}
	defer unlock()

	defaultBranch, err := github.DefaultBranch(ctx, g.token, d.Repo)
	if err != nil || defaultBranch == "" {
		return res, fmt.Errorf("default branch: %v", err)
	}
	res.DefaultBranch = defaultBranch

	prep, err := bench.Prepare(ctx)
	if err != nil {
		return res, fmt.Errorf("workbench: %w", err)
	}
	if prep.Conflicted {
		return res, fmt.Errorf("the workbench is in a named conflict (%s) — resolve it before rolling back", strings.Join(prep.ConflictFiles, ", "))
	}

	ex := workspace.Executor{User: user, PerUser: true}
	before, err := ex.RevParse(ctx, wt, "HEAD")
	if err != nil {
		return res, fmt.Errorf("workbench tip: %w", err)
	}
	res.Before, res.After = before, before

	ghLogin, ghID := g.runnerIdentity(ctx)
	msg := "Counter-booking of delivery " + d.ID + " (" + shortCommit(d.FromCommit) + ".." + shortCommit(d.ToCommit) + ")"
	conflicted, changed, err := ex.RevertRange(ctx, wt, d.FromCommit, d.ToCommit, ghLogin, ghID, msg)
	if err != nil {
		return res, fmt.Errorf("counter-booking: %w", err)
	}
	if conflicted {
		res.Conflicted = true
		return res, nil
	}
	if !changed {
		return res, nil
	}
	after, err := ex.RevParse(ctx, wt, "HEAD")
	if err != nil {
		return res, fmt.Errorf("workbench tip: %w", err)
	}
	res.Changed, res.After = true, after

	pushRefs := []string{workbench.Branch}
	if reversalBranch != "" {
		if err := ex.BranchAt(ctx, wt, reversalBranch, "HEAD"); err != nil {
			return res, fmt.Errorf("reversal branch: %w", err)
		}
		pushRefs = append(pushRefs, reversalBranch)
	}
	if _, err := ex.PushRefs(ctx, wt, g.token, false, pushRefs...); err != nil {
		return res, fmt.Errorf("push: %w", err)
	}
	return res, nil
}

// RedeliverDev re-ships the dev state after a counter-booking (REQ-025.5): the counter-booked
// workbench is what must be running, so the rollback rides the SAME delivery composition the chain
// rides — detect, build as the user, install-only as root, honest gate. There is no second deploy
// path; a repository that is not a service (or is excluded) is honestly nothing to re-deliver, and
// any other failure is NAMED in the rollback outcome, never silent.
func (g runnerGitSide) RedeliverDev(ctx context.Context, repo string) error {
	deps := g.s.ChainDeps(ChainHooks{})
	defer deps.Close()
	return redeliverOutcome(deps.Deploy().DeliverDev(ctx, repoShort(repo)))
}

// redeliverOutcome reads one re-delivery honestly: a repository that is not a service (excluded,
// library, template) has NOTHING to re-deliver and says so by succeeding; an install that did not
// come up is a named failure, never a quiet success. The self repo is the one exception to the port
// probe — its proof is the handover plus the next boot (B-2).
func redeliverOutcome(out executor.DeployOutcome, err error) error {
	switch {
	case errors.Is(err, deploy.ErrNotAService), errors.Is(err, deploy.ErrExcluded), errors.Is(err, deploy.ErrTemplateRepo):
		return nil
	case err != nil:
		return err
	case out.Self:
		return nil
	case !out.Running:
		return errors.New("installed, but the service is not running: " + nonEmptyDetail(out.Detail))
	default:
		return nil
	}
}

// nonEmptyDetail keeps a failure from reading as an empty sentence.
func nonEmptyDetail(detail string) string {
	if strings.TrimSpace(detail) == "" {
		return "the honest running gate reported no proof"
	}
	return detail
}

// runnerIdentity resolves the commit identity of the runner's linked account (best-effort —
// RevertRange falls back to the neutral identity).
func (g runnerGitSide) runnerIdentity(ctx context.Context) (string, int64) {
	v, err := github.GetViewer(ctx, g.token)
	if err != nil {
		return "", 0
	}
	return v.Login, v.ID
}

func repoShort(fullName string) string {
	if i := strings.LastIndex(fullName, "/"); i >= 0 {
		return fullName[i+1:]
	}
	return fullName
}

func shortCommit(sha string) string {
	if len(sha) > 8 {
		return sha[:8]
	}
	return sha
}

// ── Periodic delivery maintenance (composed for cmd/devlabd) ─────────────────────────────

// MaintainDeliveries is the PR-maintenance tick (deliver.Maintain) over the production
// GitHubOps — handed to the scheduler as its MaintainFunc.
func (s *Server) MaintainDeliveries(ctx context.Context) error {
	token, err := s.runnerToken()
	if err != nil {
		return err
	}
	var pub live.Publisher
	if s.broker != nil {
		pub = s.broker
	}
	return deliver.Maintain(ctx, deliverOps(s, token), s.runPRs, s.deliveries, s.results, s.runNotices, pub)
}

// VerifyRepoProtection is the recurring protection check (REQ-033.7) over every repo of the
// instance set — restores deviations and records them.
func (s *Server) VerifyRepoProtection(ctx context.Context) ([]deliver.ProtectionReport, error) {
	token, err := s.runnerToken()
	if err != nil {
		return nil, err
	}
	repos, err := discover.ReposForUser(ctx, runsTokenUser(), token)
	if err != nil {
		return nil, err
	}
	full := make([]string, 0, len(repos))
	for _, r := range repos {
		full = append(full, r.FullName)
	}
	return deliver.VerifyProtection(ctx, deliverOps(s, token), full, s.runNotices)
}

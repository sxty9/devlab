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

// runnerRepoSet reads the instance repo set as GitHub reports it for the runner's account. A
// package variable so the resolution below can be driven from a fixture set in a test while the
// resolution itself — which ids it accepts — stays under test. Production never reassigns it.
var runnerRepoSet = discover.ReposForUser

// openRunnerBench is the workbench seam of this surface. Production opens the runner's real
// per-user workspace, whose git runs AS that Linux user through the pinned sudo wrapper; a test
// substitutes a hermetic local bench, so what a handler passes down (repo id and full name) is
// provable without crossing a sudo boundary.
var openRunnerBench = (*Server).runnerBench

// deliveryWire is the ledger's wire view: the shared delivery contract PLUS the execution the
// delivery arose from. The link is what lets a surface state which executions still hold an open
// delivery by READING the two pools — the very rule the server applies (runs.ExecutionCompleted,
// B-8) — instead of re-deriving a chain stage from its name (B-35). Its proper home is
// model.Delivery; until the shared contract carries it, this projection does.
type deliveryWire struct {
	model.Delivery
	ExecutionID string `json:"executionId,omitempty"`
	// The pull-request maintenance facts of THIS delivery, joined from the pending-PR pool by its
	// ledger id (B-8 / K-5). MergeBy is the instant the auto-merge is due — the deadline the open
	// list names so a todo that only waits for its merge says so and says until when. Blocked (with
	// its reason) is the honest terminal state after the retries are spent (K-5): the delivery that
	// waits for an explicit release, not for the clock. Both are absent when the ledger record has
	// no tracked pull request — already merged, closed, or never opened.
	MergeBy       *time.Time `json:"mergeBy,omitempty"`
	Blocked       bool       `json:"blocked,omitempty"`
	BlockedReason string     `json:"blockedReason,omitempty"`
}

// prFacts is the maintenance state one pending pull request contributes to its delivery's wire
// view: the auto-merge deadline and the honest blockade (K-5).
type prFacts struct {
	mergeBy *time.Time
	blocked bool
	reason  string
}

// runDeliveriesList returns the delivery ledger as the wire view (REQ-024/F12).
func (s *Server) runDeliveriesList(w http.ResponseWriter, _ *http.Request) {
	if s.deliveries == nil {
		writeJSON(w, http.StatusOK, map[string]any{"deliveries": []deliveryWire{}})
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
	// The auto-merge deadline and the K-5 blockade live on the tracked pull request, not on the
	// ledger record — join them by the delivery id the pending PR carries, with the legacy
	// repo+number fallback for records predating that link. This is the ONE place a surface reads
	// "is this delivery blocked, and until when does it wait" from, so no view re-derives it.
	byDelivery := map[string]prFacts{}
	byRepoNum := map[string]prFacts{}
	if s.runPRs != nil {
		if prs, err := s.runPRs.List(); err == nil {
			for _, p := range prs {
				f := prFacts{blocked: p.Blocked, reason: p.BlockedReason}
				if !p.MergeBy.IsZero() {
					mb := p.MergeBy
					f.mergeBy = &mb
				}
				if p.DeliveryID != "" {
					byDelivery[p.DeliveryID] = f
				}
				byRepoNum[fmt.Sprintf("%s#%d", p.Repo, p.Number)] = f
			}
		}
	}
	out := make([]deliveryWire, 0, len(all))
	for _, d := range all {
		wire := deliveryWire{
			Delivery: model.Delivery{
				ID: d.ID, Repo: d.Repo, Branch: d.Branch,
				FromCommit: d.FromCommit, ToCommit: d.ToCommit,
				PRNumber: d.PRNumber, PRURL: d.PRURL,
				CreatedAt: d.CreatedAt, MergedAt: d.MergedAt, ReversalOf: d.ReversalOf,
				Stage: deliveryStage(d, reversed[d.ID]),
			},
			ExecutionID: d.ExecutionID,
		}
		f, ok := byDelivery[d.ID]
		if !ok && d.PRNumber != 0 {
			f, ok = byRepoNum[fmt.Sprintf("%s#%d", d.Repo, d.PRNumber)]
		}
		if ok {
			wire.MergeBy, wire.Blocked, wire.BlockedReason = f.mergeBy, f.blocked, f.reason
		}
		out = append(out, wire)
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
	// The caller names the repository the way the ledger states it — the GitHub FULL name
	// ("owner/repo"), because that is what a delivery record carries and what the surface shows.
	// That name is RESOLVED against the instance set first (which accepts the full name, the short
	// name and the id); only the RESOLVED repository is then reduced to its id, which is what
	// everything below the API works with: the workspace directory name is a single path segment.
	// Resolving before reducing is what keeps a foreign "other-owner/name" a 404 instead of silently
	// becoming this instance's repository of the same short name.
	named := strings.TrimSpace(body.Repo)
	if named == "" {
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
	full, ok := s.resolveRunnerRepo(r.Context(), token, named)
	if !ok {
		writeErr(w, http.StatusNotFound, errRepoNotFound)
		return
	}
	repoID := repoShort(full)
	actor := model.Actor{User: userFrom(r).Username}
	bench, _, unlock, err := openRunnerBench(s, r.Context(), user, token, repoID, full)
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
	// The answer names the repository the way the ledger and the surface do — fully — so the caller
	// reads back the repository it meant, not an id it never sent.
	writeJSON(w, http.StatusOK, map[string]any{"reset": true, "repo": full})
}

// resolveRunnerRepo maps a repo id onto its full name within the instance's repo set. All three
// names a repository is addressed by resolve: its id, its short name and its full name — a caller
// holding a ledger record (which stores the full name) resolves without having to know which form
// this lookup happens to be keyed by.
func (s *Server) resolveRunnerRepo(ctx context.Context, token, repoID string) (string, bool) {
	repos, err := runnerRepoSet(ctx, runsTokenUser(), token)
	if err != nil {
		return "", false
	}
	for _, r := range repos {
		if r.ID == repoID || r.Name == repoID || r.FullName == repoID {
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
	if bench, err = bench.On(workbench.LegacyShared); err != nil {
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

	// A counter-booking works on the branch that carries the delivery, which already exists —
	// so it never cuts one and the base is the default branch.
	prep, err := bench.Prepare(ctx, "", defaultBranch)
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

	pushRefs := []string{workbench.LegacyShared}
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
//
// The runner identity is resolved for the WRITING half only, and a missing one is fatal only there.
// The held pass — unarmed, which is the default AND the configuration the cutover's first start
// runs in (00-cutover.md step 6: no DEVLAB_RUNS_USER, no DEVLAB_RUNS_TOKEN_USER) — makes no foreign
// call at all: it establishes the standstill from DevLab's own pools and reports it. Returning
// early on the missing identity therefore silenced exactly the configuration the standstill report
// exists for, and the operator saw no sign that the maintenance stands still.
func (s *Server) MaintainDeliveries(ctx context.Context) error {
	var ops deliver.GitHubOps
	switch token, err := s.runnerToken(); {
	case err == nil:
		ops = deliverOps(s, token)
	case deliver.MaintainArmed():
		// Armed, the pass WRITES into foreign repositories. Without an identity it must fail by
		// name rather than report a standstill it never actually attempted to end.
		return err
	}
	var pub live.Publisher
	if s.broker != nil {
		pub = s.broker
	}
	return deliver.Maintain(ctx, ops, s.runPRs, s.deliveries, s.results, s.runNotices, pub)
}

// protectionEnforcementArmed reports whether the operator has armed protection WRITES.
//
// The check reaches every repository of the configured organisation, and restoring a deviation PATCHes
// that repository — an effect outside DevLab, on repositories nobody asked about in this session. So it
// is held: unarmed (the default), a pass READS and REPORTS; only DEVLAB_RUNS_PROTECTION_ENFORCE turns
// the findings into writes. REQ-033.7 asks for the deviation to be found and recorded, which the held
// pass does in full; the restoring half is the operator's decision, not the daemon's.
func protectionEnforcementArmed() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("DEVLAB_RUNS_PROTECTION_ENFORCE"))) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

// errProtectionHeld is what the held pass answers instead of writing. It is a refusal of THIS daemon,
// never a GitHub failure, so the caller distinguishes the two.
var errProtectionHeld = errors.New("protection enforcement is not armed (DEVLAB_RUNS_PROTECTION_ENFORCE) — the deviation is reported, not changed")

// heldProtectionOps is the report-only wrapper around the production ops: everything reads through,
// and the ONE writing call answers errProtectionHeld. Wrapping instead of re-deciding keeps a single
// definition of what "satisfied" means (deliver.VerifyProtection stays the only judge).
type heldProtectionOps struct{ deliver.GitHubOps }

func (heldProtectionOps) ProtectDefaultBranch(context.Context, string, string) error {
	return errProtectionHeld
}

// VerifyRepoProtection is the recurring protection check (REQ-033.7) over every repo of the
// instance set: it finds deviations and records them, and restores them only where the operator has
// armed enforcement. The repo set comes through the same seam every other surface resolves it with,
// so this path resolves no second set of its own.
func (s *Server) VerifyRepoProtection(ctx context.Context) ([]deliver.ProtectionReport, error) {
	token, err := s.runnerToken()
	if err != nil {
		return nil, err
	}
	repos, err := runnerRepoSet(ctx, runsTokenUser(), token)
	if err != nil {
		return nil, err
	}
	full := make([]string, 0, len(repos))
	for _, r := range repos {
		full = append(full, r.FullName)
	}

	if protectionEnforcementArmed() {
		return deliver.VerifyProtection(ctx, deliverOps(s, token), full, s.runNotices)
	}

	// Held: no notice store is handed down, because deliver's wording would report a failed
	// RESTORATION. The finding is recorded here in the words that are true — found, not changed. Only
	// the OUTCOME half of deliver's sentence is rewritten; the deviation itself keeps the wording of
	// the ONE place that formats it, so there is no second description of what "satisfied" means.
	reports, err := deliver.VerifyProtection(ctx, heldProtectionOps{deliverOps(s, token)}, full, nil)
	notify, held := s.NoticeFunc(), false
	for i := range reports {
		// A repository whose protection could not be READ is announced here too. The held pass is
		// handed no notice store (deliver's wording would speak of a failed RESTORATION), so
		// without this the one finding that hides a drift would reach no one.
		if strings.HasPrefix(reports[i].Detail, "protection unreadable: ") && notify != nil {
			notify("protection-deviation", reports[i].Repo+": branch protection could not be READ, so whether this repository is protected is UNKNOWN — "+
				strings.TrimPrefix(reports[i].Detail, "protection unreadable: "))
			continue
		}
		if !strings.Contains(reports[i].Detail, errProtectionHeld.Error()) {
			continue
		}
		held = true
		reports[i].OK, reports[i].Restored = false, false
		reports[i].Detail = strings.Replace(reports[i].Detail,
			") but restoring failed: "+errProtectionHeld.Error(),
			") — reported, NOT changed; arm DEVLAB_RUNS_PROTECTION_ENFORCE to have it restored", 1)
		if notify != nil {
			notify("protection-deviation", reports[i].Repo+": "+reports[i].Detail)
		}
	}
	if held && errors.Is(err, errProtectionHeld) {
		err = nil // the hold is this daemon's own decision, not a failure to report upwards
	}
	return reports, err
}

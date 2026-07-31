package it

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"devlab/backend/internal/deliver"
	"devlab/backend/internal/executor"
	"devlab/backend/internal/faultclass"
	"devlab/backend/internal/github"
	"devlab/backend/internal/model"
	"devlab/backend/internal/sched"
	"devlab/backend/internal/workbench"
)

// The construction-fault invariants, driven through the WHOLE composed system and proven at the
// SHIPPED code that has to hold them: the working state machine (workbench), the one delivery path
// (deliver), the one classification point (faultclass) and the motor's skip refusal. Each test names
// the change to the shipped code it would catch — that is the standard the suite is held to: an
// invariant only counts as proven if breaking the implementation breaks a test HERE, not only in the
// package's own unit tests.

// K-1 / REQ-022.1 / REQ-023.1 — committed work survives the next run. The scenario is the one that
// cost the old system 18 strands in a day: a run committed to the working branch and died BEFORE it
// could publish, so the local branch is AHEAD of the remote. The next run must fold the remote in
// and keep the local commits.
//
// Catches: replacing the fold in workbench.Prepare with a reset onto origin/mercury-dev (any form of
// re-pointing the branch at the remote) — the unpublished commit would vanish here.
func TestChainKeepsUnpublishedCommitsOfAnInterruptedRun(t *testing.T) {
	e := newEnv(t, sched.Config{Tick: 20 * time.Millisecond})
	ctx := e.ctx
	gr := e.deps.git("alpha")
	e.boot(ctx)

	// The state an interrupted run leaves: the working branch exists locally AND on the remote, and
	// carries one more commit than the remote knows about.
	mustGit(t, gr.wt, "checkout", "--quiet", "-b", workbench.LegacyShared)
	writeInto(t, gr.wt, "work/published.txt", "secured by the previous run\n")
	mustGit(t, gr.wt, "add", "-A")
	mustGit(t, gr.wt, "commit", "--quiet", "-m", "previous run, published")
	mustGit(t, gr.wt, "push", "--quiet", "origin", workbench.LegacyShared)
	writeInto(t, gr.wt, "work/unpublished.txt", "committed, never pushed — the interrupted run\n")
	mustGit(t, gr.wt, "add", "-A")
	mustGit(t, gr.wt, "commit", "--quiet", "-m", "interrupted before publish")
	unpublishedTip := gitAt(t, gr.wt, "rev-parse", "HEAD")
	if remote := gr.originHead("refs/heads/" + workbench.LegacyShared); remote == unpublishedTip {
		t.Fatalf("the premise failed: the remote already carries the unpublished commit")
	}

	e.addTodo("run_after_crash", "continue after the crash", "alpha")
	code, body := e.post("/api/mercury/runs/run_after_crash/run", map[string]any{})
	if code != http.StatusOK {
		t.Fatalf("POST run: %d %s", code, body)
	}
	var out model.StartOutcome
	mustJSON(t, body, &out)
	e.waitPhase(out.ExecutionID, model.PhaseCompleted)

	// Nothing was reset: both files are in the tree and the unpublished commit is still reachable.
	if !fileIn(gr.wt, "work/unpublished.txt") {
		t.Error("the unpublished commit was LOST — the working branch was reset onto the remote (K-1)")
	}
	if !fileIn(gr.wt, "work/published.txt") {
		t.Error("previously published work disappeared from the working state")
	}
	if !ancestorOf(t, gr.wt, unpublishedTip, "refs/heads/"+workbench.LegacyShared) {
		t.Errorf("commit %s is no longer reachable from %s — it was discarded", unpublishedTip, workbench.LegacyShared)
	}
	// And it is now secured on the remote: publish-after-commit reaches back over what the crashed
	// run could not push.
	if !ancestorOfIn(t, gr.origin, unpublishedTip, "refs/heads/"+workbench.LegacyShared) {
		t.Error("the recovered commit was never published — an abort would lose it again")
	}
}

// K-6 / REQ-019.5 — a second trigger over the SAME work adopts the open pull request instead of
// opening a second one. The observation, the adopted delivery record and the adoption itself are all
// derived here; nothing in the fixture decides it.
//
// Catches: removing the head search (adoption) from deliver.OpenOrAdoptPR — the repository would end
// up with two open pull requests for one delivery.
func TestSecondTriggerAdoptsTheOpenPullRequestInsteadOfDuplicating(t *testing.T) {
	e := newEnv(t, sched.Config{Tick: 20 * time.Millisecond})
	ctx := e.ctx
	e.deps.repo("alpha")
	e.addTodo("run_twice", "delivered once, triggered twice", "alpha")
	e.boot(ctx)

	code, body := e.post("/api/mercury/runs/run_twice/run", map[string]any{})
	if code != http.StatusOK {
		t.Fatalf("POST first run: %d %s", code, body)
	}
	var first model.StartOutcome
	mustJSON(t, body, &first)
	e.waitPhase(first.ExecutionID, model.PhaseCompleted)

	open := e.deps.openPRs()
	if len(open) != 1 {
		t.Fatalf("after the first delivery there are %d open pull requests, want 1", len(open))
	}
	firstPR := open[0].number

	// The same run again: the ledger's open delivery is the repo truth, so the chain delivers what
	// is there instead of creating anything new.
	code, body = e.post("/api/mercury/runs/run_twice/run", map[string]any{})
	if code != http.StatusOK {
		t.Fatalf("POST second run: %d %s", code, body)
	}
	var second model.StartOutcome
	mustJSON(t, body, &second)
	if !second.Started {
		t.Fatalf("the second trigger did not start: %+v", second)
	}
	if second.TaskStates["alpha"] != model.TaskImplementedUndelivered {
		t.Errorf("the gate observed %q, want implemented-undelivered", second.TaskStates["alpha"])
	}
	e.waitPhase(second.ExecutionID, model.PhaseCompleted)

	openAfter := e.deps.openPRs()
	if len(openAfter) != 1 || openAfter[0].number != firstPR {
		t.Fatalf("the second trigger produced %d open pull requests (%+v) — the open one must be ADOPTED, never duplicated",
			len(openAfter), openAfter)
	}
	stages := e.stagesOf(second.ExecutionID, "alpha")
	prStage := stageNamed(t, stages, model.StagePullRequest)
	if prStage.State != model.StepExecuted {
		t.Errorf("the pull-request stage is %s (%s), want executed", prStage.State, prStage.Reason)
	}
	if !strings.Contains(prStage.Log, "adopted") {
		t.Errorf("the stage does not report the adoption: %q", prStage.Log)
	}
	dels, err := e.deliveries.All()
	if err != nil {
		t.Fatal(err)
	}
	if len(dels) != 1 {
		t.Errorf("the ledger holds %d deliveries, want 1 (the adopted one)", len(dels))
	}
}

// K-5 / REQ-032.1 — a PERMANENT fault gets exactly one attempt with a named end. The 422 arrives as
// the typed *github.StatusError the production client raises, so the ONE classification point really
// decides.
//
// Catches: classifying a definitive client status as transient, or retrying without asking the
// classifier — the attempt counter would climb past one.
func TestPermanentDeliveryFaultIsAttemptedExactlyOnce(t *testing.T) {
	shrinkBackoff(t)
	e := newEnv(t, sched.Config{Tick: 20 * time.Millisecond})
	ctx := e.ctx
	e.deps.repo("alpha")
	e.deps.failGitHub("CreatePullRequest", &github.StatusError{Status: 422, Msg: "validation failed"})
	e.addTodo("run_permanent", "hits a permanent delivery fault", "alpha")
	e.boot(ctx)

	code, body := e.post("/api/mercury/runs/run_permanent/run", map[string]any{})
	if code != http.StatusOK {
		t.Fatalf("POST run: %d %s", code, body)
	}
	var out model.StartOutcome
	mustJSON(t, body, &out)
	if end := e.waitEnded(out.ExecutionID); end.Phase != model.PhaseFailed {
		t.Errorf("the execution ended %q, want failed", end.Phase)
	}

	if calls := e.deps.githubCalls("CreatePullRequest"); calls != 1 {
		t.Errorf("the permanent fault was attempted %d times, want exactly 1 (K-5)", calls)
	}
	stages := e.stagesOf(out.ExecutionID, "alpha")
	assertAllTerminal(t, "alpha", stages)
	prStage := stageNamed(t, stages, model.StagePullRequest)
	if prStage.State != model.StepFailed {
		t.Errorf("the pull-request stage is %s, want failed", prStage.State)
	}
	if !strings.Contains(prStage.Reason, "422") {
		t.Errorf("the failure does not name what happened: %q", prStage.Reason)
	}
	// The work itself is preserved: the stage before it published the commits.
	if stageNamed(t, stages, model.StagePublish).State != model.StepExecuted {
		t.Error("a failed pull request must not cost the published work")
	}
}

// K-5 / REQ-032.3 — the delivery maintenance blocks a pull request HONESTLY on a permanent fault:
// one attempt, then a record with reason, time and attempts that waits for an explicit resume.
//
// Catches: a maintenance loop that keeps retrying a permanent fault (the old 20-second forever), and
// a blockade without a reason.
func TestMaintenanceBlocksAPermanentMergeFaultWithoutRetrying(t *testing.T) {
	// The maintenance writes into foreign repositories and is HELD until the operator arms it
	// (deliver.EnvMaintainEnforce). This test drives the writing half, so it arms it explicitly.
	t.Setenv(deliver.EnvMaintainEnforce, "1")
	shrinkBackoff(t)
	e := newEnv(t, sched.Config{Tick: time.Hour}) // the ticks are driven by hand here
	ctx := e.ctx
	e.mergeNow()
	e.deps.repo("alpha")
	e.addTodo("run_blocked", "its merge is refused for good", "alpha")
	e.boot(ctx)

	code, body := e.post("/api/mercury/runs/run_blocked/run", map[string]any{})
	if code != http.StatusOK {
		t.Fatalf("POST run: %d %s", code, body)
	}
	var out model.StartOutcome
	mustJSON(t, body, &out)
	e.waitPhase(out.ExecutionID, model.PhaseCompleted)

	e.deps.failGitHub("MergePullRequest", &github.StatusError{Status: 403, Msg: "merging is not permitted"})
	// The FIRST pass hits the refusal and must report it; the three that follow must leave the
	// blocked record alone — a blockade waits for an explicit resume, never for another silent try.
	if err := e.maintain(ctx); err == nil {
		t.Fatal("the refused merge was reported as success")
	}
	for i := 0; i < 3; i++ {
		if err := e.maintain(ctx); err != nil {
			t.Fatalf("pass %d re-attempted the blocked record instead of leaving it blocked: %v", i+2, err)
		}
	}
	if calls := e.deps.githubCalls("MergePullRequest"); calls != 1 {
		t.Errorf("the permanent merge fault was attempted %d times over four passes, want exactly 1", calls)
	}
	tracked, err := e.prs.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(tracked) != 1 {
		t.Fatalf("tracked pull requests: %d, want 1 (the blocked one stays visible)", len(tracked))
	}
	rec := tracked[0]
	if !rec.Blocked {
		t.Fatalf("the record is not blocked: %+v", rec)
	}
	if rec.BlockedReason == "" || rec.BlockedAt.IsZero() {
		t.Errorf("the blockade names neither reason nor time: %+v", rec)
	}
	if !strings.Contains(rec.BlockedReason, "403") {
		t.Errorf("the blockade does not name the cause: %q", rec.BlockedReason)
	}
	notices, err := e.notices.List()
	if err != nil {
		t.Fatal(err)
	}
	told := false
	for _, n := range notices {
		if n.Kind == "delivery-blocked" {
			told = true
		}
	}
	if !told {
		t.Errorf("the blockade reached nobody: %+v", notices)
	}
}

// K-4 / REQ-031.3 — a skip must rest on an ATTESTED repository property. The negative case is a
// detector verdict WITHOUT evidence: the motor must record the stage as failed, never as a quiet
// not-applicable and never as success. The positive case is a real library repository, which attests
// its own kind.
//
// Catches: accepting an evidence-free skip in the motor (recording not-applicable instead of failed),
// and dropping the evidence requirement from the stage's applicability probe.
func TestEvidenceFreeSkipIsRefusedAndAttestedSkipIsAccepted(t *testing.T) {
	e := newEnv(t, sched.Config{Tick: 20 * time.Millisecond})
	ctx := e.ctx

	// A detector that answers "not to deliver" without saying why — the shape a broken or guessing
	// detection has.
	mute := e.deps.repo("mute")
	mute.detection = &executor.Detection{Kind: "excluded", Evidence: ""}
	// A repository whose LAYOUT attests it: no deliverable daemon anywhere.
	e.deps.libraryRepo("libonly")

	e.addTodo("run_mute", "skip without evidence", "mute")
	e.addTodo("run_lib", "skip with evidence", "libonly")
	e.boot(ctx)

	code, body := e.post("/api/mercury/runs/run_mute/run", map[string]any{})
	if code != http.StatusOK {
		t.Fatalf("POST mute: %d %s", code, body)
	}
	var muteOut model.StartOutcome
	mustJSON(t, body, &muteOut)
	// Wait for ANY end: the point of this test is WHICH end, so the assertion names it rather than
	// timing out on the expected one.
	if end := e.waitEnded(muteOut.ExecutionID); end.Phase != model.PhaseFailed {
		t.Errorf("the execution ended %q, want failed — a refused skip must not let the repository pass",
			end.Phase)
	}

	muteStages := e.stagesOf(muteOut.ExecutionID, "mute")
	assertAllTerminal(t, "mute", muteStages)
	dev := stageNamed(t, muteStages, model.StageDeliverDev)
	if dev.State != model.StepFailed {
		t.Errorf("an evidence-free skip was recorded as %s — it must be refused as failed (K-4)", dev.State)
	}
	if !strings.Contains(dev.Reason, "evidence") {
		t.Errorf("the refusal does not name what is missing: %q", dev.Reason)
	}
	res, ok, err := e.results.Get(muteOut.ExecutionID)
	if err != nil || !ok {
		t.Fatalf("result: ok=%v err=%v", ok, err)
	}
	for _, rp := range res.Repos {
		if rp.Repo == "mute" && rp.Succeeded {
			t.Error("a repository whose skip was refused counts as succeeded — a skip must never read green")
		}
	}

	code, body = e.post("/api/mercury/runs/run_lib/run", map[string]any{})
	if code != http.StatusOK {
		t.Fatalf("POST library: %d %s", code, body)
	}
	var libOut model.StartOutcome
	mustJSON(t, body, &libOut)
	e.waitPhase(libOut.ExecutionID, model.PhaseCompleted)

	libStages := e.stagesOf(libOut.ExecutionID, "libonly")
	assertAllTerminal(t, "libonly", libStages)
	libDev := stageNamed(t, libStages, model.StageDeliverDev)
	if libDev.State != model.StepNotApplicable {
		t.Errorf("the library repository's deliver-dev is %s, want not-applicable (%s)", libDev.State, libDev.Reason)
	}
	if libDev.Evidence == "" || !strings.Contains(libDev.Evidence, "library") {
		t.Errorf("the skip is not attested by the repository: %q", libDev.Evidence)
	}
	// not-applicable lets the chain run on (REQ-030.4): the delivery still happens.
	if pr := stageNamed(t, libStages, model.StagePullRequest); pr.State != model.StepExecuted {
		t.Errorf("a not-applicable stage stopped the chain: pull-request is %s (%s)", pr.State, pr.Reason)
	}
}

// REQ-024 / REQ-026.4 / REQ-039.2 — the delivery loop closes: the maintenance merges the tracked
// pull request with the ONE permitted method, the SAME place prunes the delivery branch, the ledger
// and the result carry the merge, and the work is then observable IN the default branch (so the next
// observation says "delivered" instead of implementing it again).
//
// Catches: dropping the auto-merge registration (nothing would ever be merged), merging with another
// method, pruning somewhere else, and a task-state derivation that does not see the arrival.
func TestDeliveryLoopMergesPrunesAndBecomesObservableInDefault(t *testing.T) {
	// The maintenance writes into foreign repositories and is HELD until the operator arms it
	// (deliver.EnvMaintainEnforce). This test drives the writing half, so it arms it explicitly.
	t.Setenv(deliver.EnvMaintainEnforce, "1")
	e := newEnv(t, sched.Config{Tick: time.Hour})
	ctx := e.ctx
	e.mergeNow()
	gr := e.deps.git("alpha")
	e.addTodo("run_loop", "delivered and merged", "alpha")
	e.boot(ctx)

	code, body := e.post("/api/mercury/runs/run_loop/run", map[string]any{})
	if code != http.StatusOK {
		t.Fatalf("POST run: %d %s", code, body)
	}
	var out model.StartOutcome
	mustJSON(t, body, &out)
	e.waitPhase(out.ExecutionID, model.PhaseCompleted)

	dels, err := e.deliveries.All()
	if err != nil || len(dels) != 1 {
		t.Fatalf("delivery ledger: %+v (%v)", dels, err)
	}
	branch := dels[0].Branch
	if gr.originHead("refs/heads/"+branch) == "" {
		t.Fatalf("the delivery branch %s was never pushed", branch)
	}
	// The tracked pull request is what makes the merge happen at all.
	tracked, err := e.prs.List()
	if err != nil || len(tracked) != 1 {
		t.Fatalf("tracked pull requests: %+v (%v) — an unregistered delivery is never merged", tracked, err)
	}

	if err := e.maintain(ctx); err != nil {
		t.Fatalf("maintenance: %v", err)
	}

	// Merged, mirrored, pruned — and the workbench branch is NEVER pruned.
	after, err := e.deliveries.All()
	if err != nil {
		t.Fatal(err)
	}
	if after[0].MergedAt == nil {
		t.Error("the ledger does not carry the merge")
	}
	if gr.originHead("refs/heads/"+branch) != "" {
		t.Errorf("the delivery branch %s survived its merge — merge and prune are the same place", branch)
	}
	if gr.originHead("refs/heads/"+workbench.LegacyShared) == "" {
		t.Errorf("the working branch %s was pruned — it never falls under the prune", workbench.LegacyShared)
	}
	if left, err := e.prs.List(); err != nil || len(left) != 0 {
		t.Errorf("the merged pull request is still tracked: %+v (%v)", left, err)
	}
	res, ok, err := e.results.Get(out.ExecutionID)
	if err != nil || !ok {
		t.Fatalf("result: ok=%v err=%v", ok, err)
	}
	if res.MergedAt == nil {
		t.Error("the execution was not completed by the merge (it would never enter the history)")
	}

	// And the arrival is OBSERVABLE: a fresh run at the same repository is told "delivered".
	f, err := e.deps.Preflight(ctx, "alpha", e.addTodo("run_probe", "asks what is there", "alpha"))
	if err != nil {
		t.Fatalf("observation: %v", err)
	}
	if f.State != model.TaskNotImplemented {
		t.Errorf("a NEW run at the merged repository observes %q, want not-implemented "+
			"(its own task is not there; the merged work is no longer undelivered): %v", f.State, f.Evidence)
	}
	contained, err := e.deps.ContainedInDefault(ctx, "alpha", dels[0].ToCommit)
	if err != nil {
		t.Fatalf("containment: %v", err)
	}
	if !contained {
		t.Error("the delivered commit is not contained in the default branch after the merge")
	}
}

// REQ-033.6 — creating a repository and protecting it is ONE pass: the protection carries the full
// contract (pull requests required, the delivery-origin status required, no force-push, no deletion,
// merge method exactly "merge").
//
// Catches: creating a repository without protecting it, and weakening any single condition.
func TestRepoCreationProtectsInTheSamePass(t *testing.T) {
	e := newEnv(t, sched.Config{Tick: 20 * time.Millisecond})
	ctx := e.ctx
	run := e.addTodo("run_create", "brand new repository", "freshrepo")
	run.Targets[0].Create = true
	if err := e.runStore.Put(run); err != nil {
		t.Fatal(err)
	}
	e.boot(ctx)

	code, body := e.post("/api/mercury/runs/run_create/run", map[string]any{})
	if code != http.StatusOK {
		t.Fatalf("POST run: %d %s", code, body)
	}
	var out model.StartOutcome
	mustJSON(t, body, &out)
	e.waitPhase(out.ExecutionID, model.PhaseCompleted)

	p, ok := e.deps.protectionOf("freshrepo")
	if !ok {
		t.Fatal("the created repository carries no protection — the creation must count as failed then")
	}
	if !p.RequirePR || p.AllowForcePush || p.AllowDeletion {
		t.Errorf("protection is incomplete: %+v", p)
	}
	if len(p.MergeMethods) != 1 || p.MergeMethods[0] != "merge" {
		t.Errorf("merge methods = %v, want exactly [merge]", p.MergeMethods)
	}
	found := false
	for _, c := range p.RequiredStatus {
		if c == "devlab/delivery-origin" {
			found = true
		}
	}
	if !found {
		t.Errorf("the delivery-origin status is not required: %v", p.RequiredStatus)
	}
}

// ── helpers ──────────────────────────────────────────────────────────────────────────────────

// stageNamed picks one stage out of a recorded pipeline.
func stageNamed(t *testing.T, stages []model.StageView, want model.Stage) model.StageView {
	t.Helper()
	for _, sv := range stages {
		if sv.Stage == want {
			return sv
		}
	}
	t.Fatalf("no %s stage recorded (%s)", want, stageNames(stages))
	return model.StageView{}
}

// shrinkBackoff shortens the growing retry intervals for the duration of one test (the two values
// are package variables for exactly this purpose). It makes "how many attempts" the measurement
// instead of "how long the test waits": a classification that wrongly reads a permanent fault as
// transient then shows up as four attempts rather than as a timeout.
func shrinkBackoff(t *testing.T) {
	t.Helper()
	base, max := faultclass.BaseDelay, faultclass.MaxDelay
	faultclass.BaseDelay, faultclass.MaxDelay = time.Millisecond, 5*time.Millisecond
	t.Cleanup(func() { faultclass.BaseDelay, faultclass.MaxDelay = base, max })
}

// mustGit runs a git command in the fixture repository, failing the test on error.
func mustGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	if err := gitQuiet(dir, args...); err != nil {
		t.Fatal(err)
	}
}

// gitAt runs a git command and returns its trimmed output.
func gitAt(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := gitOutput(dir, args...)
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(out)
}

// writeInto writes a file into a working tree, creating its directory.
func writeInto(t *testing.T, wt, rel, body string) {
	t.Helper()
	p := filepath.Join(wt, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// fileIn reports whether a working tree carries a file.
func fileIn(wt, rel string) bool {
	_, err := os.Stat(filepath.Join(wt, rel))
	return err == nil
}

// ancestorOf reports whether sha is reachable from ref in a working tree.
func ancestorOf(t *testing.T, wt, sha, ref string) bool {
	t.Helper()
	return gitQuiet(wt, "merge-base", "--is-ancestor", sha, ref) == nil
}

// ancestorOfIn reports whether sha is reachable from ref in a bare repository.
func ancestorOfIn(t *testing.T, gitDir, sha, ref string) bool {
	t.Helper()
	return gitQuiet("", "--git-dir="+gitDir, "merge-base", "--is-ancestor", sha, ref) == nil
}

package executor

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"devlab/backend/internal/execstate"
	"devlab/backend/internal/faultclass"
	"devlab/backend/internal/model"
	"devlab/backend/internal/preflight"
	"devlab/backend/internal/runs"
)

// REQ-027: the chain is exactly the five stages, as data, in the one order — no mode, no
// second form.
func TestChainIsTheFiveStages(t *testing.T) {
	chain := Chain()
	want := model.ChainStages()
	if len(chain) != len(want) {
		t.Fatalf("chain has %d stages, want %d", len(chain), len(want))
	}
	for i, spec := range chain {
		if spec.Name != want[i] {
			t.Fatalf("stage %d = %s, want %s", i, spec.Name, want[i])
		}
		if spec.Applies == nil || spec.Run == nil {
			t.Fatalf("stage %s incomplete", spec.Name)
		}
	}
}

// The happy path: all five stages executed, the repo succeeds, the PR is opened through the
// ONE PR path with the delivery span, and the prompt carried snapshot + preamble + preflight.
func TestExecuteHappyPath(t *testing.T) {
	stubConstitution(t, nil)
	deps := newFakeDeps("org/app")
	sink := newFakeSink()
	req := mkRequest(model.KindTodo, "org/app")

	if err := Execute(context.Background(), deps, req, sink); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	rp, ok := sink.done("org/app")
	if !ok {
		t.Fatalf("no RepoDone")
	}
	if !rp.Done || !rp.Succeeded {
		t.Fatalf("pipeline not succeeded: %+v", rp)
	}
	if len(rp.Stages) != 5 {
		t.Fatalf("want 5 stages, got %d", len(rp.Stages))
	}
	for _, sv := range rp.Stages {
		if sv.State != model.StepExecuted {
			t.Fatalf("stage %s = %s, want executed (%s)", sv.Stage, sv.State, sv.Reason)
		}
	}

	// The ONE PR path was used, with span and execution identity.
	if len(deps.deliver.openCalls) != 1 {
		t.Fatalf("OpenOrAdoptPR calls = %d", len(deps.deliver.openCalls))
	}
	in := deps.deliver.openCalls[0]
	if in.DeliveryID == "" || in.ExecutionID != "exec_test" || in.FromCommit == "" || in.ToCommit == "" {
		t.Fatalf("PR input incomplete: %+v", in)
	}
	if !strings.HasPrefix(in.Head, "fix/") {
		t.Fatalf("delivery branch %q not in fix/<desc> form", in.Head)
	}
	// The workbench was published during implement (publish-after-commit) AND by the publish
	// stage.
	if deps.benches["org/app"].publishes < 2 {
		t.Fatalf("publish-after-commit missing: %d publishes", deps.benches["org/app"].publishes)
	}
	// The pull-request stage carries the PR link.
	sv, _ := sink.terminal("org/app", model.StagePullRequest)
	if sv.Link == "" {
		t.Fatalf("PR stage has no link")
	}
}

// REQ-021.1 / REQ-020.2 / REQ-007.3 / REQ-002.1: the prompt is snapshot + addenda.
func TestPromptCarriesAllParts(t *testing.T) {
	stubConstitution(t, nil)
	deps := newFakeDeps("org/app")
	deps.findings["org/app"] = preflight.Finding{
		State:    model.TaskNotImplemented,
		Evidence: []string{"workbench equals the default branch @d00dfeed"},
	}
	deps.attsFn = func(ctx context.Context, repo string, atts []runs.AttachmentRef) (string, func() error, error) {
		return "- .mercury/attachments/mock.png — UI mock\n", func() error { return nil }, nil
	}
	sink := newFakeSink()
	req := mkRequest(model.KindTodo, "org/app")
	req.Run.Attachments = []runs.AttachmentRef{{ID: "att_1", Filename: "mock.png"}}

	if err := Execute(context.Background(), deps, req, sink); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(deps.agentCalls) != 1 {
		t.Fatalf("agent calls = %d", len(deps.agentCalls))
	}
	prompt := deps.agentCalls[0].prompt
	for _, want := range []string{
		"Division of labor",                       // REQ-021.1 preamble …
		"never end with a question",               // … no questions
		"three-part report",                       // … three-part report
		"SNAPSHOT-CONSTITUTION-V7",                // REQ-002.1: the composed snapshot, verbatim
		"Preflight — observed state",              // REQ-020.2 heading
		"State: not-implemented",                  // … the derived state
		"workbench equals the default branch",     // … its evidence
		".mercury/attachments/mock.png — UI mock", // REQ-007.3: the attachment manifest
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt misses %q:\n%s", want, prompt)
		}
	}
	// The preamble precedes the snapshot; the addenda follow it (B-7).
	if strings.Index(prompt, "Division of labor") > strings.Index(prompt, "SNAPSHOT-CONSTITUTION-V7") {
		t.Fatalf("preamble not first")
	}
	if strings.Index(prompt, "SNAPSHOT-CONSTITUTION-V7") > strings.Index(prompt, "Preflight — observed state") {
		t.Fatalf("finding not after the snapshot")
	}
}

// K-4/REQ-030: a failed stage names its reason; the following stages are not-executed and the
// repo never counts as success.
func TestFailedStageMakesFollowersNotExecuted(t *testing.T) {
	stubConstitution(t, nil)
	deps := newFakeDeps("org/app")
	deps.deploy.deliverErr = func(int) error { return errors.New("delivery not yet set up: no unit installed for this service") }
	sink := newFakeSink()

	err := Execute(context.Background(), deps, mkRequest(model.KindTodo, "org/app"), sink)
	var rf *ReposFailedError
	if !errors.As(err, &rf) || rf.Failed != 1 {
		t.Fatalf("want ReposFailedError with one failure, got %v", err)
	}

	rp, _ := sink.done("org/app")
	if rp.Succeeded {
		t.Fatalf("failed repo counted as success")
	}
	states := map[model.Stage]model.StageView{}
	for _, sv := range rp.Stages {
		states[sv.Stage] = sv
	}
	if states[model.StagePreflight].State != model.StepExecuted || states[model.StageImplement].State != model.StepExecuted {
		t.Fatalf("early stages wrong: %+v", rp.Stages)
	}
	dd := states[model.StageDeliverDev]
	if dd.State != model.StepFailed || !strings.Contains(dd.Reason, "delivery not yet set up") {
		t.Fatalf("deliver-dev not honestly failed: %+v", dd)
	}
	for _, st := range []model.Stage{model.StagePublish, model.StagePullRequest} {
		sv := states[st]
		if sv.State != model.StepNotExecuted {
			t.Fatalf("%s = %s, want not-executed", st, sv.State)
		}
		if !strings.Contains(sv.Reason, "deliver-dev") {
			t.Fatalf("%s does not name the failed stage: %q", st, sv.Reason)
		}
	}
	// Exactly one attempt for the permanent-class deploy failure (REQ-032.1).
	if deps.deploy.deliverCalls != 1 {
		t.Fatalf("deploy retried: %d calls", deps.deploy.deliverCalls)
	}

	// K-4/REQ-031: implemented without dev delivery ⇒ the alarm notice arises.
	found := false
	for _, n := range sink.notices {
		if n.Kind == "delivery-alarm" && n.Repo == "org/app" && strings.Contains(n.Text, "implemented without dev delivery") {
			found = true
		}
	}
	if !found {
		t.Fatalf("no delivery alarm notice: %+v", sink.notices)
	}
}

// REQ-031.3: a skip without attested repo evidence is refused — never silent, never green.
func TestSkipWithoutEvidenceRefused(t *testing.T) {
	stubConstitution(t, nil)
	deps := newFakeDeps("org/lib")
	deps.deploy.det = Detection{Kind: "library", Evidence: ""} // a claim with NO evidence
	sink := newFakeSink()

	err := Execute(context.Background(), deps, mkRequest(model.KindTodo, "org/lib"), sink)
	var rf *ReposFailedError
	if !errors.As(err, &rf) {
		t.Fatalf("refused skip must fail the repo, got %v", err)
	}
	sv, ok := sink.terminal("org/lib", model.StageDeliverDev)
	if !ok || sv.State != model.StepFailed || !strings.Contains(sv.Reason, "without attested repo evidence") {
		t.Fatalf("skip not refused: %+v", sv)
	}
}

// REQ-030.3/REQ-031.3: an evidenced not-applicable lets the chain continue and never blocks
// success.
func TestNotApplicableWithEvidenceContinues(t *testing.T) {
	stubConstitution(t, nil)
	deps := newFakeDeps("org/lib")
	deps.deploy.det = Detection{Kind: "library", Evidence: "no service entrypoint (cmd/<id>d) and no service CLI"}
	sink := newFakeSink()

	if err := Execute(context.Background(), deps, mkRequest(model.KindTodo, "org/lib"), sink); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	rp, _ := sink.done("org/lib")
	if !rp.Succeeded {
		t.Fatalf("evidenced n/a broke success: %+v", rp.Stages)
	}
	sv, _ := sink.terminal("org/lib", model.StageDeliverDev)
	if sv.State != model.StepNotApplicable || sv.Evidence == "" || sv.Reason == "" {
		t.Fatalf("deliver-dev not evidenced n/a: %+v", sv)
	}
	if sv2, _ := sink.terminal("org/lib", model.StagePullRequest); sv2.State != model.StepExecuted {
		t.Fatalf("chain did not continue past n/a: %+v", sv2)
	}
}

// K-3/REQ-020.3: implemented-undelivered creates NOTHING new — the agent is not invoked; only
// the remaining path (deliver, publish, PR) is walked, adopting the recorded delivery.
func TestImplementedUndeliveredWalksRestPathOnly(t *testing.T) {
	stubConstitution(t, nil)
	deps := newFakeDeps("org/app")
	deps.findings["org/app"] = preflight.Finding{
		State:    model.TaskImplementedUndelivered,
		Evidence: []string{"open delivery dlv_9 (branch fix/x-abc, aaaa..bbbb) recorded in the ledger"},
		OpenDelivery: &runs.Delivery{
			ID: "dlv_9", Repo: "org/app", Branch: "fix/x-abc",
			FromCommit: "aaaa11112222", ToCommit: "bbbb33334444",
		},
	}
	sink := newFakeSink()

	if err := Execute(context.Background(), deps, mkRequest(model.KindTodo, "org/app"), sink); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(deps.agentCalls) != 0 {
		t.Fatalf("agent invoked on the rest path: %d calls", len(deps.agentCalls))
	}
	rp, _ := sink.done("org/app")
	if !rp.Succeeded {
		t.Fatalf("rest path failed: %+v", rp.Stages)
	}
	if rp.TaskState != model.TaskImplementedUndelivered {
		t.Fatalf("task state lost: %s", rp.TaskState)
	}
	// The recorded delivery is ADOPTED: same id, same branch, same span (K-6 — no duplicate).
	if len(deps.deliver.openCalls) != 1 {
		t.Fatalf("OpenOrAdoptPR calls = %d", len(deps.deliver.openCalls))
	}
	in := deps.deliver.openCalls[0]
	if in.DeliveryID != "dlv_9" || in.Head != "fix/x-abc" || in.FromCommit != "aaaa11112222" || in.ToCommit != "bbbb33334444" {
		t.Fatalf("recorded delivery not adopted: %+v", in)
	}
}

// K-3: a delivered task produces only evidenced not-applicable stages — nothing runs, nothing
// is created, and the pipeline still counts as an honest success.
func TestDeliveredIsAllNotApplicable(t *testing.T) {
	stubConstitution(t, nil)
	deps := newFakeDeps("org/app")
	deps.findings["org/app"] = preflight.Finding{
		State:    model.TaskDelivered,
		Evidence: []string{"delivery dlv_5 merged at 2026-07-27T10:00:00Z; workbench not ahead of the default branch"},
	}
	sink := newFakeSink()

	if err := Execute(context.Background(), deps, mkRequest(model.KindTodo, "org/app"), sink); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(deps.agentCalls) != 0 || len(deps.deliver.openCalls) != 0 || deps.deploy.deliverCalls != 0 {
		t.Fatalf("delivered task caused effects: agent=%d pr=%d deploy=%d",
			len(deps.agentCalls), len(deps.deliver.openCalls), deps.deploy.deliverCalls)
	}
	rp, _ := sink.done("org/app")
	if !rp.Succeeded {
		t.Fatalf("delivered pipeline not successful: %+v", rp.Stages)
	}
	for _, sv := range rp.Stages[1:] { // preflight itself executed
		if sv.State != model.StepNotApplicable || sv.Evidence == "" {
			t.Fatalf("stage %s = %s (evidence %q), want evidenced n/a", sv.Stage, sv.State, sv.Evidence)
		}
	}
}

// REQ-032: a transient fault backs off with growing intervals and ends honestly blocked
// (reason, time, attempts) — and a blocked repo does NOT hold the other repos up.
func TestTransientBlocksAndIsolates(t *testing.T) {
	stubConstitution(t, nil)
	shrinkRetries(t)
	deps := newFakeDeps("org/bad", "org/good")
	calls := map[string]int{}
	deps.deliver.openErr = func(call int) error {
		in := deps.deliver.openCalls[len(deps.deliver.openCalls)-1]
		calls[in.Repo]++
		if in.Repo == "org/bad" {
			return errTransient
		}
		return nil
	}
	sink := newFakeSink()

	err := Execute(context.Background(), deps, mkRequest(model.KindTodo, "org/bad", "org/good"), sink)
	var rf *ReposFailedError
	if !errors.As(err, &rf) || rf.Blocked != 1 || rf.Succeeded != 1 {
		t.Fatalf("want 1 blocked + 1 succeeded, got %v", err)
	}

	bad, _ := sink.done("org/bad")
	if bad.Block == nil {
		t.Fatalf("blocked repo carries no backoff")
	}
	if bad.Block.Attempts != transientMaxAttempts || bad.Block.Reason == "" || bad.Block.LastAt.IsZero() {
		t.Fatalf("backoff record incomplete: %+v", bad.Block)
	}
	sv, _ := sink.terminal("org/bad", model.StagePullRequest)
	if sv.State != model.StepFailed || !strings.Contains(sv.Reason, "blocked after") {
		t.Fatalf("blocked stage not honest: %+v", sv)
	}
	// Isolation: the good repo went all the way.
	good, _ := sink.done("org/good")
	if !good.Succeeded {
		t.Fatalf("blocked repo held the other up: %+v", good.Stages)
	}
	if calls["org/bad"] != transientMaxAttempts {
		t.Fatalf("attempts = %d, want %d", calls["org/bad"], transientMaxAttempts)
	}
}

// K-5/REQ-032.1: a permanent fault (422) gets exactly ONE attempt with a named end.
func TestPermanentFaultOneAttempt(t *testing.T) {
	stubConstitution(t, nil)
	shrinkRetries(t)
	deps := newFakeDeps("org/app")
	deps.deliver.openErr = func(int) error { return fmt.Errorf("create pr: %w", statusErr(422)) }
	sink := newFakeSink()

	err := Execute(context.Background(), deps, mkRequest(model.KindTodo, "org/app"), sink)
	var rf *ReposFailedError
	if !errors.As(err, &rf) || rf.Failed != 1 || rf.Blocked != 0 {
		t.Fatalf("want one plain failure, got %v", err)
	}
	if len(deps.deliver.openCalls) != 1 {
		t.Fatalf("422 retried: %d calls", len(deps.deliver.openCalls))
	}
	sv, _ := sink.terminal("org/app", model.StagePullRequest)
	if sv.State != model.StepFailed || !strings.Contains(sv.Reason, "422") {
		t.Fatalf("permanent end not named: %+v", sv)
	}
}

// REQ-032.2: a goal already reached counts as success.
func TestAlreadySatisfiedIsSuccess(t *testing.T) {
	stubConstitution(t, nil)
	deps := newFakeDeps("org/app")
	deps.deploy.deliverErr = func(int) error {
		return fmt.Errorf("unit already running at the delivered state: %w", errSatisfiedForTest())
	}
	sink := newFakeSink()

	if err := Execute(context.Background(), deps, mkRequest(model.KindTodo, "org/app"), sink); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	sv, _ := sink.terminal("org/app", model.StageDeliverDev)
	if sv.State != model.StepExecuted || !strings.Contains(sv.Log, "already") {
		t.Fatalf("satisfied not success: %+v", sv)
	}
}

// REQ-010.4/F6/F11: a time-budget overrun is a NAMED failure with value + achieved — no raw
// technical error — and the transcript survives the kill; untouched repos are honestly marked.
func TestBudgetOverrunHonest(t *testing.T) {
	stubConstitution(t, nil)
	deps := newFakeDeps("org/slow", "org/never")
	deps.agentFn = func(ctx context.Context, repo, prompt string, tn runs.ResolvedTuning, sess AgentSession) (AgentStream, error) {
		return &blockingStream{ctx: ctx, pre: []byte(script(
			assistantEvent("m1", "started working before the kill", 50, 10),
		))}, nil
	}
	sink := newFakeSink()
	req := mkRequest(model.KindTodo, "org/slow", "org/never")
	req.Budget.Budget = 60 * time.Millisecond

	err := Execute(context.Background(), deps, req, sink)
	var be *BudgetExceededError
	if !errors.As(err, &be) {
		t.Fatalf("want BudgetExceededError, got %v", err)
	}
	if be.Budget != 60*time.Millisecond || !strings.Contains(be.Error(), "60ms") {
		t.Fatalf("budget value not named: %v", be)
	}

	// The implement stage failed with the named overrun — not a bare "context deadline
	// exceeded" — and its log is the surviving transcript (F11).
	sv, ok := sink.terminal("org/slow", model.StageImplement)
	if !ok {
		t.Fatalf("implement never terminalized")
	}
	if sv.State != model.StepFailed || !strings.Contains(sv.Reason, "time budget exceeded (60ms)") {
		t.Fatalf("overrun not named: %+v", sv)
	}
	if strings.Contains(sv.Reason, "context deadline exceeded") {
		t.Fatalf("technical error text leaked: %q", sv.Reason)
	}
	if !strings.Contains(sv.Log, "started working before the kill") {
		t.Fatalf("transcript lost on kill: %q", sv.Log)
	}
	if len(sink.transcripts["org/slow"]) == 0 {
		t.Fatalf("no transcript lines reached the sink before the kill")
	}
	// Committed work stayed published (K-1).
	if deps.benches["org/slow"].publishes == 0 {
		t.Fatalf("agent commits not published on overrun")
	}
	// The untouched repo is marked honestly — every stage not-executed with the named budget.
	rp, ok := sink.done("org/never")
	if !ok {
		t.Fatalf("untouched repo unmarked")
	}
	for _, sv := range rp.Stages {
		if sv.State != model.StepNotExecuted || !strings.Contains(sv.Reason, "time budget exceeded") {
			t.Fatalf("untouched repo not honest: %+v", sv)
		}
	}
}

// REQ-017/F7: token consumption climbs LIVE in the sink while the agent works, and the final
// figures carry the monetary equivalent. There is no cap anywhere.
func TestUsageClimbsLive(t *testing.T) {
	stubConstitution(t, nil)
	deps := newFakeDeps("org/app")
	deps.agentFn = func(ctx context.Context, repo, prompt string, tn runs.ResolvedTuning, sess AgentSession) (AgentStream, error) {
		return &scriptStream{r: strings.NewReader(script(
			assistantEvent("m1", "step one", 100, 10),
			toolEvent("m2", "Bash", "go test ./...", 150, 30),
			resultEventLine("all done", false, 400, 80, 1.25),
		))}, nil
	}
	sink := newFakeSink()

	if err := Execute(context.Background(), deps, mkRequest(model.KindTodo, "org/app"), sink); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(sink.usages) < 3 {
		t.Fatalf("usage did not stream: %d updates", len(sink.usages))
	}
	// Climbing: a later live update carries more input tokens than the first.
	if !(sink.usages[1].InputTokens > sink.usages[0].InputTokens) {
		t.Fatalf("usage not climbing: %+v", sink.usages[:2])
	}
	last := sink.usages[len(sink.usages)-1]
	if last.InputTokens != 400 || last.OutputTokens != 80 || last.CostUSD != 1.25 {
		t.Fatalf("final usage not authoritative: %+v", last)
	}
	// The usage ledger got the run sample (cross-cutting 5).
	if len(deps.usageSamples) != 1 || deps.usageSamples[0].Source != "run" || deps.usageSamples[0].In != 400 {
		t.Fatalf("AI usage not recorded: %+v", deps.usageSamples)
	}
	// The transcript compacted the tool line.
	joined := strings.Join(sink.transcripts["org/app"], "\n")
	if !strings.Contains(joined, "→ Bash go test ./...") {
		t.Fatalf("tool line missing from transcript: %s", joined)
	}
}

// REQ-016: a detected usage limit pauses ALL executions through the injected hook, persists
// the continuation, and reports the pause — never a failure, never a loss of committed work.
func TestUsageLimitPausesCollectively(t *testing.T) {
	stubConstitution(t, nil)
	deps := newFakeDeps("org/app")
	deps.agentFn = func(ctx context.Context, repo, prompt string, tn runs.ResolvedTuning, sess AgentSession) (AgentStream, error) {
		return &scriptStream{
			r: strings.NewReader(script(
				assistantEvent("m1", "working", 10, 5),
				`{"type":"result","subtype":"error","is_error":true,"result":"Claude AI usage limit reached|1785276000","usage":{"input_tokens":10,"output_tokens":5}}`,
			)),
		}, nil
	}
	sink := newFakeSink()

	err := Execute(context.Background(), deps, mkRequest(model.KindTodo, "org/app"), sink)
	if !errors.Is(err, ErrPausedUsageLimit) {
		t.Fatalf("want ErrPausedUsageLimit, got %v", err)
	}
	if len(deps.pauses) != 1 {
		t.Fatalf("collective pause hook not invoked: %d", len(deps.pauses))
	}
	if deps.pauses[0].notBefore.IsZero() {
		t.Fatalf("reset instant lost: %+v", deps.pauses[0])
	}
	if want := time.Unix(1785276000, 0).UTC(); !deps.pauses[0].notBefore.Equal(want) {
		t.Fatalf("notBefore = %v, want %v", deps.pauses[0].notBefore, want)
	}
	if len(sink.conts) != 1 || sink.conts[0].Repo != "org/app" || sink.conts[0].Stage != model.StageImplement {
		t.Fatalf("continuation not persisted: %+v", sink.conts)
	}
	// Committed work survived (published before pausing).
	if deps.benches["org/app"].publishes == 0 {
		t.Fatalf("work not published before the pause")
	}
	// The implement stage was NOT terminalized — the execution resumes right there.
	if sv, ok := sink.terminal("org/app", model.StageImplement); ok {
		t.Fatalf("implement terminalized despite pause: %+v", sv)
	}
}

// An interrupt (cancel) records the spot and returns the cancellation — resume re-enters.
func TestInterruptPersistsContinuation(t *testing.T) {
	stubConstitution(t, nil)
	deps := newFakeDeps("org/app")
	ctx, cancel := context.WithCancel(context.Background())
	deps.agentFn = func(actx context.Context, repo, prompt string, tn runs.ResolvedTuning, sess AgentSession) (AgentStream, error) {
		cancel() // the service is asked to stop while the agent runs
		return &blockingStream{ctx: actx, pre: []byte(script(assistantEvent("m1", "hi", 5, 1)))}, nil
	}
	sink := newFakeSink()

	err := Execute(ctx, deps, mkRequest(model.KindTodo, "org/app"), sink)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("want context.Canceled, got %v", err)
	}
	if len(sink.conts) != 1 || sink.conts[0].Stage != model.StageImplement {
		t.Fatalf("continuation not persisted: %+v", sink.conts)
	}
	sv, ok := sink.terminal("org/app", model.StageImplement)
	if !ok || sv.State != model.StepFailed || !strings.Contains(sv.Reason, "interrupted") {
		t.Fatalf("interrupt not recorded honestly: %+v", sv)
	}
}

// A continuation whose stage is implement resumes the agent session (the named session passes through)
// instead of taking the rest path — half-done work is finished, not delivered half-way.
func TestResumeAtImplementResumesAgent(t *testing.T) {
	stubConstitution(t, nil)
	deps := newFakeDeps("org/app")
	deps.findings["org/app"] = preflight.Finding{
		State:    model.TaskImplementedUndelivered,
		Evidence: []string{"workbench mercury-dev is ahead of the default branch @d00dfeed; no open delivery recorded"},
	}
	sink := newFakeSink()
	req := mkRequest(model.KindTodo, "org/app")
	req.Doc.Continuation = &model.ContinuationView{Repo: "org/app", Stage: model.StageImplement}

	if err := Execute(context.Background(), deps, req, sink); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(deps.agentCalls) != 1 {
		t.Fatalf("agent not resumed: %d calls", len(deps.agentCalls))
	}
	if !deps.agentCalls[0].sess.Resume || deps.agentCalls[0].sess.Key != "exec_test" {
		t.Fatalf("resume token lost: %+v", deps.agentCalls[0])
	}
}

// A continuation beyond implement re-enters idempotently: preflight re-derives, implement
// takes the rest path (no agent), the remaining stages re-run their idempotent effects.
func TestResumeBeyondImplementTakesRestPath(t *testing.T) {
	stubConstitution(t, nil)
	deps := newFakeDeps("org/app")
	deps.findings["org/app"] = preflight.Finding{
		State:    model.TaskImplementedUndelivered,
		Evidence: []string{"open delivery dlv_7 recorded in the ledger"},
		OpenDelivery: &runs.Delivery{
			ID: "dlv_7", Repo: "org/app", Branch: "fix/y-def", FromCommit: "aaaa11112222", ToCommit: "bbbb33334444",
		},
	}
	sink := newFakeSink()
	req := mkRequest(model.KindTodo, "org/app")
	req.Doc.Continuation = &model.ContinuationView{Repo: "org/app", Stage: model.StagePublish}

	if err := Execute(context.Background(), deps, req, sink); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(deps.agentCalls) != 0 {
		t.Fatalf("agent re-ran on resume beyond implement")
	}
	if len(deps.deliver.openCalls) != 1 || deps.deliver.openCalls[0].DeliveryID != "dlv_7" {
		t.Fatalf("recorded delivery not adopted on resume: %+v", deps.deliver.openCalls)
	}
}

// Completed repositories stay completed (REQ-015.2/019.2): a done document row never re-runs.
func TestDoneRepoNeverReruns(t *testing.T) {
	stubConstitution(t, nil)
	deps := newFakeDeps("org/done", "org/todo")
	sink := newFakeSink()
	req := mkRequest(model.KindTodo, "org/done", "org/todo")
	req.Doc.Repos[0].State = execstate.RepoDone

	if err := Execute(context.Background(), deps, req, sink); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if _, ok := sink.done("org/done"); ok {
		t.Fatalf("done repo re-emitted")
	}
	for _, c := range deps.agentCalls {
		if c.repo == "org/done" {
			t.Fatalf("done repo re-ran the agent")
		}
	}
	if _, ok := sink.done("org/todo"); !ok {
		t.Fatalf("pending repo skipped")
	}
}

// REQ-006.2/033.6: a Create target creates the repository WITH its protection in the same
// pass; a protection failure fails the creation (and with it the stage).
func TestCreateTargetSetsProtection(t *testing.T) {
	stubConstitution(t, nil)
	shrinkRetries(t)
	deps := newFakeDeps("org/new")
	deps.gh.defaultErr = statusErr(404) // the repo does not exist yet
	sink := newFakeSink()
	req := mkRequest(model.KindTodo, "org/new")
	req.Run.Targets = []runs.Target{{Repo: "org/new", Create: true}}

	if err := Execute(context.Background(), deps, req, sink); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(deps.gh.createCalls) != 1 || deps.gh.createCalls[0] != "org/new" {
		t.Fatalf("repo not created: %+v", deps.gh.createCalls)
	}
	if len(deps.deliver.protections) != 1 || deps.deliver.protections[0] != "org/new" {
		t.Fatalf("protection not ensured in the same pass: %+v", deps.deliver.protections)
	}
	// A feature branch for a newly planned service (REQ-026 kind rule).
	if in := deps.deliver.openCalls[0]; !strings.HasPrefix(in.Head, "feature/") {
		t.Fatalf("new-service delivery branch %q not feature/", in.Head)
	}

	// Failing protection fails the creation.
	deps2 := newFakeDeps("org/new2")
	deps2.gh.defaultErr = statusErr(404)
	deps2.deliver.protectErr = errors.New("protection API rejected")
	sink2 := newFakeSink()
	req2 := mkRequest(model.KindTodo, "org/new2")
	req2.Run.Targets = []runs.Target{{Repo: "org/new2", Create: true}}
	err := Execute(context.Background(), deps2, req2, sink2)
	var rf *ReposFailedError
	if !errors.As(err, &rf) {
		t.Fatalf("unprotected creation not failed: %v", err)
	}
	sv, _ := sink2.terminal("org/new2", model.StageImplement)
	if sv.State != model.StepFailed || !strings.Contains(sv.Reason, "protection could not be set") {
		t.Fatalf("creation failure not named: %+v", sv)
	}
}

// B-2/B-3: the SELF repo's successful deliver-dev requests the handover restart — as an
// autonomous actor on behalf of the requester; the chain continues normally.
func TestSelfDeliveryRequestsRestart(t *testing.T) {
	stubConstitution(t, nil)
	deps := newFakeDeps("org/devlab")
	deps.deploy.out = DeployOutcome{Installed: true, Running: true, Self: true, Port: 8078}
	sink := newFakeSink()

	if err := Execute(context.Background(), deps, mkRequest(model.KindTodo, "org/devlab"), sink); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(deps.restartBy) != 1 {
		t.Fatalf("restart not requested: %d", len(deps.restartBy))
	}
	if !deps.restartBy[0].Autonomous || deps.restartBy[0].OnBehalfOf != "nova" {
		t.Fatalf("restart actor wrong: %+v", deps.restartBy[0])
	}
	rp, _ := sink.done("org/devlab")
	if !rp.Succeeded {
		t.Fatalf("self delivery chain did not complete: %+v", rp.Stages)
	}
}

// REQ-002.2/5: the CLAUDE.md constitution block rides along in the delivery — and when a run
// finds nothing else, the refresh ALONE is a full delivery (a PR is opened for it).
func TestConstitutionRefreshRidesAlong(t *testing.T) {
	stubConstitution(t, func(doc string) (string, bool) {
		return doc + "\n<!-- refreshed constitution block -->\n", true
	})
	deps := newFakeDeps("org/app")
	deps.benches["org/app"].aheadNow = 0 // the agent contributed nothing …
	deps.benches["org/app"].files["CLAUDE.md"] = "# App\nold block\n"
	sink := newFakeSink()

	if err := Execute(context.Background(), deps, mkRequest(model.KindTodo, "org/app"), sink); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	b := deps.benches["org/app"]
	if _, ok := b.writes["CLAUDE.md"]; !ok {
		t.Fatalf("CLAUDE.md not rewritten")
	}
	foundCommit := false
	for _, m := range b.commitMsgs {
		if strings.Contains(m, "CLAUDE.md constitution reference") {
			foundCommit = true
		}
	}
	if !foundCommit {
		t.Fatalf("refresh not committed: %+v", b.commitMsgs)
	}
	// … yet the refresh alone is a full delivery: a PR was opened.
	if len(deps.deliver.openCalls) != 1 {
		t.Fatalf("refresh-only delivery missing: %d PR calls", len(deps.deliver.openCalls))
	}
}

// When nothing changed at all (no agent contribution, no refresh, no fold), deliver-dev and
// pull-request are evidenced n/a — never green "delivered", never a duplicate PR.
func TestNoChangesMeansNoDelivery(t *testing.T) {
	stubConstitution(t, nil)
	deps := newFakeDeps("org/app")
	deps.benches["org/app"].aheadNow = 0
	sink := newFakeSink()

	if err := Execute(context.Background(), deps, mkRequest(model.KindTodo, "org/app"), sink); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(deps.deliver.openCalls) != 0 {
		t.Fatalf("PR opened without a delivery")
	}
	sv, _ := sink.terminal("org/app", model.StagePullRequest)
	if sv.State != model.StepNotApplicable || sv.Evidence == "" {
		t.Fatalf("no-delivery not evidenced: %+v", sv)
	}
	rp, _ := sink.done("org/app")
	if !rp.Succeeded {
		t.Fatalf("clean no-op run not successful: %+v", rp.Stages)
	}
}

// REQ-028.4: a nonconforming service is a reported code-structure violation — failed, with a
// notice, never a special path.
func TestNonconformingServiceIsViolation(t *testing.T) {
	stubConstitution(t, nil)
	deps := newFakeDeps("org/odd")
	deps.deploy.det = Detection{Kind: "nonconforming", Evidence: "no service CLI, no cmd/<id>d entrypoint"}
	sink := newFakeSink()

	err := Execute(context.Background(), deps, mkRequest(model.KindTodo, "org/odd"), sink)
	var rf *ReposFailedError
	if !errors.As(err, &rf) {
		t.Fatalf("violation not failed: %v", err)
	}
	sv, _ := sink.terminal("org/odd", model.StageDeliverDev)
	if sv.State != model.StepFailed || !strings.Contains(sv.Reason, "code-structure violation") {
		t.Fatalf("violation not named: %+v", sv)
	}
	found := false
	for _, n := range sink.notices {
		if n.Kind == "code-structure-violation" {
			found = true
		}
	}
	if !found {
		t.Fatalf("violation notice missing")
	}
}

// The preflight stage follows the REQ-032 policy at the start: unreachable sources retry with
// growing intervals, then the repo is honestly blocked — never guessed about (K-3).
func TestPreflightUnreachableBlocks(t *testing.T) {
	stubConstitution(t, nil)
	shrinkRetries(t)
	deps := newFakeDeps("org/app")
	deps.findErr = errTransient
	sink := newFakeSink()

	err := Execute(context.Background(), deps, mkRequest(model.KindTodo, "org/app"), sink)
	var rf *ReposFailedError
	if !errors.As(err, &rf) || rf.Blocked != 1 {
		t.Fatalf("want a blocked repo, got %v", err)
	}
	if len(deps.agentCalls) != 0 {
		t.Fatalf("agent ran despite unknown state")
	}
	rp, _ := sink.done("org/app")
	if rp.Block == nil || rp.Block.Attempts != transientMaxAttempts {
		t.Fatalf("preflight block record: %+v", rp.Block)
	}
}

func errSatisfiedForTest() error { return faultclass.ErrSatisfied }

// A resume whose conversation no longer exists opens a FRESH one on the same workbench instead of
// ending the task. This is the shape that killed a live task on 2026-07-30: the agent answered
// "Error: --resume requires a valid session ID or session title" and the run reported the whole
// repository as failed, although its committed work was untouched on the workbench.
func TestAResumeWhoseConversationIsGoneContinuesInAFreshOne(t *testing.T) {
	stubConstitution(t, nil)
	deps := newFakeDeps("org/app")
	deps.findings["org/app"] = preflight.Finding{
		State:    model.TaskImplementedUndelivered,
		Evidence: []string{"workbench mercury-dev is ahead of the default branch @d00dfeed; no open delivery recorded"},
	}
	deps.agentFn = func(ctx context.Context, repo, prompt string, t runs.ResolvedTuning, sess AgentSession) (AgentStream, error) {
		if sess.Resume {
			return &scriptStream{
				r:       strings.NewReader(script(resultEventLine("", true, 0, 0, 0))),
				waitErr: errors.New("Error: --resume requires a valid session ID or session title when used with --print"),
			}, nil
		}
		return &scriptStream{r: strings.NewReader(script(
			assistantEvent("m1", "picking the work back up", 100, 20),
			resultEventLine("done: finished the change", false, 300, 60, 0.42),
		))}, nil
	}
	sink := newFakeSink()
	req := mkRequest(model.KindTodo, "org/app")
	req.Doc.Continuation = &model.ContinuationView{Repo: "org/app", Stage: model.StageImplement}

	if err := Execute(context.Background(), deps, req, sink); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(deps.agentCalls) != 2 {
		t.Fatalf("want the resume AND the fresh conversation, got %d calls", len(deps.agentCalls))
	}
	if !deps.agentCalls[0].sess.Resume || deps.agentCalls[1].sess.Resume {
		t.Fatalf("wrong order: %+v", deps.agentCalls)
	}
	// The implement stage must have SUCCEEDED — the missing conversation is not failed work.
	sv, ok := sink.terminal("org/app", model.StageImplement)
	if !ok || sv.State != model.StepExecuted {
		t.Fatalf("implement is %+v, want executed", sv)
	}
	// And it must be visible in the transcript, not silent.
	sink.mu.Lock()
	tail := strings.Join(sink.transcripts["org/app"], "\n")
	sink.mu.Unlock()
	if !strings.Contains(tail, "gone") {
		t.Fatalf("the fresh start was silent:\n%s", tail)
	}
}

// Only the agent's OWN wording about a missing conversation counts — an ordinary implementation
// failure is never mistaken for it and retried behind the user's back.
func TestAnOrdinaryFailureIsNotMistakenForAMissingConversation(t *testing.T) {
	for _, tc := range []struct {
		name string
		msg  string
		want bool
	}{
		{"the CLI's own refusal", "Error: --resume requires a valid session ID or session title", true},
		{"no conversation found", "No conversation found with session ID abc", true},
		{"a build failure", "exit status 1: go build ./... failed", false},
		{"a failure that merely says session", "the session manager rejected the write", false},
		{"nothing at all", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var werr error
			if tc.msg != "" {
				werr = errors.New(tc.msg)
			}
			if got := lostConversation(werr, nil); got != tc.want {
				t.Fatalf("lostConversation(%q) = %v, want %v", tc.msg, got, tc.want)
			}
		})
	}
}

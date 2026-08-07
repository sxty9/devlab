package executor

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"devlab/backend/internal/model"
	"devlab/backend/internal/runs"
)

// questionScript is an agent run that ends with the machine-read question block.
func questionScript(question, recommendation string) string {
	final := "Looked into it.\n\n" + questionOpen + "\n" + questionQ + "\n" + question +
		"\n" + questionR + "\n" + recommendation + "\n" + questionClose
	return script(
		assistantEvent("m1", "examining the options", 100, 20),
		resultEventLine(final, false, 300, 60, 0.1),
	)
}

// withAutonomy sets the run's autonomy level on a request.
func withAutonomy(req Request, level model.AutonomyLevel) Request {
	req.Run.Autonomy = level
	return req
}

// (a) A run at the most inquisitive level (collaborative) stops at a decision and blocks its repo.
func TestCollaborativeRunHaltsAndBlocksRepo(t *testing.T) {
	shrinkRetries(t)
	deps := newFakeDeps("org/app")
	deps.agentFn = func(ctx context.Context, repo, prompt string, t runs.ResolvedTuning, sess AgentSession) (AgentStream, error) {
		return &scriptStream{r: strings.NewReader(questionScript(
			"Should the pool live in package foo or bar?",
			"Keep it in foo — bar would create an import cycle."))}, nil
	}
	sink := newFakeSink()

	err := Execute(context.Background(), deps, withAutonomy(mkRequest(model.KindTodo, "org/app"), model.AutonomyCollaborative), sink)
	if err == nil {
		t.Fatalf("expected the execution to report a blocked repo")
	}

	// The implement stage failed on the block; the repo is blocked with the awaiting-answer class.
	rp, ok := sink.done("org/app")
	if !ok {
		t.Fatalf("no repo outcome recorded")
	}
	if rp.Block == nil || rp.Block.Class != "awaiting-answer" {
		t.Fatalf("repo should be blocked awaiting an answer, got %+v", rp.Block)
	}
	// The question was recorded on the Blocked surface, carrying its recommendation.
	if len(deps.questions.raised) != 1 {
		t.Fatalf("expected exactly one question raised, got %d", len(deps.questions.raised))
	}
	q := deps.questions.raised[0]
	if !strings.Contains(q.Question, "package foo or bar") {
		t.Fatalf("question text not recorded: %q", q.Question)
	}
	if !strings.Contains(q.Recommendation, "import cycle") {
		t.Fatalf("recommendation not recorded: %q", q.Recommendation)
	}
	if q.ExecutionID != "exec_test" || q.Repo != "org/app" {
		t.Fatalf("question provenance wrong: %+v", q)
	}
	// The continuation is persisted at implement, so the answer resumes the same spot.
	if len(sink.conts) == 0 || sink.conts[len(sink.conts)-1].Stage != model.StageImplement {
		t.Fatalf("expected a continuation at implement, got %+v", sink.conts)
	}
	// No delivery id was minted — the repo is held by the open question, not by a failed delivery.
	if len(deps.deliver.failedCalls) != 0 {
		t.Fatalf("a question hold must not record a failed delivery: %+v", deps.deliver.failedCalls)
	}
}

// (d) A run at the most self-reliant level (autonomous) does not ask, even if the agent emits the
// block: it proceeds to delivery.
func TestAutonomousRunDoesNotAsk(t *testing.T) {
	deps := newFakeDeps("org/app")
	deps.agentFn = func(ctx context.Context, repo, prompt string, t runs.ResolvedTuning, sess AgentSession) (AgentStream, error) {
		return &scriptStream{r: strings.NewReader(questionScript("Foo or bar?", "foo"))}, nil
	}
	sink := newFakeSink()

	err := Execute(context.Background(), deps, withAutonomy(mkRequest(model.KindTodo, "org/app"), model.AutonomyAutonomous), sink)
	if err != nil {
		t.Fatalf("autonomous run should complete, got %v", err)
	}
	if len(deps.questions.raised) != 0 {
		t.Fatalf("autonomous run must not raise a question, got %d", len(deps.questions.raised))
	}
	rp, _ := sink.done("org/app")
	if rp.Block != nil {
		t.Fatalf("autonomous run must not block, got %+v", rp.Block)
	}
	if pr, ok := sink.terminal("org/app", model.StagePullRequest); !ok || pr.State != model.StepExecuted {
		t.Fatalf("autonomous run should have delivered, got %+v", pr)
	}
}

// (b) A second order on the same repo is held while a question is open, naming it as the reason.
func TestSecondOrderHeldOnOpenQuestion(t *testing.T) {
	shrinkRetries(t)
	deps := newFakeDeps("org/app")
	// An open question from another execution already holds org/app.
	deps.questions.items = []runs.Question{{
		ID: "qst_open", RunID: "run_first", RunTitle: "First order", ExecutionID: "exec_first",
		Repo: "org/app", QKind: runs.QuestionDecision, Question: "Which serializer?",
	}}
	sink := newFakeSink()

	// A DIFFERENT execution (exec_test) now runs against the same repo.
	err := Execute(context.Background(), deps, withAutonomy(mkRequest(model.KindTodo, "org/app"), model.AutonomyBalanced), sink)
	if err == nil {
		t.Fatalf("expected the second order to be held")
	}
	rp, _ := sink.done("org/app")
	if rp.Block == nil || rp.Block.Class != "held-on-open-question" {
		t.Fatalf("second order should be held on the open question, got %+v", rp.Block)
	}
	if !strings.Contains(rp.Block.Reason, "Which serializer?") {
		t.Fatalf("hold reason should name the open question: %q", rp.Block.Reason)
	}
	// It never reached the agent — no work was branched past the unanswered question.
	if len(deps.agentCalls) != 0 {
		t.Fatalf("held order must not invoke the agent, got %d calls", len(deps.agentCalls))
	}
}

// (c) The answer resumes the run and continues the SAME conversation with the answer — it does not
// start over.
func TestAnswerResumesSameRun(t *testing.T) {
	shrinkRetries(t)
	deps := newFakeDeps("org/app")

	// First run: the agent asks and the run blocks.
	tb := t
	first := 0
	deps.agentFn = func(ctx context.Context, repo, prompt string, _ runs.ResolvedTuning, sess AgentSession) (AgentStream, error) {
		first++
		if first == 1 {
			return &scriptStream{r: strings.NewReader(questionScript("Foo or bar?", "foo"))}, nil
		}
		// The resume: the prompt must be the ANSWER continuation, not the whole task prompt again.
		if !strings.Contains(prompt, "The user's answer") || !strings.Contains(prompt, "Do not start over") {
			tb.Fatalf("resume prompt should be the answer continuation, got: %q", prompt)
		}
		if !sess.Resume {
			tb.Fatalf("resume must continue the same conversation (session.Resume)")
		}
		return &scriptStream{r: strings.NewReader(script(
			assistantEvent("m2", "applying the answer", 100, 20),
			resultEventLine("done: implemented with foo", false, 300, 60, 0.1),
		))}, nil
	}
	sink := newFakeSink()
	if err := Execute(context.Background(), deps, withAutonomy(mkRequest(model.KindTodo, "org/app"), model.AutonomyBalanced), sink); err == nil {
		t.Fatalf("first run should block")
	}
	if len(deps.questions.raised) != 1 {
		t.Fatalf("expected one question, got %d", len(deps.questions.raised))
	}

	// The user answers.
	qid := deps.questions.raised[0].ID
	deps.questions.answer(qid, "Use foo.")

	// Resume: the same execution document, now carrying the continuation at implement.
	req := withAutonomy(mkRequest(model.KindTodo, "org/app"), model.AutonomyBalanced)
	req.Doc.Continuation = &model.ContinuationView{Repo: "org/app", Stage: model.StageImplement}
	sink2 := newFakeSink()
	if err := Execute(context.Background(), deps, req, sink2); err != nil {
		t.Fatalf("resume should complete, got %v", err)
	}
	// The answered question was consumed (resolved), not re-asked.
	if len(deps.questions.resolve) == 0 || deps.questions.resolve[0] != qid {
		t.Fatalf("the answered question should be marked consumed, got %+v", deps.questions.resolve)
	}
	if pr, ok := sink2.terminal("org/app", model.StagePullRequest); !ok || pr.State != model.StepExecuted {
		t.Fatalf("resumed run should deliver, got %+v", pr)
	}
}

// THE FREED HANDLE (point 5): a drifted root wrapper is presented as an approval question that blocks
// — regardless of autonomy level — and NOTHING is installed. The chain re-measures on resume: only
// once the scripts genuinely match does the install proceed and the question leave the surface.
func TestWrapperDriftRaisesApprovalQuestionAndReVerifies(t *testing.T) {
	shrinkRetries(t)
	deps := newFakeDeps("org/devlab")
	drift := "root wrapper(s) devlab-install differ from the stack tip"
	deps.deploy.wrapperDrift = drift
	// The renewal offers EXACTLY the stack-tip content — one file, one checksum.
	deps.deploy.workingGrants = []runs.WrapperGrant{{Name: "devlab-install", SHA: strings.Repeat("a", 64)}}
	sink := newFakeSink()

	// Even at the autonomous level the wrapper renewal asks — it is a security gate, not a decision.
	if err := Execute(context.Background(), deps, withAutonomy(mkRequest(model.KindTodo, "org/devlab"), model.AutonomyAutonomous), sink); err == nil {
		t.Fatalf("expected the delivery to block on the wrapper drift")
	}
	rp, _ := sink.done("org/devlab")
	if rp.Block == nil || rp.Block.Class != "awaiting-wrapper-approval" {
		t.Fatalf("expected a wrapper-approval block, got %+v", rp.Block)
	}
	if len(deps.questions.raised) != 1 || deps.questions.raised[0].QKind != runs.QuestionWrapperRenewal {
		t.Fatalf("expected a wrapper-renewal question, got %+v", deps.questions.raised)
	}
	// The question pins the stack-tip (file, checksum) set the approval covers.
	if got := deps.questions.raised[0].Wrappers; len(got) != 1 || got[0].Name != "devlab-install" {
		t.Fatalf("the question must carry the stack-tip grant set, got %+v", got)
	}
	if !strings.Contains(deps.questions.raised[0].Detail, "devlab-install") {
		t.Fatalf("the question must show the exact difference, got %q", deps.questions.raised[0].Detail)
	}
	// The implement stage delivered nothing to install-over: the deliver-dev stage failed, so the
	// install never ran on an unapproved drift.
	if dd, ok := sink.terminal("org/devlab", model.StageDeliverDev); !ok || dd.State != model.StepFailed {
		t.Fatalf("deliver-dev should have failed on the drift, got %+v", dd)
	}

	// The user APPROVES (the single-use green light). The write half now installs the approved
	// stack-tip content through the root tool — the drift is gone on the same resume.
	deps.questions.approve(deps.questions.raised[0].ID, "Approved renewing devlab-install.")

	req := withAutonomy(mkRequest(model.KindTodo, "org/devlab"), model.AutonomyAutonomous)
	sink2 := newFakeSink()
	if err := Execute(context.Background(), deps, req, sink2); err != nil {
		t.Fatalf("resume should deliver once the write half renewed the wrappers, got %v", err)
	}
	// The write half was invoked with the approved question.
	if len(deps.deploy.renewed) != 1 || len(deps.deploy.renewed[0].Wrappers) != 1 {
		t.Fatalf("the approved renewal should have been applied through the root tool, got %+v", deps.deploy.renewed)
	}
	if dd, ok := sink2.terminal("org/devlab", model.StageDeliverDev); !ok || dd.State != model.StepExecuted {
		t.Fatalf("deliver-dev should succeed after the renewal, got %+v", dd)
	}
	// The wrapper question was consumed once the renewal took effect.
	if len(deps.questions.resolve) == 0 {
		t.Fatalf("the wrapper question should be resolved after a successful renewal")
	}
}

// A GRANTED APPROVAL OUTLIVES THE EXECUTION THAT ASKED (task test a): the approval belongs to the
// ORDER, so a LATER execution — a restart mints a fresh execution id — redeems it without asking the
// human again. This is the trap the fix closes: the write half used to look the approval up by the
// execution id, so a new execution never read the old approval and re-asked forever.
func TestWrapperApprovalRedeemedByLaterExecution(t *testing.T) {
	shrinkRetries(t)
	deps := newFakeDeps("org/devlab")
	deps.deploy.wrapperDrift = "root wrapper(s) devlab-install differ from the stack tip"
	deps.deploy.workingGrants = []runs.WrapperGrant{{Name: "devlab-install", SHA: strings.Repeat("a", 64)}}

	// exec_test raises the approval question and blocks.
	if err := Execute(context.Background(), deps, withAutonomy(mkRequest(model.KindTodo, "org/devlab"), model.AutonomyAutonomous), newFakeSink()); err == nil {
		t.Fatalf("expected the first execution to block on the wrapper drift")
	}
	if len(deps.questions.raised) != 1 {
		t.Fatalf("expected exactly one wrapper question, got %d", len(deps.questions.raised))
	}
	// The user approves — but the execution that asked (exec_test) has since ENDED.
	deps.questions.approve(deps.questions.raised[0].ID, "Approved renewing devlab-install.")

	// A RESTART: a brand-new execution id, same order, same repo. The approval must still redeem.
	req := withAutonomy(mkRequest(model.KindTodo, "org/devlab"), model.AutonomyAutonomous)
	req.Doc.ID = "exec_restart"
	sink2 := newFakeSink()
	if err := Execute(context.Background(), deps, req, sink2); err != nil {
		t.Fatalf("the restarted execution should redeem the approval and deliver, got %v", err)
	}
	if len(deps.deploy.renewed) != 1 {
		t.Fatalf("the later execution should have redeemed the order's approval, got %+v", deps.deploy.renewed)
	}
	// The human was NOT asked again — no second wrapper question was raised.
	if len(deps.questions.raised) != 1 {
		t.Fatalf("a redeemed approval must not re-ask; raised=%d", len(deps.questions.raised))
	}
	if dd, ok := sink2.terminal("org/devlab", model.StageDeliverDev); !ok || dd.State != model.StepExecuted {
		t.Fatalf("deliver-dev should succeed on the restart, got %+v", dd)
	}
}

// A GRANTED APPROVAL WHOSE CONTENT CHANGED IS NOT REDEEMED (task test b): the checksum binding is the
// protection. When the source content moved after the approval, the write half installs nothing and a
// FRESH question is raised for the new content — the stale approval never carries over silently.
func TestWrapperApprovalNotRedeemedWhenContentChanged(t *testing.T) {
	shrinkRetries(t)
	deps := newFakeDeps("org/devlab")
	deps.deploy.wrapperDrift = "root wrapper(s) devlab-install differ from the stack tip"
	deps.deploy.workingGrants = []runs.WrapperGrant{{Name: "devlab-install", SHA: strings.Repeat("a", 64)}}

	if err := Execute(context.Background(), deps, withAutonomy(mkRequest(model.KindTodo, "org/devlab"), model.AutonomyAutonomous), newFakeSink()); err == nil {
		t.Fatalf("expected the first execution to block on the wrapper drift")
	}
	firstQID := deps.questions.raised[0].ID
	deps.questions.approve(firstQID, "Approved renewing devlab-install.")

	// The source content moved after the approval: the write half refuses (checksum mismatch), and the
	// stack tip now offers a DIFFERENT checksum for the same file.
	deps.deploy.renewErr = fmt.Errorf("renew devlab-install: stack-tip content is now different, not the approved sha — refusing")
	deps.deploy.workingGrants = []runs.WrapperGrant{{Name: "devlab-install", SHA: strings.Repeat("c", 64)}}

	req := withAutonomy(mkRequest(model.KindTodo, "org/devlab"), model.AutonomyAutonomous)
	req.Doc.ID = "exec_restart"
	sink2 := newFakeSink()
	if err := Execute(context.Background(), deps, req, sink2); err == nil {
		t.Fatalf("a changed-content approval must not deliver; it re-asks")
	}
	// Nothing was installed under the stale approval.
	if len(deps.deploy.renewed) != 0 {
		t.Fatalf("no renewal may run when the content no longer matches the approval, got %+v", deps.deploy.renewed)
	}
	// A FRESH question was raised for the new content, and the stale (answered) one was withdrawn.
	if len(deps.questions.raised) != 2 {
		t.Fatalf("a changed-content refusal should raise a fresh question, raised=%d", len(deps.questions.raised))
	}
	fresh := deps.questions.raised[1]
	if len(fresh.Wrappers) != 1 || fresh.Wrappers[0].SHA != strings.Repeat("c", 64) {
		t.Fatalf("the fresh question should pin the NEW content checksum, got %+v", fresh.Wrappers)
	}
	if q, ok := deps.questions.byID(firstQID); !ok || !q.Resolved {
		t.Fatalf("the stale approval should be withdrawn (resolved), got %+v", q)
	}
	if rp, _ := sink2.done("org/devlab"); rp.Block == nil || rp.Block.Class != "awaiting-wrapper-approval" {
		t.Fatalf("the changed-content case should block awaiting a fresh approval, got %+v", rp.Block)
	}
}

// THE RUN ITSELF CHANGED A ROOT SCRIPT (task point 1): the installed scripts already match the
// standard branch, so nothing MERGED can be renewed — but the run's own working branch carries the
// changed script. The chain no longer halts for good; it raises the SAME wrapper-renewal question with
// this run's own content as the source, the user approves, and the write half installs the run's
// version so deliver-dev continues on the same resume.
func TestRunChangedRootScriptRaisesWorkingSourceQuestionAndRenews(t *testing.T) {
	shrinkRetries(t)
	deps := newFakeDeps("org/devlab")
	deps.deploy.wrapperDrift = "installed devlab-install differs from this run's delivering branch"
	// THIS run changed devlab-install on its own delivering branch — the sole reference the renewal offers.
	deps.deploy.workingGrants = []runs.WrapperGrant{{Name: "devlab-install", SHA: strings.Repeat("b", 64),
		Summary: "+7/-2 lines vs the installed script"}}
	sink := newFakeSink()

	// Even at the autonomous level the wrapper renewal asks — root rights are always the human's call.
	if err := Execute(context.Background(), deps, withAutonomy(mkRequest(model.KindTodo, "org/devlab"), model.AutonomyAutonomous), sink); err == nil {
		t.Fatalf("expected the delivery to block on the run's own wrapper change")
	}
	rp, _ := sink.done("org/devlab")
	if rp.Block == nil || rp.Block.Class != "awaiting-wrapper-approval" {
		t.Fatalf("expected a wrapper-approval block (not a dead-end halt), got %+v", rp.Block)
	}
	if len(deps.questions.raised) != 1 || deps.questions.raised[0].QKind != runs.QuestionWrapperRenewal {
		t.Fatalf("expected one wrapper-renewal question, got %+v", deps.questions.raised)
	}
	q := deps.questions.raised[0]
	// The question is sourced from THIS run's working branch and pins its (file, checksum) set.
	if q.WrapperSource != runs.WrapperSourceWorking {
		t.Fatalf("the question must name the working-branch source, got %q", q.WrapperSource)
	}
	if len(q.Wrappers) != 1 || q.Wrappers[0].Name != "devlab-install" || q.Wrappers[0].SHA != strings.Repeat("b", 64) {
		t.Fatalf("the question must carry the working-branch grant set, got %+v", q.Wrappers)
	}
	// It is readable (task point 4): it names the file, summarizes what changes in it (not the full
	// content), and makes clear the run's OWN change is being installed — a human decides without the branch.
	if !strings.Contains(q.Detail, "devlab-install") || !strings.Contains(q.Detail, "+7/-2 lines") ||
		!strings.Contains(strings.ToLower(q.Question), "this run") {
		t.Fatalf("the question must name the file, summarize the change, and name this run's own change, got detail=%q question=%q", q.Detail, q.Question)
	}
	if dd, ok := sink.terminal("org/devlab", model.StageDeliverDev); !ok || dd.State != model.StepFailed {
		t.Fatalf("deliver-dev should have failed on the unapproved change, got %+v", dd)
	}

	// The user APPROVES the run's own content. The write half installs it through the root tool and the
	// drift is gone on the same resume — deliver-dev proceeds.
	deps.questions.approve(q.ID, "Approved installing this run's devlab-install.")
	req := withAutonomy(mkRequest(model.KindTodo, "org/devlab"), model.AutonomyAutonomous)
	sink2 := newFakeSink()
	if err := Execute(context.Background(), deps, req, sink2); err != nil {
		t.Fatalf("resume should deliver once the write half renewed the wrapper, got %v", err)
	}
	if len(deps.deploy.renewed) != 1 || deps.deploy.renewed[0].WrapperSource != runs.WrapperSourceWorking {
		t.Fatalf("the approved renewal should carry the working-branch source, got %+v", deps.deploy.renewed)
	}
	if dd, ok := sink2.terminal("org/devlab", model.StageDeliverDev); !ok || dd.State != model.StepExecuted {
		t.Fatalf("deliver-dev should succeed after the renewal, got %+v", dd)
	}
}

// hasNotice reports whether the sink recorded a notice of the given kind (a disturbance raised instead
// of a question).
func hasNotice(s *fakeSink, kind string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, n := range s.notices {
		if n.Kind == kind {
			return true
		}
	}
	return false
}

// (a) TASK TEST a — TWO CHECKS DEMAND OPPOSITE ⇒ NO QUESTION, A NAMED STÖRUNG. The self-delivery gate
// refused the root wrappers as stale, but the stand it is measured against (this run's delivering
// branch) shows NO difference from the installed scripts: the two checks contradict, so no approval
// could resolve it. The chain names the contradiction as a disturbance and holds — it never hands the
// user a click that resolves nothing (the pendulum's root, BEFUND 1).
func TestWrapperContradictionRaisesDisturbanceNotQuestion(t *testing.T) {
	shrinkRetries(t)
	deps := newFakeDeps("org/devlab")
	deps.deploy.wrapperDrift = "installed root wrappers are stale"
	deps.deploy.workingGrants = nil // the authoritative stand shows NO drift — the two checks contradict
	sink := newFakeSink()

	if err := Execute(context.Background(), deps, withAutonomy(mkRequest(model.KindTodo, "org/devlab"), model.AutonomyAutonomous), sink); err == nil {
		t.Fatalf("expected the delivery to be held on the contradiction")
	}
	// No question was posed — a click could not resolve it.
	if len(deps.questions.raised) != 0 {
		t.Fatalf("a contradiction must not be posed as a question, raised=%d", len(deps.questions.raised))
	}
	// It is a NAMED disturbance, and the repo is held with the contradiction class.
	rp, _ := sink.done("org/devlab")
	if rp.Block == nil || rp.Block.Class != "wrapper-check-contradiction" {
		t.Fatalf("expected a wrapper-check-contradiction hold, got %+v", rp.Block)
	}
	if !hasNotice(sink, questionUnresolvableNotice) {
		t.Fatalf("the contradiction must be recorded as a disturbance notice, got %+v", sink.notices)
	}
}

// (b) TASK TEST b — AN APPROVAL THAT DID NOT TAKE EFFECT IS NOT RE-ASKED. The user approved renewing
// exactly these scripts, but the install physically failed and the same drift persists unchanged. The
// identical question is NOT posed a second time (its answer is presumed wirkungslos, points 3 & 5); the
// chain names the real reason as a disturbance and holds (BEFUND 3).
func TestIneffectiveApprovalRaisesDisturbanceNotSecondQuestion(t *testing.T) {
	shrinkRetries(t)
	deps := newFakeDeps("org/devlab")
	deps.deploy.wrapperDrift = "installed devlab-install differs from this run's delivering branch"
	deps.deploy.workingGrants = []runs.WrapperGrant{{Name: "devlab-install", SHA: strings.Repeat("a", 64)}}

	if err := Execute(context.Background(), deps, withAutonomy(mkRequest(model.KindTodo, "org/devlab"), model.AutonomyAutonomous), newFakeSink()); err == nil {
		t.Fatalf("expected the first execution to block on the wrapper drift")
	}
	if len(deps.questions.raised) != 1 {
		t.Fatalf("expected exactly one wrapper question, got %d", len(deps.questions.raised))
	}
	// The user approves — but the install will fail and the SAME content stays required.
	deps.questions.approve(deps.questions.raised[0].ID, "Approved renewing devlab-install.")
	deps.deploy.renewErr = fmt.Errorf("cannot stage a temp file in /usr/local/sbin: read-only file system")

	req := withAutonomy(mkRequest(model.KindTodo, "org/devlab"), model.AutonomyAutonomous)
	req.Doc.ID = "exec_restart"
	sink2 := newFakeSink()
	if err := Execute(context.Background(), deps, req, sink2); err == nil {
		t.Fatalf("an ineffective approval must hold, not deliver")
	}
	// No second identical question was posed.
	if len(deps.questions.raised) != 1 {
		t.Fatalf("the same question must not be re-asked; raised=%d", len(deps.questions.raised))
	}
	// Nothing was installed, and the real reason is named with the ineffective class.
	if len(deps.deploy.renewed) != 0 {
		t.Fatalf("a failed renewal installs nothing, got %+v", deps.deploy.renewed)
	}
	rp, _ := sink2.done("org/devlab")
	if rp.Block == nil || rp.Block.Class != "wrapper-approval-ineffective" {
		t.Fatalf("expected a wrapper-approval-ineffective hold, got %+v", rp.Block)
	}
	if !hasNotice(sink2, questionUnresolvableNotice) {
		t.Fatalf("the ineffective approval must be recorded as a disturbance, got %+v", sink2.notices)
	}
}

// (d) TASK TEST d — THE NORMAL CASE STANDS: an approval that WILL take effect is asked exactly once,
// installs on the resume, and the delivery proceeds — no disturbance, no repeat.
func TestEffectiveApprovalAskedExactlyOnce(t *testing.T) {
	shrinkRetries(t)
	deps := newFakeDeps("org/devlab")
	deps.deploy.wrapperDrift = "installed devlab-install differs from this run's delivering branch"
	deps.deploy.workingGrants = []runs.WrapperGrant{{Name: "devlab-install", SHA: strings.Repeat("a", 64)}}
	sink := newFakeSink()

	if err := Execute(context.Background(), deps, withAutonomy(mkRequest(model.KindTodo, "org/devlab"), model.AutonomyAutonomous), sink); err == nil {
		t.Fatalf("expected the first execution to block on the drift")
	}
	if len(deps.questions.raised) != 1 {
		t.Fatalf("the question must be asked exactly once, got %d", len(deps.questions.raised))
	}
	if hasNotice(sink, questionUnresolvableNotice) {
		t.Fatalf("the normal case must not raise a disturbance, got %+v", sink.notices)
	}
	deps.questions.approve(deps.questions.raised[0].ID, "Approved.")

	req := withAutonomy(mkRequest(model.KindTodo, "org/devlab"), model.AutonomyAutonomous)
	req.Doc.ID = "exec_2"
	sink2 := newFakeSink()
	if err := Execute(context.Background(), deps, req, sink2); err != nil {
		t.Fatalf("resume should deliver once the renewal takes effect, got %v", err)
	}
	if len(deps.questions.raised) != 1 {
		t.Fatalf("an effective approval must not be re-asked, got %d", len(deps.questions.raised))
	}
	if len(deps.deploy.renewed) != 1 {
		t.Fatalf("the approved renewal should have installed once, got %+v", deps.deploy.renewed)
	}
	if dd, ok := sink2.terminal("org/devlab", model.StageDeliverDev); !ok || dd.State != model.StepExecuted {
		t.Fatalf("deliver-dev should succeed, got %+v", dd)
	}
}

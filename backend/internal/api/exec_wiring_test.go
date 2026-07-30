package api

import (
	"context"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"devlab/backend/internal/deploy"
	"devlab/backend/internal/execstate"
	"devlab/backend/internal/executor"
	"devlab/backend/internal/live"
	"devlab/backend/internal/model"
	"devlab/backend/internal/runs"
	"devlab/backend/internal/sched"
	"devlab/backend/internal/statepath"
)

// execFixture is the recording half of the execution seam over a real state root: real pools, a real
// broker, a real scheduler (so the document sink writes into real documents).
type execFixture struct {
	t     *testing.T
	srv   *Server
	docs  *execstate.Store
	sch   *sched.Scheduler
	paths *statepath.Paths
}

func newExecFixture(t *testing.T) *execFixture {
	t.Helper()
	dir := t.TempDir()
	for _, k := range []string{
		"DEVLAB_MERCURY_RUNS", "DEVLAB_MERCURY_RUNS_HISTORY", "DEVLAB_MERCURY_EXECUTIONS",
		"DEVLAB_MERCURY_RUNS_RESULTS", "DEVLAB_MERCURY_SETTINGS", "DEVLAB_MERCURY_NOTICES",
		"DEVLAB_MERCURY_ATTACHMENTS", "DEVLAB_RUNS_USER", "DEVLAB_RUNS_TOKEN_USER",
	} {
		t.Setenv(k, "")
	}
	paths := &statepath.Paths{Root: filepath.Join(dir, "state")}
	docs, err := execstate.Open(paths)
	if err != nil {
		t.Fatal(err)
	}
	rs := runs.NewStore(paths)
	res := runs.NewResultStore(paths)
	set := runs.NewSettingsStore(paths, runs.Settings{MaxConcurrency: 2})
	scheduler := sched.New(sched.Config{Tick: time.Hour}, docs, rs, res, set, nil,
		func(ctx context.Context, _ execstate.Doc, _ runs.Run) error { return nil }, nil, nil)

	srv := &Server{
		paths:      paths,
		runs:       rs,
		results:    res,
		runNotices: runs.NewNoticeStore(paths),
		deliveries: runs.NewDeliveryStore(paths),
	}
	srv.SetSettings(set)
	srv.SetExecution(docs, scheduler)
	srv.SetBroker(live.NewBroker())
	return &execFixture{t: t, srv: srv, docs: docs, sch: scheduler, paths: paths}
}

// doc mints one execution document over a stored run.
func (f *execFixture) doc(runID string, repos ...string) (execstate.Doc, runs.Run) {
	f.t.Helper()
	run := runs.Run{ID: runID, Kind: model.KindTodo, Title: "do the thing", PromptSnapshot: "# prompt"}
	for _, r := range repos {
		run.Targets = append(run.Targets, runs.Target{Repo: r})
	}
	if err := f.srv.runs.Put(run); err != nil {
		f.t.Fatal(err)
	}
	doc, err := f.docs.Create(runID, model.KindTodo, repos, false, model.Actor{User: "someone"})
	if err != nil {
		f.t.Fatal(err)
	}
	return doc, run
}

// The recorder is what makes a RUNNING execution visible: the result document exists from the first
// moment, the stage array is replaced in place, consumption climbs, and the transcript is appended
// line by line (so a kill keeps whatever the agent already said — F7/F11).
func TestRecorderWritesTheResultAndTranscriptLive(t *testing.T) {
	f := newExecFixture(t)
	doc, run := f.doc("run_live", "alpha")
	rec := f.srv.NewResultRecorder(doc, run)

	// Visible before anything finished.
	res, ok, err := f.srv.results.Get(doc.ID)
	if err != nil || !ok {
		t.Fatalf("the result document does not exist while the execution runs: ok=%v err=%v", ok, err)
	}
	if res.RunID != "run_live" || res.Prompt != "# prompt" || res.EndedAt != nil {
		t.Errorf("the opening record is wrong: %+v", res)
	}

	started := time.Now().UTC()
	rec.StageUpdate("alpha", model.StageView{Stage: model.StageImplement, State: model.StepRunning, StartedAt: &started})
	rec.Transcript("alpha", []byte(`{"at":"t1","text":"reading the repository"}`))
	rec.Transcript("alpha", []byte(`{"at":"t2","text":"implemented the change"}`))
	rec.Usage(model.UsageView{InputTokens: 1200, OutputTokens: 300, CostUSD: 0.02})
	rec.StageUpdate("alpha", model.StageView{Stage: model.StageImplement, State: model.StepExecuted, EndedAt: &started})

	res, _, err = f.srv.results.Get(doc.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Repos) != 1 || len(res.Repos[0].Stages) != 1 {
		t.Fatalf("the stage was appended twice instead of replaced in place: %+v", res.Repos)
	}
	if res.Repos[0].Stages[0].State != model.StepExecuted {
		t.Errorf("the stage kept its transient state: %+v", res.Repos[0].Stages[0])
	}
	if res.Usage.InputTokens != 1200 || res.Usage.OutputTokens != 300 {
		t.Errorf("consumption did not climb into the document: %+v", res.Usage)
	}

	tail, err := f.srv.results.ReadTranscriptTail(doc.ID, 8192)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"reading the repository", "implemented the change"} {
		if !strings.Contains(tail, want) {
			t.Errorf("the transcript journal lost %q:\n%s", want, tail)
		}
	}
	if lines := strings.Count(strings.TrimSpace(tail), "\n") + 1; lines != 2 {
		t.Errorf("the journal holds %d lines, want one per emitted block", lines)
	}
}

// Finish is honest about what an outcome IS: an end stamps the record, a pause or an interruption
// leaves it open so the resume continues inside the same record (REQ-019.1).
func TestFinishEndsOnlyWhatActuallyEnded(t *testing.T) {
	f := newExecFixture(t)

	cases := []struct {
		name    string
		err     error
		ended   bool
		reports string
	}{
		{"success", nil, true, "## implemented"},
		{"failure", errors.New("2 of 3 repositories failed"), true, "## not implemented"},
		{"usage limit", executor.ErrPausedUsageLimit, false, "## paused"},
		{"interrupted", context.Canceled, false, "## interrupted"},
	}
	for i, tc := range cases {
		doc, run := f.doc("run_finish_"+string(rune('a'+i)), "alpha")
		rec := f.srv.NewResultRecorder(doc, run)
		rec.Finish(tc.err)
		res, ok, err := f.srv.results.Get(doc.ID)
		if err != nil || !ok {
			t.Fatalf("%s: result missing: ok=%v err=%v", tc.name, ok, err)
		}
		if (res.EndedAt != nil) != tc.ended {
			t.Errorf("%s: endedAt set=%v, want %v", tc.name, res.EndedAt != nil, tc.ended)
		}
		if !strings.Contains(res.Report, tc.reports) {
			t.Errorf("%s: report %q does not name the outcome (%s)", tc.name, res.Report, tc.reports)
		}
	}
}

// A resume keeps the record: the second attempt re-opens the stored result instead of erasing the
// stages, the consumption and the transcript the first attempt produced.
func TestResumeContinuesInTheSameRecord(t *testing.T) {
	f := newExecFixture(t)
	doc, run := f.doc("run_resume", "alpha", "beta")

	first := f.srv.NewResultRecorder(doc, run)
	done := time.Now().UTC()
	first.RepoDone(model.RepoPipeline{
		Repo: "alpha", Done: true, Succeeded: true,
		Stages: []model.StageView{{Stage: model.StageImplement, State: model.StepExecuted, EndedAt: &done}},
	})
	first.Usage(model.UsageView{InputTokens: 500, OutputTokens: 100})
	first.Transcript("alpha", []byte(`{"at":"t1","text":"alpha done"}`))
	first.Finish(context.Canceled)

	// The next process resumes the SAME execution document.
	second := f.srv.NewResultRecorder(doc, run)
	second.RepoDone(model.RepoPipeline{
		Repo: "beta", Done: true, Succeeded: true,
		Stages: []model.StageView{{Stage: model.StageImplement, State: model.StepExecuted, EndedAt: &done}},
	})
	second.Finish(nil)

	res, ok, err := f.srv.results.Get(doc.ID)
	if err != nil || !ok {
		t.Fatalf("result missing: ok=%v err=%v", ok, err)
	}
	if len(res.Repos) != 2 {
		t.Fatalf("the resume dropped the earlier repository: %+v", res.Repos)
	}
	if res.Usage.InputTokens != 500 {
		t.Errorf("the resume erased the consumption of the first attempt: %+v", res.Usage)
	}
	if res.EndedAt == nil || !strings.Contains(res.Report, "## implemented") {
		t.Errorf("the completed resume did not close the record: endedAt=%v report=%q", res.EndedAt, res.Report)
	}
	tail, err := f.srv.results.ReadTranscriptTail(doc.ID, 8192)
	if err != nil || !strings.Contains(tail, "alpha done") {
		t.Errorf("the transcript of the first attempt is gone: %q (err %v)", tail, err)
	}
}

// The composed sink feeds BOTH halves: the document (so admission, slots and resume read the truth)
// and the result (so the view and the history read one file). A notice belongs to the recorder half
// alone — a state document holds no operator findings.
func TestExecutionSinkFeedsBothHalves(t *testing.T) {
	f := newExecFixture(t)
	doc, run := f.doc("run_sink", "alpha")
	rec := f.srv.NewResultRecorder(doc, run)
	sink := ExecutionSink(f.sch.DocSink(doc.ID), rec)

	started := time.Now().UTC()
	sink.StageUpdate("alpha", model.StageView{Stage: model.StageImplement, State: model.StepRunning, StartedAt: &started})
	sink.Continuation(model.ContinuationView{Repo: "alpha", Stage: model.StageImplement})
	sink.Notice(executor.NoticeEvent{Kind: "delivery-alarm", Repo: "alpha", Text: "implemented without dev delivery"})

	// The document half: the repo is claimed active (repo exclusivity reads this) and the
	// continuation point is persisted.
	got, ok, err := f.docs.Get(doc.ID)
	if err != nil || !ok {
		t.Fatalf("document missing: ok=%v err=%v", ok, err)
	}
	if row := got.Repo("alpha"); row == nil || row.State != execstate.RepoActive {
		t.Errorf("the document does not claim the repo as active: %+v", row)
	}
	if got.Continuation == nil || got.Continuation.Repo != "alpha" {
		t.Errorf("the continuation point was not persisted: %+v", got.Continuation)
	}

	// The recorder half: the stage array carries the same update.
	res, _, err := f.srv.results.Get(doc.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Repos) != 1 || len(res.Repos[0].Stages) != 1 {
		t.Errorf("the result did not receive the stage update: %+v", res.Repos)
	}

	// The notice landed in the pool, once, coalesced.
	list, err := f.srv.runNotices.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].Kind != "delivery-alarm" {
		t.Fatalf("the notice did not reach the pool: %+v", list)
	}
	sink.Notice(executor.NoticeEvent{Kind: "delivery-alarm", Repo: "alpha", Text: "implemented without dev delivery"})
	if list, _ = f.srv.runNotices.List(); len(list) != 1 {
		t.Errorf("a repeated finding became a second entry instead of one coalesced record: %+v", list)
	}
}

// Without a runner account the chain cannot act — and says so through EVERY workbench operation.
// A silent skip here would be exactly the "green by default" the acceptance matrix forbids (K-4).
func TestChainDepsWithoutARunnerAccountFailsByName(t *testing.T) {
	f := newExecFixture(t)
	t.Setenv("DEVLAB_RUNS_USER", "")
	deps := f.srv.ChainDeps(ChainHooks{})
	defer deps.Close()

	wb := deps.Workbench("alpha")
	ctx := context.Background()
	if _, err := wb.Prepare(ctx); err == nil || !strings.Contains(err.Error(), "runner account") {
		t.Errorf("Prepare must name the missing runner account, got %v", err)
	}
	if _, err := wb.Head(ctx); err == nil {
		t.Errorf("Head must refuse without a runner account")
	}
	if err := wb.Publish(ctx); err == nil {
		t.Errorf("Publish must refuse without a runner account")
	}
	if _, err := deps.Agent(ctx, "alpha", "do it", runs.ResolvedTuning{}, executor.AgentSession{}); err == nil {
		t.Errorf("the agent must refuse without a runner account")
	}
	if _, _, err := deps.WorkbenchState(ctx, "alpha"); err == nil {
		t.Errorf("the observation must refuse without a runner account")
	}
	// The hooks are optional in the observation form, and their absence is NAMED, not ignored.
	if err := deps.RequestRestart(model.Actor{Autonomous: true}); err == nil {
		t.Errorf("a missing restart coordinator must be named")
	}
	if err := deps.PauseAllUsageLimit("limit", time.Time{}); err == nil {
		t.Errorf("a missing pause coordinator must be named")
	}
}

// SchedulerHooks adapts the scheduler's own answer shape onto the motor's: RequestRestart reports
// the restart STATE, the motor only needs to know that the request went through (B-3).
func TestSchedulerHooksAdaptTheRestartSignature(t *testing.T) {
	f := newExecFixture(t)
	hooks := SchedulerHooks(f.sch)
	if err := hooks.RequestRestart(model.Actor{User: "someone"}); err != nil {
		t.Fatalf("RequestRestart: %v", err)
	}
	if st := f.sch.RestartState(); !st.Pending || st.RequestedBy.User != "someone" {
		t.Errorf("the restart marker does not carry the requester: %+v", st)
	}
	// Idempotent: a second request keeps the first one's record.
	if err := hooks.RequestRestart(model.Actor{User: "other"}); err != nil {
		t.Fatalf("second RequestRestart: %v", err)
	}
	if st := f.sch.RestartState(); st.RequestedBy.User != "someone" {
		t.Errorf("a second request reset the requester to %q", st.RequestedBy.User)
	}
	if err := hooks.PauseAllUsageLimit("limit reached", time.Now().Add(time.Hour)); err != nil {
		t.Errorf("PauseAllUsageLimit: %v", err)
	}
}

// The effort ladder maps onto the CLI's own levels; DevLab's maximal tier "ultracode" is the CLI's
// top level PLUS the directive in the preamble — one name, never a second flag.
func TestChainEffortAndPreamble(t *testing.T) {
	for in, want := range map[string]string{"": "", "low": "low", "max": "max", "ultracode": "max"} {
		if got := chainEffort(in); got != want {
			t.Errorf("chainEffort(%q) = %q, want %q", in, got, want)
		}
	}
	plain := chainPreamble("alpha", "high")
	if !strings.Contains(plain, "alpha") || !strings.Contains(plain, "mercury-dev") {
		t.Errorf("the preamble names neither the repository nor the working branch: %q", plain)
	}
	if strings.Contains(plain, "most thorough") {
		t.Errorf("the ultracode directive leaked into an ordinary run: %q", plain)
	}
	if !strings.Contains(chainPreamble("alpha", "ultracode"), "most thorough") {
		t.Errorf("the ultracode tier carries no directive")
	}
}

// The agent adapter turns a blocking, callback-streaming primitive into the motor's shape: the
// output ends at EOF when the process ends, Wait reports the outcome (and may be called twice), and
// Kill stops the invocation.
func TestAgentStreamAdapter(t *testing.T) {
	// A successful invocation: two streamed lines, then EOF, then the outcome.
	ex := fakeStreamer{lines: []string{`{"type":"assistant"}`, `{"type":"result"}`}}
	a := startAgentStreamWith(context.Background(), ex.run)
	out, err := io.ReadAll(a.Output())
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.Contains(string(out), `"assistant"`) || !strings.Contains(string(out), `"result"`) {
		t.Errorf("the stream lost lines: %q", out)
	}
	if err := a.Wait(); err != nil {
		t.Errorf("Wait: %v", err)
	}
	if err := a.Wait(); err != nil {
		t.Errorf("a second Wait must report the same outcome, got %v", err)
	}

	// A failing invocation reports its error through Wait, and whatever it emitted is kept.
	failing := fakeStreamer{lines: []string{`{"type":"assistant"}`}, err: errors.New("agent exploded")}
	b := startAgentStreamWith(context.Background(), failing.run)
	if out, _ := io.ReadAll(b.Output()); !strings.Contains(string(out), `"assistant"`) {
		t.Errorf("a failed invocation lost its output: %q", out)
	}
	if err := b.Wait(); err == nil || !strings.Contains(err.Error(), "exploded") {
		t.Errorf("Wait must report the failure, got %v", err)
	}

	// Kill cancels the invocation: the blocked primitive returns the context error.
	blocking := fakeStreamer{block: true}
	c := startAgentStreamWith(context.Background(), blocking.run)
	if err := c.Kill(); err != nil {
		t.Fatalf("Kill: %v", err)
	}
	if _, err := io.ReadAll(c.Output()); err != nil {
		t.Fatalf("read after kill: %v", err)
	}
	if err := c.Wait(); !errors.Is(err, context.Canceled) {
		t.Errorf("Wait after Kill = %v, want the cancellation", err)
	}
}

// fakeStreamer stands in for the workspace primitive: it pushes lines through the callback and then
// returns its outcome, exactly as the real one does.
type fakeStreamer struct {
	lines []string
	err   error
	block bool
}

func (f fakeStreamer) run(ctx context.Context, onStdout func([]byte)) error {
	for _, l := range f.lines {
		onStdout([]byte(l + "\n"))
	}
	if f.block {
		<-ctx.Done()
		return ctx.Err()
	}
	return f.err
}

// The re-delivery after a counter-booking (REQ-025.5) rides the ONE delivery composition and reads
// its answer honestly: nothing to deliver is a success, an install without proof is a failure.
func TestRedeliverOutcomeIsHonest(t *testing.T) {
	cases := []struct {
		name string
		out  executor.DeployOutcome
		err  error
		want string // "" = nil, otherwise a substring the error must name
	}{
		{"a library has nothing to re-deliver", executor.DeployOutcome{}, deploy.ErrNotAService, ""},
		{"an excluded repository is not delivered", executor.DeployOutcome{}, deploy.ErrExcluded, ""},
		{"the pristine template is never delivered", executor.DeployOutcome{}, deploy.ErrTemplateRepo, ""},
		{"a build failure surfaces", executor.DeployOutcome{}, errors.New("artifact build failed"), "artifact build failed"},
		{
			"installed but not running is NOT success",
			executor.DeployOutcome{Installed: true, Detail: "port 8123 is not held"}, nil, "not running",
		},
		{"installed but not running names the gate even without a detail", executor.DeployOutcome{Installed: true}, nil, "no proof"},
		{"installed and running is the success", executor.DeployOutcome{Installed: true, Running: true, Port: 8123}, nil, ""},
		{"the self repo proves itself by the handover, not by a port", executor.DeployOutcome{Installed: true, Self: true}, nil, ""},
	}
	for _, tc := range cases {
		err := redeliverOutcome(tc.out, tc.err)
		if tc.want == "" {
			if err != nil {
				t.Errorf("%s: want success, got %v", tc.name, err)
			}
			continue
		}
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: want an error naming %q, got %v", tc.name, tc.want, err)
		}
	}
}

package executor

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"devlab/backend/internal/execstate"
	"devlab/backend/internal/faultclass"
	"devlab/backend/internal/github"
	"devlab/backend/internal/live"
	"devlab/backend/internal/model"
	"devlab/backend/internal/preflight"
	"devlab/backend/internal/runs"
	"devlab/backend/internal/telemetry"
)

// ── fake workbench ───────────────────────────────────────────────────────────────────────

type fakeBench struct {
	mu          sync.Mutex
	prep        PrepareInfo
	prepErr     error
	branch      string // the working-tree branch CurrentBranch reports (default "mercury-dev")
	head        string
	aheadNow    int // CommitsAhead result; CommitAll increments it
	uncommitted bool
	files       map[string]string
	mergeBase   string
	publishErr  func(call int) error

	prepares   int
	cleans     int
	publishes  int
	commitMsgs []string
	writes     map[string]string
	branchAt   map[string]string
	pushed     []string
}

func newFakeBench() *fakeBench {
	return &fakeBench{
		prep:      PrepareInfo{Head: "d00dfeedbeef"},
		head:      "d00dfeedbeef",
		mergeBase: "ba5eba5eba5e",
		files:     map[string]string{},
		writes:    map[string]string{},
		branchAt:  map[string]string{},
	}
}

func (b *fakeBench) Prepare(ctx context.Context, branch, base string) (PrepareInfo, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.prepares++
	if b.prepErr != nil {
		return PrepareInfo{}, b.prepErr
	}
	return b.prep, nil
}
func (b *fakeBench) CleanUntracked(ctx context.Context) error { b.cleans++; return nil }
func (b *fakeBench) Head(ctx context.Context) (string, error) { return b.head, nil }
func (b *fakeBench) CurrentBranch(ctx context.Context) string {
	if b.branch != "" {
		return b.branch
	}
	return "mercury-dev"
}
func (b *fakeBench) CommitsAhead(ctx context.Context, since string) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.aheadNow, nil
}
func (b *fakeBench) HasUncommitted(ctx context.Context) (bool, error) { return b.uncommitted, nil }
func (b *fakeBench) CommitAll(ctx context.Context, message string) (string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.commitMsgs = append(b.commitMsgs, message)
	b.uncommitted = false
	b.aheadNow++
	return b.head, nil
}
func (b *fakeBench) Publish(ctx context.Context) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.publishes++
	if b.publishErr != nil {
		return b.publishErr(b.publishes)
	}
	return nil
}
func (b *fakeBench) ReadFile(ctx context.Context, rel string) (string, bool, error) {
	s, ok := b.files[rel]
	return s, ok, nil
}
func (b *fakeBench) WriteFile(ctx context.Context, rel string, data []byte) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.files[rel] = string(data)
	b.writes[rel] = string(data)
	return nil
}
func (b *fakeBench) BranchAt(ctx context.Context, name, at string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.branchAt[name] = at
	return nil
}
func (b *fakeBench) PushBranch(ctx context.Context, name string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.pushed = append(b.pushed, name)
	return nil
}
func (b *fakeBench) MergeBaseDefault(ctx context.Context) (string, error) { return b.mergeBase, nil }

// ── fake agent stream ────────────────────────────────────────────────────────────────────

// scriptStream replays a fixed stream-json script and completes with waitErr.
type scriptStream struct {
	r       io.Reader
	waitErr error
}

func (s *scriptStream) Output() io.Reader { return s.r }
func (s *scriptStream) Wait() error       { return s.waitErr }
func (s *scriptStream) Kill() error       { return nil }

// blockingStream emits pre, then blocks until its context dies, then EOFs; Wait reports the
// context's error — the shape of a budget kill.
type blockingStream struct {
	ctx  context.Context
	pre  []byte
	sent bool
}

func (s *blockingStream) Output() io.Reader { return s }
func (s *blockingStream) Read(p []byte) (int, error) {
	if !s.sent {
		s.sent = true
		n := copy(p, s.pre)
		return n, nil
	}
	<-s.ctx.Done()
	return 0, io.EOF
}
func (s *blockingStream) Wait() error {
	<-s.ctx.Done()
	return s.ctx.Err()
}
func (s *blockingStream) Kill() error { return nil }

// Stream-json fixtures.
func assistantEvent(id, text string, in, out int) string {
	return fmt.Sprintf(`{"type":"assistant","message":{"id":%q,"usage":{"input_tokens":%d,"output_tokens":%d},"content":[{"type":"text","text":%q}]}}`, id, in, out, text)
}

func toolEvent(id, tool, arg string, in, out int) string {
	return fmt.Sprintf(`{"type":"assistant","message":{"id":%q,"usage":{"input_tokens":%d,"output_tokens":%d},"content":[{"type":"tool_use","name":%q,"input":{"command":%q}}]}}`, id, in, out, tool, arg)
}

func resultEventLine(text string, isErr bool, in, out int, cost float64) string {
	return fmt.Sprintf(`{"type":"result","subtype":"success","is_error":%t,"result":%q,"num_turns":3,"total_cost_usd":%g,"usage":{"input_tokens":%d,"output_tokens":%d}}`, isErr, text, cost, in, out)
}

func script(lines ...string) string { return strings.Join(lines, "\n") + "\n" }

// ── fake GitHub / Deliver / Deploy ───────────────────────────────────────────────────────

type fakeGH struct {
	mu             sync.Mutex
	defaultBranch  string
	defaultErr     error
	createErr      error
	createCalls    []string
	defaultCalls   int
	createReturned bool
}

func (g *fakeGH) DefaultBranch(ctx context.Context, repo string) (string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.defaultCalls++
	if g.defaultErr != nil {
		return "", g.defaultErr
	}
	if g.defaultBranch == "" {
		return "main", nil
	}
	return g.defaultBranch, nil
}
func (g *fakeGH) CreateRepo(ctx context.Context, repo string, private bool) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.createCalls = append(g.createCalls, repo)
	return g.createErr
}

type fakeDeliver struct {
	mu          sync.Mutex
	base        string
	baseErr     error
	pr          model.PRRef
	adopted     bool
	openErr     func(call int) error
	openCalls   []DeliverPRIn
	protectErr  error
	protections []string
	// failedTip is the failed delivery the fake reports at the tip (nil = sound tip); failedCalls
	// records every RecordFailedDelivery the motor makes.
	failedTip   *runs.Delivery
	failedCalls []DeliverFailedIn
}

func (d *fakeDeliver) NextPRBase(ctx context.Context, repo, head string) (string, error) {
	if d.baseErr != nil {
		return "", d.baseErr
	}
	if d.base == "" {
		return "main", nil
	}
	return d.base, nil
}
func (d *fakeDeliver) OpenOrAdoptPR(ctx context.Context, in DeliverPRIn) (model.PRRef, bool, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.openCalls = append(d.openCalls, in)
	if d.openErr != nil {
		if err := d.openErr(len(d.openCalls)); err != nil {
			return model.PRRef{}, false, err
		}
	}
	pr := d.pr
	if pr.Number == 0 {
		pr = model.PRRef{Number: 1, URL: "https://example.invalid/pr/1", HeadBranch: in.Head}
	}
	return pr, d.adopted, nil
}
func (d *fakeDeliver) EnsureProtection(ctx context.Context, repo string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.protections = append(d.protections, repo)
	return d.protectErr
}
func (d *fakeDeliver) FailedTip(ctx context.Context, repo string) (*runs.Delivery, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.failedTip, nil
}
func (d *fakeDeliver) RecordFailedDelivery(ctx context.Context, in DeliverFailedIn) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.failedCalls = append(d.failedCalls, in)
	return nil
}

type fakeDeploy struct {
	mu           sync.Mutex
	det          Detection
	detErr       error
	out          DeployOutcome
	deliverErr   func(call int) error
	deliverCalls int
	// wrapperDrift, when set, makes DeliverDev refuse with ErrWrappersStale carrying this drift — the
	// self-delivery guard's refusal the deliver-dev stage turns into a wrapper-renewal question.
	wrapperDrift string
}

func (d *fakeDeploy) Detect(ctx context.Context, repo string) (Detection, error) {
	if d.detErr != nil {
		return Detection{}, d.detErr
	}
	if d.det.Kind == "" {
		return Detection{Kind: "service", Evidence: "conforming service CLI at ./service"}, nil
	}
	return d.det, nil
}
func (d *fakeDeploy) DeliverDev(ctx context.Context, repo string) (DeployOutcome, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.deliverCalls++
	if d.wrapperDrift != "" {
		return DeployOutcome{Self: true, WrapperDrift: d.wrapperDrift},
			fmt.Errorf("%w: %s", ErrWrappersStale, d.wrapperDrift)
	}
	if d.deliverErr != nil {
		if err := d.deliverErr(d.deliverCalls); err != nil {
			return DeployOutcome{}, err
		}
	}
	out := d.out
	if out.Port == 0 {
		out = DeployOutcome{Installed: true, Running: true, Port: 8099, Detail: "unit active"}
	}
	return out, nil
}

// ── fake questions ───────────────────────────────────────────────────────────────────────

// fakeQuestions is the in-memory blocking-question pool the motor holds and resumes against.
type fakeQuestions struct {
	mu      sync.Mutex
	items   []runs.Question
	raised  []runs.Question
	resolve []string
}

func (q *fakeQuestions) OpenForRepo(ctx context.Context, repo, exceptExecutionID string) (*runs.Question, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	for i := range q.items {
		it := q.items[i]
		if it.Open() && it.Repo == repo && it.ExecutionID != exceptExecutionID {
			return &it, nil
		}
	}
	return nil, nil
}

func (q *fakeQuestions) AnsweredForExec(ctx context.Context, executionID, repo string) (*runs.Question, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	for i := range q.items {
		it := q.items[i]
		if it.Answered() && it.ExecutionID == executionID && it.Repo == repo {
			return &it, nil
		}
	}
	return nil, nil
}

func (q *fakeQuestions) Raise(ctx context.Context, in runs.Question) (runs.Question, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if in.ID == "" {
		in.ID = fmt.Sprintf("qst_%d", len(q.items)+1)
	}
	if in.AskedAt.IsZero() {
		in.AskedAt = time.Now().UTC()
	}
	q.items = append(q.items, in)
	q.raised = append(q.raised, in)
	return in, nil
}

func (q *fakeQuestions) Resolve(ctx context.Context, id string) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.resolve = append(q.resolve, id)
	for i := range q.items {
		if q.items[i].ID == id {
			now := time.Now().UTC()
			q.items[i].Resolved = true
			q.items[i].ResolvedAt = &now
		}
	}
	return nil
}

// answer marks a stored question answered (test helper for the resume path).
func (q *fakeQuestions) answer(id, answer string) {
	q.mu.Lock()
	defer q.mu.Unlock()
	for i := range q.items {
		if q.items[i].ID == id {
			now := time.Now().UTC()
			q.items[i].Answer = answer
			q.items[i].AnsweredAt = &now
		}
	}
}

// ── fake Deps ────────────────────────────────────────────────────────────────────────────

type agentCall struct {
	repo, prompt string
	sess         AgentSession
}

type pauseCall struct {
	msg       string
	notBefore time.Time
}

type fakeDeps struct {
	mu        sync.Mutex
	benches   map[string]*fakeBench
	gh        *fakeGH
	deliver   *fakeDeliver
	deploy    *fakeDeploy
	questions *fakeQuestions
	agentFn   func(ctx context.Context, repo, prompt string, t runs.ResolvedTuning, sess AgentSession) (AgentStream, error)
	attsFn    func(ctx context.Context, repo string, atts []runs.AttachmentRef) (string, func() error, error)
	findings  map[string]preflight.Finding
	findErr   error

	agentCalls   []agentCall
	restartBy    []model.Actor
	restartErr   error
	pauses       []pauseCall
	usageSamples []telemetry.UsageSample
	topics       []live.Topic

	// the examined-stand pool, faked as what it is: a passive record per repo (BEFUND 3).
	scope        map[string]string // repo → the section the renderer produced
	scopeAsked   []string          // repos the motor asked about, in order
	scopeRecords []scopeRecord     // what the motor wrote back
	scopeErr     error             // a pool that cannot be written
}

// scopeRecord is one write into the faked examined-stand pool.
type scopeRecord struct {
	repo, commit string
	ids          []string
}

func newFakeDeps(repos ...string) *fakeDeps {
	d := &fakeDeps{
		benches:   map[string]*fakeBench{},
		gh:        &fakeGH{},
		deliver:   &fakeDeliver{},
		deploy:    &fakeDeploy{},
		questions: &fakeQuestions{},
		findings:  map[string]preflight.Finding{},
	}
	for _, r := range repos {
		b := newFakeBench()
		b.aheadNow = 1 // the agent "committed one delivery-worthy change"
		d.benches[r] = b
		d.findings[r] = preflight.Finding{
			State:      model.TaskNotImplemented,
			Evidence:   []string{"workbench equals the default branch @d00dfeed; no delivery recorded for this run"},
			ObservedAt: time.Now().UTC(),
		}
	}
	d.agentFn = func(ctx context.Context, repo, prompt string, t runs.ResolvedTuning, sess AgentSession) (AgentStream, error) {
		return &scriptStream{r: strings.NewReader(script(
			assistantEvent("m1", "working on it", 100, 20),
			resultEventLine("done: implemented the change", false, 300, 60, 0.42),
		))}, nil
	}
	return d
}

func (d *fakeDeps) Workbench(repo string) WorkbenchOps { return d.benches[repo] }
func (d *fakeDeps) Agent(ctx context.Context, repo, prompt string, t runs.ResolvedTuning, sess AgentSession) (AgentStream, error) {
	d.mu.Lock()
	d.agentCalls = append(d.agentCalls, agentCall{repo: repo, prompt: prompt, sess: sess})
	d.mu.Unlock()
	return d.agentFn(ctx, repo, prompt, t, sess)
}
func (d *fakeDeps) StageAttachments(ctx context.Context, repo string, atts []runs.AttachmentRef) (string, func() error, error) {
	if d.attsFn != nil {
		return d.attsFn(ctx, repo, atts)
	}
	return "", func() error { return nil }, nil
}
func (d *fakeDeps) GitHub() GitHubOps      { return d.gh }
func (d *fakeDeps) Deliver() DeliverOps    { return d.deliver }
func (d *fakeDeps) Deploy() DeployOps      { return d.deploy }
func (d *fakeDeps) Questions() QuestionOps { return d.questions }
func (d *fakeDeps) Preflight(ctx context.Context, repo string, run runs.Run) (preflight.Finding, error) {
	if d.findErr != nil {
		return preflight.Finding{State: model.TaskUnknown}, d.findErr
	}
	return d.findings[repo], nil
}
func (d *fakeDeps) AxiomScope(ctx context.Context, repo string, run runs.Run) string {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.scopeAsked = append(d.scopeAsked, repo)
	return d.scope[repo]
}

func (d *fakeDeps) RecordAxiomScope(repo string, run runs.Run, commit string, at time.Time) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.scopeRecords = append(d.scopeRecords, scopeRecord{repo: repo, commit: commit, ids: run.AxiomIDs})
	return d.scopeErr
}

func (d *fakeDeps) RequestRestart(by model.Actor) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.restartBy = append(d.restartBy, by)
	return d.restartErr
}
func (d *fakeDeps) PauseAllUsageLimit(msg string, notBefore time.Time) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.pauses = append(d.pauses, pauseCall{msg: msg, notBefore: notBefore})
	return nil
}
func (d *fakeDeps) RecordAiUsage(u telemetry.UsageSample) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.usageSamples = append(d.usageSamples, u)
}
func (d *fakeDeps) Publish(t live.Topic) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.topics = append(d.topics, t)
}
func (d *fakeDeps) Now() time.Time { return time.Now() }

// ── fake sink ────────────────────────────────────────────────────────────────────────────

type fakeSink struct {
	mu          sync.Mutex
	updates     map[string][]model.StageView
	transcripts map[string][]string
	usages      []model.UsageView
	dones       []model.RepoPipeline
	conts       []model.ContinuationView
	notices     []NoticeEvent
}

func newFakeSink() *fakeSink {
	return &fakeSink{updates: map[string][]model.StageView{}, transcripts: map[string][]string{}}
}

func (s *fakeSink) StageUpdate(repo string, sv model.StageView) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.updates[repo] = append(s.updates[repo], sv)
}
func (s *fakeSink) Transcript(repo string, line []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.transcripts[repo] = append(s.transcripts[repo], string(line))
}
func (s *fakeSink) Usage(u model.UsageView) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.usages = append(s.usages, u)
}
func (s *fakeSink) RepoDone(rp model.RepoPipeline) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.dones = append(s.dones, rp)
}
func (s *fakeSink) Continuation(c model.ContinuationView) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.conts = append(s.conts, c)
}
func (s *fakeSink) Notice(n NoticeEvent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.notices = append(s.notices, n)
}

// terminal returns the last TERMINAL view of one stage at one repo (found=false when none).
func (s *fakeSink) terminal(repo string, stage model.Stage) (model.StageView, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out model.StageView
	found := false
	for _, sv := range s.updates[repo] {
		if sv.Stage == stage && sv.State.Terminal() {
			out, found = sv, true
		}
	}
	return out, found
}

func (s *fakeSink) done(repo string) (model.RepoPipeline, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, rp := range s.dones {
		if rp.Repo == repo {
			return rp, true
		}
	}
	return model.RepoPipeline{}, false
}

// ── request scaffolding ──────────────────────────────────────────────────────────────────

func mkRequest(kind model.RunKind, repos ...string) Request {
	r := runs.Run{
		ID:             "run_test",
		Kind:           kind,
		Title:          "Passive comment pool",
		Task:           "make the comment store passive",
		PromptSnapshot: "SNAPSHOT-CONSTITUTION-V7: full wording of axioms and rules",
	}
	rows := make([]execstate.RepoProgress, 0, len(repos))
	for _, repo := range repos {
		r.Targets = append(r.Targets, runs.Target{Repo: repo})
		rows = append(rows, execstate.RepoProgress{Repo: repo, State: execstate.RepoPending})
	}
	return Request{
		Run: r,
		Doc: execstate.Doc{
			ID: "exec_test", RunID: r.ID, Kind: kind, Phase: model.PhaseRunning, Repos: rows,
			Requested: model.Authorship{Created: model.Actor{User: "nova"}},
		},
		Budget: runs.ResolvedTuning{Model: "fable", ModelVersion: "5", Effort: "high"},
	}
}

// stubConstitution replaces the (B6-rendered) constitution renderer for the test's duration.
func stubConstitution(t interface{ Cleanup(func()) }, fn func(string) (string, bool)) {
	old := replaceConstitution
	if fn == nil {
		fn = func(doc string) (string, bool) { return doc, false }
	}
	replaceConstitution = fn
	t.Cleanup(func() { replaceConstitution = old })
}

// shrinkRetries makes the growing backoff test-fast for this package's integration tests.
func shrinkRetries(t interface{ Cleanup(func()) }) {
	oldBase, oldMax := faultclass.BaseDelay, faultclass.MaxDelay
	faultclass.BaseDelay, faultclass.MaxDelay = time.Millisecond, 4*time.Millisecond
	t.Cleanup(func() { faultclass.BaseDelay, faultclass.MaxDelay = oldBase, oldMax })
}

var errTransient = errors.New("dial tcp: connection refused")

func statusErr(code int) error { return &github.StatusError{Status: code, Msg: "fixture"} }

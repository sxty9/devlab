package deliver

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"devlab/backend/internal/faultclass"
	"devlab/backend/internal/github"
	"devlab/backend/internal/live"
	"devlab/backend/internal/model"
	"devlab/backend/internal/runs"
	"devlab/backend/internal/workbench"
)

// ── Fixtures ─────────────────────────────────────────────────────────────────────────────

// fakeGH is the fixture GitHubOps (and, unless disabled, the GitSide) every deliver test runs
// against.
type fakeGH struct {
	mu sync.Mutex

	defaultBranch string
	openByHead    map[string]*model.PRRef // "repo|head" → open PR

	createdPRs   []string // "repo|head|base"
	createPRErr  error
	prState      map[string]PRState // "repo|number"
	getPRCalls   int
	getPRErr     error
	mergeCalls   []string // "repo|number|method"
	mergeErr     error
	closeCalls   []string // "repo|number"
	closeReasons []string
	deleted      []string // "repo|branch"
	deleteErr    error
	statuses     []string // "repo|sha|context|state|desc"
	protection   map[string]Protection
	protectCalls []string
	protectErr   error
	createdRepos []string
	listOpen     map[string][]PRState

	cb           CounterBookResult
	cbErr        error
	cbCalls      []string // "repo|reversalBranch"
	redelivered  []string
	redeliverErr error
}

func newFakeGH() *fakeGH {
	return &fakeGH{
		defaultBranch: "main",
		openByHead:    map[string]*model.PRRef{},
		prState:       map[string]PRState{},
		protection:    map[string]Protection{},
		listOpen:      map[string][]PRState{},
	}
}

func (f *fakeGH) CreatePullRequest(_ context.Context, repo, head, base, title, body string) (model.PRRef, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.createPRErr != nil {
		return model.PRRef{}, f.createPRErr
	}
	f.createdPRs = append(f.createdPRs, repo+"|"+head+"|"+base)
	n := 100 + len(f.createdPRs)
	ref := model.PRRef{Number: n, URL: "https://github.example/" + repo + "/pull/" + fmt.Sprint(n), HeadBranch: head}
	f.openByHead[repo+"|"+head] = &ref
	f.prState[key(repo, n)] = PRState{Number: n, State: "open", HeadRef: head, HeadSHA: "sha-" + head}
	return ref, nil
}

func (f *fakeGH) FindOpenPRByHead(_ context.Context, repo, head string) (*model.PRRef, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if ref, ok := f.openByHead[repo+"|"+head]; ok {
		cp := *ref
		return &cp, nil
	}
	return nil, nil
}

func (f *fakeGH) GetPullRequest(_ context.Context, repo string, number int) (PRState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.getPRCalls++
	if f.getPRErr != nil {
		return PRState{}, f.getPRErr
	}
	st, ok := f.prState[key(repo, number)]
	if !ok {
		return PRState{}, &github.StatusError{Status: 404, Msg: "no such PR"}
	}
	return st, nil
}

func (f *fakeGH) ListOpenPullRequests(_ context.Context, repo string) ([]PRState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.listOpen[repo], nil
}

func (f *fakeGH) MergePullRequest(_ context.Context, repo string, number int, method string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.mergeCalls = append(f.mergeCalls, key(repo, number)+"|"+method)
	if f.mergeErr != nil {
		return f.mergeErr
	}
	st := f.prState[key(repo, number)]
	st.State, st.Merged = "closed", true
	now := time.Now().UTC()
	st.MergedAt = &now
	f.prState[key(repo, number)] = st
	return nil
}

func (f *fakeGH) ClosePullRequest(_ context.Context, repo string, number int, reason string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closeCalls = append(f.closeCalls, key(repo, number))
	f.closeReasons = append(f.closeReasons, reason)
	return nil
}

func (f *fakeGH) DeleteBranch(_ context.Context, repo, branch string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.deleteErr != nil {
		return f.deleteErr
	}
	f.deleted = append(f.deleted, repo+"|"+branch)
	return nil
}

func (f *fakeGH) CreateRepo(_ context.Context, name string, _ bool) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.createdRepos = append(f.createdRepos, name)
	return "org/" + name, nil
}

func (f *fakeGH) ProtectDefaultBranch(_ context.Context, repo, requiredStatus string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.protectCalls = append(f.protectCalls, repo+"|"+requiredStatus)
	if f.protectErr != nil {
		return f.protectErr
	}
	f.protection[repo] = Protection{
		RequirePR: true, RequiredStatus: []string{requiredStatus}, MergeMethods: []string{"merge"},
	}
	return nil
}

func (f *fakeGH) GetProtection(_ context.Context, repo string) (Protection, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.protection[repo], nil
}

func (f *fakeGH) PostCommitStatus(_ context.Context, repo, sha, statusContext, state, desc string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.statuses = append(f.statuses, strings.Join([]string{repo, sha, statusContext, state, desc}, "|"))
	return nil
}

func (f *fakeGH) DefaultBranch(_ context.Context, _ string) (string, error) {
	return f.defaultBranch, nil
}

func (f *fakeGH) CounterBook(_ context.Context, d runs.Delivery, reversalBranch string) (CounterBookResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cbCalls = append(f.cbCalls, d.Repo+"|"+reversalBranch)
	if f.cbErr != nil {
		return CounterBookResult{}, f.cbErr
	}
	cb := f.cb
	if cb.DefaultBranch == "" {
		cb.DefaultBranch = f.defaultBranch
	}
	return cb, nil
}

func (f *fakeGH) RedeliverDev(_ context.Context, repo string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.redelivered = append(f.redelivered, repo)
	return f.redeliverErr
}

func key(repo string, number int) string { return repo + "|" + fmt.Sprint(number) }

// ghOnly hides the GitSide so the "no git side wired" refusal can be pinned.
type ghOnly struct{ GitHubOps }

// fakePub counts topic ticks.
type fakePub struct {
	mu     sync.Mutex
	topics []live.Topic
}

func (p *fakePub) Publish(t live.Topic) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.topics = append(p.topics, t)
}

func tempLedger(t *testing.T) *runs.DeliveryStore {
	t.Helper()
	t.Setenv("DEVLAB_MERCURY_RUNS_DELIVERIES", filepath.Join(t.TempDir(), "deliveries.json"))
	return runs.NewDeliveryStore(nil)
}

func tempPRs(t *testing.T) *runs.PRStore {
	t.Helper()
	t.Setenv("DEVLAB_MERCURY_RUNS_PRS", filepath.Join(t.TempDir(), "prs.json"))
	return runs.NewPRStore(nil)
}

func tempNotices(t *testing.T) *runs.NoticeStore {
	t.Helper()
	t.Setenv("DEVLAB_MERCURY_RUNS_NOTICES", filepath.Join(t.TempDir(), "notices.json"))
	return runs.NewNoticeStore(nil)
}

func tempRuns(t *testing.T) *runs.Store {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("DEVLAB_MERCURY_RUNS", filepath.Join(dir, "runs.json"))
	t.Setenv("DEVLAB_MERCURY_RUNS_HISTORY", filepath.Join(dir, "history"))
	return runs.NewStore(nil)
}

func tempResults(t *testing.T) *runs.ResultStore {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("DEVLAB_MERCURY_EXECUTIONS", filepath.Join(dir, "executions"))
	t.Setenv("DEVLAB_MERCURY_RUNS_RESULTS", filepath.Join(dir, "legacy"))
	return runs.NewResultStore(nil)
}

// stubClassification pins the K-5 seams for tests that exercise fault paths, so this
// package's tests stay deterministic while faultclass is filled in the same wave.
func stubClassification(t *testing.T) {
	t.Helper()
	oldClassify, oldNext := classify, advanceBackoff
	classify = func(err error) faultclass.Class {
		var se *github.StatusError
		if errors.As(err, &se) && (se.Status == 404 || se.Status == 403 || se.Status == 422) {
			return faultclass.Permanent
		}
		return faultclass.Transient
	}
	advanceBackoff = func(b model.Backoff, now time.Time, maxAttempts int) (model.Backoff, bool) {
		b.Attempts++
		b.LastAt = now
		if b.FirstAt.IsZero() {
			b.FirstAt = now
		}
		b.NextAt = now.Add(time.Duration(b.Attempts) * time.Minute)
		return b, b.Attempts >= maxAttempts
	}
	t.Cleanup(func() { classify, advanceBackoff = oldClassify, oldNext })
}

var t0 = time.Date(2026, 7, 27, 0, 0, 0, 0, time.UTC)

// ── Stacked bases (REQ-024) ──────────────────────────────────────────────────────────────

// TestNextPRBase pins the stacking rule: the last open delivery's branch is the next base;
// without one the default branch is; empty and workbench branches are never bases.
func TestNextPRBase(t *testing.T) {
	if got := NextPRBase(nil, "main"); got != "main" {
		t.Errorf("empty stack: base = %q, want main", got)
	}
	open := []runs.Delivery{
		{ID: "d1", Branch: "fix/first-aaa111"},
		{ID: "d2", Branch: "fix/second-bbb222"},
	}
	if got := NextPRBase(open, "main"); got != "fix/second-bbb222" {
		t.Errorf("base = %q, want the LAST open delivery's branch", got)
	}
	open = append(open, runs.Delivery{ID: "d3", Branch: ""}, runs.Delivery{ID: "d4", Branch: workbench.Branch})
	if got := NextPRBase(open, "main"); got != "fix/second-bbb222" {
		t.Errorf("base = %q — empty and %s branches must be skipped", got, workbench.Branch)
	}
}

// TestStackedDeliveries is the REQ-024 acceptance shape: the second delivery's PR bases on
// the first delivery's branch (not on main), so each PR shows only its own changes and the
// merge order follows the structure.
func TestStackedDeliveries(t *testing.T) {
	gh := newFakeGH()
	ledger := tempLedger(t)
	ctx := context.Background()

	first := runs.Delivery{ID: "dlv_1", Repo: "o/x", Branch: "fix/one-aaa111", FromCommit: "c0", ToCommit: "c1", CreatedAt: t0, ExecutionID: "exec_1"}
	if err := ledger.Put(first); err != nil {
		t.Fatal(err)
	}
	base1 := "main"
	if open, _ := ledger.Open("o/x"); len(open) != 1 {
		t.Fatal("precondition: one open delivery")
	} else if NextPRBase(open[:0], "main") != "main" {
		t.Fatal("first delivery must base on main")
	}
	if _, _, err := OpenOrAdoptPR(ctx, gh, ledger, PRIn{Repo: "o/x", Head: first.Branch, Base: base1, Title: "one", DeliveryID: "dlv_1"}); err != nil {
		t.Fatal(err)
	}

	second := runs.Delivery{ID: "dlv_2", Repo: "o/x", Branch: "fix/two-bbb222", FromCommit: "c1", ToCommit: "c2", CreatedAt: t0.Add(time.Hour), ExecutionID: "exec_2"}
	if err := ledger.Put(second); err != nil {
		t.Fatal(err)
	}
	open, _ := ledger.Open("o/x")
	// The second PR's base: the stack minus the delivery being opened.
	var prior []runs.Delivery
	for _, d := range open {
		if d.ID != "dlv_2" {
			prior = append(prior, d)
		}
	}
	base2 := NextPRBase(prior, "main")
	if base2 != "fix/one-aaa111" {
		t.Fatalf("second base = %q, want the first delivery's branch", base2)
	}
	if _, _, err := OpenOrAdoptPR(ctx, gh, ledger, PRIn{Repo: "o/x", Head: second.Branch, Base: base2, Title: "two", DeliveryID: "dlv_2"}); err != nil {
		t.Fatal(err)
	}
	if len(gh.createdPRs) != 2 || gh.createdPRs[1] != "o/x|fix/two-bbb222|fix/one-aaa111" {
		t.Errorf("created PRs = %v — the second must ride on the first branch (own changes only)", gh.createdPRs)
	}
}

// ── One PR path: create, adopt, intent (K-6, REQ-019.5, B-1) ─────────────────────────────

// TestOpenOrAdoptPRCreatesWithIntent: intent-before-PR — the ledger entry exists first, the
// create writes the PR number back, and the fresh PR carries a SUCCESS origin status.
func TestOpenOrAdoptPRCreatesWithIntent(t *testing.T) {
	gh := newFakeGH()
	ledger := tempLedger(t)
	d := runs.Delivery{ID: "dlv_1", Repo: "o/x", Branch: "fix/login-aaa111", FromCommit: "c0", ToCommit: "sha-fix/login-aaa111", CreatedAt: t0}
	if err := ledger.Put(d); err != nil {
		t.Fatal(err)
	}

	ref, adopted, err := OpenOrAdoptPR(context.Background(), gh, ledger, PRIn{
		Repo: "o/x", Head: d.Branch, Base: "main", Title: "Login fix", DeliveryID: "dlv_1",
	})
	if err != nil || adopted {
		t.Fatalf("create: adopted=%v err=%v", adopted, err)
	}
	got, _, _ := ledger.ByID("dlv_1")
	if got.PRNumber != ref.Number || got.PRURL != ref.URL {
		t.Errorf("PR number/url not written back onto the intent: %+v vs %+v", got, ref)
	}
	// The delivery head (ToCommit == head SHA) carries a success status immediately.
	found := false
	for _, s := range gh.statuses {
		if strings.HasPrefix(s, "o/x|sha-fix/login-aaa111|"+OriginStatusContext+"|success|") {
			found = true
		}
	}
	if !found {
		t.Errorf("no success origin status posted; statuses = %v", gh.statuses)
	}
}

// TestOpenOrAdoptPRAdopts: an open PR with the same head is adopted — never a second one
// (K-6/REQ-019.5) — and the intent still receives the number.
func TestOpenOrAdoptPRAdopts(t *testing.T) {
	gh := newFakeGH()
	ledger := tempLedger(t)
	existing := model.PRRef{Number: 7, URL: "https://github.example/o/x/pull/7", HeadBranch: "fix/login-aaa111"}
	gh.openByHead["o/x|fix/login-aaa111"] = &existing
	gh.prState["o/x|7"] = PRState{Number: 7, State: "open", HeadRef: existing.HeadBranch, HeadSHA: "c9"}
	if err := ledger.Put(runs.Delivery{ID: "dlv_1", Repo: "o/x", Branch: existing.HeadBranch, FromCommit: "c0", ToCommit: "c9", CreatedAt: t0}); err != nil {
		t.Fatal(err)
	}

	ref, adopted, err := OpenOrAdoptPR(context.Background(), gh, ledger, PRIn{
		Repo: "o/x", Head: existing.HeadBranch, Base: "main", Title: "again", DeliveryID: "dlv_1",
	})
	if err != nil || !adopted {
		t.Fatalf("adoption expected: adopted=%v err=%v", adopted, err)
	}
	if ref.Number != 7 {
		t.Errorf("adopted PR number = %d, want 7", ref.Number)
	}
	if len(gh.createdPRs) != 0 {
		t.Errorf("adoption must not create a second PR, created %v", gh.createdPRs)
	}
	if got, _, _ := ledger.ByID("dlv_1"); got.PRNumber != 7 {
		t.Errorf("adoption must write the number onto the intent, got %+v", got)
	}
}

// TestOpenOrAdoptPRHuman: the IDE route (B-1) — DeliveryID "" writes NO ledger entry, and the
// human PR immediately carries the explained FAILURE origin status (REQ-033: it will not pass
// the required check without a recorded delivery).
func TestOpenOrAdoptPRHuman(t *testing.T) {
	gh := newFakeGH()
	ledger := tempLedger(t)

	ref, adopted, err := OpenOrAdoptPR(context.Background(), gh, ledger, PRIn{
		Repo: "o/x", Head: "my-hand-branch", Base: "main", Title: "hand work",
	})
	if err != nil || adopted {
		t.Fatalf("human create: adopted=%v err=%v", adopted, err)
	}
	if all, _ := ledger.All(); len(all) != 0 {
		t.Errorf("a human PR must write NO ledger entry, got %+v", all)
	}
	if ref.Number == 0 {
		t.Error("the human PR must still be opened (the ONE path serves both writers)")
	}
	var failure string
	for _, s := range gh.statuses {
		if strings.Contains(s, "|"+OriginStatusContext+"|failure|") {
			failure = s
		}
	}
	if failure == "" {
		t.Fatalf("the human PR must carry a failure origin status, statuses = %v", gh.statuses)
	}
	if !strings.Contains(failure, "todo in Mercury") {
		t.Errorf("the rejection must explain the admissible path, got %q", failure)
	}
}

// TestOpenOrAdoptPRGuards: the workbench branch is never turned into a PR, and a chain PR
// without its recorded intent is refused (intent-before-PR).
func TestOpenOrAdoptPRGuards(t *testing.T) {
	gh := newFakeGH()
	ledger := tempLedger(t)
	if _, _, err := OpenOrAdoptPR(context.Background(), gh, ledger, PRIn{Repo: "o/x", Head: workbench.Branch, Base: "main"}); err == nil {
		t.Errorf("%s must never become a pull request", workbench.Branch)
	}
	_, _, err := OpenOrAdoptPR(context.Background(), gh, ledger, PRIn{Repo: "o/x", Head: "fix/a-abc123", Base: "main", DeliveryID: "dlv_missing"})
	if !errors.Is(err, ErrDeliveryNotFound) {
		t.Errorf("missing intent must be refused with ErrDeliveryNotFound, got %v", err)
	}
	if len(gh.createdPRs) != 0 {
		t.Errorf("no PR may be created on a guard refusal, got %v", gh.createdPRs)
	}
}

// ── Repo creation with protection (REQ-033.6, REQ-006.2) ─────────────────────────────────

// TestCreateProtectedRepo: creation and protection happen in the same pass; a failure to
// protect fails the creation.
func TestCreateProtectedRepo(t *testing.T) {
	gh := newFakeGH()
	full, err := CreateProtectedRepo(context.Background(), gh, "new-service")
	if err != nil {
		t.Fatalf("CreateProtectedRepo: %v", err)
	}
	if full != "org/new-service" {
		t.Errorf("full name = %q", full)
	}
	if len(gh.protectCalls) != 1 || gh.protectCalls[0] != "org/new-service|"+OriginStatusContext {
		t.Errorf("protection must be set in the same pass, calls = %v", gh.protectCalls)
	}

	gh2 := newFakeGH()
	gh2.protectErr = errors.New("forbidden")
	if _, err := CreateProtectedRepo(context.Background(), gh2, "unlucky"); err == nil {
		t.Fatal("a failed protection write must fail the creation (REQ-033.6)")
	} else if !strings.Contains(err.Error(), "counts as failed") {
		t.Errorf("the failure must name the rule, got %v", err)
	}
}

// TestEnsureProtectionSatisfied: a repo already carrying the contract is Satisfied — no write.
func TestEnsureProtectionSatisfied(t *testing.T) {
	gh := newFakeGH()
	gh.protection["o/x"] = Protection{RequirePR: true, RequiredStatus: []string{OriginStatusContext}, MergeMethods: []string{"merge"}}
	rep, err := EnsureProtection(context.Background(), gh, "o/x")
	if err != nil || !rep.OK || rep.Restored {
		t.Fatalf("satisfied: %+v err=%v", rep, err)
	}
	if len(gh.protectCalls) != 0 {
		t.Errorf("a satisfied repo must not be re-written, calls = %v", gh.protectCalls)
	}
}

// TestVerifyProtectionRestores (REQ-033.7): a removed condition is restored AND recorded.
func TestVerifyProtectionRestores(t *testing.T) {
	gh := newFakeGH()
	n := tempNotices(t)
	gh.protection["o/ok"] = Protection{RequirePR: true, RequiredStatus: []string{OriginStatusContext}, MergeMethods: []string{"merge"}}
	gh.protection["o/drifted"] = Protection{RequirePR: false, RequiredStatus: nil, MergeMethods: []string{"merge", "squash"}}

	reps, err := VerifyProtection(context.Background(), gh, []string{"o/ok", "o/drifted"}, n)
	if err != nil {
		t.Fatalf("VerifyProtection: %v", err)
	}
	if len(reps) != 2 || !reps[0].OK || reps[0].Restored {
		t.Errorf("ok repo report = %+v", reps[0])
	}
	if !reps[1].OK || !reps[1].Restored || !strings.Contains(reps[1].Detail, "restored") {
		t.Errorf("drifted repo must be restored, report = %+v", reps[1])
	}
	if len(gh.protectCalls) != 1 || !strings.HasPrefix(gh.protectCalls[0], "o/drifted|") {
		t.Errorf("only the drifted repo gets a write, calls = %v", gh.protectCalls)
	}
	notes, _ := n.List()
	if len(notes) != 1 || notes[0].Kind != "protection-deviation" || !strings.Contains(notes[0].Reason, "o/drifted") {
		t.Errorf("the deviation must be recorded, notices = %+v", notes)
	}
}

// ── Rollback (REQ-025) ───────────────────────────────────────────────────────────────────

// TestRollbackConflictRaisesTodo: later work builds on the delivery ⇒ the reverse conflicts ⇒
// NO guess — a concrete ToDo is raised (repo target, both deliveries named, history rewriting
// forbidden) and the delivery stays un-reverted.
func TestRollbackConflictRaisesTodo(t *testing.T) {
	gh := newFakeGH()
	ledger := tempLedger(t)
	rs := tempRuns(t)
	_ = ledger.Put(runs.Delivery{ID: "dlv_1", Repo: "o/x", Branch: "fix/one-aaa111", FromCommit: "c0", ToCommit: "c1", PRNumber: 5, CreatedAt: t0})
	_ = ledger.Put(runs.Delivery{ID: "dlv_2", Repo: "o/x", Branch: "fix/two-bbb222", FromCommit: "c1", ToCommit: "c2", PRNumber: 6, CreatedAt: t0.Add(time.Minute)})
	gh.prState["o/x|5"] = PRState{Number: 5, State: "open"}
	gh.cb = CounterBookResult{Conflicted: true, ConflictFiles: []string{"main.go"}}

	out, err := Rollback(context.Background(), gh, ledger, rs, "dlv_1", model.Actor{User: "tester"})
	if err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	if out.ConflictTodoID == "" {
		t.Fatalf("expected a conflict todo, got %+v", out)
	}
	if d, _, _ := ledger.ByID("dlv_1"); d.ClosedAt != nil || d.MergedAt != nil {
		t.Errorf("a conflicting rollback must not close the delivery: %+v", d)
	}
	all, _ := rs.List()
	var todo *runs.Run
	for i := range all {
		if all[i].ID == out.ConflictTodoID {
			todo = &all[i]
		}
	}
	if todo == nil {
		t.Fatalf("todo %s not in the run store", out.ConflictTodoID)
	}
	if todo.Kind != model.KindTodo || todo.DueAt == nil {
		t.Errorf("the todo must be a due todo, got %+v", todo)
	}
	if len(todo.Targets) != 1 || todo.Targets[0].Repo != "x" {
		t.Errorf("the todo must target the repo, got %+v", todo.Targets)
	}
	if !strings.Contains(todo.Task, "dlv_1") || !strings.Contains(todo.Task, "dlv_2") {
		t.Errorf("the todo must name the delivery and the later work:\n%s", todo.Task)
	}
	if !strings.Contains(strings.ToLower(todo.Task), "do not rewrite history") {
		t.Errorf("the todo must forbid history rewriting:\n%s", todo.Task)
	}
	if !todo.Authorship.Created.Autonomous {
		t.Errorf("the automatic todo must be marked autonomous, got %+v", todo.Authorship.Created)
	}
}

// TestRollbackOpenClosesPR: the open direction — the PR is closed WITH the justification, the
// delivery is closed with the rollback reason, no reversal PR, and dev is re-delivered.
func TestRollbackOpenClosesPR(t *testing.T) {
	gh := newFakeGH()
	ledger := tempLedger(t)
	_ = ledger.Put(runs.Delivery{ID: "dlv_1", Repo: "o/x", Branch: "fix/one-aaa111", FromCommit: "c0", ToCommit: "c1", PRNumber: 5, CreatedAt: t0})
	gh.prState["o/x|5"] = PRState{Number: 5, State: "open"}
	gh.cb = CounterBookResult{Changed: true, Before: "c1", After: "c9"}

	out, err := Rollback(context.Background(), gh, ledger, nil, "dlv_1", model.Actor{User: "tester"})
	if err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	if out.ClosedPR == nil || out.ClosedPR.Number != 5 || out.ReversalPR != nil {
		t.Fatalf("want closed PR 5 and no reversal, got %+v", out)
	}
	if len(gh.closeCalls) != 1 || gh.closeCalls[0] != "o/x|5" {
		t.Errorf("close calls = %v", gh.closeCalls)
	}
	if len(gh.closeReasons) != 1 || !strings.Contains(gh.closeReasons[0], "rolled back by tester") {
		t.Errorf("the close must carry the justification, got %v", gh.closeReasons)
	}
	d, _, _ := ledger.ByID("dlv_1")
	if d.ClosedAt == nil || !strings.Contains(d.ClosedReason, "rolled back by tester") {
		t.Errorf("the delivery must be closed with the rollback reason: %+v", d)
	}
	if len(gh.createdPRs) != 0 {
		t.Errorf("no reversal PR for an unmerged delivery, created %v", gh.createdPRs)
	}
	if len(gh.redelivered) != 1 || gh.redelivered[0] != "o/x" {
		t.Errorf("dev must be re-delivered after the counter-booking, got %v", gh.redelivered)
	}
}

// TestRollbackMergedCreatesReversalPR: the merged direction — the counter-booking is itself a
// delivery (intent first) with a stacked reversal PR through the SAME chain; history is never
// rewritten (the reversal is a NEW branch/commit range, the original record survives).
func TestRollbackMergedCreatesReversalPR(t *testing.T) {
	gh := newFakeGH()
	ledger := tempLedger(t)
	mergedAt := t0.Add(time.Hour)
	_ = ledger.Put(runs.Delivery{ID: "dlv_1", Repo: "o/x", Branch: "fix/one-aaa111", FromCommit: "c0", ToCommit: "c1", PRNumber: 5, CreatedAt: t0, MergedAt: &mergedAt, ExecutionID: "exec_1"})
	gh.prState["o/x|5"] = PRState{Number: 5, State: "closed", Merged: true, MergedAt: &mergedAt}
	gh.cb = CounterBookResult{Changed: true, Before: "c1", After: "c9", DefaultBranch: "main"}

	out, err := Rollback(context.Background(), gh, ledger, nil, "dlv_1", model.Actor{User: "tester"})
	if err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	if out.ReversalPR == nil || out.ReversalDeliveryID == "" {
		t.Fatalf("expected a reversal PR, got %+v", out)
	}
	wantBranch := reversalBranchFor(runs.Delivery{ID: "dlv_1", Branch: "fix/one-aaa111"})
	if len(gh.cbCalls) != 1 || gh.cbCalls[0] != "o/x|"+wantBranch {
		t.Errorf("counter-book calls = %v, want the deterministic reversal branch %q", gh.cbCalls, wantBranch)
	}
	if len(gh.createdPRs) != 1 || gh.createdPRs[0] != "o/x|"+wantBranch+"|main" {
		t.Errorf("reversal PR = %v, want head %q on main (no other open delivery)", gh.createdPRs, wantBranch)
	}
	rev, ok, _ := ledger.ByID(out.ReversalDeliveryID)
	if !ok || rev.ReversalOf != "dlv_1" || rev.FromCommit != "c1" || rev.ToCommit != "c9" {
		t.Fatalf("reversal delivery not recorded correctly: %+v ok=%v", rev, ok)
	}
	if rev.PRNumber != out.ReversalPR.Number {
		t.Errorf("the reversal intent must carry its PR number (the same chain), got %+v", rev)
	}
	// The original stays merged — history is never rewritten; the reversal is the record.
	if d, _, _ := ledger.ByID("dlv_1"); d.MergedAt == nil {
		t.Errorf("the original merged delivery must keep its history: %+v", d)
	}
	// Idempotence: a second rollback reuses the existing reversal, creating nothing new.
	out2, err := Rollback(context.Background(), gh, ledger, nil, "dlv_1", model.Actor{User: "tester"})
	if err != nil {
		t.Fatal(err)
	}
	if out2.ReversalDeliveryID != out.ReversalDeliveryID || len(gh.createdPRs) != 1 || len(gh.cbCalls) != 1 {
		t.Errorf("a repeated rollback must be an idempotent no-op, got %+v (created %v)", out2, gh.createdPRs)
	}
}

// TestRollbackMergedStacksOnOpenDelivery: a reversal PR stacks like any other change — on the
// latest OPEN delivery when one exists.
func TestRollbackMergedStacksOnOpenDelivery(t *testing.T) {
	gh := newFakeGH()
	ledger := tempLedger(t)
	mergedAt := t0.Add(time.Hour)
	_ = ledger.Put(runs.Delivery{ID: "dlv_1", Repo: "o/x", Branch: "fix/one-aaa111", FromCommit: "c0", ToCommit: "c1", PRNumber: 5, CreatedAt: t0, MergedAt: &mergedAt})
	_ = ledger.Put(runs.Delivery{ID: "dlv_2", Repo: "o/x", Branch: "fix/two-bbb222", FromCommit: "c1", ToCommit: "c2", PRNumber: 6, CreatedAt: t0.Add(2 * time.Hour)})
	gh.prState["o/x|5"] = PRState{Number: 5, State: "closed", Merged: true}
	gh.cb = CounterBookResult{Changed: true, Before: "c2", After: "c9", DefaultBranch: "main"}

	if _, err := Rollback(context.Background(), gh, ledger, nil, "dlv_1", model.Actor{User: "tester"}); err != nil {
		t.Fatal(err)
	}
	if len(gh.createdPRs) != 1 || !strings.HasSuffix(gh.createdPRs[0], "|fix/two-bbb222") {
		t.Errorf("the reversal PR must stack on the open delivery, got %v", gh.createdPRs)
	}
}

// TestRollbackNoChange: the idempotent no-op — the effect is already absent; an open PR is
// still closed and the record marked, nothing else happens.
func TestRollbackNoChange(t *testing.T) {
	gh := newFakeGH()
	ledger := tempLedger(t)
	_ = ledger.Put(runs.Delivery{ID: "dlv_1", Repo: "o/x", Branch: "fix/one-aaa111", FromCommit: "c0", ToCommit: "c1", PRNumber: 5, CreatedAt: t0})
	gh.prState["o/x|5"] = PRState{Number: 5, State: "open"}
	gh.cb = CounterBookResult{Changed: false, Before: "c1", After: "c1"}

	out, err := Rollback(context.Background(), gh, ledger, nil, "dlv_1", model.Actor{User: "tester"})
	if err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	if out.ClosedPR == nil || len(gh.createdPRs) != 0 {
		t.Fatalf("no-change open rollback must close the PR and create nothing, got %+v %v", out, gh.createdPRs)
	}
	if d, _, _ := ledger.ByID("dlv_1"); d.ClosedAt == nil {
		t.Errorf("the delivery must be marked rolled back: %+v", d)
	}
}

// TestRollbackGuards: unknown delivery ⇒ ErrDeliveryNotFound; a GitHubOps without the git
// side is refused (nothing half-done).
func TestRollbackGuards(t *testing.T) {
	gh := newFakeGH()
	ledger := tempLedger(t)
	if _, err := Rollback(context.Background(), gh, ledger, nil, "dlv_missing", model.Actor{User: "t"}); !errors.Is(err, ErrDeliveryNotFound) {
		t.Errorf("want ErrDeliveryNotFound, got %v", err)
	}
	_ = ledger.Put(runs.Delivery{ID: "dlv_1", Repo: "o/x", Branch: "fix/one-aaa111", CreatedAt: t0})
	if _, err := Rollback(context.Background(), ghOnly{gh}, ledger, nil, "dlv_1", model.Actor{User: "t"}); err == nil || !strings.Contains(err.Error(), "git side") {
		t.Errorf("a missing git side must be refused, got %v", err)
	}
}

// ── Maintenance (F14, REQ-026.4, B-8, K-5) ───────────────────────────────────────────────

func trackedPR(d runs.Delivery, mergeBy time.Time) runs.PendingPR {
	return runs.PendingPR{
		Repo: d.Repo, Number: d.PRNumber, URL: d.PRURL, DeliveryID: d.ID,
		CreatedAt: d.CreatedAt, MergeBy: mergeBy,
	}
}

// armMaintain arms the writing half of the maintenance — the operator's named handover. Every test
// that expects a WRITE says so through this line: unarmed is the shipped default (pinned by
// TestMaintainHeldWritesNothing), so a pass that merges, prunes or stamps runs only after it.
func armMaintain(t *testing.T) {
	t.Helper()
	t.Setenv(EnvMaintainEnforce, "1")
}

// TestMaintainHeldWritesNothing — the standstill of the first start: unarmed (the shipped default)
// the maintenance makes NO call into a foreign repository at all, changes none of its own records,
// and says in the feed why nothing moves. Armed, the very same situation is worked.
func TestMaintainHeldWritesNothing(t *testing.T) {
	if MaintainArmed() {
		t.Fatalf("%s must be off by default — a first start must not write into foreign repositories", EnvMaintainEnforce)
	}
	gh := newFakeGH()
	ledger, prs, res, n, pub := tempLedger(t), tempPRs(t), tempResults(t), tempNotices(t), &fakePub{}
	d := runs.Delivery{ID: "dlv_1", Repo: "o/x", Branch: "fix/one-aaa111", FromCommit: "c0", ToCommit: "c1", PRNumber: 5, CreatedAt: t0, ExecutionID: "exec_1"}
	_ = ledger.Put(d)
	_ = prs.Add(trackedPR(d, time.Now().Add(-time.Hour))) // overdue: an armed pass WOULD merge it
	gh.prState["o/x|5"] = PRState{Number: 5, State: "open", HeadRef: d.Branch, HeadSHA: "c1"}
	_ = res.Put(runs.Result{ID: "exec_1", RunID: "run_1", Kind: model.KindTodo, StartedAt: t0})

	if err := Maintain(context.Background(), gh, prs, ledger, res, n, pub); err != nil {
		t.Fatalf("a hold is a decision, not a failure: %v", err)
	}
	// Not one mutating call — and not even a read: the held pass needs neither token nor network.
	if len(gh.mergeCalls) != 0 || len(gh.deleted) != 0 || len(gh.statuses) != 0 || len(gh.closeCalls) != 0 || len(gh.protectCalls) != 0 {
		t.Errorf("the held pass wrote into a foreign repository: merges=%v deleted=%v statuses=%v closed=%v protect=%v",
			gh.mergeCalls, gh.deleted, gh.statuses, gh.closeCalls, gh.protectCalls)
	}
	if gh.getPRCalls != 0 {
		t.Errorf("the held pass called GitHub %d time(s) — it establishes the situation from its own pools", gh.getPRCalls)
	}
	// Nothing of DevLab's own moved either: a half-mirrored state is nobody's truth.
	if got, _, _ := ledger.ByID("dlv_1"); got.MergedAt != nil || got.ClosedAt != nil {
		t.Errorf("the held pass settled a delivery: %+v", got)
	}
	if left, _ := prs.List(); len(left) != 1 {
		t.Errorf("the tracked pull request must stay tracked, left %+v", left)
	}
	if r, _, _ := res.Get("exec_1"); r.MergedAt != nil {
		t.Errorf("the held pass closed an execution: %+v", r)
	}
	// …and the standstill is stated, with the switch that ends it.
	notices, err := n.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(notices) != 1 || notices[0].Kind != runs.NoticeDeliveryHeld {
		t.Fatalf("the standstill is not in the feed: %+v", notices)
	}
	if !strings.Contains(notices[0].Message(), EnvMaintainEnforce) {
		t.Errorf("the notice does not name the switch that ends the hold: %q", notices[0].Message())
	}
	// A second held pass bundles onto the same record instead of filing a row every tick.
	if err := Maintain(context.Background(), gh, prs, ledger, res, n, pub); err != nil {
		t.Fatal(err)
	}
	if notices, _ = n.List(); len(notices) != 1 || notices[0].Count != 2 {
		t.Errorf("the repeated standstill must bundle into one row with a count, got %+v", notices)
	}

	// Armed — and the very same situation is worked.
	armMaintain(t)
	if err := Maintain(context.Background(), gh, prs, ledger, res, n, pub); err != nil {
		t.Fatalf("Maintain (armed): %v", err)
	}
	if len(gh.mergeCalls) != 1 || gh.mergeCalls[0] != "o/x|5|merge" {
		t.Errorf("the armed pass must merge what is due, calls = %v", gh.mergeCalls)
	}
	if len(gh.deleted) != 1 {
		t.Errorf("the armed pass must prune the delivery branch, deleted = %v", gh.deleted)
	}
}

// TestMaintainHeldStaysSilentWithNothingWaiting: no tracked pull request and no open delivery means
// there is nothing the hold explains — and the feed stays silent.
func TestMaintainHeldStaysSilentWithNothingWaiting(t *testing.T) {
	ledger, prs, res, n := tempLedger(t), tempPRs(t), tempResults(t), tempNotices(t)
	if err := Maintain(context.Background(), newFakeGH(), prs, ledger, res, n, &fakePub{}); err != nil {
		t.Fatalf("Maintain: %v", err)
	}
	if notices, _ := n.List(); len(notices) != 0 {
		t.Errorf("an empty pool needs no explanation, got %+v", notices)
	}
}

// TestMaintainMergesAndPrunes: an overdue open PR is merged with the LITERAL method "merge",
// the SAME pass deletes the delivery branch, mirrors MergedAt onto the ledger, drops the pool
// entry, closes the execution result per B-8 and ticks the deliveries topic.
func TestMaintainMergesAndPrunes(t *testing.T) {
	armMaintain(t)
	gh := newFakeGH()
	ledger, prs, res, n, pub := tempLedger(t), tempPRs(t), tempResults(t), tempNotices(t), &fakePub{}
	d := runs.Delivery{ID: "dlv_1", Repo: "o/x", Branch: "fix/one-aaa111", FromCommit: "c0", ToCommit: "c1", PRNumber: 5, CreatedAt: t0, ExecutionID: "exec_1"}
	_ = ledger.Put(d)
	_ = prs.Add(trackedPR(d, time.Now().Add(-time.Hour))) // overdue
	gh.prState["o/x|5"] = PRState{Number: 5, State: "open", HeadRef: d.Branch, HeadSHA: "c1"}
	_ = res.Put(runs.Result{ID: "exec_1", RunID: "run_1", Kind: model.KindTodo, StartedAt: t0})

	if err := Maintain(context.Background(), gh, prs, ledger, res, n, pub); err != nil {
		t.Fatalf("Maintain: %v", err)
	}
	if len(gh.mergeCalls) != 1 || gh.mergeCalls[0] != "o/x|5|merge" {
		t.Fatalf("merge calls = %v — the method must be the literal \"merge\"", gh.mergeCalls)
	}
	if len(gh.deleted) != 1 || gh.deleted[0] != "o/x|fix/one-aaa111" {
		t.Errorf("the SAME pass must delete the delivery branch, deleted = %v", gh.deleted)
	}
	if got, _, _ := ledger.ByID("dlv_1"); got.MergedAt == nil {
		t.Errorf("MergedAt must be mirrored onto the ledger: %+v", got)
	}
	if left, _ := prs.List(); len(left) != 0 {
		t.Errorf("the pool entry must be gone, left %+v", left)
	}
	r, _, _ := res.Get("exec_1")
	if r.MergedAt == nil {
		t.Errorf("B-8: all deliveries terminal ⇒ Result.MergedAt set, got %+v", r)
	}
	pub.mu.Lock()
	ticks := len(pub.topics)
	pub.mu.Unlock()
	if ticks == 0 {
		t.Error("a ledger change must tick the deliveries topic")
	}
}

// TestMaintainDetectsHumanMerge: a PR merged by a human between ticks is finalized the same
// way — mirrored (with GitHub's merge time), pruned, untracked.
func TestMaintainDetectsHumanMerge(t *testing.T) {
	armMaintain(t)
	gh := newFakeGH()
	ledger, prs, res, n, pub := tempLedger(t), tempPRs(t), tempResults(t), tempNotices(t), &fakePub{}
	d := runs.Delivery{ID: "dlv_1", Repo: "o/x", Branch: "fix/one-aaa111", FromCommit: "c0", ToCommit: "c1", PRNumber: 5, CreatedAt: t0, ExecutionID: "exec_1"}
	_ = ledger.Put(d)
	_ = prs.Add(trackedPR(d, time.Now().Add(24*time.Hour))) // NOT overdue — merged by hand
	mergedAt := t0.Add(2 * time.Hour)
	gh.prState["o/x|5"] = PRState{Number: 5, State: "closed", Merged: true, MergedAt: &mergedAt, HeadRef: d.Branch, HeadSHA: "c1"}

	if err := Maintain(context.Background(), gh, prs, ledger, res, n, pub); err != nil {
		t.Fatalf("Maintain: %v", err)
	}
	if len(gh.mergeCalls) != 0 {
		t.Errorf("a merged PR must not be re-merged, calls = %v", gh.mergeCalls)
	}
	got, _, _ := ledger.ByID("dlv_1")
	if got.MergedAt == nil || !got.MergedAt.Equal(mergedAt) {
		t.Errorf("the ledger must carry GitHub's merge time, got %+v", got.MergedAt)
	}
	if len(gh.deleted) != 1 {
		t.Errorf("the delivery branch must be pruned, deleted = %v", gh.deleted)
	}
}

// TestMaintainBranchAlreadyGone: "the branch no longer exists" is SUCCESS (404 = Satisfied) —
// the record completes normally.
func TestMaintainBranchAlreadyGone(t *testing.T) {
	armMaintain(t)
	gh := newFakeGH()
	ledger, prs, res, n, pub := tempLedger(t), tempPRs(t), tempResults(t), tempNotices(t), &fakePub{}
	d := runs.Delivery{ID: "dlv_1", Repo: "o/x", Branch: "fix/one-aaa111", PRNumber: 5, CreatedAt: t0}
	_ = ledger.Put(d)
	_ = prs.Add(trackedPR(d, time.Now().Add(-time.Hour)))
	gh.prState["o/x|5"] = PRState{Number: 5, State: "open", HeadRef: d.Branch, HeadSHA: "c1"}
	gh.deleteErr = &github.StatusError{Status: 404, Msg: "gone"}

	if err := Maintain(context.Background(), gh, prs, ledger, res, n, pub); err != nil {
		t.Fatalf("a 404 on the prune is Satisfied, got %v", err)
	}
	if left, _ := prs.List(); len(left) != 0 {
		t.Errorf("the record must complete despite the missing branch, left %+v", left)
	}
}

// TestMaintainNeverPrunesWorkbench: mercury-dev never falls under the prune — even when a
// corrupt record names it.
func TestMaintainNeverPrunesWorkbench(t *testing.T) {
	armMaintain(t)
	gh := newFakeGH()
	ledger, prs, res, n, pub := tempLedger(t), tempPRs(t), tempResults(t), tempNotices(t), &fakePub{}
	d := runs.Delivery{ID: "dlv_1", Repo: "o/x", Branch: workbench.Branch, PRNumber: 5, CreatedAt: t0}
	_ = ledger.Put(d)
	_ = prs.Add(trackedPR(d, time.Now().Add(-time.Hour)))
	gh.prState["o/x|5"] = PRState{Number: 5, State: "open", HeadRef: workbench.Branch, HeadSHA: "c1"}

	if err := Maintain(context.Background(), gh, prs, ledger, res, n, pub); err != nil {
		t.Fatalf("Maintain: %v", err)
	}
	for _, del := range gh.deleted {
		if strings.HasSuffix(del, "|"+workbench.Branch) {
			t.Fatalf("%s must NEVER be deleted, deleted = %v", workbench.Branch, gh.deleted)
		}
	}
	if left, _ := prs.List(); len(left) != 0 {
		t.Errorf("the merge itself still completes, left %+v", left)
	}
}

// TestMaintainCreationOrder: PRs of one repo merge strictly in creation order — a failing
// older PR gates the younger one this tick.
func TestMaintainCreationOrder(t *testing.T) {
	armMaintain(t)
	stubClassification(t)
	gh := newFakeGH()
	ledger, prs, res, n, pub := tempLedger(t), tempPRs(t), tempResults(t), tempNotices(t), &fakePub{}
	d1 := runs.Delivery{ID: "dlv_1", Repo: "o/x", Branch: "fix/one-aaa111", PRNumber: 5, CreatedAt: t0}
	d2 := runs.Delivery{ID: "dlv_2", Repo: "o/x", Branch: "fix/two-bbb222", PRNumber: 6, CreatedAt: t0.Add(time.Minute)}
	_ = ledger.Put(d1)
	_ = ledger.Put(d2)
	_ = prs.Add(trackedPR(d1, time.Now().Add(-time.Hour)))
	_ = prs.Add(trackedPR(d2, time.Now().Add(-time.Hour)))
	gh.prState["o/x|5"] = PRState{Number: 5, State: "open", HeadRef: d1.Branch, HeadSHA: "c1"}
	gh.prState["o/x|6"] = PRState{Number: 6, State: "open", HeadRef: d2.Branch, HeadSHA: "c2"}
	gh.mergeErr = errors.New("required status check pending")

	_ = Maintain(context.Background(), gh, prs, ledger, res, n, pub)
	if len(gh.mergeCalls) != 1 || !strings.HasPrefix(gh.mergeCalls[0], "o/x|5|") {
		t.Fatalf("only the OLDER PR may be attempted, calls = %v", gh.mergeCalls)
	}
}

// TestMaintainBlockedSkipped (F4): an honestly blocked record gets NO GitHub read and no
// merge attempt until an explicit resume clears it — and it gates its repo's queue.
func TestMaintainBlockedSkipped(t *testing.T) {
	gh := newFakeGH()
	ledger, prs, res, n, pub := tempLedger(t), tempPRs(t), tempResults(t), tempNotices(t), &fakePub{}
	d := runs.Delivery{ID: "dlv_1", Repo: "o/x", Branch: "fix/one-aaa111", PRNumber: 5, CreatedAt: t0}
	_ = ledger.Put(d)
	p := trackedPR(d, time.Now().Add(-time.Hour))
	p.Blocked, p.BlockedReason = true, "merge failed permanently"
	_ = prs.Add(p)

	if err := Maintain(context.Background(), gh, prs, ledger, res, n, pub); err != nil {
		t.Fatalf("Maintain: %v", err)
	}
	if gh.getPRCalls != 0 || len(gh.mergeCalls) != 0 {
		t.Errorf("a blocked record must be skipped before any GitHub call: reads=%d merges=%v", gh.getPRCalls, gh.mergeCalls)
	}
	if left, _ := prs.List(); len(left) != 1 || !left[0].Blocked {
		t.Errorf("the blocked record must stay, left %+v", left)
	}
}

// TestMaintainBackoffThenBlock (K-5): a transient fault backs off with growing intervals at
// the PR record; exhausted attempts block it honestly (reason, time, attempts) and the
// blockade is surfaced as a notice.
func TestMaintainBackoffThenBlock(t *testing.T) {
	armMaintain(t)
	stubClassification(t)
	gh := newFakeGH()
	ledger, prs, res, n, pub := tempLedger(t), tempPRs(t), tempResults(t), tempNotices(t), &fakePub{}
	d := runs.Delivery{ID: "dlv_1", Repo: "o/x", Branch: "fix/one-aaa111", PRNumber: 5, CreatedAt: t0}
	_ = ledger.Put(d)
	_ = prs.Add(trackedPR(d, time.Now().Add(-time.Hour)))
	gh.getPRErr = errors.New("dial tcp: connection refused")

	// First fault: a backoff appears, not yet blocked.
	_ = Maintain(context.Background(), gh, prs, ledger, res, n, pub)
	cur, _ := prs.List()
	if cur[0].Backoff == nil || cur[0].Backoff.Attempts != 1 || cur[0].Blocked {
		t.Fatalf("first transient fault must back off, got %+v", cur[0])
	}
	prevNext := cur[0].Backoff.NextAt

	// Drive the record to exhaustion (the backoff gate is bypassed by rewinding NextAt).
	for i := 0; i < maintainMaxAttempts; i++ {
		_, _ = prs.Update("o/x", 5, func(p *runs.PendingPR) { p.Backoff.NextAt = time.Now().Add(-time.Second) })
		_ = Maintain(context.Background(), gh, prs, ledger, res, n, pub)
	}
	cur, _ = prs.List()
	if !cur[0].Blocked || cur[0].BlockedReason == "" || cur[0].BlockedAt.IsZero() {
		t.Fatalf("exhausted attempts must block honestly (reason/time/attempts), got %+v", cur[0])
	}
	if cur[0].Backoff.Attempts < maintainMaxAttempts {
		t.Errorf("attempts = %d, want ≥ %d", cur[0].Backoff.Attempts, maintainMaxAttempts)
	}
	if !cur[0].Backoff.NextAt.After(prevNext.Add(-time.Hour)) {
		t.Errorf("the backoff must carry growing intervals")
	}
	notes, _ := n.List()
	found := false
	for _, note := range notes {
		if note.Kind == "delivery-blocked" && strings.Contains(note.Reason, "o/x") {
			found = true
		}
	}
	if !found {
		t.Errorf("the blockade must surface as a notice, got %+v", notes)
	}
}

// TestMaintainOriginStatuses (REQ-033): the status pass derives SOLELY from the ledger — a
// recorded head passes, a hand PR and a namesake branch whose head moved both fail with the
// explained path.
func TestMaintainOriginStatuses(t *testing.T) {
	armMaintain(t)
	gh := newFakeGH()
	ledger, prs, res, n, pub := tempLedger(t), tempPRs(t), tempResults(t), tempNotices(t), &fakePub{}
	_ = ledger.Put(runs.Delivery{ID: "dlv_1", Repo: "o/x", Branch: "fix/one-aaa111", FromCommit: "c0", ToCommit: "c1", PRNumber: 5, CreatedAt: t0})
	gh.listOpen["o/x"] = []PRState{
		{Number: 5, State: "open", HeadRef: "fix/one-aaa111", HeadSHA: "c1"},  // recorded delivery
		{Number: 9, State: "open", HeadRef: "hand-branch", HeadSHA: "h1"},     // hand PR, no ledger entry
		{Number: 10, State: "open", HeadRef: "fix/one-aaa111", HeadSHA: "h2"}, // namesake head, moved past the span
	}

	if err := Maintain(context.Background(), gh, prs, ledger, res, n, pub); err != nil {
		t.Fatalf("Maintain: %v", err)
	}
	var got = map[string]string{}
	for _, s := range gh.statuses {
		parts := strings.SplitN(s, "|", 5)
		got[parts[1]] = parts[3] + "|" + parts[4]
	}
	if v := got["c1"]; !strings.HasPrefix(v, "success|") {
		t.Errorf("the recorded delivery head must pass, got %q", v)
	}
	if v := got["h1"]; !strings.HasPrefix(v, "failure|") || !strings.Contains(v, "todo in Mercury") {
		t.Errorf("the hand PR must fail with the explained path, got %q", v)
	}
	if v := got["h2"]; !strings.HasPrefix(v, "failure|") {
		t.Errorf("a namesake branch without its own record must fail, got %q", v)
	}
}

// TestSettleExecution pins B-8 in isolation: one open delivery keeps the result open; closing
// the last one sets MergedAt to the LAST terminal time.
func TestSettleExecution(t *testing.T) {
	ledger, res := tempLedger(t), tempResults(t)
	m1 := t0.Add(time.Hour)
	_ = ledger.Put(runs.Delivery{ID: "dlv_1", Repo: "o/x", Branch: "fix/one-aaa111", CreatedAt: t0, ExecutionID: "exec_1", MergedAt: &m1})
	_ = ledger.Put(runs.Delivery{ID: "dlv_2", Repo: "o/y", Branch: "fix/two-bbb222", CreatedAt: t0, ExecutionID: "exec_1"})
	_ = res.Put(runs.Result{ID: "exec_1", RunID: "run_1", Kind: model.KindTodo, StartedAt: t0})

	if err := SettleExecution(ledger, res, "exec_1"); err != nil {
		t.Fatal(err)
	}
	if r, _, _ := res.Get("exec_1"); r.MergedAt != nil {
		t.Fatalf("an open delivery must keep the execution unsettled, got %+v", r.MergedAt)
	}

	m2 := t0.Add(3 * time.Hour)
	d2, _, _ := ledger.ByID("dlv_2")
	d2.ClosedAt = &m2
	d2.ClosedReason = "rolled back by tester"
	_ = ledger.Put(d2)

	if err := SettleExecution(ledger, res, "exec_1"); err != nil {
		t.Fatal(err)
	}
	r, _, _ := res.Get("exec_1")
	if r.MergedAt == nil || !r.MergedAt.Equal(m2) {
		t.Fatalf("MergedAt must be the LAST terminal time %v, got %v", m2, r.MergedAt)
	}
}

// ── Emergency override (REQ-033.4) ───────────────────────────────────────────────────────

// TestAdminOverride: the incident carries who/when/PR/why; an unnamed reason or actor is
// refused.
func TestAdminOverride(t *testing.T) {
	n := tempNotices(t)
	pr := model.PRRef{Number: 12, URL: "https://github.example/o/x/pull/12"}
	if err := AdminOverride(context.Background(), n, model.Actor{User: "alice"}, pr, "hotfix for the broken login"); err != nil {
		t.Fatalf("AdminOverride: %v", err)
	}
	notes, _ := n.List()
	if len(notes) != 1 || notes[0].Kind != "admin-override" {
		t.Fatalf("notices = %+v", notes)
	}
	for _, want := range []string{"alice", "#12", "hotfix for the broken login", "pull/12"} {
		if !strings.Contains(notes[0].Reason, want) {
			t.Errorf("the incident must carry %q, got %q", want, notes[0].Reason)
		}
	}
	if err := AdminOverride(context.Background(), n, model.Actor{User: "alice"}, pr, "  "); err == nil {
		t.Error("an override without a reason must be refused")
	}
	if err := AdminOverride(context.Background(), n, model.Actor{}, pr, "reason"); err == nil {
		t.Error("an override without an actor must be refused")
	}
}

// ── Branch naming (REQ-026) ──────────────────────────────────────────────────────────────

// TestBranchNameForm pins the uniform naming: <fix|feature>/<description>-<uniq>, lowercase,
// underscores for spaces, collision-proof via the unique suffix; the description call's
// result is what lands in the ref (never discarded).
func TestBranchNameForm(t *testing.T) {
	form := regexp.MustCompile(`^(fix|feature)/[a-z0-9_.-]+$`)
	name1 := runs.BranchName(runs.BranchKindFix, "Passive Comment Pool", runs.NewBranchToken())
	name2 := runs.BranchName(runs.BranchKindFix, "Passive Comment Pool", runs.NewBranchToken())
	for _, n := range []string{name1, name2} {
		if !form.MatchString(n) {
			t.Errorf("branch %q does not follow <kind>/<description>-<uniq>", n)
		}
		if strings.Contains(n, " ") {
			t.Errorf("branch %q carries a space", n)
		}
		if !strings.Contains(n, "passive_comment_pool") {
			t.Errorf("branch %q must carry the slugified description", n)
		}
	}
	if name1 == name2 {
		t.Errorf("two firings over the same description must not collide: %q", name1)
	}
	// The reversal branch follows the same form and is DETERMINISTIC per delivery.
	d := runs.Delivery{ID: "dlv_abc123", Branch: "fix/one-aaa111"}
	r1, r2 := reversalBranchFor(d), reversalBranchFor(d)
	if r1 != r2 {
		t.Errorf("the reversal branch must be deterministic, got %q vs %q", r1, r2)
	}
	if !form.MatchString(r1) || !strings.HasPrefix(r1, "fix/revert_") {
		t.Errorf("reversal branch %q must follow the fix/revert_… form", r1)
	}
	if workbench.Branch == runs.BranchName(runs.BranchKindFix, workbench.Branch, "") {
		t.Errorf("%s must not be producible by the naming scheme", workbench.Branch)
	}
}

// TestPostOriginStatus pins the architecture surface: the PR's repo resolves from its URL,
// the verdict derives solely from the ledger, and the status lands on the head COMMIT.
func TestPostOriginStatus(t *testing.T) {
	gh := newFakeGH()
	ledger := tempLedger(t)
	_ = ledger.Put(runs.Delivery{ID: "dlv_1", Repo: "o/x", Branch: "fix/one-aaa111", FromCommit: "c0", ToCommit: "c1", PRNumber: 5, CreatedAt: t0})
	gh.prState["o/x|5"] = PRState{Number: 5, State: "open", HeadRef: "fix/one-aaa111", HeadSHA: "c1"}

	pr := model.PRRef{Number: 5, URL: "https://github.example/o/x/pull/5", HeadBranch: "fix/one-aaa111"}
	if err := PostOriginStatus(context.Background(), gh, ledger, pr); err != nil {
		t.Fatalf("PostOriginStatus: %v", err)
	}
	found := false
	for _, s := range gh.statuses {
		if strings.HasPrefix(s, "o/x|c1|"+OriginStatusContext+"|success|") {
			found = true
		}
	}
	if !found {
		t.Errorf("the recorded delivery's head must carry a success status, got %v", gh.statuses)
	}
	// An unresolvable PR (no URL, unknown branch) is a named error, never a silent skip.
	if err := PostOriginStatus(context.Background(), gh, ledger, model.PRRef{Number: 9}); err == nil {
		t.Error("an unresolvable PR must be refused by name")
	}
}

// ── K-6 grep: exactly ONE caller of github.CreatePullRequest, in deliver ─────────────────

// TestSingleCreatePullRequestCaller walks the backend sources: github.CreatePullRequest is
// called in exactly one file — deliver/github.go (this package's production adapter).
func TestSingleCreatePullRequestCaller(t *testing.T) {
	root := filepath.Clean(filepath.Join("..", ".."))
	var callers []string
	err := filepath.WalkDir(root, func(path string, dEntry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if dEntry.IsDir() {
			name := dEntry.Name()
			if name == "node_modules" || name == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		b, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		src := string(b)
		if strings.Contains(src, "github.CreatePullRequest(") {
			callers = append(callers, path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(callers) != 1 || !strings.HasSuffix(filepath.ToSlash(callers[0]), "internal/deliver/github.go") {
		t.Fatalf("github.CreatePullRequest must have exactly ONE caller (deliver/github.go), found %v", callers)
	}
}

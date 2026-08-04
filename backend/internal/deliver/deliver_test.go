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

	budget        RateBudget // the observed request budget the maintenance consults before reading
	budgetPerRead int        // >0: every read draws this many requests from the window, as GitHub's real headers do
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
	f.prState[key(repo, n)] = PRState{Number: n, State: "open", HeadRef: head, HeadSHA: "sha-" + head, BaseRef: base}
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
	// A real response — success OR failure — carries the window's updated headers, so a read draws
	// the budget down either way. This is what lets a test model the token's finite hourly window.
	if f.budgetPerRead > 0 && f.budget.Known {
		f.budget.Remaining -= f.budgetPerRead
	}
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
	// Model GitHub: deleting a branch re-targets every OPEN pull request based on it. Real GitHub
	// re-targets onto the deleted branch's own base; for a linear delivery stack that resolves to the
	// default branch, which is what the maintenance relies on to merge the next pull request correctly.
	for k, st := range f.prState {
		if strings.HasPrefix(k, repo+"|") && st.State == "open" && st.BaseRef == branch {
			st.BaseRef = f.defaultBranch
			f.prState[k] = st
		}
	}
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

func (f *fakeGH) RateBudget() RateBudget {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.budget
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
	advanceBackoff = func(b model.Backoff, now time.Time, maxAttempts int, maxDelay time.Duration) (model.Backoff, bool) {
		b.Attempts++
		b.LastAt = now
		if b.FirstAt.IsZero() {
			b.FirstAt = now
		}
		iv := time.Duration(b.Attempts) * time.Minute
		if iv > maxDelay {
			iv = maxDelay
		}
		b.NextAt = now.Add(iv)
		return b, maxAttempts > 0 && b.Attempts >= maxAttempts
	}
	t.Cleanup(func() { classify, advanceBackoff = oldClassify, oldNext })
}

var t0 = time.Date(2026, 7, 27, 0, 0, 0, 0, time.UTC)

// ── Stacked bases (REQ-024) ──────────────────────────────────────────────────────────────

// TestNextPRBase pins the stacking rule: the most recent open delivery's branch OTHER THAN head
// is the next base; without one the default branch is; empty, head, and workbench branches are
// never bases.
func TestNextPRBase(t *testing.T) {
	if got := NextPRBase(nil, "fix/head-zzz999", "main"); got != "main" {
		t.Errorf("empty stack: base = %q, want main", got)
	}
	open := []runs.Delivery{
		{ID: "d1", Branch: "fix/first-aaa111"},
		{ID: "d2", Branch: "fix/second-bbb222"},
	}
	if got := NextPRBase(open, "fix/head-zzz999", "main"); got != "fix/second-bbb222" {
		t.Errorf("base = %q, want the LAST open delivery's branch", got)
	}
	open = append(open, runs.Delivery{ID: "d3", Branch: ""}, runs.Delivery{ID: "d4", Branch: workbench.LegacyShared})
	if got := NextPRBase(open, "fix/head-zzz999", "main"); got != "fix/second-bbb222" {
		t.Errorf("base = %q — empty and %s branches must be skipped", got, workbench.LegacyShared)
	}
}

// TestNextPRBaseNeverHead is the regression for the self-referential PR (GitHub 422 "No commits
// between X and X", measured 2026-08-01 in notify): the branch being delivered is the NEWEST open
// delivery — it was just written to the ledger — yet NextPRBase must never return it as its own
// base. It skips every same-branch open record and stacks on the one before, or the default when
// nothing else is open.
func TestNextPRBaseNeverHead(t *testing.T) {
	head := "fix/self-cmlmg5"
	// The head is the single newest open delivery: no other work is open, so the base is the
	// DEFAULT branch — never the head itself.
	own := []runs.Delivery{{ID: "d_self", Branch: head}}
	if got := NextPRBase(own, head, "main"); got != "main" {
		t.Fatalf("self-referential base = %q — the own branch must never be its own base; want main", got)
	}
	if got := NextPRBase(own, head, "main"); got == head {
		t.Fatalf("base equals head (%q) — a PR would be opened against itself (422)", got)
	}
	// The head is the newest, but an EARLIER task is still open: the base is that earlier task's
	// branch, and the head is skipped even though it sits at the top of the ledger.
	stack := []runs.Delivery{
		{ID: "d_prior", Branch: "fix/prior-aaa111"},
		{ID: "d_self", Branch: head},
	}
	if got := NextPRBase(stack, head, "main"); got != "fix/prior-aaa111" {
		t.Fatalf("base = %q — head must be skipped and the prior open delivery chosen", got)
	}
	// Two open records share the head branch (a re-run wrote a second intent for the same branch):
	// BOTH are skipped, so the base is still the prior task's branch.
	dupes := []runs.Delivery{
		{ID: "d_prior", Branch: "fix/prior-aaa111"},
		{ID: "d_self_1", Branch: head},
		{ID: "d_self_2", Branch: head},
	}
	if got := NextPRBase(dupes, head, "main"); got != "fix/prior-aaa111" {
		t.Fatalf("base = %q — every same-branch open record must be skipped", got)
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
	// The first delivery is the only open one and it IS the head — so it bases on main, never
	// on itself.
	openNow, _ := ledger.Open("o/x")
	if len(openNow) != 1 {
		t.Fatal("precondition: one open delivery")
	}
	base1 := NextPRBase(openNow, first.Branch, "main")
	if base1 != "main" {
		t.Fatalf("first delivery base = %q, want main (it must not base on itself)", base1)
	}
	if _, _, err := OpenOrAdoptPR(ctx, gh, ledger, PRIn{Repo: "o/x", Head: first.Branch, Base: base1, Title: "one", DeliveryID: "dlv_1"}); err != nil {
		t.Fatal(err)
	}

	second := runs.Delivery{ID: "dlv_2", Repo: "o/x", Branch: "fix/two-bbb222", FromCommit: "c1", ToCommit: "c2", CreatedAt: t0.Add(time.Hour), ExecutionID: "exec_2"}
	if err := ledger.Put(second); err != nil {
		t.Fatal(err)
	}
	// The second task's PR bases on the FIRST task's branch: NextPRBase excludes the second's own
	// branch (head) though it is the newest open delivery, and returns the one before it. No manual
	// filtering — the function does the exclusion.
	open, _ := ledger.Open("o/x")
	base2 := NextPRBase(open, second.Branch, "main")
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
	if _, _, err := OpenOrAdoptPR(context.Background(), gh, ledger, PRIn{Repo: "o/x", Head: workbench.LegacyShared, Base: "main"}); err == nil {
		t.Errorf("%s must never become a pull request", workbench.LegacyShared)
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

// TestRollbackUnlandedDissolves (REQ-025.4): a delivery that NEVER landed — deliver-dev failed, no
// PR, nothing on the dev branch, so the reverse conflicts — is DISSOLVED off the stack. "0 later
// open deliveries" is not a conflict: no manual-work todo is raised, the record settles as rolled
// back, and the failed tip is gone.
func TestRollbackUnlandedDissolves(t *testing.T) {
	gh := newFakeGH()
	ledger := tempLedger(t)
	rs := tempRuns(t)
	// A sound merged layer beneath, then A's failed layer whose work never reached the dev branch.
	mergedAt := t0
	_ = ledger.Put(runs.Delivery{ID: "dlv_base", Repo: "o/x", Branch: "fix/base-000", FromCommit: "c0", ToCommit: "base0", PRNumber: 4, CreatedAt: t0, MergedAt: &mergedAt})
	if err := RecordFailedDelivery(ledger, FailedDeliveryIn{
		DeliveryID: "dlv_a", ExecutionID: "exec_a", Repo: "o/x",
		Branch: "fix/a-aaa111", FromCommit: "base0", ToCommit: "workA", Reason: "deliver-dev stage failed",
	}); err != nil {
		t.Fatal(err)
	}
	// A manual counter-booking todo was left behind by an EARLIER (buggy) rollback of this same
	// delivery — the exact residue of run_qixaesuafucrxhnq/run_7ifbgcepeeh2f7vu. Dissolving must
	// retire it (self-healing), not leave it describing work the chain now does itself.
	staleTodo := buildRollbackTodo(runs.Delivery{ID: "dlv_a", Repo: "o/x", Branch: "fix/a-aaa111"}, nil, nil, model.Actor{User: "tester"}, t0)
	if err := rs.Put(staleTodo); err != nil {
		t.Fatal(err)
	}
	// The reverse of work that never landed on dev conflicts — the git side reports it.
	gh.cb = CounterBookResult{Conflicted: true, ConflictFiles: []string{"main.go"}, DefaultBranch: "main"}

	out, err := Rollback(context.Background(), gh, ledger, rs, "dlv_a", model.Actor{User: "tester"})
	if err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	if out.ConflictTodoID != "" {
		t.Fatalf("an unlanded delivery must NOT raise a manual-work todo, got %q", out.ConflictTodoID)
	}
	if todos, _ := rs.List(); len(todos) != 0 {
		t.Errorf("the obsolete counter-booking todo must be retired and none raised, got %d", len(todos))
	}
	if !strings.Contains(out.Detail, "retired 1 obsolete counter-booking todo") || !strings.Contains(out.Detail, staleTodo.ID) {
		t.Errorf("the outcome must name the retired todo, got %q", out.Detail)
	}
	if strings.Contains(out.Detail, "0 later") || strings.Contains(strings.ToLower(out.Detail), "conflict") {
		t.Errorf("the outcome must not read as a conflict, got %q", out.Detail)
	}
	if !strings.Contains(strings.ToLower(out.Detail), "never landed") {
		t.Errorf("the outcome must name the dissolution, got %q", out.Detail)
	}
	d, _, _ := ledger.ByID("dlv_a")
	if d.Failed() || d.ClosedAt == nil || d.FailedAt != nil {
		t.Fatalf("the dissolved delivery must be settled (closed) and no longer failed, got %+v", d)
	}
	if !strings.Contains(d.ClosedReason, "rolled back by tester") {
		t.Errorf("the record must carry the rollback reason, got %q", d.ClosedReason)
	}
	// The tip is sound again: the merged layer beneath holds no follower.
	if tip, _ := FailedTip(ledger, "o/x"); tip != nil {
		t.Errorf("after the dissolution the tip must be sound, got %+v", tip)
	}
	// Nothing was booked, so nothing is re-delivered and no PR is touched (there was none).
	if len(gh.closeCalls) != 0 {
		t.Errorf("a delivery that never landed has no PR to close, got %v", gh.closeCalls)
	}
	if len(gh.redelivered) != 0 {
		t.Errorf("dissolution changes nothing on dev, so no re-delivery, got %v", gh.redelivered)
	}
}

// TestRollbackConflictDetailNamesLaterWork: when a real conflict remains (later open work builds on
// the delivery), the outcome names the concrete colliding deliveries — never a bare "0".
func TestRollbackConflictDetailNamesLaterWork(t *testing.T) {
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
		t.Fatalf("later open work must raise a conflict todo, got %+v", out)
	}
	if !strings.Contains(out.Detail, "dlv_2") {
		t.Errorf("the outcome must name the colliding later delivery, got %q", out.Detail)
	}
	if strings.Contains(out.Detail, "0 later") {
		t.Errorf("the outcome must never read as a bare 0, got %q", out.Detail)
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
	// WHAT-1: a merge is NOT the end of the chain — production is. The merge pass mirrors MergedAt onto
	// the ledger and drops the pool entry, but the execution stays OUT of the history until its
	// production step succeeds (the separate deliver.MaintainProd pass), so Result.MergedAt is still nil.
	r, _, _ := res.Get("exec_1")
	if r.MergedAt != nil {
		t.Errorf("a merged delivery still owes production, so its execution must not settle on merge: %+v", r)
	}
	if got, _, _ := ledger.ByID("dlv_1"); !got.NeedsProd() {
		t.Errorf("after the merge the delivery must owe production, got %+v", got)
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

// TestMaintainMergedIntoNonDefaultIsNotDelivered is the NACHWEIS for point 5: a pull request GitHub
// reports as MERGED, but into a branch that is NOT the default branch, did NOT reach main. Its content
// sits on a stale stacked base. The ledger must NEVER read that as delivered — the 2026-08-04 gap,
// where four deliveries stood on "merged" with a production stamp while main carried none of their
// work. The delivery is marked FAILED (it becomes the repo's tip so the stack halts), the finding is
// delivered, and nothing is pruned (the delivery branch is the only remaining copy of the work).
func TestMaintainMergedIntoNonDefaultIsNotDelivered(t *testing.T) {
	armMaintain(t)
	stubClassification(t)
	gh := newFakeGH() // defaultBranch = "main"
	ledger, prs, res, n, pub := tempLedger(t), tempPRs(t), tempResults(t), tempNotices(t), &fakePub{}
	d := runs.Delivery{ID: "dlv_1", Repo: "o/x", Branch: "fix/one-aaa111", FromCommit: "c0", ToCommit: "c1", PRNumber: 5, CreatedAt: t0, ExecutionID: "exec_1"}
	_ = ledger.Put(d)
	_ = prs.Add(trackedPR(d, time.Now().Add(24*time.Hour))) // NOT overdue — merged by hand, onto the wrong base
	mergedAt := t0.Add(2 * time.Hour)
	// Merged — but into a sibling delivery branch that was never pruned, not into the default branch.
	gh.prState["o/x|5"] = PRState{Number: 5, State: "closed", Merged: true, MergedAt: &mergedAt, HeadRef: d.Branch, HeadSHA: "c1", BaseRef: "fix/stale-predecessor"}

	if err := Maintain(context.Background(), gh, prs, ledger, res, n, pub); err != nil {
		t.Fatalf("Maintain: %v", err)
	}
	got, _, _ := ledger.ByID("dlv_1")
	if got.MergedAt != nil {
		t.Errorf("a pull request merged into a non-default branch must NOT be recorded as merged: %+v", got.MergedAt)
	}
	if !got.Failed() {
		t.Errorf("the delivery must be marked FAILED (its work never reached main), got %+v", got)
	}
	if !strings.Contains(got.FailedReason, "non-default branch") {
		t.Errorf("the failure reason must name the misdelivery, got %q", got.FailedReason)
	}
	// It becomes the repo's tip — the stack halts on it until the work is re-delivered onto main.
	if tip, _ := FailedTip(ledger, "o/x"); tip == nil || tip.ID != "dlv_1" {
		t.Errorf("the misdelivered delivery must become the failed tip, got %+v", tip)
	}
	// The finding is delivered outward (a disturbance by the default rule).
	notes, _ := n.List()
	var found bool
	for _, note := range notes {
		if note.Kind == runs.NoticeMisdelivered {
			found = true
		}
	}
	if !found {
		t.Errorf("a misdelivery must raise the %q notice, got %+v", runs.NoticeMisdelivered, notes)
	}
	// Nothing is pruned: the delivery branch is the only remaining copy of work that never reached main.
	if len(gh.deleted) != 0 {
		t.Errorf("a misdelivered branch must NOT be pruned, deleted = %v", gh.deleted)
	}
	// GitHub reports it merged; there is nothing left to poll, so tracking drops it.
	if left, _ := prs.List(); len(left) != 0 {
		t.Errorf("the misdelivered pull request must be dropped from tracking, left %+v", left)
	}
}

// TestMaintainHoldsMergeUntilBaseIsDefault is the NACHWEIS for point 4's PREVENT half: the chain
// refuses to merge an open pull request whose base is still a sibling delivery branch, not the
// default branch. Merging it there would land the work on a stale base and never reach main. The
// repo is held and the finding reported; the merge waits until the predecessor prunes and GitHub
// re-targets this pull request onto the default branch.
func TestMaintainHoldsMergeUntilBaseIsDefault(t *testing.T) {
	armMaintain(t)
	stubClassification(t)
	gh := newFakeGH() // defaultBranch = "main"
	ledger, prs, res, n, pub := tempLedger(t), tempPRs(t), tempResults(t), tempNotices(t), &fakePub{}
	d := runs.Delivery{ID: "dlv_1", Repo: "o/x", Branch: "fix/two-bbb222", PRNumber: 6, CreatedAt: t0, ExecutionID: "exec_1"}
	_ = ledger.Put(d)
	_ = prs.Add(trackedPR(d, time.Now().Add(-time.Hour))) // overdue — a healthy pass WOULD merge it
	gh.prState["o/x|6"] = PRState{Number: 6, State: "open", HeadRef: d.Branch, HeadSHA: "c2", BaseRef: "fix/one-aaa111"}

	if err := Maintain(context.Background(), gh, prs, ledger, res, n, pub); err != nil {
		t.Fatalf("Maintain: %v", err)
	}
	if len(gh.mergeCalls) != 0 {
		t.Errorf("a pull request based on a non-default branch must NOT be merged, calls = %v", gh.mergeCalls)
	}
	if len(gh.deleted) != 0 {
		t.Errorf("nothing is pruned while the merge is held, deleted = %v", gh.deleted)
	}
	got, _, _ := ledger.ByID("dlv_1")
	if got.MergedAt != nil || got.Failed() {
		t.Errorf("a HELD pull request is neither delivered nor failed — it simply waits, got %+v", got)
	}
	if left, _ := prs.List(); len(left) != 1 {
		t.Errorf("the held pull request stays tracked, left %+v", left)
	}
	notes, _ := n.List()
	var found bool
	for _, note := range notes {
		if note.Kind == runs.NoticeMisdelivered {
			found = true
		}
	}
	if !found {
		t.Errorf("holding a misdelivery-in-waiting must raise the %q notice, got %+v", runs.NoticeMisdelivered, notes)
	}
}

// TestMaintainStackedMergesOnePerPassThenRetargets is the NACHWEIS for point 4's ORDER: stacked pull
// requests are merged ONE per pass, the base branch is deleted right after, and only the DELETION
// re-targets the next pull request onto the default branch so the following pass merges it correctly.
// Two stacked deliveries: #5 (base main) below #6 (base = #5's branch). Pass 1 merges ONLY #5 and
// prunes its branch, which re-targets #6 onto main; #6 stays open and untouched. Pass 2 merges #6,
// now correctly based on main. No misdelivery is ever recorded — each merge lands on the default branch.
func TestMaintainStackedMergesOnePerPassThenRetargets(t *testing.T) {
	armMaintain(t)
	stubClassification(t)
	gh := newFakeGH() // defaultBranch = "main"
	ledger, prs, res, n, pub := tempLedger(t), tempPRs(t), tempResults(t), tempNotices(t), &fakePub{}
	d1 := runs.Delivery{ID: "dlv_1", Repo: "o/x", Branch: "fix/one-aaa111", PRNumber: 5, CreatedAt: t0, ExecutionID: "exec_1"}
	d2 := runs.Delivery{ID: "dlv_2", Repo: "o/x", Branch: "fix/two-bbb222", PRNumber: 6, CreatedAt: t0.Add(time.Minute), ExecutionID: "exec_2"}
	_ = ledger.Put(d1)
	_ = ledger.Put(d2)
	_ = prs.Add(trackedPR(d1, time.Now().Add(-time.Hour)))
	_ = prs.Add(trackedPR(d2, time.Now().Add(-time.Hour)))
	gh.prState["o/x|5"] = PRState{Number: 5, State: "open", HeadRef: d1.Branch, HeadSHA: "c1", BaseRef: "main"}
	gh.prState["o/x|6"] = PRState{Number: 6, State: "open", HeadRef: d2.Branch, HeadSHA: "c2", BaseRef: d1.Branch}

	// Pass 1: only the older #5 merges; its branch is pruned; #6 is re-targeted onto main and left open.
	if err := Maintain(context.Background(), gh, prs, ledger, res, n, pub); err != nil {
		t.Fatalf("pass 1: %v", err)
	}
	if len(gh.mergeCalls) != 1 || gh.mergeCalls[0] != "o/x|5|merge" {
		t.Fatalf("pass 1 must merge ONLY the older #5, calls = %v", gh.mergeCalls)
	}
	if len(gh.deleted) != 1 || gh.deleted[0] != "o/x|fix/one-aaa111" {
		t.Fatalf("pass 1 must prune #5's branch right after the merge, deleted = %v", gh.deleted)
	}
	if gh.prState["o/x|6"].BaseRef != "main" {
		t.Fatalf("pruning #5's branch must re-target #6 onto main, base = %q", gh.prState["o/x|6"].BaseRef)
	}
	if m1, _, _ := ledger.ByID("dlv_1"); m1.MergedAt == nil {
		t.Errorf("#5 must be recorded merged, got %+v", m1)
	}
	if m2, _, _ := ledger.ByID("dlv_2"); m2.MergedAt != nil || m2.Failed() {
		t.Errorf("#6 must still be open after pass 1, got %+v", m2)
	}

	// Pass 2: #6, now based on main, merges and prunes.
	if err := Maintain(context.Background(), gh, prs, ledger, res, n, pub); err != nil {
		t.Fatalf("pass 2: %v", err)
	}
	if len(gh.mergeCalls) != 2 || gh.mergeCalls[1] != "o/x|6|merge" {
		t.Fatalf("pass 2 must merge #6, calls = %v", gh.mergeCalls)
	}
	if m2, _, _ := ledger.ByID("dlv_2"); m2.MergedAt == nil {
		t.Errorf("#6 must be recorded merged after pass 2, got %+v", m2)
	}
	// No misdelivery anywhere: every merge landed on the default branch.
	notes, _ := n.List()
	for _, note := range notes {
		if note.Kind == runs.NoticeMisdelivered {
			t.Errorf("a correctly stacked merge must raise NO misdelivery notice, got %+v", note)
		}
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

// TestMaintainBranchAlreadyGone422 (NACHWEIS point 5a): GitHub answers the delete of an
// already-gone ref with 422 "Reference does not exist" just as often as with 404 — both mean the
// branch is gone, which IS the goal of the prune. The record completes; it is NOT stilled.
func TestMaintainBranchAlreadyGone422(t *testing.T) {
	armMaintain(t)
	gh := newFakeGH()
	ledger, prs, res, n, pub := tempLedger(t), tempPRs(t), tempResults(t), tempNotices(t), &fakePub{}
	d := runs.Delivery{ID: "dlv_1", Repo: "o/x", Branch: "fix/one-aaa111", PRNumber: 5, CreatedAt: t0}
	_ = ledger.Put(d)
	_ = prs.Add(trackedPR(d, time.Now().Add(-time.Hour)))
	gh.prState["o/x|5"] = PRState{Number: 5, State: "open", HeadRef: d.Branch, HeadSHA: "c1"}
	gh.deleteErr = &github.StatusError{Status: 422, Msg: `github: status 422: {"message":"Reference does not exist"}`}

	if err := Maintain(context.Background(), gh, prs, ledger, res, n, pub); err != nil {
		t.Fatalf("a 422 \"Reference does not exist\" on the prune is Satisfied, got %v", err)
	}
	if left, _ := prs.List(); len(left) != 0 {
		t.Errorf("the record must complete despite the already-gone branch, left %+v", left)
	}
	notes, _ := n.List()
	for _, note := range notes {
		if note.Kind == runs.NoticeDeliveryBlocked {
			t.Errorf("reaching the prune's goal must raise no blockade notice, got %+v", note)
		}
	}
}

// TestMaintainTimeoutBacksOffNotBlocked (NACHWEIS point 5b): a read that times out — the HTTP
// client's own deadline while awaiting an answer — is a SELF-ENDING obstacle. It earns a growing
// backoff, never a standstill. This test runs the REAL classifier (no stub) so it proves the
// distinction where it is actually made.
func TestMaintainTimeoutBacksOffNotBlocked(t *testing.T) {
	armMaintain(t)
	gh := newFakeGH()
	ledger, prs, res, n, pub := tempLedger(t), tempPRs(t), tempResults(t), tempNotices(t), &fakePub{}
	d := runs.Delivery{ID: "dlv_1", Repo: "o/x", Branch: "fix/one-aaa111", PRNumber: 5, CreatedAt: t0}
	_ = ledger.Put(d)
	_ = prs.Add(trackedPR(d, time.Now().Add(-time.Hour)))
	// The GitHub read times out: the client's Client.Timeout fires while awaiting headers, which
	// wraps context.DeadlineExceeded — exactly the FALL-2 error observed in production.
	gh.getPRErr = fmt.Errorf(`Get "https://api.github.com/repos/o/x/pulls/5": %w (Client.Timeout exceeded while awaiting headers)`, context.DeadlineExceeded)

	if err := Maintain(context.Background(), gh, prs, ledger, res, n, pub); err == nil {
		t.Fatalf("a failing read must be reported")
	}
	cur, _ := prs.List()
	if len(cur) != 1 {
		t.Fatalf("the record must stay tracked, got %+v", cur)
	}
	if cur[0].Blocked {
		t.Fatalf("a timeout must NEVER still a record — it ends by itself, got %+v", cur[0])
	}
	if cur[0].Backoff == nil || cur[0].Backoff.NextAt.IsZero() {
		t.Fatalf("a timeout must earn a growing backoff with a next attempt, got %+v", cur[0])
	}
	notes, _ := n.List()
	for _, note := range notes {
		if note.Kind == runs.NoticeDeliveryBlocked {
			t.Errorf("a self-ending timeout must raise no blockade notice, got %+v", note)
		}
	}
}

// TestReviveStaleBlocks (NACHWEIS points 5c + 5d): at service start, a block whose reason the
// corrected classification no longer counts as a fault is lifted — a prune that reached its goal
// (422 "Reference does not exist"), a timeout that ends by itself — while a genuine durable fault
// (a missing right, a gone pull request) stays blocked and still waits for a person.
func TestReviveStaleBlocks(t *testing.T) {
	prs := tempPRs(t)
	block := func(number int, reason string) runs.PendingPR {
		return runs.PendingPR{Repo: "o/x", Number: number, CreatedAt: t0, Blocked: true, BlockedReason: reason, BlockedAt: t0}
	}
	_ = prs.Add(block(5, prunePrefix+`github: status 422: {"message":"Reference does not exist"}`))
	_ = prs.Add(block(6, `reading the pull request failed: Get "https://api.github.com/repos/o/x/pulls/6": context deadline exceeded (Client.Timeout exceeded while awaiting headers)`))
	_ = prs.Add(block(7, `reading the pull request failed: github: status 403: {"message":"Resource not accessible"}`)) // genuine missing right
	_ = prs.Add(block(8, `auto-merge failed: github: status 404: {"message":"Not Found"}`))                             // the pull request is gone

	revived, err := ReviveStaleBlocks(prs)
	if err != nil {
		t.Fatalf("ReviveStaleBlocks: %v", err)
	}
	if revived != 2 {
		t.Fatalf("revived = %d, want 2 (the prune-goal and the timeout)", revived)
	}
	byNumber := map[int]runs.PendingPR{}
	cur, _ := prs.List()
	for _, p := range cur {
		byNumber[p.Number] = p
	}
	if byNumber[5].Blocked {
		t.Errorf("the prune that reached its goal must be lifted, got %+v", byNumber[5])
	}
	if byNumber[6].Blocked {
		t.Errorf("the self-ending timeout must be lifted, got %+v", byNumber[6])
	}
	if !byNumber[7].Blocked {
		t.Errorf("a genuine missing right must stay blocked, got %+v", byNumber[7])
	}
	if !byNumber[8].Blocked {
		t.Errorf("a gone pull request must stay blocked, got %+v", byNumber[8])
	}
}

// TestMaintainNeverPrunesWorkbench: mercury-dev never falls under the prune — even when a
// corrupt record names it.
func TestMaintainNeverPrunesWorkbench(t *testing.T) {
	armMaintain(t)
	gh := newFakeGH()
	ledger, prs, res, n, pub := tempLedger(t), tempPRs(t), tempResults(t), tempNotices(t), &fakePub{}
	d := runs.Delivery{ID: "dlv_1", Repo: "o/x", Branch: workbench.LegacyShared, PRNumber: 5, CreatedAt: t0}
	_ = ledger.Put(d)
	_ = prs.Add(trackedPR(d, time.Now().Add(-time.Hour)))
	gh.prState["o/x|5"] = PRState{Number: 5, State: "open", HeadRef: workbench.LegacyShared, HeadSHA: "c1"}

	if err := Maintain(context.Background(), gh, prs, ledger, res, n, pub); err != nil {
		t.Fatalf("Maintain: %v", err)
	}
	for _, del := range gh.deleted {
		if strings.HasSuffix(del, "|"+workbench.LegacyShared) {
			t.Fatalf("%s must NEVER be deleted, deleted = %v", workbench.LegacyShared, gh.deleted)
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

// TestMaintainTransientNeverBlocks is the NACHWEIS point 7(a): an obstacle that ends by itself is
// retried for ever. After TWENTY failed attempts the record is STILL not stilled — it carries a
// visible, growing backoff (reason, attempts, first seen, next attempt) — and once the obstacle
// passes the very next pass merges it, with no human release in between.
func TestMaintainTransientNeverBlocks(t *testing.T) {
	armMaintain(t)
	stubClassification(t)
	gh := newFakeGH()
	ledger, prs, res, n, pub := tempLedger(t), tempPRs(t), tempResults(t), tempNotices(t), &fakePub{}
	d := runs.Delivery{ID: "dlv_1", Repo: "o/x", Branch: "fix/one-aaa111", PRNumber: 5, CreatedAt: t0, ExecutionID: "exec_1"}
	_ = ledger.Put(d)
	_ = prs.Add(trackedPR(d, time.Now().Add(-time.Hour))) // overdue — a healthy pass WOULD merge it
	gh.prState["o/x|5"] = PRState{Number: 5, State: "open", HeadRef: d.Branch, HeadSHA: "c1"}
	_ = res.Put(runs.Result{ID: "exec_1", RunID: "run_1", Kind: model.KindTodo, StartedAt: t0})
	// A self-ending obstacle: the read keeps failing with a connectivity error (classified Transient).
	gh.getPRErr = errors.New("dial tcp: connection refused")

	const attempts = 20
	var firstSeen time.Time
	for i := 0; i < attempts; i++ {
		// Rewind the backoff gate so each pass actually retries (the growing interval would otherwise
		// hold it back — which is the point, but here we prove the retries never give up).
		_, _ = prs.Update("o/x", 5, func(p *runs.PendingPR) {
			if p.Backoff != nil {
				p.Backoff.NextAt = time.Now().Add(-time.Second)
			}
		})
		if err := Maintain(context.Background(), gh, prs, ledger, res, n, pub); err == nil {
			t.Fatalf("pass %d: a failing read must be reported, got nil", i)
		}
		cur, _ := prs.List()
		if cur[0].Blocked {
			t.Fatalf("a self-ending obstacle must NEVER be stilled — blocked after %d attempts: %+v", i+1, cur[0])
		}
		if cur[0].Backoff == nil {
			t.Fatalf("pass %d: the retry must stay visible as a backoff record, got %+v", i, cur[0])
		}
		if i == 0 {
			firstSeen = cur[0].Backoff.FirstAt
		}
	}
	cur, _ := prs.List()
	// Visible: what klemmt, since when, how often, when next.
	if cur[0].Backoff.Attempts != attempts {
		t.Errorf("attempts = %d, want %d — every retry counted", cur[0].Backoff.Attempts, attempts)
	}
	if cur[0].Backoff.Reason == "" || !cur[0].Backoff.FirstAt.Equal(firstSeen) || cur[0].Backoff.NextAt.IsZero() {
		t.Errorf("the retry must state reason, since-when and next attempt, got %+v", cur[0].Backoff)
	}
	// No blocked NOTICE was ever raised — a self-ending obstacle does not alarm anyone.
	notes, _ := n.List()
	for _, note := range notes {
		if note.Kind == runs.NoticeDeliveryBlocked {
			t.Errorf("a self-ending obstacle must raise no blockade notice, got %+v", note)
		}
	}

	// The obstacle passes — the next pass succeeds on its own, no human release needed.
	gh.getPRErr = nil
	_, _ = prs.Update("o/x", 5, func(p *runs.PendingPR) {
		if p.Backoff != nil {
			p.Backoff.NextAt = time.Now().Add(-time.Second)
		}
	})
	if err := Maintain(context.Background(), gh, prs, ledger, res, n, pub); err != nil {
		t.Fatalf("once the obstacle passes the retry must succeed: %v", err)
	}
	if len(gh.mergeCalls) != 1 || gh.mergeCalls[0] != "o/x|5|merge" {
		t.Fatalf("the recovered pass must merge what is due, merges = %v", gh.mergeCalls)
	}
	if left, _ := prs.List(); len(left) != 0 {
		t.Errorf("the merged pull request must be dropped from tracking, left %+v", left)
	}
}

// TestMaintainPermanentBlocks is the NACHWEIS point 7(b): a DURABLE obstacle — one no repetition can
// change — is stilled at once and waits for an explicit release, and the blockade surfaces as a
// disturbance the user is told about. A missing right on a READ (403) is exactly such an obstacle:
// unlike a vanished pull request (404), "forbidden" is not a reached goal, and the split hangs on the
// status number — so this runs the REAL classifier (no stub) to prove it where the distinction is
// actually made.
func TestMaintainPermanentBlocks(t *testing.T) {
	armMaintain(t)
	gh := newFakeGH()
	ledger, prs, res, n, pub := tempLedger(t), tempPRs(t), tempResults(t), tempNotices(t), &fakePub{}
	d := runs.Delivery{ID: "dlv_1", Repo: "o/x", Branch: "fix/one-aaa111", PRNumber: 5, CreatedAt: t0}
	_ = ledger.Put(d)
	_ = prs.Add(trackedPR(d, time.Now().Add(-time.Hour)))
	// The read is forbidden (403) — a missing right, which a person alone can resolve. This is NOT a
	// vanished pull request: it stays blocked.
	gh.getPRErr = &github.StatusError{Status: 403, Msg: "Resource not accessible by integration"}

	if err := Maintain(context.Background(), gh, prs, ledger, res, n, pub); err == nil {
		t.Fatalf("a permanent fault must be reported")
	}
	cur, _ := prs.List()
	if len(cur) != 1 {
		t.Fatalf("a forbidden read stays tracked and blocked, got %+v", cur)
	}
	if !cur[0].Blocked || cur[0].BlockedReason == "" || cur[0].BlockedAt.IsZero() {
		t.Fatalf("a durable obstacle must be stilled honestly (reason/time), got %+v", cur[0])
	}
	if cur[0].Backoff != nil {
		t.Errorf("a stilled record carries no retry timer — it waits for a person, got %+v", cur[0].Backoff)
	}
	found := false
	notes, _ := n.List()
	for _, note := range notes {
		if note.Kind == runs.NoticeDeliveryBlocked && strings.Contains(note.Reason, "o/x") {
			found = true
		}
	}
	if !found {
		t.Errorf("the blockade must surface as a %q notice", runs.NoticeDeliveryBlocked)
	}
}

// TestMaintainReadGoneUntracksNotBlocks is the NACHWEIS: a pull request GitHub no longer knows (404
// on the READ) is a reached goal, not a blockade. The record is UNTRACKED — dropped from the tracked
// set, its ledger entry mirrored closed so NextPRBase stops counting it open — never stilled for a
// person, and the untracking is announced so nothing vanishes silently. This runs the REAL classifier
// (no stub): 404 would classify Permanent, yet the read-side goal is decided BEFORE recordFault, on
// the status number.
func TestMaintainReadGoneUntracksNotBlocks(t *testing.T) {
	armMaintain(t)
	gh := newFakeGH()
	ledger, prs, res, n, pub := tempLedger(t), tempPRs(t), tempResults(t), tempNotices(t), &fakePub{}
	d := runs.Delivery{ID: "dlv_1", Repo: "o/x", Branch: "fix/one-aaa111", PRNumber: 5, CreatedAt: t0, ExecutionID: "exec_1"}
	_ = ledger.Put(d)
	_ = prs.Add(trackedPR(d, time.Now().Add(-time.Hour)))
	_ = res.Put(runs.Result{ID: "exec_1", RunID: "run_1", Kind: model.KindTodo, StartedAt: t0})
	// The pull request is gone: GitHub answers the READ with 404 (as sxty9/axioma #5 does today).
	gh.getPRErr = &github.StatusError{Status: 404, Msg: "Not Found"}

	if err := Maintain(context.Background(), gh, prs, ledger, res, n, pub); err != nil {
		t.Fatalf("a vanished pull request is a reached goal, not an error: %v", err)
	}
	if left, _ := prs.List(); len(left) != 0 {
		t.Errorf("a vanished pull request must be UNTRACKED, not kept, left %+v", left)
	}
	// The ledger is pulled along so NextPRBase no longer counts it open.
	got, ok, _ := ledger.ByID("dlv_1")
	if !ok || got.OpenState() {
		t.Errorf("the ledger entry must be mirrored closed, got %+v (ok=%v)", got, ok)
	}
	// It is untracked VISIBLY, never silently, and never as a blockade.
	notes, _ := n.List()
	sawUntrack, sawBlock := false, false
	for _, note := range notes {
		if note.Kind == runs.NoticeDeliverySelfCheck && strings.Contains(note.Message(), "no longer exists") {
			sawUntrack = true
		}
		if note.Kind == runs.NoticeDeliveryBlocked {
			sawBlock = true
		}
	}
	if !sawUntrack {
		t.Errorf("the untracking must be announced, notices = %+v", notes)
	}
	if sawBlock {
		t.Errorf("a vanished pull request must raise NO blockade notice, notices = %+v", notes)
	}
}

// TestMaintainReadGoneResolvesExistingBlock is the NACHWEIS point 4: a block ALREADY recorded for a
// vanished pull request (the state sxty9/axioma #5 is in today) dissolves by itself on the next
// maintenance run — the record is untracked, no restart and no human release needed.
func TestMaintainReadGoneResolvesExistingBlock(t *testing.T) {
	armMaintain(t)
	gh := newFakeGH()
	ledger, prs, res, n, pub := tempLedger(t), tempPRs(t), tempResults(t), tempNotices(t), &fakePub{}
	d := runs.Delivery{ID: "dlv_1", Repo: "o/x", Branch: "fix/one-aaa111", PRNumber: 5, CreatedAt: t0}
	_ = ledger.Put(d)
	p := trackedPR(d, time.Now().Add(-time.Hour))
	// The record was stilled by the OLD reading: a read 404 filed as a durable blockade.
	p.Blocked = true
	p.BlockedReason = readFailPrefix + (&github.StatusError{Status: 404, Msg: "Not Found"}).Error()
	p.BlockedAt = t0
	_ = prs.Add(p)

	if err := Maintain(context.Background(), gh, prs, ledger, res, n, pub); err != nil {
		t.Fatalf("resolving a stale vanished-PR block is not an error: %v", err)
	}
	if left, _ := prs.List(); len(left) != 0 {
		t.Errorf("the stale block must be untracked on the next run, left %+v", left)
	}
	// No GitHub read is even needed — the block reason already proves the pull request is gone.
	if gh.getPRCalls != 0 {
		t.Errorf("resolving the stale block needs no GitHub read, reads = %d", gh.getPRCalls)
	}
}

// TestMaintainServerErrorBacksOffNotBlocked is the NACHWEIS point 5c: a read that fails with a server
// error (500) is a SELF-ENDING obstacle. It earns a growing backoff and keeps retrying — never a
// standstill waiting on a person. Runs the REAL classifier where the distinction is made.
func TestMaintainServerErrorBacksOffNotBlocked(t *testing.T) {
	armMaintain(t)
	gh := newFakeGH()
	ledger, prs, res, n, pub := tempLedger(t), tempPRs(t), tempResults(t), tempNotices(t), &fakePub{}
	d := runs.Delivery{ID: "dlv_1", Repo: "o/x", Branch: "fix/one-aaa111", PRNumber: 5, CreatedAt: t0}
	_ = ledger.Put(d)
	_ = prs.Add(trackedPR(d, time.Now().Add(-time.Hour)))
	gh.getPRErr = &github.StatusError{Status: 500, Msg: "Internal Server Error"}

	if err := Maintain(context.Background(), gh, prs, ledger, res, n, pub); err == nil {
		t.Fatalf("a failing read must be reported")
	}
	cur, _ := prs.List()
	if len(cur) != 1 {
		t.Fatalf("the record must stay tracked, got %+v", cur)
	}
	if cur[0].Blocked {
		t.Fatalf("a 500 must NEVER still a record — it ends by itself, got %+v", cur[0])
	}
	if cur[0].Backoff == nil || cur[0].Backoff.NextAt.IsZero() {
		t.Fatalf("a 500 must earn a growing backoff with a next attempt, got %+v", cur[0])
	}
	notes, _ := n.List()
	for _, note := range notes {
		if note.Kind == runs.NoticeDeliveryBlocked {
			t.Errorf("a self-ending server error must raise no blockade notice, got %+v", note)
		}
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

// TestMaintainThrottlesReadsByDeadline is the NACHWEIS (point 6): with many tracked pull requests
// whose merge deadlines are far away, a run of maintenance passes produces a bounded, NAMED number
// of GitHub reads — exactly one read per pull request across the whole run, NOT one per pull request
// per pass. Before the fix, 50 pull requests over 5 passes read 250 times; here they read 50 times,
// because a not-yet-due pull request rechecked moments ago spends no request.
func TestMaintainThrottlesReadsByDeadline(t *testing.T) {
	armMaintain(t)
	gh := newFakeGH()
	ledger, prs, res, n, pub := tempLedger(t), tempPRs(t), tempResults(t), tempNotices(t), &fakePub{}

	const nPRs = 50
	farOff := time.Now().Add(720 * time.Hour) // the shipped default window — weeks away, never due
	for i := 1; i <= nPRs; i++ {
		d := runs.Delivery{ID: fmt.Sprintf("dlv_%d", i), Repo: "o/x", Branch: fmt.Sprintf("fix/n%d-aaa111", i), PRNumber: i, CreatedAt: t0.Add(time.Duration(i) * time.Second)}
		_ = ledger.Put(d)
		_ = prs.Add(runs.PendingPR{Repo: "o/x", Number: i, DeliveryID: d.ID, CreatedAt: d.CreatedAt, MergeBy: farOff})
		gh.prState[key("o/x", i)] = PRState{Number: i, State: "open", HeadRef: d.Branch, HeadSHA: fmt.Sprintf("c%d", i)}
	}

	const passes = 5
	for p := 0; p < passes; p++ {
		if err := Maintain(context.Background(), gh, prs, ledger, res, n, pub); err != nil {
			t.Fatalf("Maintain pass %d: %v", p, err)
		}
	}

	// The NAMED bound: one read per pull request across ALL passes — the first pass learns each
	// pull request's state, the rest find the recheck interval not yet elapsed and read nothing.
	if gh.getPRCalls != nPRs {
		t.Fatalf("throttle failed: %d pull requests over %d passes must read %d times (once each), not %d — %d per tick would be %d",
			nPRs, passes, nPRs, gh.getPRCalls, nPRs, nPRs*passes)
	}
	// Nothing was merged (none is due) and everything stays tracked for its eventual window.
	if len(gh.mergeCalls) != 0 {
		t.Errorf("no far-off pull request may be merged, merges = %v", gh.mergeCalls)
	}
	if left, _ := prs.List(); len(left) != nPRs {
		t.Errorf("every pull request must stay tracked, left %d of %d", len(left), nPRs)
	}
}

// TestMaintainSixtySevenEntriesStayUnderHourlyCap is the NACHWEIS point 7(d): 67 entries whose reads
// keep failing (the 2026-07-31 shape) cannot themselves storm the request window. However hard the
// maintenance is driven, the reserve stops it before the hourly budget is spent — reads stay under
// the NAMED bound (the window minus the reserve) — the standstill is stated, and NOT ONE entry is
// stilled: a self-ending obstacle is retried, never given up on.
func TestMaintainSixtySevenEntriesStayUnderHourlyCap(t *testing.T) {
	armMaintain(t)
	stubClassification(t)
	gh := newFakeGH()
	ledger, prs, res, n, pub := tempLedger(t), tempPRs(t), tempResults(t), tempNotices(t), &fakePub{}

	const nPRs = 67
	// A finite window modelled small so the drain is quick; the mechanism (a reserve the pass never
	// spends) is identical at the real 5000. The named bound proven is limit − rateFloor.
	const limit = rateFloor + 400
	now := time.Now()
	// A finite hourly window that the reads draw down, exactly as the token's real one does.
	gh.budget = RateBudget{Known: true, Remaining: limit, Limit: limit, Reset: now.Add(time.Hour)}
	gh.budgetPerRead = 1
	// The obstacle that ends by itself: every read fails with connectivity (classified Transient).
	gh.getPRErr = errors.New("dial tcp: connection refused")
	for i := 1; i <= nPRs; i++ {
		// Each in its OWN repo, so no per-repo gate hides the storm — all 67 are candidates every tick.
		repo := fmt.Sprintf("o/r%d", i)
		d := runs.Delivery{ID: fmt.Sprintf("dlv_%d", i), Repo: repo, Branch: fmt.Sprintf("fix/n%d-aaa111", i), PRNumber: i, CreatedAt: t0.Add(time.Duration(i) * time.Second)}
		_ = ledger.Put(d)
		_ = prs.Add(trackedPR(d, now.Add(-time.Hour))) // overdue — read every tick unless gated
	}

	// Drive the maintenance far more often than any scheduler would within an hour, rewinding the
	// backoff each tick so the retries are never what limits the reads — only the reserve is. Ceil of
	// (limit − reserve)/nPRs ticks drains the window; a margin past that proves it then stands still.
	for tick := 0; tick < 20; tick++ {
		_ = Maintain(context.Background(), gh, prs, ledger, res, n, pub)
		all, _ := prs.List()
		for _, p := range all {
			if p.Backoff != nil {
				_, _ = prs.Update(p.Repo, p.Number, func(rec *runs.PendingPR) { rec.Backoff.NextAt = now.Add(-time.Second) })
			}
		}
	}

	// The NAMED hourly bound: the reserve is never spent, so the window is never discovered empty by a
	// rejected request. Reads stay strictly under (limit − rateFloor).
	if gh.getPRCalls > limit-rateFloor {
		t.Fatalf("the reserve was spent: %d reads, must stay under %d (limit %d − reserve %d)", gh.getPRCalls, limit-rateFloor, limit, rateFloor)
	}
	// The shared, self-ending standstill is stated — once the drawn-down window hits the reserve.
	sawQuota := false
	notes, _ := n.List()
	for _, note := range notes {
		if note.Kind == runs.NoticeGitHubQuota {
			sawQuota = true
		}
		if note.Kind == runs.NoticeDeliveryBlocked {
			t.Errorf("a self-ending obstacle must block no entry, got %+v", note)
		}
	}
	if !sawQuota {
		t.Errorf("the shared %q standstill must be stated when the window is drawn down", runs.NoticeGitHubQuota)
	}
	// Not one of the 67 is stilled — every one is retried, none waits for a person.
	left, _ := prs.List()
	if len(left) != nPRs {
		t.Fatalf("every entry must stay tracked and retrying, left %d of %d", len(left), nPRs)
	}
	for _, p := range left {
		if p.Blocked {
			t.Fatalf("no entry may be stilled by a self-ending obstacle, got %+v", p)
		}
	}
}

// TestMaintainQuotaExhaustedIsSharedAndSelfEnding pins points 3 and 5: an empty request budget is
// ONE shared standstill, not a blockade on each entry. The pass reads nothing, blocks no pull
// request, states the standstill once, and — when the window refills — resumes on its own.
func TestMaintainQuotaExhaustedIsSharedAndSelfEnding(t *testing.T) {
	armMaintain(t)
	gh := newFakeGH()
	ledger, prs, res, n, pub := tempLedger(t), tempPRs(t), tempResults(t), tempNotices(t), &fakePub{}
	now := time.Now()
	d := runs.Delivery{ID: "dlv_1", Repo: "o/x", Branch: "fix/one-aaa111", FromCommit: "c0", ToCommit: "c1", PRNumber: 5, CreatedAt: t0, ExecutionID: "exec_1"}
	_ = ledger.Put(d)
	_ = prs.Add(trackedPR(d, now.Add(-time.Hour))) // overdue — an armed pass with budget WOULD merge it
	gh.prState["o/x|5"] = PRState{Number: 5, State: "open", HeadRef: d.Branch, HeadSHA: "c1"}
	_ = res.Put(runs.Result{ID: "exec_1", RunID: "run_1", Kind: model.KindTodo, StartedAt: t0})

	// Budget exhausted, window not yet reset.
	gh.budget = RateBudget{Known: true, Remaining: 0, Limit: 5000, Reset: now.Add(time.Hour)}

	if err := Maintain(context.Background(), gh, prs, ledger, res, n, pub); err != nil {
		t.Fatalf("an empty budget is a standstill, not a failure: %v", err)
	}
	if gh.getPRCalls != 0 {
		t.Errorf("the exhausted pass must read nothing, read %d time(s)", gh.getPRCalls)
	}
	if len(gh.mergeCalls) != 0 {
		t.Errorf("the exhausted pass must merge nothing, merges = %v", gh.mergeCalls)
	}
	// Not a per-PR blockade: the single tracked pull request stays exactly as it was.
	left, _ := prs.List()
	if len(left) != 1 || left[0].Blocked || left[0].Backoff != nil {
		t.Errorf("an empty budget must not block a pull request, got %+v", left)
	}
	// One shared standstill in the feed, and it names the self-ending nature.
	notices, _ := n.List()
	if len(notices) != 1 || notices[0].Kind != runs.NoticeGitHubQuota {
		t.Fatalf("the shared standstill is not in the feed exactly once: %+v", notices)
	}
	if !strings.Contains(notices[0].Message(), "resumes by itself") {
		t.Errorf("the notice must state that it ends on its own, got %q", notices[0].Message())
	}

	// The window refills — and the SAME situation is worked, without any per-PR resume.
	gh.budget = RateBudget{Known: true, Remaining: 5000, Limit: 5000, Reset: now.Add(time.Hour)}
	if err := Maintain(context.Background(), gh, prs, ledger, res, n, pub); err != nil {
		t.Fatalf("Maintain after refill: %v", err)
	}
	if len(gh.mergeCalls) != 1 || gh.mergeCalls[0] != "o/x|5|merge" {
		t.Errorf("the refilled pass must merge what is due, merges = %v", gh.mergeCalls)
	}
}

// TestSettleExecution pins B-8 in isolation: one open delivery keeps the result open; closing
// the last one sets MergedAt to the LAST terminal time.
func TestSettleExecution(t *testing.T) {
	ledger, res := tempLedger(t), tempResults(t)
	m1 := t0.Add(time.Hour)
	// dlv_1 is merged AND its production step is done (ProdNotApplicable — o/x is no service here), so
	// it is settled; this test is about the merge/close mechanics, not the production step (WHAT-1).
	_ = ledger.Put(runs.Delivery{ID: "dlv_1", Repo: "o/x", Branch: "fix/one-aaa111", CreatedAt: t0, ExecutionID: "exec_1", MergedAt: &m1, ProdNotApplicable: true})
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
	if workbench.LegacyShared == runs.BranchName(runs.BranchKindFix, workbench.LegacyShared, "") {
		t.Errorf("%s must not be producible by the naming scheme", workbench.LegacyShared)
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

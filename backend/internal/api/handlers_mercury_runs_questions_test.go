package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"devlab/backend/internal/auth"
	"devlab/backend/internal/execstate"
	"devlab/backend/internal/model"
	"devlab/backend/internal/runs"
	"devlab/backend/internal/sched"
	"devlab/backend/internal/statepath"
)

// questionFixture wires a real scheduler, run store and question pool behind a bare Server, so the
// Blocked-surface handlers are exercised exactly as the router calls them.
type questionFixture struct {
	t     *testing.T
	srv   *Server
	sch   *sched.Scheduler
	docs  *execstate.Store
	runs  *runs.Store
	qs    *runs.QuestionStore
	execs chan string
}

func newQuestionFixture(t *testing.T) *questionFixture {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("DEVLAB_MERCURY_RUNS", filepath.Join(dir, "runs.json"))
	t.Setenv("DEVLAB_MERCURY_RUNS_HISTORY", filepath.Join(dir, "hist"))
	t.Setenv("DEVLAB_MERCURY_EXECUTIONS", filepath.Join(dir, "state", "mercury", "executions"))
	t.Setenv("DEVLAB_MERCURY_RUNS_QUESTIONS", filepath.Join(dir, "questions.json"))
	paths := &statepath.Paths{Root: filepath.Join(dir, "state")}
	docs, err := execstate.Open(paths)
	if err != nil {
		t.Fatal(err)
	}
	rs := runs.NewStore(nil)
	f := &questionFixture{t: t, docs: docs, runs: rs, qs: runs.NewQuestionStore(paths), execs: make(chan string, 16)}
	exec := func(ctx context.Context, doc execstate.Doc, _ runs.Run) error {
		f.execs <- doc.ID
		<-ctx.Done()
		return nil
	}
	f.sch = sched.New(sched.Config{}, docs, rs, runs.NewResultStore(paths), nil, nil, exec, nil, nil)
	f.srv = &Server{paths: paths, runs: rs, runQuestions: f.qs}
	f.srv.SetExecution(docs, f.sch)
	return f
}

func (f *questionFixture) req(method, path, body string) *http.Request {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	u := &auth.User{Username: "ada", IsAdmin: true}
	return r.WithContext(context.WithValue(r.Context(), userCtxKey, u))
}

// blockedRun seeds an order, a blocked execution for it (as a raised question leaves it), and the
// open question — the exact state a run stands in while it waits on the Blocked surface.
func (f *questionFixture) blockedRun(runID, repo, qKind string) runs.Question {
	f.t.Helper()
	if err := f.runs.Put(runs.Run{ID: runID, Kind: model.KindTodo, Title: runID, Targets: []runs.Target{{Repo: repo}}}); err != nil {
		f.t.Fatal(err)
	}
	doc, err := f.docs.Create(runID, model.KindTodo, []string{repo}, false, model.Actor{User: "ada"})
	if err != nil {
		f.t.Fatal(err)
	}
	if _, err := f.docs.Update(doc.ID, func(d *execstate.Doc) error {
		d.Repos[0].State = execstate.RepoBlocked
		d.SetPhase(model.PhaseBlocked, "awaiting an answer", model.Actor{Autonomous: true}, time.Now().UTC())
		return nil
	}); err != nil {
		f.t.Fatal(err)
	}
	q, err := f.qs.Raise(runs.Question{RunID: runID, ExecutionID: doc.ID, Repo: repo, QKind: qKind, Question: "Renew the root scripts?"})
	if err != nil {
		f.t.Fatal(err)
	}
	return q
}

// (a)/(b) A guarded question is DECLINED through the one answer endpoint: the run ends as failed, its
// slot frees, the question holds nothing, and another order on the same repository is admitted at once.
func TestDeclineEndsRunAndFreesRepo(t *testing.T) {
	f := newQuestionFixture(t)
	q := f.blockedRun("run_a", "org/app", runs.QuestionWrapperRenewal)

	w := httptest.NewRecorder()
	f.srv.runsQuestionAnswer(w, f.req("POST", "/api/mercury/runs/questions/answer", `{"id":"`+q.ID+`","decline":true}`))
	if w.Code != http.StatusOK {
		t.Fatalf("decline: %d %s", w.Code, w.Body)
	}
	var out struct {
		Question runs.Question `json:"question"`
		Declined bool          `json:"declined"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if !out.Declined || !out.Question.Declined || out.Question.Open() {
		t.Fatalf("the reply must confirm a resolved rejection: %+v", out)
	}

	// The run ended as failed (its slot is free), the question holds nothing.
	d, err := f.docs.LiveForRun("run_a")
	if err != nil {
		t.Fatal(err)
	}
	if d != nil {
		t.Fatalf("the declined run must have no live execution, is %s", d.Phase)
	}
	if held, _ := f.qs.OpenForRepo("org/app", "run_b"); held != nil {
		t.Fatalf("no question may hold the repo after a rejection, got %+v", held)
	}

	// (b) Another order on the same repository starts immediately.
	if err := f.runs.Put(runs.Run{ID: "run_b", Kind: model.KindTodo, Title: "B", Targets: []runs.Target{{Repo: "org/app"}}}); err != nil {
		t.Fatal(err)
	}
	outB, err := f.sch.Submit(context.Background(), sched.StartRequest{RunID: "run_b", By: model.Actor{User: "ada"}})
	if err != nil {
		t.Fatal(err)
	}
	if !outB.Started {
		t.Fatalf("a new order on the freed repo must start at once: %+v", outB)
	}
	<-f.execs
}

// retiredMergedConsent is the wrong wording this task removes — the consent must never claim the merged
// (standard-branch) version when a wrapper renewal always installs the run's own delivering branch.
const retiredMergedConsent = "standard-branch (merged) version"

// TASK TESTS a & b + points 1 & 3 — the consent a human reads and the answer booked afterwards are ONE
// wording, derived from the question's own subject. The Blocked read fills the derived consent (naming
// the run's own not-yet-merged version and the exact file+checksum); approving books EXACTLY that same
// wording — not text the client sent — so the ledger cannot claim a version other than the one installed.
func TestGuardedApprovalBooksTheDerivedConsent(t *testing.T) {
	f := newQuestionFixture(t)
	if err := f.runs.Put(runs.Run{ID: "run_a", Kind: model.KindTodo, Title: "run_a", Targets: []runs.Target{{Repo: "org/app"}}}); err != nil {
		t.Fatal(err)
	}
	doc, err := f.docs.Create("run_a", model.KindTodo, []string{"org/app"}, false, model.Actor{User: "ada"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.docs.Update(doc.ID, func(d *execstate.Doc) error {
		d.Repos[0].State = execstate.RepoBlocked
		d.SetPhase(model.PhaseBlocked, "awaiting an answer", model.Actor{Autonomous: true}, time.Now().UTC())
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	sha := strings.Repeat("a", 64)
	q, err := f.qs.Raise(runs.Question{
		RunID: "run_a", ExecutionID: doc.ID, Repo: "org/app", QKind: runs.QuestionWrapperRenewal,
		WrapperSource: runs.WrapperSourceWorking, Question: "Renew the root scripts?",
		Wrappers: []runs.WrapperGrant{{Name: "devlab-install", SHA: sha}},
	})
	if err != nil {
		t.Fatal(err)
	}
	consent := q.GuardedApprovalStatement()

	// The Blocked read carries the derived consent, so the surface shows exactly what will be booked.
	w := httptest.NewRecorder()
	f.srv.runsQuestionsList(w, f.req("GET", "/api/mercury/runs/questions", ""))
	var listed struct {
		Questions []runs.Question `json:"questions"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed.Questions) != 1 || listed.Questions[0].ApprovalStatement != consent {
		t.Fatalf("the surface must carry the derived consent, got %+v", listed.Questions)
	}
	if !strings.Contains(consent, "this run") || !strings.Contains(consent, sha) || strings.Contains(consent, retiredMergedConsent) {
		t.Fatalf("consent must name the run's version and the checksum, never the merged wording: %q", consent)
	}

	// Approving with NO note books the consent verbatim — the client sent no answer text at all.
	w = httptest.NewRecorder()
	f.srv.runsQuestionAnswer(w, f.req("POST", "/api/mercury/runs/questions/answer", `{"id":"`+q.ID+`","approve":true}`))
	if w.Code != http.StatusOK {
		t.Fatalf("approve: %d %s", w.Code, w.Body)
	}
	got, _, _ := f.qs.Get(q.ID)
	if !got.Approved || got.Answer != consent {
		t.Fatalf("the booked answer must be the derived consent verbatim, got answer=%q approved=%v", got.Answer, got.Approved)
	}
	<-f.execs // the run resumed (a fresh execution redeems the approval)
}

// A user's optional note is appended below the affirmation — the affirmation itself is still the
// derived consent, so the ledger keeps naming the correct version even when a note is added.
func TestGuardedApprovalKeepsConsentAndAppendsNote(t *testing.T) {
	f := newQuestionFixture(t)
	q := f.blockedRun("run_a", "org/app", runs.QuestionWrapperRenewal)
	consent := q.GuardedApprovalStatement()

	w := httptest.NewRecorder()
	f.srv.runsQuestionAnswer(w, f.req("POST", "/api/mercury/runs/questions/answer", `{"id":"`+q.ID+`","approve":true,"answer":"checked on the console"}`))
	if w.Code != http.StatusOK {
		t.Fatalf("approve: %d %s", w.Code, w.Body)
	}
	got, _, _ := f.qs.Get(q.ID)
	if !strings.HasPrefix(got.Answer, consent) || !strings.Contains(got.Answer, "checked on the console") {
		t.Fatalf("the booked answer must be the consent followed by the note, got %q", got.Answer)
	}
	<-f.execs
}

// A plain decision question is equally rejectable — the co-equal "no" is not reserved for guarded
// questions. Declining an unknown id is a named 404.
func TestDeclineDecisionQuestion(t *testing.T) {
	f := newQuestionFixture(t)
	q := f.blockedRun("run_a", "org/app", runs.QuestionDecision)

	w := httptest.NewRecorder()
	f.srv.runsQuestionAnswer(w, f.req("POST", "/api/mercury/runs/questions/answer", `{"id":"`+q.ID+`","decline":true,"answer":"not this way"}`))
	if w.Code != http.StatusOK {
		t.Fatalf("decline: %d %s", w.Code, w.Body)
	}
	if got, _, _ := f.qs.Get(q.ID); !got.Declined || got.CloseNote != "not this way" {
		t.Fatalf("the rejection note must be recorded: %+v", got)
	}

	w = httptest.NewRecorder()
	f.srv.runsQuestionAnswer(w, f.req("POST", "/api/mercury/runs/questions/answer", `{"id":"nope","decline":true}`))
	if w.Code != http.StatusNotFound {
		t.Fatalf("declining an unknown question must be 404, got %d", w.Code)
	}
}

// (c) The run of an open question no longer exists → reading the Blocked surface retires the moot
// question, so it is no longer open and holds nothing.
func TestMootQuestionClosedOnList(t *testing.T) {
	f := newQuestionFixture(t)
	// A question whose run was never put into the run store (it "no longer exists").
	q, err := f.qs.Raise(runs.Question{RunID: "run_gone", ExecutionID: "exec_x", Repo: "org/app", QKind: runs.QuestionDecision, Question: "orphaned?"})
	if err != nil {
		t.Fatal(err)
	}
	// Before the sweep it would still hold the repo.
	if held, _ := f.qs.OpenForRepo("org/app", "run_other"); held == nil {
		t.Fatal("precondition: the open question holds the repo")
	}

	w := httptest.NewRecorder()
	f.srv.runsQuestionsList(w, f.req("GET", "/api/mercury/runs/questions", ""))
	if w.Code != http.StatusOK {
		t.Fatalf("list: %d %s", w.Code, w.Body)
	}

	if got, _, _ := f.qs.Get(q.ID); !got.Moot || got.Open() {
		t.Fatalf("the moot question must be closed after the read: %+v", got)
	}
	if held, _ := f.qs.OpenForRepo("org/app", "run_other"); held != nil {
		t.Fatalf("a moot question must hold nothing, got %+v", held)
	}
}

// TASK TEST c (end-to-end) — an order whose execution has ENDED (completed) leaves no open question
// standing: the Blocked read sweeps it, so its answer can no longer be clicked to no effect. A run that
// is still WAITING on its question (its execution blocked, not ended) keeps its question open.
func TestEndedOrderQuestionClosedOnList(t *testing.T) {
	f := newQuestionFixture(t)

	// A run whose execution COMPLETED but left an open question behind (the corpse case).
	if err := f.runs.Put(runs.Run{ID: "run_done", Kind: model.KindTodo, Title: "done", Targets: []runs.Target{{Repo: "org/app"}}}); err != nil {
		t.Fatal(err)
	}
	doneDoc, err := f.docs.Create("run_done", model.KindTodo, []string{"org/app"}, false, model.Actor{User: "ada"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.docs.Update(doneDoc.ID, func(d *execstate.Doc) error {
		d.SetPhase(model.PhaseCompleted, "", model.Actor{Autonomous: true}, time.Now().UTC())
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	dead, err := f.qs.Raise(runs.Question{RunID: "run_done", ExecutionID: doneDoc.ID, Repo: "org/app", QKind: runs.QuestionDecision, Question: "left open?"})
	if err != nil {
		t.Fatal(err)
	}

	// A run that is still WAITING on its question (execution blocked, not ended) must keep it open.
	waiting := f.blockedRun("run_waiting", "org/other", runs.QuestionDecision)

	w := httptest.NewRecorder()
	f.srv.runsQuestionsList(w, f.req("GET", "/api/mercury/runs/questions", ""))
	if w.Code != http.StatusOK {
		t.Fatalf("list: %d %s", w.Code, w.Body)
	}

	if got, _, _ := f.qs.Get(dead.ID); !got.Ended || got.Open() {
		t.Fatalf("the ended order's open question must be closed after the read: %+v", got)
	}
	if got, _, _ := f.qs.Get(waiting.ID); !got.Open() {
		t.Fatalf("a run still waiting on its question must keep it open: %+v", got)
	}
}

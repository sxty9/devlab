package runs

import (
	"path/filepath"
	"testing"

	"devlab/backend/internal/model"
	"devlab/backend/internal/statepath"
)

func newTestQuestions(t *testing.T) *QuestionStore {
	t.Helper()
	t.Setenv("DEVLAB_MERCURY_RUNS_QUESTIONS", filepath.Join(t.TempDir(), "questions.json"))
	return NewQuestionStore(&statepath.Paths{Root: t.TempDir()})
}

// The pool holds open questions, excludes the raising ORDER from the hold, feeds an answered one
// back, and marks it consumed — the four moves the Blocked surface and the resume rely on.
func TestQuestionStoreLifecycle(t *testing.T) {
	s := newTestQuestions(t)

	var fired int
	s.SetOnNew(func(Question) { fired++ })

	if _, err := s.Raise(Question{RunID: "run_a", ExecutionID: "exec_a", Repo: "org/app", QKind: QuestionDecision, Question: "Foo or bar?"}); err != nil {
		t.Fatalf("raise: %v", err)
	}
	if fired != 1 {
		t.Fatalf("on-new should fire once per new question, got %d", fired)
	}

	// A DIFFERENT order is held by the open question.
	held, err := s.OpenForRepo("org/app", "run_other")
	if err != nil || held == nil {
		t.Fatalf("open question should hold another order, got %v %v", held, err)
	}
	// The RAISING order is never held by its own question.
	if own, _ := s.OpenForRepo("org/app", "run_a"); own != nil {
		t.Fatalf("an order must not be held by its own question")
	}

	// An answer flips it out of open and makes it feed the resume.
	answered, err := s.Answer(held.ID, "Use foo.", false, model.Actor{User: "nova"})
	if err != nil {
		t.Fatalf("answer: %v", err)
	}
	if answered.Open() {
		t.Fatalf("an answered question is not open")
	}
	if held2, _ := s.OpenForRepo("org/app", "run_other"); held2 != nil {
		t.Fatalf("an answered question no longer holds a repo")
	}
	fed, _ := s.AnsweredForRun("run_a", "org/app")
	if fed == nil || fed.Answer != "Use foo." {
		t.Fatalf("the answered question should feed the resume, got %+v", fed)
	}

	// Consumed, it no longer feeds.
	if err := s.Resolve(fed.ID); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if again, _ := s.AnsweredForRun("run_a", "org/app"); again != nil {
		t.Fatalf("a consumed answer must not feed the resume twice")
	}
}

// An answer belongs to the ORDER, not the single execution that asked: a LATER execution of the same
// order redeems it, and the order is never held by a question one of its own earlier executions
// raised. This is the fix for the trap where a restart minted a new execution and lost the approval.
func TestQuestionAnswerRedeemedByLaterExecution(t *testing.T) {
	s := newTestQuestions(t)

	// exec_1 of run_a raises and blocks; the user answers it.
	q, _ := s.Raise(Question{RunID: "run_a", ExecutionID: "exec_1", Repo: "org/app", QKind: QuestionWrapperRenewal, Question: "Renew?"})
	if _, err := s.Answer(q.ID, "yes", true, model.Actor{User: "op"}); err != nil {
		t.Fatalf("answer: %v", err)
	}

	// A restart mints exec_2. The order's own open/answered question must NOT hold it (by run id)...
	if held, _ := s.OpenForRepo("org/app", "run_a"); held != nil {
		t.Fatalf("a later execution of the same order must not be held by the order's own question")
	}
	// ...and exec_2 redeems the approval by RUN, though its own execution id differs.
	fed, _ := s.AnsweredForRun("run_a", "org/app")
	if fed == nil || !fed.Approved || fed.ExecutionID != "exec_1" {
		t.Fatalf("the later execution should redeem the earlier order's approval, got %+v", fed)
	}
}

// A withdrawn question stops being open and stops feeding — so a fresh question can replace a stale
// one a since-ended execution left behind, without two blockers piling up on the same repository.
func TestQuestionWithdrawForRun(t *testing.T) {
	s := newTestQuestions(t)

	s.Raise(Question{RunID: "run_a", ExecutionID: "exec_1", Repo: "org/app", QKind: QuestionWrapperRenewal, Question: "Renew v1?"})
	other, _ := s.Raise(Question{RunID: "run_b", ExecutionID: "exec_x", Repo: "org/app", QKind: QuestionWrapperRenewal, Question: "unrelated"})

	n, err := s.WithdrawForRun("run_a", "org/app", QuestionWrapperRenewal)
	if err != nil || n != 1 {
		t.Fatalf("withdraw should retire exactly the order's own question, got n=%d err=%v", n, err)
	}
	// The withdrawn question no longer holds anyone or feeds a resume.
	if held, _ := s.OpenForRepo("org/app", "run_other"); held == nil || held.ID == "" || held.RunID != "run_b" {
		t.Fatalf("only the unrelated order's question should still hold the repo, got %+v", held)
	}
	if fed, _ := s.AnsweredForRun("run_a", "org/app"); fed != nil {
		t.Fatalf("a withdrawn question must not feed the resume")
	}
	// A different order's question is untouched.
	if o, _, _ := s.Get(other.ID); o.Resolved {
		t.Fatalf("withdraw must not touch another order's question")
	}
}

// An answer is never overwritten, and an unknown id is a named miss.
func TestQuestionStoreAnswerIsFinal(t *testing.T) {
	s := newTestQuestions(t)
	q, _ := s.Raise(Question{ExecutionID: "e", Repo: "r", QKind: QuestionDecision, Question: "q?"})
	if _, err := s.Answer(q.ID, "first", true, model.Actor{User: "a"}); err != nil {
		t.Fatalf("answer: %v", err)
	}
	got, _ := s.Answer(q.ID, "second", false, model.Actor{User: "b"})
	if got.Answer != "first" || !got.Approved {
		t.Fatalf("an answer must not be overwritten, got %+v", got)
	}
	if _, err := s.Answer("nope", "x", false, model.Actor{}); err != ErrNotFound {
		t.Fatalf("unknown id should be ErrNotFound, got %v", err)
	}
}

// A host-key question holds the PRODUCTION send, not a dev branch: OpenForRepo (the branch-halt) skips
// it, so new orders on the same repository are not blocked by it — a production failure never blocks
// the stack (WHAT-3). Its own lookups (open + approved-per-host) find it instead.
func TestQuestionStoreHostKeyDoesNotHoldDevBranch(t *testing.T) {
	s := newTestQuestions(t)
	raised, err := s.Raise(Question{QKind: QuestionProdHostKey, Repo: "org/app", HostKeyTarget: "prod.example", HostKeyFingerprint: "SHA256:K", Question: "approve?"})
	if err != nil {
		t.Fatal(err)
	}
	// The branch-halt must NOT return a host-key question.
	if held, _ := s.OpenForRepo("org/app", "some-other-run"); held != nil {
		t.Fatal("a host-key question must not hold a repository's dev branch")
	}
	// The host-level lookup finds it.
	if q, _ := s.OpenHostKeyQuestion("prod.example"); q == nil || q.ID != raised.ID {
		t.Fatal("OpenHostKeyQuestion must find the open host-key question by target")
	}
	// Not approved yet → not returned as an approval.
	if q, _ := s.ApprovedHostKeyQuestion("prod.example"); q != nil {
		t.Fatal("an unanswered host-key question is not an approval")
	}
	// After an explicit approval it is redeemable, and no longer 'open'.
	if _, err := s.Answer(raised.ID, "ok", true, model.Actor{}); err != nil {
		t.Fatal(err)
	}
	if q, _ := s.ApprovedHostKeyQuestion("prod.example"); q == nil || q.ID != raised.ID {
		t.Fatal("an approved host-key question must be redeemable per host")
	}
	if q, _ := s.OpenHostKeyQuestion("prod.example"); q != nil {
		t.Fatal("an answered host-key question is no longer open")
	}
	// A declined (answered but not approved) question is never returned as an approval.
	other, _ := s.Raise(Question{QKind: QuestionProdHostKey, Repo: "org/b", HostKeyTarget: "h2", HostKeyFingerprint: "SHA256:X"})
	_, _ = s.Answer(other.ID, "no", false, model.Actor{})
	if q, _ := s.ApprovedHostKeyQuestion("h2"); q != nil {
		t.Fatal("a declined host-key question must not count as an approval")
	}
}

// A REJECTION is a full answer, not a missing one: it resolves the question so it holds no repository
// and feeds no resume, records who rejected it and why, and never approves. It is the safe exit — it
// changes nothing on its own; the caller ends the run.
func TestQuestionDecline(t *testing.T) {
	s := newTestQuestions(t)
	q, _ := s.Raise(Question{RunID: "run_a", ExecutionID: "exec_1", Repo: "org/app", QKind: QuestionWrapperRenewal, Question: "Renew the root scripts?"})

	// A second, unrelated order's question must be untouched by the rejection.
	other, _ := s.Raise(Question{RunID: "run_b", ExecutionID: "exec_x", Repo: "org/app", QKind: QuestionDecision, Question: "unrelated"})

	dq, err := s.Decline(q.ID, "not now", model.Actor{User: "op"})
	if err != nil {
		t.Fatalf("decline: %v", err)
	}
	if !dq.Declined || dq.Approved || dq.Open() || dq.Answered() {
		t.Fatalf("a declined question is resolved, not approved, and neither open nor answered: %+v", dq)
	}
	if dq.CloseNote != "not now" || dq.DeclinedBy.User != "op" {
		t.Fatalf("the rejection records its note and who made it: %+v", dq)
	}
	// It holds nobody and feeds no resume.
	if held, _ := s.OpenForRepo("org/app", "run_c"); held == nil || held.RunID != "run_b" {
		t.Fatalf("only the unrelated order's question should still hold the repo, got %+v", held)
	}
	if fed, _ := s.AnsweredForRun("run_a", "org/app"); fed != nil {
		t.Fatalf("a declined question must not feed a resume")
	}
	// Idempotent: declining again returns it unchanged, never an error, and never overwrites.
	if again, err := s.Decline(q.ID, "second", model.Actor{User: "x"}); err != nil || again.CloseNote != "not now" {
		t.Fatalf("declining an already-closed question is an unchanged no-op, got %+v err=%v", again, err)
	}
	// A different order's question is untouched.
	if o, _, _ := s.Get(other.ID); o.Resolved {
		t.Fatalf("decline must not touch another order's question")
	}
	// Unknown id is a named miss.
	if _, err := s.Decline("nope", "", model.Actor{}); err != ErrNotFound {
		t.Fatalf("unknown id should be ErrNotFound, got %v", err)
	}
}

// A question whose run no longer exists is moot: told the set of runs that still exist, the pool
// closes exactly the questions that fall outside it, so a moot question holds nothing. A question of
// a run that still exists is left alone, and an empty (authoritative) set closes everything open.
func TestQuestionCloseMoot(t *testing.T) {
	s := newTestQuestions(t)
	gone, _ := s.Raise(Question{RunID: "run_gone", ExecutionID: "e1", Repo: "org/app", QKind: QuestionDecision, Question: "orphan?"})
	live, _ := s.Raise(Question{RunID: "run_live", ExecutionID: "e2", Repo: "org/app", QKind: QuestionDecision, Question: "still here?"})

	closed, err := s.CloseMoot(map[string]bool{"run_live": true}, "run gone")
	if err != nil {
		t.Fatalf("close moot: %v", err)
	}
	if len(closed) != 1 || closed[0].ID != gone.ID {
		t.Fatalf("only the moot question should close, got %+v", closed)
	}
	// The moot question holds nothing now; the live one still holds the repo.
	if g, _, _ := s.Get(gone.ID); !g.Moot || !g.Resolved || g.Open() {
		t.Fatalf("the moot question must be resolved and hold nothing: %+v", g)
	}
	if held, _ := s.OpenForRepo("org/app", "run_c"); held == nil || held.RunID != "run_live" {
		t.Fatalf("the surviving run's question must still hold the repo, got %+v", held)
	}
	// Idempotent: nothing new closes on a second sweep.
	if again, _ := s.CloseMoot(map[string]bool{"run_live": true}, "run gone"); len(again) != 0 {
		t.Fatalf("a second sweep closes nothing new, got %+v", again)
	}
	_ = live
}

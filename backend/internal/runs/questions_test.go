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

// The pool holds open questions, excludes a run's own question from the hold, feeds an answered one
// back, and marks it consumed — the four moves the Blocked surface and the resume rely on.
func TestQuestionStoreLifecycle(t *testing.T) {
	s := newTestQuestions(t)

	var fired int
	s.SetOnNew(func(Question) { fired++ })

	if _, err := s.Raise(Question{ExecutionID: "exec_a", Repo: "org/app", QKind: QuestionDecision, Question: "Foo or bar?"}); err != nil {
		t.Fatalf("raise: %v", err)
	}
	if fired != 1 {
		t.Fatalf("on-new should fire once per new question, got %d", fired)
	}

	// A DIFFERENT execution is held by the open question.
	held, err := s.OpenForRepo("org/app", "exec_other")
	if err != nil || held == nil {
		t.Fatalf("open question should hold another execution, got %v %v", held, err)
	}
	// The RAISING execution is never held by its own question.
	if own, _ := s.OpenForRepo("org/app", "exec_a"); own != nil {
		t.Fatalf("a run must not be held by its own question")
	}

	// An answer flips it out of open and makes it feed the resume.
	answered, err := s.Answer(held.ID, "Use foo.", false, model.Actor{User: "nova"})
	if err != nil {
		t.Fatalf("answer: %v", err)
	}
	if answered.Open() {
		t.Fatalf("an answered question is not open")
	}
	if held2, _ := s.OpenForRepo("org/app", "exec_other"); held2 != nil {
		t.Fatalf("an answered question no longer holds a repo")
	}
	fed, _ := s.AnsweredForExec("exec_a", "org/app")
	if fed == nil || fed.Answer != "Use foo." {
		t.Fatalf("the answered question should feed the resume, got %+v", fed)
	}

	// Consumed, it no longer feeds.
	if err := s.Resolve(fed.ID); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if again, _ := s.AnsweredForExec("exec_a", "org/app"); again != nil {
		t.Fatalf("a consumed answer must not feed the resume twice")
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

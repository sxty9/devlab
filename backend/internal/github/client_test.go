package github

import (
	"context"
	"errors"
	"net/http"
	"testing"
)

// TestCreatePullRequestTypes422 pins the fault-classification input for the self-referential PR
// (measured 2026-08-01 in notify): when GitHub rejects the create with 422 "No commits between
// X and X", CreatePullRequest must surface a typed *StatusError{422}. Untyped it would fall
// through faultclass's Transient default and be retried four times; typed it is classified
// Permanent — one attempt, a named dead end. The 422 body carries GitHub's own message.
func TestCreatePullRequestTypes422(t *testing.T) {
	const head = "fix/self-cmlmg5"
	withFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/o/x/pulls" {
			http.NotFound(w, r)
			return
		}
		http.Error(w, `{"message":"Validation Failed","errors":[{"message":"No commits between `+head+` and `+head+`"}]}`, http.StatusUnprocessableEntity)
	}))

	_, err := CreatePullRequest(context.Background(), "tok", "o/x", head, head, "self", "body")
	var se *StatusError
	if !errors.As(err, &se) || se.Status != http.StatusUnprocessableEntity {
		t.Fatalf("a 422 create must surface as *StatusError{422}, got %v", err)
	}
	if se.Msg == "" {
		t.Error("the typed error must carry GitHub's own message for the honest report")
	}
}

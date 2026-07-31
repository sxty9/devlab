package api

// IDE-route PR tests (B-1): the second write path is the SAME ONE PR path — openPR goes
// through deliver.OpenOrAdoptPR (non-ledger branch); it never talks to
// github.CreatePullRequest itself. The adoption/idempotence semantics live in package
// deliver's tests; here the route's guards and its error translation are pinned.

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"devlab/backend/internal/auth"
)

// TestOpenPRDevBypass: under dev-bypass there is no GitHub account — the action answers an
// explained 400 before touching anything.
func TestOpenPRDevBypass(t *testing.T) {
	t.Setenv("DEVLAB_DEV_BYPASS_AUTH", "1")
	s := &Server{v: auth.New()}
	req := authedReq(http.MethodPost, "/api/repos/x/pr", map[string]string{"title": "t"}, "dev")
	req.SetPathValue("id", "x")
	rec := httptest.NewRecorder()
	s.openPR(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 under dev-bypass", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "GitHub") {
		t.Errorf("the refusal must explain the missing link, got %s", rec.Body.String())
	}
}

// TestPRErrorTranslation: GitHub's verbose PR failures land as actionable one-liners.
func TestPRErrorTranslation(t *testing.T) {
	cases := []struct {
		in   error
		want string
	}{
		{errors.New("422 no commits between main and feature/x"), "No commits to propose"},
		{errors.New("head sha can't be blank; not all refs are readable"), "push it first"},
		{errors.New(strings.Repeat("x", 400)), "Could not open the pull request"},
	}
	for _, c := range cases {
		got := prError(c.in)
		if !strings.Contains(got, c.want) {
			t.Errorf("prError(%q) = %q, want it to contain %q", c.in, got, c.want)
		}
		if len(got) > 300 {
			t.Errorf("prError must clip verbose errors, got %d bytes", len(got))
		}
	}
}

// TestOpenPRUsesTheOnePath (B-1 pin): the IDE route's source goes through
// deliver.OpenOrAdoptPR and never calls github.CreatePullRequest directly — the route is the
// non-ledger branch of the ONE PR path, not a second writer.
func TestOpenPRUsesTheOnePath(t *testing.T) {
	src, err := os.ReadFile("handlers_pr.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(src), "deliver.OpenOrAdoptPR(") {
		t.Error("openPR must go through deliver.OpenOrAdoptPR (the ONE PR path)")
	}
	if strings.Contains(string(src), "github.CreatePullRequest(") {
		t.Error("the IDE route must not call github.CreatePullRequest directly (K-6)")
	}
}

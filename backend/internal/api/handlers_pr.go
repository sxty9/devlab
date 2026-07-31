package api

import (
	"net/http"
	"strings"

	"devlab/backend/internal/deliver"
	"devlab/backend/internal/github"
	"devlab/backend/internal/model"
)

// prOps builds the GitHubOps of the IDE route over the CALLER's token (a human PR is the
// user's PR, not the runner's). A seam so the handler is testable without GitHub.
var prOps = deliver.NewGitHub

// openPR pushes the caller's current branch and opens a GitHub pull request into the default branch
// (the "Delivery" pipeline action). It needs push permission (guarded by mutateCtx). If a PR for the
// branch is already open it focuses that one instead of erroring — the action is idempotent. Under
// dev-bypass there is no GitHub token, so it's a no-op 400.
func (s *Server) openPR(w http.ResponseWriter, r *http.Request) {
	if !s.requireRealGitHub(w, "Pull requests") {
		return
	}
	wc, unlock, ok := s.mutateCtx(w, r, true)
	if !ok {
		return
	}
	defer unlock()

	var body struct {
		Title string `json:"title"`
		Body  string `json:"body"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}

	head := wc.exec.CurrentBranch(r.Context(), wc.wt)
	if head == "" || head == "HEAD" {
		writeErr(w, http.StatusBadRequest, "Cannot open a PR from a detached HEAD")
		return
	}

	u := userFrom(r)
	full, ok := s.resolveFullName(r.Context(), u, r.PathValue("id"))
	if !ok {
		writeErr(w, http.StatusNotFound, errRepoNotFound)
		return
	}
	base, err := github.DefaultBranch(r.Context(), wc.token, full)
	if err != nil {
		writeErr(w, http.StatusBadGateway, "Could not resolve the default branch")
		return
	}
	if head == base {
		writeErr(w, http.StatusBadRequest, "You're on the default branch — create a feature branch first")
		return
	}

	// Push the branch so GitHub has the head ref, then open (or adopt) the PR through the ONE
	// PR path (B-1): deliver.OpenOrAdoptPR, non-ledger branch (DeliveryID "") — a human PR gets
	// no ledger entry, and REQ-033 keeps it from merging without one.
	if _, err := wc.exec.Push(r.Context(), wc.wt, wc.token); err != nil {
		writeErr(w, http.StatusBadGateway, "Push failed: "+err.Error())
		return
	}

	title := strings.TrimSpace(body.Title)
	if title == "" {
		title = head
	}
	ref, adopted, err := deliver.OpenOrAdoptPR(r.Context(), prOps(wc.token), s.deliveries, deliver.PRIn{
		Repo: full, Head: head, Base: base, Title: title, Body: body.Body,
	})
	if err != nil {
		writeErr(w, http.StatusBadGateway, prError(err))
		return
	}
	writeJSON(w, http.StatusOK, model.PullRequestResult{
		Number: ref.Number, URL: ref.URL, State: "open",
		Title: title, Branch: head, Base: base, Existed: adopted,
	})
}

// prError trims GitHub's verbose PR errors to something actionable.
func prError(err error) string {
	s := err.Error()
	low := strings.ToLower(s)
	switch {
	case strings.Contains(low, "no commits between"):
		return "No commits to propose — this branch matches the base branch."
	case strings.Contains(low, "not all refs are readable"), strings.Contains(low, "head sha"):
		return "The branch isn't on GitHub yet — push it first."
	default:
		if len(s) > 240 {
			s = s[:240] + "…"
		}
		return "Could not open the pull request: " + s
	}
}

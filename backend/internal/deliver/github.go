// The production GitHubOps: a thin adapter over the ported github client, closed over one
// token. github.CreatePullRequest has its ONE caller here (K-6 grep) — the chain and the IDE
// route both arrive through OpenOrAdoptPR.
package deliver

import (
	"context"
	"errors"

	"devlab/backend/internal/discover"
	"devlab/backend/internal/github"
	"devlab/backend/internal/model"
)

// liveGitHub implements GitHubOps over the github package for one token.
type liveGitHub struct {
	token string
}

// NewGitHub binds a token into the production GitHubOps.
func NewGitHub(token string) GitHubOps { return liveGitHub{token: token} }

func (g liveGitHub) CreatePullRequest(ctx context.Context, repo, head, base, title, body string) (model.PRRef, error) {
	pr, err := github.CreatePullRequest(ctx, g.token, repo, head, base, title, body)
	if err != nil {
		return model.PRRef{}, err
	}
	return model.PRRef{Number: pr.Number, URL: pr.HTMLURL, HeadBranch: head}, nil
}

func (g liveGitHub) FindOpenPRByHead(ctx context.Context, repo, head string) (*model.PRRef, error) {
	pr, found := github.FindOpenPullRequest(ctx, g.token, repo, head)
	if !found {
		return nil, nil
	}
	return &model.PRRef{Number: pr.Number, URL: pr.HTMLURL, HeadBranch: head}, nil
}

func (g liveGitHub) GetPullRequest(ctx context.Context, repo string, number int) (PRState, error) {
	st, err := github.GetPullState(ctx, g.token, repo, number)
	if err != nil {
		return PRState{}, err
	}
	return PRState(st), nil
}

func (g liveGitHub) ListOpenPullRequests(ctx context.Context, repo string) ([]PRState, error) {
	heads, err := github.ListOpenPullHeads(ctx, g.token, repo)
	if err != nil {
		return nil, err
	}
	out := make([]PRState, 0, len(heads))
	for _, h := range heads {
		out = append(out, PRState(h))
	}
	return out, nil
}

// MergePullRequest merges with method "merge" ONLY — the sole admissible method (REQ-033.5).
func (g liveGitHub) MergePullRequest(ctx context.Context, repo string, number int, method string) error {
	if method != "merge" {
		return errors.New(`merge method must be "merge"`)
	}
	return github.MergePullRequest(ctx, g.token, repo, number)
}

func (g liveGitHub) ClosePullRequest(ctx context.Context, repo string, number int, reason string) error {
	if reason != "" {
		_ = github.AddPullRequestComment(ctx, g.token, repo, number, reason) // best-effort: the close carries the reason
	}
	return github.ClosePullRequest(ctx, g.token, repo, number)
}

func (g liveGitHub) DeleteBranch(ctx context.Context, repo, branch string) error {
	return github.DeleteBranch(ctx, g.token, repo, branch)
}

// CreateRepo creates the repository in the instance's holistic namespace (owner + topic from
// the runtime configuration — never a literal) and reports the resulting full name. An
// already-existing repo is returned as-is (Satisfied, REQ-033.6). The `private` flag is
// accepted for the interface's sake; the client creates repositories private.
func (g liveGitHub) CreateRepo(ctx context.Context, name string, private bool) (string, error) {
	_ = private
	return github.CreateRepo(ctx, g.token, discover.Owner(), name,
		"Holistic service repository, created by the delivery chain", discover.Topic())
}

func (g liveGitHub) ProtectDefaultBranch(ctx context.Context, repo, requiredStatus string) error {
	return github.ProtectDefaultBranch(ctx, g.token, repo, requiredStatus)
}

func (g liveGitHub) GetProtection(ctx context.Context, repo string) (Protection, error) {
	p, err := github.GetProtection(ctx, g.token, repo)
	if err != nil {
		return Protection{}, err
	}
	return Protection{
		RequirePR: p.RequirePR, RequiredStatus: p.RequiredStatus,
		AllowForcePush: p.AllowForcePush, AllowDeletion: p.AllowDeletion, MergeMethods: p.MergeMethods,
	}, nil
}

func (g liveGitHub) PostCommitStatus(ctx context.Context, repo, sha, statusContext, state, desc string) error {
	return github.PostCommitStatus(ctx, g.token, repo, sha, statusContext, state, desc)
}

func (g liveGitHub) DefaultBranch(ctx context.Context, repo string) (string, error) {
	return github.DefaultBranch(ctx, g.token, repo)
}

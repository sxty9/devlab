// The production GitHubOps: a thin adapter over the ported github client, closed over one
// token. github.CreatePullRequest has its ONE caller here (K-6 grep) — the chain and the IDE
// route both arrive through OpenOrAdoptPR.
package deliver

import (
	"context"
	"errors"

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

// MergePullRequest merges with method "merge" ONLY — the sole admissible method (REQ-024/F14).
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

func (g liveGitHub) CreateRepo(ctx context.Context, name string, private bool) error {
	panic("TODO(B4)") // github.CreateRepo signature alignment happens with the protection pass
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

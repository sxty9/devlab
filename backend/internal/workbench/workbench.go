// Package workbench is the ONE fold-in state machine of the working state (S9, K-1): the
// persistent branch mercury-dev grows, is folded into, published — and NEVER reset by the
// machinery. There is no reset primitive on the automated path: Prepare folds the remote in
// (merge), a conflict is NAMED and nothing is rolled back, orphaned commits get a rescue ref.
// ResetToDefault exists ONLY behind the explicit UI confirmation (REQ-022.4).
//
// mercury-dev is pushed as a backup and is NEVER itself turned into a pull request.
//
// All git operations go through the ported workspace.Executor primitives — the runner works
// exclusively in the runner user's workspace (guard, B 1.3a).
package workbench

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"devlab/backend/internal/git"
	"devlab/backend/internal/workspace"
)

// LegacyShared is the retired shared working branch. Every task used to commit onto this ONE
// branch per repository, so what lay on it never said WHOSE work it was — the fault that let a
// fresh task be declared done and skipped (measured 2026-07-31 across all 23 repositories). It
// survives here as a name to recognise, never to write: the newest open delivery branch carries
// everything it carried, and that is what a new task now branches from.
const LegacyShared = "mercury-dev"

// branchRe mirrors the ported Executor's branch validation so no option-shaped or otherwise
// hostile name ever reaches git through the local bridge below.
var branchRe = regexp.MustCompile(`^[A-Za-z0-9._/-]+$`)

// Bench operates the working state of one repo. repo is the absolute path of the working
// tree (the per-user workspace checkout of this repository).
type Bench struct {
	ex   *workspace.Executor
	repo string
	// branch is THIS task's own working branch. One branch per task, cut from the top of the
	// stack — never a branch shared with another task, so ownership can never be lost.
	branch string
	token  string
}

// On points the bench at one task's working branch. Empty is refused: a bench without a named
// branch would fall back to a shared one, which is exactly the construction this replaced.
func (b *Bench) On(branch string) (*Bench, error) {
	if strings.TrimSpace(branch) == "" {
		return nil, errors.New("workbench: empty working branch")
	}
	if !branchRe.MatchString(branch) {
		return nil, fmt.Errorf("workbench: invalid working branch %q", branch)
	}
	c := *b
	c.branch = branch
	return &c, nil
}

// Name is the working branch this bench operates on.
func (b *Bench) Name() string { return b.branch }

// New builds a bench. Guard: the runner works ONLY in the runner user's workspace (B 1.3a) —
// a foreign workspace path is refused. The per-user workspace layout is
// <workspaces-root>/<user>/<repo>, so when the executor carries a user identity the path's
// owning-user segment must be exactly that user; the old pitfall "the night run erases a
// human's uncommitted IDE edits" is structurally excluded. An executor without a user
// identity (the hermetic test form) carries no claim to verify.
func New(ex *workspace.Executor, repo string) (*Bench, error) {
	if ex == nil {
		return nil, errors.New("workbench: nil executor")
	}
	if strings.TrimSpace(repo) == "" {
		return nil, errors.New("workbench: empty working-tree path")
	}
	if ex.User != "" {
		if owner := filepath.Base(filepath.Dir(filepath.Clean(repo))); owner != ex.User {
			return nil, fmt.Errorf(
				"workbench: refused — workspace %q belongs to %q, executor runs as %q; the runner never enters a foreign workspace (B 1.3a)",
				repo, owner, ex.User)
		}
	}
	return &Bench{ex: ex, repo: repo}, nil
}

// WithToken attaches the GitHub token authorizing this bench's network operations
// (fetch/push of private repositories). The token travels exactly like in the ported
// primitives — process env, never argv, never persisted. Empty means unauthenticated
// (public repos, local test remotes). Returns the bench for wiring convenience.
func (b *Bench) WithToken(token string) *Bench {
	b.token = token
	return b
}

// Head names the workbench's current commit — the nameable delivered state (REQ-022.3).
// It resolves the branch ref itself, so the answer is right even while the working tree
// is parked elsewhere.
func (b *Bench) Head(ctx context.Context) (string, error) {
	sha, err := b.ex.RevParse(ctx, b.repo, "refs/heads/"+b.branch)
	if err != nil {
		return "", fmt.Errorf("workbench %s does not exist yet: %w", b.branch, err)
	}
	return sha, nil
}

// refExists reports whether a fully-qualified ref resolves — through the ported RevParse
// primitive, so a bad name is reported as absent rather than reaching git.
func (b *Bench) refExists(ctx context.Context, ref string) bool {
	_, err := b.ex.RevParse(ctx, b.repo, ref)
	return err == nil
}

// defaultBranch resolves the repository's default branch through the ported read-only
// package git (origin/HEAD, else main/master, else current). The workbench itself never
// counts as a default branch (REQ-026: mercury-dev falls outside every naming scheme).
func (b *Bench) defaultBranch() (string, error) {
	def := git.DefaultBranch(b.repo)
	if !branchRe.MatchString(def) || strings.HasPrefix(def, "-") {
		return "", fmt.Errorf("workbench: unusable default branch name %q", def)
	}
	if def == b.branch {
		return "", fmt.Errorf("workbench: default branch resolves to %s itself — refusing to derive the workbench from itself", b.branch)
	}
	return def, nil
}

// ─── Local git bridge ───────────────────────────────────────────────────────
//
// The ported workspace.Executor exposes every mutating primitive the workbench composes
// EXCEPT three ops its surface never carried: reset --hard HEAD (drop uncommitted edits),
// clean -fdx (remove untracked/ignored remnants) and update-ref (rescue refs). Those run
// here through the SAME per-user pattern the Executor uses — sudo + the pinned devlab-exec
// wrapper in production, direct git under dev-bypass — as a local bridge in workbench
// ownership (BAUPLAN §0.2: the frozen workspace files are not touched; the gap is reported
// to the orchestrator). Read-only lookups (rev-list, fsck, merge-base, merge-tree) run
// directly as the service user, exactly like the ported read-only package git.

// execWrapper mirrors the ported per-user op runner path (a system constant, not an
// instance literal).
const execWrapper = "/usr/local/sbin/devlab-exec"

// gitUser runs a MUTATING git op in the working tree as the workspace owner: per-user via
// the pinned devlab-exec wrapper, direct under dev-bypass. Mirrors the ported gitIn/runGit
// pair; no credentials are involved (network ops stay on the Executor primitives).
func (b *Bench) gitUser(ctx context.Context, args ...string) (string, error) {
	var cmd *exec.Cmd
	if b.ex.PerUser {
		full := append([]string{"-n", "-u", b.ex.User, execWrapper, "git", b.repo}, args...)
		cmd = exec.CommandContext(ctx, "sudo", full...)
	} else {
		full := append([]string{"-c", "safe.directory=*", "-C", b.repo}, args...)
		cmd = exec.CommandContext(ctx, "git", full...)
	}
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.CombinedOutput()
	s := strings.TrimRight(string(out), "\n")
	if err != nil {
		if s == "" {
			s = err.Error()
		}
		return s, errors.New(s)
	}
	return s, nil
}

// gitRO runs a READ-ONLY git op in the working tree directly as the service user (the
// ported package-git pattern: no lock contention, readable across user-owned checkouts).
func (b *Bench) gitRO(ctx context.Context, args ...string) (string, error) {
	full := append([]string{"--no-optional-locks", "-c", "safe.directory=*", "-C", b.repo}, args...)
	cmd := exec.CommandContext(ctx, "git", full...)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	s := strings.TrimRight(string(out), "\n")
	if err != nil {
		detail := strings.TrimSpace(stderr.String())
		if detail == "" {
			detail = err.Error()
		}
		return s, errors.New(detail)
	}
	return s, nil
}

// conflictNames simulates the failed fold read-only (merge-tree) to NAME the conflicted
// files after FoldInBranch has already aborted the merge and restored the tree. Naming is
// best-effort: on any simulation hiccup the conflict stays reported, just without a file
// list — never an error, never a mutation.
func (b *Bench) conflictNames(ctx context.Context, ours, theirs string) []string {
	out, _ := b.gitRO(ctx, "merge-tree", "--write-tree", "--name-only", ours, theirs)
	lines := strings.Split(out, "\n")
	if len(lines) < 2 {
		return nil
	}
	var names []string
	for _, l := range lines[1:] { // line 0 is the written tree OID
		l = strings.TrimSpace(l)
		if l == "" {
			break // the informational section follows the name list
		}
		names = append(names, l)
	}
	return names
}

// DefaultBranchName is the repository's default branch, read from the local checkout. It is the
// same truth GitHub would report, asked of the clone — so resolving it never depends on the network
// being reachable.
func (b *Bench) DefaultBranchName() (string, error) { return b.defaultBranch() }

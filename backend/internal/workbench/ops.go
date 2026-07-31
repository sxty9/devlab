// The neutral primitives the chain motor composes over the workbench (S9): what the workbench is
// ahead of, where its delivery span starts, whether anything is loose, and the read/write of a
// single repo file. They live HERE, next to the branch they operate on, so the motor's seam adapter
// stays a one-to-one shim and no caller re-derives a git invocation of its own.
//
// Mutating ops go through the same per-user path as the rest of the workbench (gitUser); lookups
// run read-only as the service user (gitRO), exactly like the ported read-only package git.
package workbench

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"devlab/backend/internal/workspace"
)

// AheadOfDefault reports whether the workbench carries commits the default branch does not, and
// names its tip. A workbench that does not exist yet is not ahead and has no tip — that is an
// ANSWER, not an error: the preflight observation "nothing implemented" needs exactly this.
func (b *Bench) AheadOfDefault(ctx context.Context) (ahead bool, head string, err error) {
	if !b.refExists(ctx, "refs/heads/"+Branch) {
		return false, "", nil
	}
	head, err = b.Head(ctx)
	if err != nil {
		return false, "", err
	}
	def, err := b.defaultBranch()
	if err != nil {
		return false, head, err
	}
	base := b.defaultRef(ctx, def)
	if base == "" {
		// No default ref to compare against: the workbench exists, so work is present, but by
		// what margin is unknown — say "ahead" rather than claim a clean slate.
		return true, head, nil
	}
	n, err := b.countAhead(ctx, base, "refs/heads/"+Branch)
	if err != nil {
		return false, head, err
	}
	return n > 0, head, nil
}

// ContainedInDefault reports whether commit is contained in the default branch — the honest
// "already delivered" probe (REQ-020/K-3). An empty commit is never contained; an unresolvable
// default ref is an error, never a guessed false.
func (b *Bench) ContainedInDefault(ctx context.Context, commit string) (bool, error) {
	if strings.TrimSpace(commit) == "" || strings.HasPrefix(commit, "-") {
		return false, nil
	}
	def, err := b.defaultBranch()
	if err != nil {
		return false, err
	}
	base := b.defaultRef(ctx, def)
	if base == "" {
		return false, fmt.Errorf("workbench: default branch %s resolves to no ref — containment unknown", def)
	}
	// merge-base --is-ancestor answers by exit status: 0 contained, 1 not, anything else is a
	// real failure (a bad object) and must not read as "not contained".
	if _, err := b.gitRO(ctx, "merge-base", "--is-ancestor", commit, base); err != nil {
		if _, cerr := b.gitRO(ctx, "cat-file", "-e", commit+"^{commit}"); cerr != nil {
			return false, fmt.Errorf("workbench: %s is not a commit in this repository: %w", commit, cerr)
		}
		return false, nil
	}
	return true, nil
}

// MergeBaseDefault returns the merge base of the workbench and the default branch — the start of
// the span of undelivered work (the rest path of an already-implemented repo, REQ-020.3).
func (b *Bench) MergeBaseDefault(ctx context.Context) (string, error) {
	def, err := b.defaultBranch()
	if err != nil {
		return "", err
	}
	base := b.defaultRef(ctx, def)
	if base == "" {
		return "", fmt.Errorf("workbench: default branch %s resolves to no ref", def)
	}
	out, err := b.gitRO(ctx, "merge-base", base, "refs/heads/"+Branch)
	if err != nil {
		return "", fmt.Errorf("workbench: merge base of %s and %s: %w", Branch, base, err)
	}
	return strings.TrimSpace(out), nil
}

// CommitsAhead counts the commits the workbench carries beyond since.
func (b *Bench) CommitsAhead(ctx context.Context, since string) (int, error) {
	if strings.TrimSpace(since) == "" || strings.HasPrefix(since, "-") {
		return 0, fmt.Errorf("workbench: invalid span start %q", since)
	}
	return b.countAhead(ctx, since, "refs/heads/"+Branch)
}

// HasUncommitted reports whether anything is loose in the working tree (tracked edits, staged
// changes or untracked files) — the question "did the agent leave work behind that nobody
// committed?" (a clean tree after an agent that committed itself is NOT emptiness).
func (b *Bench) HasUncommitted(ctx context.Context) (bool, error) {
	out, err := b.gitRO(ctx, "status", "--porcelain")
	if err != nil {
		return false, fmt.Errorf("workbench: inspect working tree: %w", err)
	}
	return strings.TrimSpace(out) != "", nil
}

// CommitAll stages everything loose and commits it on the workbench, returning the new tip. It
// only ever ADDS a commit — the branch pointer is never moved backwards (K-1). An empty tree is
// not an error: the tip is returned unchanged, so a caller need not pre-check.
func (b *Bench) CommitAll(ctx context.Context, message, ghLogin string, ghID int64) (string, error) {
	if strings.TrimSpace(message) == "" {
		return "", errors.New("workbench: empty commit message")
	}
	dirty, err := b.HasUncommitted(ctx)
	if err != nil {
		return "", err
	}
	if !dirty {
		return b.Head(ctx)
	}
	if _, err := b.gitUser(ctx, "add", "-A"); err != nil {
		return "", fmt.Errorf("workbench: stage loose work: %w", err)
	}
	args := []string{}
	if ghLogin != "" {
		args = append(args, "-c", "user.name="+ghLogin,
			"-c", "user.email="+fmt.Sprintf("%d+%s@users.noreply.github.com", ghID, ghLogin))
	}
	if _, err := b.gitUser(ctx, append(args, "commit", "-m", message)...); err != nil {
		return "", fmt.Errorf("workbench: commit loose work: %w", err)
	}
	return b.Head(ctx)
}

// ReadFile reads one repo-relative file out of the working tree. exists=false is the answer for an
// absent file, never an error — a caller that maintains a block in a file must be able to ask.
func (b *Bench) ReadFile(rel string) (content string, exists bool, err error) {
	p, err := workspace.SafePath(b.repo, rel)
	if err != nil {
		return "", false, err
	}
	data, err := os.ReadFile(p)
	if err != nil {
		if os.IsNotExist(err) {
			return "", false, nil
		}
		return "", false, err
	}
	return string(data), true, nil
}

// WriteFile writes one repo-relative file in the working tree, as the workspace owner.
func (b *Bench) WriteFile(rel string, data []byte) error {
	return b.ex.WriteFileBytes(b.repo, rel, data)
}

// CurrentBranch names the branch the working tree is actually checked out on — the honest branch a
// log line reports instead of the workbench constant. Best-effort through the ported read-only
// primitive: "" (or "HEAD" when detached) on any hiccup, never an error that could sink a run.
func (b *Bench) CurrentBranch(ctx context.Context) string {
	return b.ex.CurrentBranch(ctx, b.repo)
}

// BranchAt snapshots a branch at a commit WITHOUT checking it out — the delivery branch is cut
// from the workbench, so the workbench itself is never re-pointed (K-1).
func (b *Bench) BranchAt(ctx context.Context, name, at string) error {
	return b.ex.BranchAt(ctx, b.repo, name, at)
}

// PushBranch pushes one named branch to origin (never forced).
func (b *Bench) PushBranch(ctx context.Context, name string) error {
	if name == Branch {
		// The workbench has its own publish path (fold + retry); routing it through here would
		// be a second, weaker way to the same effect.
		return b.Publish(ctx)
	}
	_, err := b.ex.PushRefs(ctx, b.repo, b.token, false, name)
	return err
}

// defaultRef picks the ref the default branch is compared through: the fetched remote state when
// present (the truth others pushed), else the local branch. "" when neither resolves.
func (b *Bench) defaultRef(ctx context.Context, def string) string {
	if ref := "refs/remotes/origin/" + def; b.refExists(ctx, ref) {
		return ref
	}
	if ref := "refs/heads/" + def; b.refExists(ctx, ref) {
		return ref
	}
	return ""
}

// countAhead counts commits reachable from head but not from base.
func (b *Bench) countAhead(ctx context.Context, base, head string) (int, error) {
	out, err := b.gitRO(ctx, "rev-list", "--count", base+".."+head)
	if err != nil {
		return 0, fmt.Errorf("workbench: count %s..%s: %w", base, head, err)
	}
	n, err := strconv.Atoi(strings.TrimSpace(out))
	if err != nil {
		return 0, fmt.Errorf("workbench: unparsable commit count %q: %w", out, err)
	}
	return n, nil
}

// RepoDir names the working tree this bench operates — the path the deploy build and the
// detection read (they work on files, not on refs).
func (b *Bench) RepoDir() string { return filepath.Clean(b.repo) }

package workspace

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// Git plumbing for the Mercury rollout. The rollout writes one file (CLAUDE.md) into every holistic
// repo without ever checking a working tree out: it reads the file at the remote's default branch,
// splices the generated block, and builds a new commit purely from objects (hash-object → mktree →
// commit-tree → push). Because nothing is checked out, a repo's uncommitted local state, its
// current branch, and even a CLAUDE.MD → CLAUDE.md rename are all irrelevant, and the operation is
// idempotent: an unchanged block yields the same tree and no commit.

// plumbRaw runs one plumbing subcommand and returns its stdout verbatim (no trimming). Errors carry
// stderr. stdin is optional (hash-object/mktree take content on stdin). Used where output bytes are
// content (cat-file), where a stray trim would corrupt the round-trip.
func (e Executor) plumbRaw(ctx context.Context, wt, token string, stdin []byte, args ...string) (string, error) {
	cmd := e.gitIn(ctx, wt, token, args...)
	if stdin != nil {
		cmd.Stdin = strings.NewReader(string(stdin))
	}
	var out, errb strings.Builder
	cmd.Stdout = &out
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(errb.String())
		if msg == "" {
			msg = err.Error()
		}
		return "", errors.New(msg)
	}
	return out.String(), nil
}

// plumb is plumbRaw with the trailing newline trimmed — for subcommands whose output is a single
// token (an object hash, a ref), not content.
func (e Executor) plumb(ctx context.Context, wt, token string, stdin []byte, args ...string) (string, error) {
	out, err := e.plumbRaw(ctx, wt, token, stdin, args...)
	return strings.TrimRight(out, "\n"), err
}

// RolloutResult reports what a single-repo rollout did.
type RolloutResult struct {
	Changed bool   `json:"changed"` // false when the block was already current (idempotent)
	Commit  string `json:"commit,omitempty"`
	Branch  string `json:"branch,omitempty"` // where the commit was pushed (a PR branch, or the default branch)
	OldFile string `json:"-"` // the pre-rollout CLAUDE.md content (for a dry-run diff)
	NewFile string `json:"-"` // the post-rollout content
}

// SyncFile brings `path` on the remote's `branch` to `newContent`, via pure plumbing, and pushes.
// It never touches the working tree, so it is safe over a repo with local changes and needs no
// checkout. It is idempotent: if the resulting root tree equals the base tree, nothing is pushed.
// dryRun does everything except the push, so a caller can preview the exact change. It also drops a
// second file at the root (oldPath, e.g. CLAUDE.MD) if present — the case-rename that a working-tree
// checkout could not do.
//
// pushBranch decides WHERE the new commit lands: empty pushes onto `branch` itself (the historical
// direct-to-default-branch rollout), a branch name pushes there instead so the caller can raise a pull
// request. The latter is what makes a protected default branch possible — the constitution then reaches
// a repo the same way every other change does.
func (e Executor) SyncFile(ctx context.Context, wt, token, branch, path, oldPath string, splice func(old string) string, dryRun bool, pushBranch string) (RolloutResult, error) {
	if err := e.Fetch(ctx, wt, token); err != nil {
		return RolloutResult{}, fmt.Errorf("fetch: %w", err)
	}
	base, err := e.plumb(ctx, wt, token, nil, "rev-parse", "origin/"+branch)
	if err != nil {
		return RolloutResult{}, fmt.Errorf("rev-parse origin/%s: %w", branch, err)
	}

	// Read the file to splice: the correctly-cased path if it exists, else the wrong-cased one
	// (which we are migrating away from) so its content carries into the new CLAUDE.md.
	old := e.catFile(ctx, wt, token, base+":"+path)
	if old == "" && oldPath != "" {
		old = e.catFile(ctx, wt, token, base+":"+oldPath)
	}
	res := RolloutResult{OldFile: old, NewFile: splice(old)}

	blob, err := e.plumb(ctx, wt, token, []byte(res.NewFile), "hash-object", "-w", "--stdin")
	if err != nil {
		return res, fmt.Errorf("hash-object: %w", err)
	}

	// Rebuild the ROOT tree: every base entry except the CLAUDE files, plus our new CLAUDE.md.
	lsRaw, err := e.plumb(ctx, wt, token, nil, "ls-tree", base)
	if err != nil {
		return res, fmt.Errorf("ls-tree: %w", err)
	}
	var tree strings.Builder
	for _, line := range strings.Split(lsRaw, "\n") {
		if line == "" {
			continue
		}
		// "<mode> <type> <sha>\t<name>"
		name := line[strings.IndexByte(line, '\t')+1:]
		if name == path || (oldPath != "" && name == oldPath) {
			continue
		}
		tree.WriteString(line)
		tree.WriteByte('\n')
	}
	tree.WriteString(fmt.Sprintf("100644 blob %s\t%s\n", blob, path))

	newTree, err := e.plumb(ctx, wt, token, []byte(tree.String()), "mktree")
	if err != nil {
		return res, fmt.Errorf("mktree: %w", err)
	}
	baseTree, err := e.plumb(ctx, wt, token, nil, "rev-parse", base+"^{tree}")
	if err != nil {
		return res, fmt.Errorf("rev-parse tree: %w", err)
	}
	if newTree == baseTree {
		return res, nil // idempotent: nothing changed
	}
	res.Changed = true
	if dryRun {
		return res, nil
	}

	commit, err := e.plumb(ctx, wt, token, nil, "commit-tree", newTree, "-p", base, "-m", "mercury: sync Holistic axioms + implementation rules")
	if err != nil {
		return res, fmt.Errorf("commit-tree: %w", err)
	}
	res.Commit = commit
	target := branch
	if pushBranch != "" {
		target = pushBranch
	}
	if _, err := e.plumb(ctx, wt, token, nil, "push", "--force", "origin", commit+":refs/heads/"+target); err != nil {
		return res, fmt.Errorf("push: %w", err)
	}
	res.Branch = target
	if lerr := assertNoLeak(wt, token); lerr != nil {
		return res, lerr
	}
	return res, nil
}

// catFile returns the content at a git ref path, or "" when it does not exist (a repo without a
// CLAUDE.md — normal). Errors other than absence also collapse to "" so a missing file is not fatal.
func (e Executor) catFile(ctx context.Context, wt, token, ref string) string {
	out, err := e.plumbRaw(ctx, wt, token, nil, "cat-file", "-p", ref)
	if err != nil {
		return ""
	}
	return out
}

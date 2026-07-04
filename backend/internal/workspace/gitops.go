package workspace

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
	"devlab/backend/internal/model"
)

const maxFileBytes = 2 << 20 // 2 MiB, matches the editor read cap in package git

var (
	errEscape   = errors.New("path escapes repository")
	errDotGit   = errors.New("refused: .git is not editable")
	errTooLarge = errors.New("file too large")
	branchRe    = regexp.MustCompile(`^[A-Za-z0-9._/-]+$`)
)

// gitCmd builds a git invocation in worktree wt. When token != "" it injects an inline credential
// helper that reads the token from the process ENV (DEVLAB_GH_TOKEN) — never from argv, the remote
// URL, or .git/config — so the secret never lands in `ps`, the reflog, or on disk. The leading
// empty `credential.helper=` clears any inherited system/global helper. GIT_TERMINAL_PROMPT=0
// makes auth failures fail fast instead of blocking on a prompt.
func gitCmd(ctx context.Context, wt, token string, args ...string) *exec.Cmd {
	var pre []string
	if wt != "" {
		pre = append(pre, "-C", wt)
	}
	if token != "" {
		helper := `!f() { printf 'username=x-access-token\npassword=%s\n' "$DEVLAB_GH_TOKEN"; }; f`
		pre = append(pre, "-c", "credential.helper=", "-c", "credential.helper="+helper)
	}
	cmd := exec.CommandContext(ctx, "git", append(pre, args...)...)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	if token != "" {
		cmd.Env = append(cmd.Env, "DEVLAB_GH_TOKEN="+token)
	}
	return cmd
}

// runGit executes a git command and returns combined stdout+stderr (trimmed). On failure the
// output is returned as the error detail (surfaced verbatim to the client).
func runGit(cmd *exec.Cmd) (string, error) {
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

// assertNoLeak is defense-in-depth: confirm the token never ended up in .git/config after a
// network op. Since we never write it there, this should always pass.
func assertNoLeak(wt, token string) error {
	if token == "" {
		return nil
	}
	b, err := os.ReadFile(filepath.Join(wt, ".git", "config"))
	if err != nil {
		return nil
	}
	if strings.Contains(string(b), token) {
		return errors.New("aborted: credential leaked into .git/config")
	}
	return nil
}

func clone(ctx context.Context, wt, url, token string) error {
	// core.symlinks=false makes git materialize any committed symlink as a plain text file (its
	// target path) instead of a real symlink, neutralizing symlink-escape reads/writes for the
	// whole life of this clone (persisted in .git/config, so branch switches honor it too).
	if _, err := runGit(gitCmd(ctx, "", token, "-c", "core.symlinks=false", "clone", "--", url, wt)); err != nil {
		return fmt.Errorf("clone failed: %w", err)
	}
	if err := assertNoLeak(wt, token); err != nil {
		return err
	}
	return nil
}

// ─── Path safety ────────────────────────────────────────────────────────────

// safePath resolves a repo-relative path against the worktree, refusing traversal and any path
// under .git. It also refuses to follow a symlink that escapes the (symlink-resolved) worktree.
func safePath(wt, rel string) (string, error) {
	// Refuse any explicit parent traversal outright (clearer than silently clamping it inside).
	for _, part := range strings.Split(rel, "/") {
		if part == ".." {
			return "", errEscape
		}
	}
	clean := filepath.Clean("/" + rel) // anchor so .. cannot climb above root
	root := filepath.Clean(wt)
	abs := filepath.Join(root, clean)
	if abs != root && !strings.HasPrefix(abs, root+string(os.PathSeparator)) {
		return "", errEscape
	}
	relClean := strings.TrimPrefix(clean, "/")
	if relClean == ".git" || strings.HasPrefix(relClean, ".git/") {
		return "", errDotGit
	}
	if err := assertNoSymlinkEscape(root, abs); err != nil {
		return "", err
	}
	return abs, nil
}

// SafePath resolves a repo-relative path against the worktree with the traversal/.git/symlink
// guards and returns the absolute path. Exported for the Vision raw-byte file server.
func SafePath(wt, rel string) (string, error) { return safePath(wt, rel) }

// assertNoSymlinkEscape resolves the deepest existing ancestor of abs and verifies it stays inside
// the symlink-resolved worktree, so a pre-existing symlinked directory can't redirect a write out.
func assertNoSymlinkEscape(wt, abs string) error {
	root, err := filepath.EvalSymlinks(wt)
	if err != nil {
		return err
	}
	p := abs
	for {
		if _, err := os.Lstat(p); err == nil {
			real, err := filepath.EvalSymlinks(p)
			if err != nil {
				return err
			}
			if real != root && !strings.HasPrefix(real, root+string(os.PathSeparator)) {
				return errEscape
			}
			return nil
		}
		parent := filepath.Dir(p)
		if parent == p {
			return nil // reached the top without an existing ancestor (shouldn't happen)
		}
		p = parent
	}
}

// relFor validates rel and returns the repo-relative pathspec git should act on.
func relFor(wt, rel string) (string, error) {
	if _, err := safePath(wt, rel); err != nil {
		return "", err
	}
	return strings.TrimPrefix(filepath.Clean("/"+rel), "/"), nil
}

// ─── Mutating ops (token only for the network ones) ─────────────────────────

// WriteFile writes content to a repo-relative path (creating parent dirs), refusing traversal,
// .git, oversize, and symlink escapes.
func WriteFile(wt, rel string, content []byte) error {
	return writeBytes(wt, rel, content, maxFileBytes)
}

// maxVisionBytes is the upload cap for Vision-Catalog assets (images/PDFs) — larger than the
// editor's text cap since binary artifacts are legitimately bigger.
const maxVisionBytes = 25 << 20 // 25 MiB

// WriteFileBytes writes arbitrary bytes (e.g. an uploaded image/PDF) with the vision cap and the
// same traversal/.git/symlink guards as WriteFile.
func WriteFileBytes(wt, rel string, content []byte) error {
	return writeBytes(wt, rel, content, maxVisionBytes)
}

func writeBytes(wt, rel string, content []byte, limit int) error {
	if len(content) > limit {
		return errTooLarge
	}
	abs, err := safePath(wt, rel)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return err
	}
	return os.WriteFile(abs, content, 0o644)
}

// DeleteFile removes a working-tree file (traversal/.git/symlink guarded). Idempotent when the
// file is already gone. A tracked file then shows as a deletion for the caller to commit+push.
func DeleteFile(wt, rel string) error {
	abs, err := safePath(wt, rel)
	if err != nil {
		return err
	}
	if err := os.Remove(abs); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// Stage adds a path to the index.
func Stage(ctx context.Context, wt, rel string) error {
	p, err := relFor(wt, rel)
	if err != nil {
		return err
	}
	_, err = runGit(gitCmd(ctx, wt, "", "add", "--", p))
	return err
}

// Unstage removes a path from the index (keeping working-tree changes).
func Unstage(ctx context.Context, wt, rel string) error {
	p, err := relFor(wt, rel)
	if err != nil {
		return err
	}
	// restore --staged is the modern reset-of-index; works whether or not the file is tracked.
	_, err = runGit(gitCmd(ctx, wt, "", "restore", "--staged", "--", p))
	return err
}

// Commit commits the staged index, authored as the linked GitHub identity (noreply email). The
// short hash and the post-commit branch name are returned.
func Commit(ctx context.Context, wt, message, ghLogin string, ghID int64) (hash, branch string, err error) {
	if strings.TrimSpace(message) == "" {
		return "", "", errors.New("empty commit message")
	}
	var pre []string
	if ghLogin != "" {
		email := fmt.Sprintf("%d+%s@users.noreply.github.com", ghID, ghLogin)
		pre = append(pre, "-c", "user.name="+ghLogin, "-c", "user.email="+email)
	}
	args := append(pre, "commit", "-m", message)
	if _, err := runGit(gitCmd(ctx, wt, "", args...)); err != nil {
		return "", "", err
	}
	short, _ := runGit(gitCmd(ctx, wt, "", "rev-parse", "--short", "HEAD"))
	return short, CurrentBranch(ctx, wt), nil
}

// Push pushes the current branch to origin (setting upstream on first push). Token authorizes it.
func Push(ctx context.Context, wt, token string) (string, error) {
	branch := CurrentBranch(ctx, wt)
	if branch == "" || branch == "HEAD" {
		return "", errors.New("cannot push a detached HEAD")
	}
	out, err := runGit(gitCmd(ctx, wt, token, "push", "-u", "origin", branch))
	if lerr := assertNoLeak(wt, token); lerr != nil {
		return out, lerr
	}
	return out, err
}

// Pull fast-forwards the current branch from origin. Token authorizes it.
func Pull(ctx context.Context, wt, token string) (string, error) {
	out, err := runGit(gitCmd(ctx, wt, token, "pull", "--ff-only"))
	if lerr := assertNoLeak(wt, token); lerr != nil {
		return out, lerr
	}
	return out, err
}

// Fetch updates remote-tracking refs without touching the working tree. Token authorizes it.
func Fetch(ctx context.Context, wt, token string) error {
	_, err := runGit(gitCmd(ctx, wt, token, "fetch", "--prune", "origin"))
	if lerr := assertNoLeak(wt, token); lerr != nil {
		return lerr
	}
	return err
}

// CreateBranch creates and checks out a new branch (optionally from a start point).
func CreateBranch(ctx context.Context, wt, name, from string) error {
	if !branchRe.MatchString(name) || strings.Contains(name, "..") || strings.HasPrefix(name, "-") {
		return fmt.Errorf("invalid branch name %q", name)
	}
	args := []string{"switch", "-c", name}
	if from != "" {
		if !branchRe.MatchString(from) || strings.HasPrefix(from, "-") {
			return fmt.Errorf("invalid start point %q", from)
		}
		args = append(args, from)
	}
	_, err := runGit(gitCmd(ctx, wt, "", args...))
	return err
}

// Checkout switches the working tree to an existing branch.
func Checkout(ctx context.Context, wt, name string) error {
	if !branchRe.MatchString(name) || strings.HasPrefix(name, "-") {
		return fmt.Errorf("invalid branch name %q", name)
	}
	_, err := runGit(gitCmd(ctx, wt, "", "switch", "--", name))
	return err
}

// CurrentBranch returns the checked-out branch (empty on error / detached returns "HEAD").
func CurrentBranch(ctx context.Context, wt string) string {
	s, err := runGit(gitCmd(ctx, wt, "", "rev-parse", "--abbrev-ref", "HEAD"))
	if err != nil {
		return ""
	}
	return s
}

// Changes is a thin re-export so handlers can refresh the VCS view after a mutation.
func Changes(wt string) []model.Change { return git.Changes(wt) }

// Branches re-exports the branch list with refreshed tracking info.
func Branches(wt string) []model.Branch { return git.Branches(wt) }

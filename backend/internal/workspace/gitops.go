package workspace

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"devlab/backend/internal/git"
	"devlab/backend/internal/model"
)

const (
	maxFileBytes   = 2 << 20  // 2 MiB, matches the editor read cap in package git
	maxVisionBytes = 25 << 20 // 25 MiB cap for Vision-Catalog assets (images/PDFs)

	execWrapper        = "/usr/local/sbin/devlab-exec"        // per-user op runner (sudo -u <user>)
	mkWorkspaceWrapper = "/usr/local/sbin/devlab-mkworkspace" // root provisioner (chown per-user dir)
)

var (
	errEscape   = errors.New("path escapes repository")
	errDotGit   = errors.New("refused: .git is not editable")
	errTooLarge = errors.New("file too large")
	branchRe    = regexp.MustCompile(`^[A-Za-z0-9._/-]+$`)
)

// Executor runs the MUTATING git/file ops. In production (PerUser) they run AS the target Linux user
// via the pinned devlab-exec sudo wrapper — so files are owned by the user, the terminal/agent share
// one coherently-owned repo, and the service can never write outside a user's workspace. Under
// dev-bypass they run directly as the devlab service user (single-operator sandbox). Read-only git
// (package git) always runs directly, with -c safe.directory so it can read user-owned repos.
type Executor struct {
	User    string
	PerUser bool
}

// credHelperArgs returns the inline credential-helper -c args when a token is present. The token
// stays in the process ENV (DEVLAB_GH_TOKEN), never on argv — even across the sudo boundary, where
// the sudoers env_keep forwards it.
func credHelperArgs(token string) []string {
	if token == "" {
		return nil
	}
	helper := `!f() { printf 'username=x-access-token\npassword=%s\n' "$DEVLAB_GH_TOKEN"; }; f`
	return []string{"-c", "credential.helper=", "-c", "credential.helper=" + helper}
}

// gitIn builds a git invocation that runs in wt. Direct (bypass): `git -C wt …`. Per-user:
// `sudo -n -u <user> devlab-exec git wt …` (the wrapper cd's to wt, confined to the user's root).
func (e Executor) gitIn(ctx context.Context, wt, token string, args ...string) *exec.Cmd {
	gitArgs := append(credHelperArgs(token), args...)
	var cmd *exec.Cmd
	if e.PerUser {
		full := append([]string{"-n", "-u", e.User, execWrapper, "git", wt}, gitArgs...)
		cmd = exec.CommandContext(ctx, "sudo", full...)
	} else {
		full := append([]string{"-c", "safe.directory=*", "-C", wt}, gitArgs...)
		cmd = exec.CommandContext(ctx, "git", full...)
	}
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	if token != "" {
		cmd.Env = append(cmd.Env, "DEVLAB_GH_TOKEN="+token)
	}
	return cmd
}

// runGit executes a git command and returns combined stdout+stderr (trimmed). On failure the output
// is returned as the error detail (surfaced verbatim to the client).
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

// runPipe runs a wrapper op that takes file bytes on stdin (write verb).
func runPipe(cmd *exec.Cmd, stdin []byte) error {
	cmd.Stdin = bytes.NewReader(stdin)
	out, err := cmd.CombinedOutput()
	if err != nil {
		s := strings.TrimSpace(string(out))
		if s == "" {
			s = err.Error()
		}
		return errors.New(s)
	}
	return nil
}

// assertNoLeak is defense-in-depth: confirm the token never ended up in .git/config after a network
// op. Read-only, runs as the service user (config is group-readable in production).
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

// clone clones fullName's repo into wt (creating it), with core.symlinks=false so committed symlinks
// never materialize. Per-user: cwd = the workspace root (validated by the wrapper); the token is env.
func (e Executor) clone(ctx context.Context, wt, url, token string) error {
	args := append(credHelperArgs(token), "-c", "core.symlinks=false", "clone", "--", url, wt)
	var cmd *exec.Cmd
	if e.PerUser {
		root := filepath.Dir(wt)
		full := append([]string{"-n", "-u", e.User, execWrapper, "git", root}, args...)
		cmd = exec.CommandContext(ctx, "sudo", full...)
	} else {
		cmd = exec.CommandContext(ctx, "git", args...)
	}
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	if token != "" {
		cmd.Env = append(cmd.Env, "DEVLAB_GH_TOKEN="+token)
	}
	if _, err := runGit(cmd); err != nil {
		return fmt.Errorf("clone failed: %w", err)
	}
	return assertNoLeak(wt, token)
}

// Agent runs the claude CLI as the user inside the workspace via the pinned devlab-exec `claude`
// verb, returning its stdout (the CLI's JSON result). This is the FULL agentic AI: claude reads
// and edits the real workspace with the user's OWN ~/.claude config, rights and subscription —
// no key is injected. Per-user: `sudo -n -u <user> devlab-exec claude wt …` (the wrapper cd's to
// wt as the user and sets their HOME). Under dev-bypass it runs claude directly in wt (sandbox).
// The prompt and flags are passed as argv (exec, not a shell) so there is no injection surface.
// Long-running; the caller owns the context deadline. stderr is folded into the error on failure.
func (e Executor) Agent(ctx context.Context, wt string, args ...string) ([]byte, error) {
	return runAgentCmd(ctx, e.buildAgentCmd(wt, args...), nil)
}

// AgentStream is Agent that ALSO reports stdout line by line as it arrives (via onStdout), so the
// autonomous runner can be followed live while it works. onStdout receives each raw line with its
// trailing newline stripped; it must not retain the slice. The FULL stdout is still returned, so the
// caller parses the final result exactly as in the buffered path. A nil onStdout is exactly Agent.
func (e Executor) AgentStream(ctx context.Context, wt string, onStdout func([]byte), args ...string) ([]byte, error) {
	return runAgentCmd(ctx, e.buildAgentCmd(wt, args...), onStdout)
}

// buildAgentCmd constructs the claude CLI invocation (per-user via devlab-exec, or direct under
// dev-bypass), with its own process group so a cancellation can kill the WHOLE tree.
func (e Executor) buildAgentCmd(wt string, args ...string) *exec.Cmd {
	var cmd *exec.Cmd
	if e.PerUser {
		full := append([]string{"-n", "-u", e.User, execWrapper, "claude", wt}, args...)
		cmd = exec.Command("sudo", full...)
	} else {
		cmd = exec.Command("claude", args...)
		cmd.Dir = wt
	}
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	// Own process group so a cancellation kills the WHOLE tree (sudo → devlab-exec → claude). NOT
	// CommandContext: it SIGKILLs only the direct child (sudo), orphaning the long-running claude —
	// a broken kill-switch that keeps consuming the subscription and holds the run lock.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	return cmd
}

// runAgentCmd starts cmd, drains its stdout to EOF (streaming each line to onStdout when non-nil), and
// enforces the process-group kill-switch on ctx cancellation. It returns the FULL stdout captured — even
// on error or cancel, so the caller keeps whatever the agent produced — and folds stderr into the error.
// The single implementation behind both Agent (onStdout nil, buffered) and AgentStream (live).
func runAgentCmd(ctx context.Context, cmd *exec.Cmd, onStdout func([]byte)) ([]byte, error) {
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	var stderr bytes.Buffer // an io.Writer (not *os.File) → os/exec drains it in its own goroutine (no deadlock)
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			if cmd.Process != nil {
				// Negative pid = signal the whole process group; SIGTERM first (let claude flush),
				// then SIGKILL as a backstop. Wait for the SIGTERM to take before escalating — but
				// stop if the process is already reaped (done), so a recycled pgid is never SIGKILL'd.
				_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
				select {
				case <-done:
				case <-time.After(3 * time.Second):
					// Re-check done non-blockingly: if Wait() reaped the group in the instant the timer
					// fired (select picks randomly when both are ready), the pgid may be recycled — skip
					// the SIGKILL rather than signal an unrelated group.
					select {
					case <-done:
					default:
						_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
					}
				}
			}
		case <-done:
		}
	}()

	// Read to EOF (the process dying closes the pipe). ReadBytes, not bufio.Scanner, so one very large
	// line — a big tool result in stream-json — never trips a token-size cap. Must finish reading before
	// Wait() (StdoutPipe closes on exit).
	var out bytes.Buffer
	br := bufio.NewReader(stdout)
	for {
		line, rerr := br.ReadBytes('\n')
		if len(line) > 0 {
			out.Write(line)
			if onStdout != nil {
				onStdout(bytes.TrimRight(line, "\r\n"))
			}
		}
		if rerr != nil {
			break
		}
	}

	werr := cmd.Wait()
	close(done)
	if werr != nil {
		if ctx.Err() != nil {
			return out.Bytes(), ctx.Err() // report the cancellation/deadline, not the wait error
		}
		s := strings.TrimSpace(stderr.String())
		if s == "" {
			s = strings.TrimSpace(out.String())
		}
		if s == "" {
			s = werr.Error()
		}
		return out.Bytes(), errors.New(s)
	}
	return out.Bytes(), nil
}

// provisionWorkspace creates the user's workspace root (owned by the user) via the root helper.
func provisionWorkspace(user string) error {
	cmd := exec.Command("sudo", "-n", mkWorkspaceWrapper, user)
	cmd.Env = os.Environ()
	if out, err := cmd.CombinedOutput(); err != nil {
		s := strings.TrimSpace(string(out))
		if s == "" {
			s = err.Error()
		}
		return fmt.Errorf("provision workspace: %s", s)
	}
	return nil
}

// ─── Path safety ────────────────────────────────────────────────────────────

// safePath resolves a repo-relative path against the worktree, refusing traversal and any path under
// .git, and refusing to follow a symlink that escapes the (symlink-resolved) worktree.
func safePath(wt, rel string) (string, error) {
	for _, part := range strings.Split(rel, "/") {
		if part == ".." {
			return "", errEscape
		}
	}
	clean := filepath.Clean("/" + rel)
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

// SafePath resolves a repo-relative path with the traversal/.git/symlink guards. Exported for the
// Vision raw-byte file server (reads).
func SafePath(wt, rel string) (string, error) { return safePath(wt, rel) }

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
			return nil
		}
		p = parent
	}
}

func relFor(wt, rel string) (string, error) {
	if _, err := safePath(wt, rel); err != nil {
		return "", err
	}
	return strings.TrimPrefix(filepath.Clean("/"+rel), "/"), nil
}

// ─── Mutating ops (run as the user in production) ───────────────────────────

// WriteFile writes editor content to a repo-relative path (creating parent dirs).
func (e Executor) WriteFile(wt, rel string, content []byte) error {
	return e.writeBytes(wt, rel, content, maxFileBytes)
}

// WriteFileBytes writes arbitrary bytes (e.g. an uploaded image/PDF) with the vision cap.
func (e Executor) WriteFileBytes(wt, rel string, content []byte) error {
	return e.writeBytes(wt, rel, content, maxVisionBytes)
}

func (e Executor) writeBytes(wt, rel string, content []byte, limit int) error {
	if len(content) > limit {
		return errTooLarge
	}
	abs, err := safePath(wt, rel) // service-side guard (the wrapper re-validates too)
	if err != nil {
		return err
	}
	if !e.PerUser {
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			return err
		}
		return os.WriteFile(abs, content, 0o644)
	}
	return runPipe(exec.Command("sudo", "-n", "-u", e.User, execWrapper, "write", abs), content)
}

// DeleteFile removes a working-tree file (idempotent). A tracked file then shows as a deletion.
func (e Executor) DeleteFile(wt, rel string) error {
	abs, err := safePath(wt, rel)
	if err != nil {
		return err
	}
	if !e.PerUser {
		if err := os.Remove(abs); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	cmd := exec.Command("sudo", "-n", "-u", e.User, execWrapper, "rm", abs)
	cmd.Env = os.Environ()
	if out, err := cmd.CombinedOutput(); err != nil {
		return errors.New(strings.TrimSpace(string(out)))
	}
	return nil
}

// Stage adds a path to the index.
func (e Executor) Stage(ctx context.Context, wt, rel string) error {
	p, err := relFor(wt, rel)
	if err != nil {
		return err
	}
	_, err = runGit(e.gitIn(ctx, wt, "", "add", "--", p))
	return err
}

// Unstage removes a path from the index (keeping working-tree changes).
func (e Executor) Unstage(ctx context.Context, wt, rel string) error {
	p, err := relFor(wt, rel)
	if err != nil {
		return err
	}
	_, err = runGit(e.gitIn(ctx, wt, "", "restore", "--staged", "--", p))
	return err
}

// Commit commits the staged index, authored as the linked GitHub identity (noreply email).
func (e Executor) Commit(ctx context.Context, wt, message, ghLogin string, ghID int64) (hash, branch string, err error) {
	if strings.TrimSpace(message) == "" {
		return "", "", errors.New("empty commit message")
	}
	var pre []string
	if ghLogin != "" {
		email := fmt.Sprintf("%d+%s@users.noreply.github.com", ghID, ghLogin)
		pre = append(pre, "-c", "user.name="+ghLogin, "-c", "user.email="+email)
	}
	if _, err := runGit(e.gitIn(ctx, wt, "", append(pre, "commit", "-m", message)...)); err != nil {
		return "", "", err
	}
	short, _ := runGit(e.gitIn(ctx, wt, "", "rev-parse", "--short", "HEAD"))
	return short, e.CurrentBranch(ctx, wt), nil
}

// Push pushes the current branch to origin (setting upstream on first push).
func (e Executor) Push(ctx context.Context, wt, token string) (string, error) {
	branch := e.CurrentBranch(ctx, wt)
	if branch == "" || branch == "HEAD" {
		return "", errors.New("cannot push a detached HEAD")
	}
	out, err := runGit(e.gitIn(ctx, wt, token, "push", "-u", "origin", branch))
	if lerr := assertNoLeak(wt, token); lerr != nil {
		return out, lerr
	}
	return out, err
}

// Pull fast-forwards the current branch from origin.
func (e Executor) Pull(ctx context.Context, wt, token string) (string, error) {
	out, err := runGit(e.gitIn(ctx, wt, token, "pull", "--ff-only"))
	if lerr := assertNoLeak(wt, token); lerr != nil {
		return out, lerr
	}
	return out, err
}

// Fetch updates remote-tracking refs without touching the working tree.
func (e Executor) Fetch(ctx context.Context, wt, token string) error {
	_, err := runGit(e.gitIn(ctx, wt, token, "fetch", "--prune", "origin"))
	if lerr := assertNoLeak(wt, token); lerr != nil {
		return lerr
	}
	return err
}

// ResetToRemote materializes <branch> at origin/<branch> and checks it out, dropping untracked
// leftovers. For the AUTONOMOUS RUNNER only — Manager.Ensure clones once and then returns the existing
// checkout forever, so without this a build reads whatever was cloned the first time, silently working
// against stale code. It is used to materialize the MERGED default branch for a prod build.
//
// Destructive to <branch> and the working tree by design (a stale checkout must not leak into a build),
// but it operates on the NAMED branch only: it force-moves <branch> and never touches any OTHER local
// branch. Crucially it must not clobber the persistent dev branch (mercury-dev) — an interrupted run's
// committed-but-unpublished dev commits live ONLY on that local ref, so re-pointing whatever happens to be
// checked out (the old `reset --hard`) could destroy them (req 4). Checking out <branch> explicitly leaves
// mercury-dev's ref exactly where it was.
func (e Executor) ResetToRemote(ctx context.Context, wt, token, branch string) error {
	if !branchRe.MatchString(branch) || strings.HasPrefix(branch, "-") {
		return fmt.Errorf("invalid branch name %q", branch)
	}
	if err := e.Fetch(ctx, wt, token); err != nil {
		return fmt.Errorf("fetch: %w", err)
	}
	if _, err := runGit(e.gitIn(ctx, wt, "", "checkout", "-f", "-B", branch, "origin/"+branch)); err != nil {
		return fmt.Errorf("checkout %s: %w", branch, err)
	}
	// -x also removes ignored files (build artefacts, node_modules): a stale artefact can make an
	// agent's verification pass for the wrong reason.
	if _, err := runGit(e.gitIn(ctx, wt, "", "clean", "-fdx")); err != nil {
		return fmt.Errorf("clean: %w", err)
	}
	return nil
}

// ─── Growing dev integration branch (the autonomous runner) ─────────────────
//
// The runner no longer RECONSTRUCTS each run's state from "default branch + a hand-picked set of open
// PRs" (which silently dropped finished work whenever the pick missed a PR). Instead a single
// persistent dev branch per repo (mercury-dev) GROWS: every run sits on the accumulated state, folds
// the default branch INTO it, and adds its work on top. The remote mercury-dev is the durable, nameable
// record of exactly what dev serves. See EnsureDevBranch / FoldInBranch / CleanWorktree.

// ErrMergeConflict is returned by FoldInBranch when folding the default branch conflicts with the
// accumulated dev state. It is NON-fatal by design: the caller records a step and proceeds on the dev
// branch as-is (the state simply lacks the very newest default until a run resolves it) — the run is
// never sunk by it.
var ErrMergeConflict = errors.New("merge conflict")

// remoteRefExists reports whether origin/<branch> exists in the local remote-tracking refs. Call after
// a Fetch so the answer reflects the real remote. A bad branch name is reported as "does not exist"
// rather than reaching git.
func (e Executor) remoteRefExists(ctx context.Context, wt, branch string) bool {
	if !branchRe.MatchString(branch) || strings.HasPrefix(branch, "-") {
		return false
	}
	_, err := runGit(e.gitIn(ctx, wt, "", "rev-parse", "--verify", "--quiet", "refs/remotes/origin/"+branch))
	return err == nil
}

// localRefExists reports whether a LOCAL branch exists (refs/heads/<branch>) in this workspace — the
// signal that a previous run already established the dev branch here and its committed work must be
// protected. A bad name is reported as absent rather than reaching git.
func (e Executor) localRefExists(ctx context.Context, wt, branch string) bool {
	if !branchRe.MatchString(branch) || strings.HasPrefix(branch, "-") {
		return false
	}
	_, err := runGit(e.gitIn(ctx, wt, "", "rev-parse", "--verify", "--quiet", "refs/heads/"+branch))
	return err == nil
}

// unpublishedCount counts commits reachable from HEAD but from NEITHER the default branch NOR (when it
// exists) the pushed dev branch — the committed work an interrupted run left that no remote yet carries.
// Best-effort: a git error yields 0 (the caller only uses it to report, never to gate work).
func (e Executor) unpublishedCount(ctx context.Context, wt, defaultBranch, devBranch string, remoteDev bool) int {
	args := []string{"rev-list", "--count", "HEAD", "--not", "origin/" + defaultBranch}
	if remoteDev {
		args = append(args, "origin/"+devBranch)
	}
	out, err := runGit(e.gitIn(ctx, wt, "", args...))
	if err != nil {
		return 0
	}
	n, _ := strconv.Atoi(strings.TrimSpace(out))
	return n
}

// DevBranchPrep reports what preparing the dev branch found, so the caller records it honestly. Its whole
// point is that the committed dev history is NEVER reset onto a remote: whatever an interrupted prior run
// committed locally is always retained.
type DevBranchPrep struct {
	Created        bool // devBranch existed NEITHER locally nor on the remote and was created from the default branch
	RemoteConflict bool // folding the pushed dev state into the kept local state conflicted; local retained unchanged
	Recovered      int  // committed-but-unpublished commits an interrupted prior run left in this workspace, retained here
}

// EnsureDevBranch puts the workspace on the persistent dev integration branch, so a run builds on the
// ACCUMULATED work of previous runs instead of a fresh default-branch checkout.
//
// The local dev branch is NEVER reset onto a remote (req 1). When it already exists in this workspace —
// possibly carrying commits an interrupted run made but never pushed — its committed history is kept and
// the pushed dev state is FOLDED IN (a merge, the same direction the default branch is folded in), never
// checked out over it. Only when no local dev branch exists is one created: from the pushed dev state if
// there is one (grow), or from the default branch the very first time (Created=true). A fold conflict is
// non-fatal (req 2): the local state is left exactly as it was, RemoteConflict is reported, and the run
// decides whether to proceed on it. Getting onto the branch drops only an aborted run's uncommitted edits
// and untracked leftovers (req 4) — committed work is untouched. Contrast ResetDevToDefault, the explicit,
// deliberate way back.
func (e Executor) EnsureDevBranch(ctx context.Context, wt, token, defaultBranch, devBranch string) (prep DevBranchPrep, err error) {
	for _, b := range []string{defaultBranch, devBranch} {
		if !branchRe.MatchString(b) || strings.HasPrefix(b, "-") {
			return prep, fmt.Errorf("invalid branch name %q", b)
		}
	}
	if err := e.Fetch(ctx, wt, token); err != nil {
		return prep, fmt.Errorf("fetch: %w", err)
	}
	remoteExists := e.remoteRefExists(ctx, wt, devBranch)

	// No local dev branch in THIS workspace yet → nothing committed here to protect. Create it: from the
	// pushed dev state when one exists (grow onto it), else from the default branch (first run → Created).
	if !e.localRefExists(ctx, wt, devBranch) {
		start := "origin/" + devBranch
		if !remoteExists {
			start, prep.Created = "origin/"+defaultBranch, true
		}
		if _, err := runGit(e.gitIn(ctx, wt, "", "checkout", "-f", "-B", devBranch, start)); err != nil {
			return prep, fmt.Errorf("checkout %s: %w", devBranch, err)
		}
		return prep, nil
	}

	// The local dev branch already exists — an interrupted prior run may have left UNPUBLISHED commits on
	// it. Get pristine ONTO it without re-pointing it at any remote: checkout keeps its committed history,
	// reset --hard drops uncommitted tracked edits, clean removes untracked/ignored leftovers (req 4).
	if _, err := runGit(e.gitIn(ctx, wt, "", "checkout", "-f", devBranch)); err != nil {
		return prep, fmt.Errorf("checkout %s: %w", devBranch, err)
	}
	if _, err := runGit(e.gitIn(ctx, wt, "", "reset", "--hard", "HEAD")); err != nil {
		return prep, fmt.Errorf("reset %s: %w", devBranch, err)
	}
	if _, err := runGit(e.gitIn(ctx, wt, "", "clean", "-fdx")); err != nil {
		return prep, fmt.Errorf("clean: %w", err)
	}
	// Recovered work: commits reachable from HEAD but from NEITHER the pushed dev state NOR the default
	// branch — exactly what a reset onto the remote would have destroyed. They are already in HEAD (kept);
	// the caller reports the count and the dev push publishes them.
	prep.Recovered = e.unpublishedCount(ctx, wt, defaultBranch, devBranch, remoteExists)
	// Fold the pushed dev state IN — never reset onto it (req 1/2). "Already up to date" (local is ahead of
	// or equal to the remote, the interrupted-run case) is a clean no-op that keeps the local commits. A
	// conflict aborts the merge, leaves the local state untouched, and is reported as non-fatal (req 2).
	if remoteExists {
		if ferr := e.FoldInBranch(ctx, wt, devBranch); ferr != nil {
			if errors.Is(ferr, ErrMergeConflict) {
				prep.RemoteConflict = true
			} else {
				return prep, fmt.Errorf("fold %s: %w", devBranch, ferr)
			}
		}
	}
	return prep, nil
}

// CleanWorktree frees the working tree of unversioned remnants so an aborted run leaves no half-changes,
// WITHOUT touching history: it hard-resets to HEAD (dropping uncommitted edits to tracked files) and
// removes untracked and ignored files. The current branch pointer is untouched — this is hygiene, not a
// reset of the accumulated state. Contrast ResetToRemote, which moves the branch onto a remote tip.
func (e Executor) CleanWorktree(ctx context.Context, wt string) error {
	if _, err := runGit(e.gitIn(ctx, wt, "", "reset", "--hard", "HEAD")); err != nil {
		return fmt.Errorf("reset: %w", err)
	}
	if _, err := runGit(e.gitIn(ctx, wt, "", "clean", "-fdx")); err != nil {
		return fmt.Errorf("clean: %w", err)
	}
	return nil
}

// FoldInBranch merges origin/<branch> INTO the current branch — the "fold it in, do not reset onto it"
// rule. It serves both folds a run performs: the default branch into the dev state, and (when preparing a
// reused workspace) the pushed dev state into the kept local dev branch. "Already up to date" (origin/
// <branch> is an ancestor of the current state — the common case, including a local dev branch ahead of
// its unpushed remote) is a clean success with no new commit and no pollution of any delivery diff. A
// CONFLICT aborts the merge, leaves the branch exactly as before, and returns ErrMergeConflict (non-fatal
// — the caller notes it and proceeds). A merge that succeeds is authored as the neutral mercury-run identity.
func (e Executor) FoldInBranch(ctx context.Context, wt, branch string) error {
	if !branchRe.MatchString(branch) || strings.HasPrefix(branch, "-") {
		return fmt.Errorf("invalid branch name %q", branch)
	}
	if _, err := runGit(e.gitIn(ctx, wt, "",
		"-c", "user.name=mercury-run", "-c", "user.email=mercury-run@local",
		"merge", "--no-edit", "origin/"+branch)); err != nil {
		_, _ = runGit(e.gitIn(ctx, wt, "", "merge", "--abort"))
		return ErrMergeConflict
	}
	return nil
}

// ResetDevToDefault is the EXPLICIT way back: it deliberately discards the accumulated dev state and
// moves the dev branch back onto the default branch (fetch → force-checkout devBranch at
// origin/defaultBranch → clean). Destructive by design and ONLY ever invoked on an explicit request,
// never as a side effect of a run. The caller force-pushes devBranch afterwards to publish the reset.
func (e Executor) ResetDevToDefault(ctx context.Context, wt, token, defaultBranch, devBranch string) error {
	for _, b := range []string{defaultBranch, devBranch} {
		if !branchRe.MatchString(b) || strings.HasPrefix(b, "-") {
			return fmt.Errorf("invalid branch name %q", b)
		}
	}
	if err := e.Fetch(ctx, wt, token); err != nil {
		return fmt.Errorf("fetch: %w", err)
	}
	if _, err := runGit(e.gitIn(ctx, wt, "", "checkout", "-f", "-B", devBranch, "origin/"+defaultBranch)); err != nil {
		return fmt.Errorf("reset %s to %s: %w", devBranch, defaultBranch, err)
	}
	if _, err := runGit(e.gitIn(ctx, wt, "", "clean", "-fdx")); err != nil {
		return fmt.Errorf("clean: %w", err)
	}
	return nil
}

// commitRe restricts revert/branch-point commits to a hex object id — the shape RevParse returns for a
// stored delivery. A hostile value cannot reach git as an option or a ref expression.
var commitRe = regexp.MustCompile(`^[0-9a-fA-F]{7,40}$`)

// BranchAt creates (or force-moves) a local branch pointing at `at`, WITHOUT checking it out — used to
// snapshot the dev tip as an immutable per-delivery branch for its stacked PR while the workspace stays
// on the dev branch.
func (e Executor) BranchAt(ctx context.Context, wt, name, at string) error {
	if !branchRe.MatchString(name) || strings.Contains(name, "..") || strings.HasPrefix(name, "-") {
		return fmt.Errorf("invalid branch name %q", name)
	}
	if at != "HEAD" && !commitRe.MatchString(at) {
		return fmt.Errorf("invalid branch point %q", at)
	}
	_, err := runGit(e.gitIn(ctx, wt, "", "branch", "-f", name, at))
	return err
}

// PushRefs pushes the named branches to origin in one invocation. --atomic makes a multi-ref push
// all-or-nothing, so the dev branch never advances without its delivery snapshot branch (or vice versa).
// force uses --force (for the explicit dev reset only); normal growth pushes fast-forward. The token
// stays in ENV, never on argv.
func (e Executor) PushRefs(ctx context.Context, wt, token string, force bool, branches ...string) (string, error) {
	if len(branches) == 0 {
		return "", errors.New("no branches to push")
	}
	args := []string{"push", "--atomic"}
	if force {
		args = append(args, "--force")
	}
	args = append(args, "origin")
	for _, b := range branches {
		if !branchRe.MatchString(b) || strings.HasPrefix(b, "-") {
			return "", fmt.Errorf("invalid branch name %q", b)
		}
		args = append(args, b)
	}
	out, err := runGit(e.gitIn(ctx, wt, token, args...))
	if lerr := assertNoLeak(wt, token); lerr != nil {
		return out, lerr
	}
	return out, err
}

// RevertRange counter-books a delivery: it applies the reverse of every commit in from..to as ONE new
// reversing commit on the current branch (the "Gegenbuchung"). History is NEVER rewritten — the original
// commits remain and the dev branch only grows (req 10). The range MUST be linear (the delivery range is
// captured AFTER the default-branch fold, so it holds only the agent's own commits, no merges).
//
// It returns three outcomes so the caller reacts precisely:
//   - conflicted=true: the reverse does not apply cleanly — later work built on this delivery and touched
//     the same lines (or the range contains a merge). No guess is made (req 12): the branch is restored
//     untouched and the caller raises a ToDo instead of committing a mangled revert.
//   - changed=false (conflicted=false, err=nil): the reverse applied but produced NOTHING to book — the
//     delivery's effect is already absent (already counter-booked, or a later change removed it). No
//     commit is made; the caller treats it as an idempotent no-op rather than an error.
//   - changed=true: a single counter-booking commit was made.
func (e Executor) RevertRange(ctx context.Context, wt, from, to, ghLogin string, ghID int64, message string) (conflicted, changed bool, err error) {
	if !commitRe.MatchString(from) || !commitRe.MatchString(to) {
		return false, false, fmt.Errorf("invalid commit range %q..%q", from, to)
	}
	if from == to {
		return false, false, errors.New("empty delivery range (from == to)")
	}
	if strings.TrimSpace(message) == "" {
		return false, false, errors.New("empty counter-booking message")
	}
	restore := func() {
		_, _ = runGit(e.gitIn(ctx, wt, "", "revert", "--abort")) // clears any sequencer state
		_, _ = runGit(e.gitIn(ctx, wt, "", "reset", "--hard", "HEAD"))
		_, _ = runGit(e.gitIn(ctx, wt, "", "clean", "-fd"))
	}
	// --no-commit stages the reverse of the whole range so it lands as a single counter-booking commit.
	if _, rerr := runGit(e.gitIn(ctx, wt, "", "revert", "--no-commit", from+".."+to)); rerr != nil {
		restore() // conflict (or a merge in range) → make no guess, leave the branch untouched
		return true, false, nil
	}
	// A revert that stages nothing means the effect is already gone (idempotent retry, or later work
	// removed it) — NOT an error and NOT a commit. Clear the sequencer state and report "no change".
	if _, diffErr := runGit(e.gitIn(ctx, wt, "", "diff", "--cached", "--quiet")); diffErr == nil {
		restore()
		return false, false, nil
	}
	name, email := ghLogin, ""
	if name == "" {
		name, email = "mercury-run", "mercury-run@local" // neutral fallback so the commit never fails on a missing identity
	} else {
		email = fmt.Sprintf("%d+%s@users.noreply.github.com", ghID, ghLogin)
	}
	pre := []string{"-c", "user.name=" + name, "-c", "user.email=" + email}
	if _, cerr := runGit(e.gitIn(ctx, wt, "", append(pre, "commit", "-m", message)...)); cerr != nil {
		restore()
		return false, false, fmt.Errorf("counter-booking commit: %w", cerr)
	}
	return false, true, nil
}

// CommitsAhead counts commits on HEAD that base does not have — i.e. how much there is to push.
// The autonomous runner needs this because an agent commits its own work: after such a run the
// working tree is CLEAN, so a working-tree-only check concludes "nothing happened" and silently
// throws the implementation away. What is ahead of the base branch is the honest measure.
func (e Executor) CommitsAhead(ctx context.Context, wt, base string) (int, error) {
	if base == "" || strings.HasPrefix(base, "-") {
		return 0, fmt.Errorf("invalid base %q", base)
	}
	out, err := runGit(e.gitIn(ctx, wt, "", "rev-list", "--count", base+"..HEAD"))
	if err != nil {
		return 0, err
	}
	n, err := strconv.Atoi(strings.TrimSpace(out))
	if err != nil {
		return 0, fmt.Errorf("unparsable rev-list count %q: %w", out, err)
	}
	return n, nil
}

// CreateBranch creates and checks out a new branch (optionally from a start point).
func (e Executor) CreateBranch(ctx context.Context, wt, name, from string) error {
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
	_, err := runGit(e.gitIn(ctx, wt, "", args...))
	return err
}

// Checkout switches the working tree to an existing branch.
func (e Executor) Checkout(ctx context.Context, wt, name string) error {
	if !branchRe.MatchString(name) || strings.HasPrefix(name, "-") {
		return fmt.Errorf("invalid branch name %q", name)
	}
	_, err := runGit(e.gitIn(ctx, wt, "", "switch", "--", name))
	return err
}

// CurrentBranch returns the checked-out branch (empty on error / "HEAD" when detached).
func (e Executor) CurrentBranch(ctx context.Context, wt string) string {
	s, err := runGit(e.gitIn(ctx, wt, "", "rev-parse", "--abbrev-ref", "HEAD"))
	if err != nil {
		return ""
	}
	return s
}

// ArtifactBuild builds the repo's deployable artifact IN wt, AS THE WORKSPACE OWNER, via the pinned
// per-user devlab-exec wrapper (artifact-build verb). It must not run as root (Finding C — a build hook
// would execute as root) nor as the devlabd service user (which cannot write the runs-user-owned
// workspace). Produces <wt>/.mercury-artifact (the Go daemon binary + the built web SPA in web/). Returns
// the combined build output for error context; the artifact path is the fixed <wt>/.mercury-artifact.
func (e Executor) ArtifactBuild(ctx context.Context, wt string) (string, error) {
	if !e.PerUser {
		return "", fmt.Errorf("ArtifactBuild requires per-user execution")
	}
	cmd := exec.CommandContext(ctx, "sudo", "-n", "-u", e.User, execWrapper, "artifact-build", wt)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.CombinedOutput()
	return strings.TrimRight(string(out), "\n"), err
}

// RevParse resolves ref to a full commit SHA. Used by the autonomous runner to capture the tip AFTER it
// bases a run on main + pending PRs, so it can later tell whether the AGENT contributed anything new.
func (e Executor) RevParse(ctx context.Context, wt, ref string) (string, error) {
	if ref == "" || strings.HasPrefix(ref, "-") {
		return "", fmt.Errorf("invalid ref %q", ref)
	}
	s, err := runGit(e.gitIn(ctx, wt, "", "rev-parse", ref))
	return strings.TrimSpace(s), err
}

// mergeRefRe restricts MergeRef to a well-formed origin/ branch ref — the shape the autonomous runner
// folds into a run's base (the still-open pending Mercury PRs, whose heads now follow the human
// <kind>/<description> convention rather than a fixed prefix). The caller has already vetted these as
// Mercury's OWN PRs (hidden body marker); this regex is the structural safety net: it demands the origin/
// prefix and an alphanumeric first segment character, so a hostile or malformed branch name cannot reach
// git (path traversal is additionally blocked by the ".." check in MergeRef).
var mergeRefRe = regexp.MustCompile(`^origin/[A-Za-z0-9][A-Za-z0-9._/-]*$`)

// MergeRef merges ref into the current branch (default merge; a merge commit is authored as the runner
// identity when the histories diverge). On conflict it ABORTS the merge and returns an error, leaving the
// branch exactly as before — the caller then proceeds without that pending PR in the base. Restricted to
// well-formed origin/* refs (see mergeRefRe). This is how the runner bases a run on "main + pending PRs"
// so it never redoes not-yet-merged work while still implementing what a (possibly different-axiom) run
// still needs.
func (e Executor) MergeRef(ctx context.Context, wt, ref string) error {
	if strings.Contains(ref, "..") || !mergeRefRe.MatchString(ref) {
		return fmt.Errorf("invalid merge ref %q", ref)
	}
	if _, err := runGit(e.gitIn(ctx, wt, "",
		"-c", "user.name=mercury-run", "-c", "user.email=mercury-run@local",
		"merge", "--no-edit", ref)); err != nil {
		_, _ = runGit(e.gitIn(ctx, wt, "", "merge", "--abort"))
		return fmt.Errorf("merge %s: %w", ref, err)
	}
	return nil
}

// Changes / Branches are read-only re-exports (run directly with safe.directory in package git).
func Changes(wt string) []model.Change  { return git.Changes(wt) }
func Branches(wt string) []model.Branch { return git.Branches(wt) }

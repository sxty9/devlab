// Package axiomrepo is the store behind Mercury: the Holistic constitution — axioms, implementation
// rules and Laufregeln — held in a dedicated Git repository rather than in a service's opaque object
// store.
//
// Why a repository. The constitution is the one artefact every service and every autonomous run is
// measured against, so its history matters as much as its content: who changed which sentence, when,
// and against what. Git gives that natively. It also resolves the branch-protection question — the code
// repositories are protected and change only through pull requests, while the constitution is data and
// must stay editable in one step, so it lives in a repository that is deliberately NOT protected.
//
// Shape on disk mirrors the record paths exactly: `axiome/architecture/minimalism/keine-redundanz.md`
// is that file. The path IS the category, the file IS the record — nothing is duplicated into an index.
//
// Concurrency and honesty: every write takes the lock, refreshes from the remote, applies the change,
// commits and pushes; a push rejected because someone else wrote first is retried on the refreshed
// state. Reads serve the local clone, refreshed at most once per interval — the store never pretends a
// write happened that did not reach the remote.
package axiomrepo

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// ErrExists is returned by Put when the record is already there and overwrite was not asked for.
var ErrExists = errors.New("record exists")

// ErrNoStore is returned when the constitution store was never configured — a dev/preview process
// without a link store, or a test that exercises a handler which only touches other data. Returning it
// keeps every caller on the ordinary error path instead of dereferencing a nil store.
var ErrNoStore = errors.New("constitution store not configured")

// ErrNotFound is returned when a path holds no record.
var ErrNotFound = errors.New("record not found")

// ErrNoToken means no GitHub credential could be obtained to reach the repository (no linked account,
// or an empty token). It is kept distinct from ErrUnavailable so the surface can say "link an account"
// rather than "the network is down" — and, crucially, so neither is ever mistaken for "no axioms".
var ErrNoToken = errors.New("no GitHub token for the axiom repository")

// ErrUnavailable means the repository itself could not be reached — the clone or a required refresh
// failed (no network, remote gone, bad URL). A read that hits this returns the error, never an empty
// constitution, so "unreachable" and "genuinely empty" stay distinguishable.
var ErrUnavailable = errors.New("axiom repository unavailable")

// TokenFunc yields the GitHub token to push with. It is a function, not a value, so a token that is
// rotated (or linked only later) is picked up without restarting the service.
type TokenFunc func() (string, error)

// Store is the working copy of the constitution repository.
type Store struct {
	dir      string    // local clone
	fullName string    // owner/repo, or a filesystem/URL remote used verbatim (see localRemote)
	token    TokenFunc // push credentials
	branch   string

	// local is true when fullName is a filesystem path or explicit URL rather than an owner/repo pair:
	// the remote is then used verbatim and no GitHub credential is injected. Real deployments pass
	// owner/repo (local=false); tests pass a bare-repo path (local=true), which is what lets an
	// out-of-package test drive the store against a genuine git remote with no auth plumbing.
	local bool

	mu        sync.Mutex
	lastFetch time.Time
	cloned    bool

	// onBeforePush is a test seam: when non-nil it is invoked inside the write critical section, after
	// our commit and just before the push. Production leaves it nil; a test sets it to land a competing
	// commit on the remote first, deterministically exercising the reject-refresh-retry path.
	onBeforePush func()
}

// fetchTTL bounds how stale a read may be. Writes always refresh first, so this only matters when a
// SECOND instance (e.g. prod beside dev) edits the constitution concurrently.
const fetchTTL = 30 * time.Second

// maxWriteAttempts caps the reject-refresh-retry loop. Each losing attempt re-reads the remote's newest
// state before re-applying, so a write is never silently lost — it either lands on the fresh state or,
// only after this many genuine collisions, fails loudly.
const maxWriteAttempts = 5

// New builds the store. Nothing touches the network until the first use.
func New(dir, fullName string, token TokenFunc) *Store {
	return &Store{dir: dir, fullName: fullName, token: token, branch: "main", local: localRemote(fullName)}
}

// localRemote reports whether fullName is a filesystem path or explicit URL (used verbatim, no auth)
// rather than a GitHub owner/repo pair (cloned over https with the injected token).
func localRemote(fullName string) bool {
	return strings.HasPrefix(fullName, "/") || strings.HasPrefix(fullName, ".") ||
		strings.HasPrefix(fullName, "file://")
}

// Dir is the local clone's path (exposed for diagnostics).
func (s *Store) Dir() string { return s.dir }

// FullName is the owner/repo this store is backed by.
func (s *Store) FullName() string { return s.fullName }

// tokenEnvVar carries the credential to git IN THE PROCESS ENVIRONMENT. /proc/<pid>/cmdline is
// world-readable — a token on argv is visible to every local account through a plain `ps auxww` for
// as long as the command runs — while /proc/<pid>/environ is readable by its owner alone. The
// variable and the helper below are the SAME ones the per-user git primitives use
// (workspace.credHelperArgs), so a token reaches git in exactly one way across the service.
const tokenEnvVar = "DEVLAB_GH_TOKEN"

// credentialArgs are the TOKENLESS arguments that point git at our inline credential helper; the
// helper reads the secret out of the environment, so nothing secret is ever an argument. The empty
// first assignment resets any inherited system/global helper, so no foreign helper can answer.
func credentialArgs() []string {
	return []string{
		"-c", "credential.helper=",
		"-c", `credential.helper=!f() { printf 'username=x-access-token\npassword=%s\n' "$` + tokenEnvVar + `"; }; f`,
	}
}

// noSymlinkArgs disarms committed symlinks: with core.symlinks=false git checks a symlink out as a
// small PLAIN FILE holding the link text, so no record path can ever traverse one. This is the same
// characteristic the per-user git primitives set on their clones (workspace.clone) — a clone this
// service makes never materializes what a repository committed, wherever the clone is made.
//
// They are passed BOTH on the clone (where -c persists into the new repository's config) and on
// every later invocation: a clone created before this existed carries neither the config nor the
// disarmed state, and its refresh must not re-materialize a link the remote still holds.
func noSymlinkArgs() []string { return []string{"-c", "core.symlinks=false"} }

// gitEnv is the environment of one git invocation: the terminal prompt is off (a missing credential
// must fail, never block) and the token — when there is one — travels here, never on argv.
func gitEnv(token string) []string {
	env := append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	if token != "" {
		env = append(env, tokenEnvVar+"="+token)
	}
	return env
}

// git runs one git command in the clone, with credentials injected per invocation rather than written
// into the remote URL — a token must never end up in .git/config, where every later log would leak it.
func (s *Store) git(ctx context.Context, args ...string) (string, error) {
	var token string
	full := []string{"-C", s.dir, "-c", "user.name=Mercury", "-c", "user.email=mercury@holistic.local"}
	full = append(full, noSymlinkArgs()...)
	if !s.local {
		t, err := s.credential()
		if err != nil {
			return "", err
		}
		token = t
		full = append(full, credentialArgs()...)
	}
	cmd := exec.CommandContext(ctx, "git", append(full, args...)...)
	cmd.Env = gitEnv(token)
	var out, errb strings.Builder
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(errb.String())
		if msg == "" {
			msg = err.Error()
		}
		return out.String(), errors.New(redact(msg, token))
	}
	return out.String(), nil
}

// ensure makes sure the clone exists, and refreshes it when the caller needs current data (a write, or
// a read whose last fetch aged out). The caller holds the lock.
func (s *Store) ensure(ctx context.Context, force bool) error {
	if !s.cloned {
		if _, err := os.Stat(filepath.Join(s.dir, ".git")); err != nil {
			if err := s.clone(ctx); err != nil {
				return err
			}
		}
		s.cloned = true
	}
	if !force && time.Since(s.lastFetch) < fetchTTL {
		return nil
	}
	if _, err := s.git(ctx, "fetch", "--prune", "origin"); err != nil {
		if !force {
			return nil // a read may serve the local state through a network blip
		}
		if errors.Is(err, ErrNoToken) {
			return err // a credential problem must not be masked as a generic outage
		}
		return fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	if _, err := s.git(ctx, "reset", "--hard", "origin/"+s.branch); err != nil {
		return err
	}
	s.lastFetch = time.Now()
	return nil
}

func (s *Store) clone(ctx context.Context) error {
	if err := os.MkdirAll(filepath.Dir(s.dir), 0o700); err != nil {
		return err
	}
	var token string
	args := noSymlinkArgs()
	url := s.fullName // a local path / URL remote is used verbatim
	if !s.local {
		t, err := s.credential()
		if err != nil {
			return err
		}
		token = t
		args = append(args, credentialArgs()...)
		url = "https://github.com/" + s.fullName + ".git"
	}
	args = append(args, "clone", "--quiet", url, s.dir)
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Env = gitEnv(token)
	var errb strings.Builder
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%w: %s", ErrUnavailable, redact(strings.TrimSpace(errb.String()), token))
	}
	return nil
}

// credential fetches the push token, mapping a missing or empty token onto ErrNoToken so the surface
// can tell "no account linked" apart from "the remote is unreachable".
func (s *Store) credential() (string, error) {
	t, err := s.token()
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrNoToken, err)
	}
	if strings.TrimSpace(t) == "" {
		return "", ErrNoToken
	}
	return t, nil
}

// List returns every record path under prefix (empty = all), sorted — the same flat, sorted shape the
// tree projection is built from.
func (s *Store) List(ctx context.Context, prefix string) ([]string, error) {
	if s == nil {
		return nil, ErrNoStore
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensure(ctx, false); err != nil {
		return nil, err
	}
	var paths []string
	root := s.dir
	err := filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			if info.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		rel, rerr := filepath.Rel(root, p)
		if rerr != nil || !strings.HasSuffix(rel, ".md") || rel == "README.md" {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if prefix == "" || rel == prefix || strings.HasPrefix(rel, strings.TrimSuffix(prefix, "/")+"/") {
			paths = append(paths, rel)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(paths)
	return paths, nil
}

// Get returns one record's bytes. found is false (with a nil error) when the path holds none.
func (s *Store) Get(ctx context.Context, path string) ([]byte, bool, error) {
	if s == nil {
		return nil, false, ErrNoStore
	}
	if err := safePath(path); err != nil {
		return nil, false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensure(ctx, false); err != nil {
		return nil, false, err
	}
	abs, err := s.resolveInClone(path)
	if err != nil {
		return nil, false, err
	}
	b, err := os.ReadFile(abs)
	if os.IsNotExist(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return b, true, nil
}

// Put writes a record. overwrite=false refuses to replace an existing one (ErrExists), which is what
// makes "create" distinguishable from "edit" at the store level rather than by a racy pre-check.
func (s *Store) Put(ctx context.Context, path, content, message, actor string, overwrite bool) error {
	if s == nil {
		return ErrNoStore
	}
	if err := safePath(path); err != nil {
		return err
	}
	return s.write(ctx, message, actor, func() error {
		abs, err := s.resolveInClone(path)
		if err != nil {
			return err
		}
		if !overwrite {
			if _, err := os.Stat(abs); err == nil {
				return ErrExists
			}
		}
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			return err
		}
		return os.WriteFile(abs, []byte(content), 0o644)
	})
}

// Delete removes a record, or a whole category when path names a directory.
func (s *Store) Delete(ctx context.Context, path, message, actor string) error {
	if s == nil {
		return ErrNoStore
	}
	if err := safePath(path); err != nil {
		return err
	}
	return s.write(ctx, message, actor, func() error {
		abs, err := s.resolveInClone(path)
		if err != nil {
			return err
		}
		if _, err := os.Stat(abs); os.IsNotExist(err) {
			return ErrNotFound
		}
		return os.RemoveAll(abs)
	})
}

// Move relocates a record or a category. Its content travels with the path, so an axiom keeps its id
// (and every run referencing it) across a re-filing.
func (s *Store) Move(ctx context.Context, from, to, message, actor string) error {
	if s == nil {
		return ErrNoStore
	}
	if err := safePath(from); err != nil {
		return err
	}
	if err := safePath(to); err != nil {
		return err
	}
	return s.write(ctx, message, actor, func() error {
		src, err := s.resolveInClone(from)
		if err != nil {
			return err
		}
		dst, err := s.resolveInClone(to)
		if err != nil {
			return err
		}
		if _, err := os.Stat(src); os.IsNotExist(err) {
			return ErrNotFound
		}
		if _, err := os.Stat(dst); err == nil {
			return ErrExists
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		return os.Rename(src, dst)
	})
}

// write is the single write path: refresh → apply → commit → push, retried against a remote that moved
// underneath us. Each retry re-reads the newest remote state and re-applies on top, so a write is never
// silently lost. A change that produces no diff commits nothing (idempotent, like the rollout).
func (s *Store) write(ctx context.Context, message, actor string, apply func() error) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	for attempt := 0; attempt < maxWriteAttempts; attempt++ {
		if err := s.ensure(ctx, true); err != nil {
			return err
		}
		if err := apply(); err != nil {
			return err
		}
		if _, err := s.git(ctx, "add", "-A"); err != nil {
			return err
		}
		if out, err := s.git(ctx, "status", "--porcelain"); err == nil && strings.TrimSpace(out) == "" {
			return nil // nothing actually changed
		}
		full := message
		if actor != "" {
			full += "\n\nChanged by: " + actor
		}
		if _, err := s.git(ctx, "commit", "-m", full); err != nil {
			return err
		}
		if s.onBeforePush != nil {
			s.onBeforePush()
		}
		if _, err := s.git(ctx, "push", "origin", "HEAD:"+s.branch); err == nil {
			s.lastFetch = time.Time{} // force the next read to see our own commit's remote state
			return nil
		} else if attempt == maxWriteAttempts-1 {
			return err
		}
		// Someone else pushed first: drop our local commit so the next ensure can fast-forward to their
		// state, then re-apply our change on top. Nothing of ours is lost — only re-based.
		if _, rerr := s.git(ctx, "reset", "--hard", "HEAD~1"); rerr != nil {
			return rerr
		}
	}
	return fmt.Errorf("push rejected after %d attempts (constitution repository under heavy contention)", maxWriteAttempts)
}

// safePath rejects anything that could escape the repository or hit its git directory. It is a pure
// STRING check and therefore only the first half of the confinement: it cannot see a symlink, so
// every access resolves the path as well (resolveInClone).
func safePath(p string) error {
	if p == "" || strings.HasPrefix(p, "/") || strings.Contains(p, "..") || strings.HasPrefix(p, ".git") {
		return fmt.Errorf("invalid path %q", p)
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == "" || seg == "." || seg == ".git" {
			return fmt.Errorf("invalid path %q", p)
		}
	}
	return nil
}

// resolveInClone turns a record path into the absolute path to operate on, and confines it under the
// clone by RESOLVING it — the string check above cannot: the repository's own content may carry a
// symlink (`axiome/x -> /etc`), and reading or writing through it leaves the constitution entirely.
// Like the root wrappers, the deepest EXISTING ancestor is resolved (so a record that does not exist
// yet is confined through its parent) and required to stay inside the clone. Callers hold the lock
// and call this AFTER the refresh, because a refresh can re-materialise such a link.
//
// "Inside the clone" is not the whole boundary: the clone's own `.git` lies inside it, and that
// directory is not data — it holds the refs, the hooks and the config. Resolving into it is refused
// for exactly the reason safePath refuses a `.git` path segment, so both halves of the confinement
// draw the SAME line: a link `axiome/x -> .git` would otherwise serve the config as a record and let
// a write install a hook. Newer clones cannot carry such a link at all (noSymlinkArgs), but a clone
// made before that holds its links materialized and its refresh does not undo them.
func (s *Store) resolveInClone(rel string) (string, error) {
	if err := safePath(rel); err != nil {
		return "", err
	}
	root, err := filepath.EvalSymlinks(s.dir)
	if err != nil {
		return "", fmt.Errorf("constitution clone unusable: %w", err)
	}
	gitDir := filepath.Join(root, ".git")
	abs := filepath.Join(s.dir, filepath.FromSlash(rel))
	for probe := abs; ; {
		real, err := filepath.EvalSymlinks(probe)
		if err == nil {
			if real != root && !strings.HasPrefix(real, root+string(os.PathSeparator)) {
				return "", fmt.Errorf("invalid path %q: it resolves to %q, outside the constitution repository", rel, real)
			}
			if real == gitDir || strings.HasPrefix(real, gitDir+string(os.PathSeparator)) {
				return "", fmt.Errorf("invalid path %q: it resolves to %q, inside the clone's git directory", rel, real)
			}
			return abs, nil
		}
		if !os.IsNotExist(err) {
			return "", err
		}
		// A DANGLING symlink fails to resolve too. Walking past it would let the access land at the
		// link's target the moment that target appears, so it is refused rather than followed.
		if fi, lerr := os.Lstat(probe); lerr == nil && fi.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("invalid path %q: %q is a symlink that does not resolve inside the constitution repository", rel, probe)
		}
		parent := filepath.Dir(probe)
		if parent == probe || len(parent) < len(s.dir) {
			return "", fmt.Errorf("invalid path %q: no existing ancestor inside the constitution repository", rel)
		}
		probe = parent
	}
}

// redact removes the token from any message that is about to be logged or returned. The base64 form
// is redacted too: nothing here BUILDS it (the credential travels in the environment), but git
// assembles a Basic header from the helper's answer itself and a verbose failure can echo it.
func redact(msg, token string) string {
	if token == "" {
		return msg
	}
	return strings.ReplaceAll(strings.ReplaceAll(msg, token, "***"), base64Std("x-access-token:"+token), "***")
}

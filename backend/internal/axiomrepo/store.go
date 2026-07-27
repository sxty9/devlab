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

// ErrNotFound is returned when a path holds no record.
var ErrNotFound = errors.New("record not found")

// TokenFunc yields the GitHub token to push with. It is a function, not a value, so a token that is
// rotated (or linked only later) is picked up without restarting the service.
type TokenFunc func() (string, error)

// Store is the working copy of the constitution repository.
type Store struct {
	dir      string    // local clone
	fullName string    // owner/repo
	token    TokenFunc // push credentials
	branch   string

	mu        sync.Mutex
	lastFetch time.Time
	cloned    bool

	// plain skips the Authorization header, for a remote that needs none (a local path in tests).
	plain bool
}

// fetchTTL bounds how stale a read may be. Writes always refresh first, so this only matters when a
// SECOND instance (e.g. prod beside dev) edits the constitution concurrently.
const fetchTTL = 30 * time.Second

// New builds the store. Nothing touches the network until the first use.
func New(dir, fullName string, token TokenFunc) *Store {
	return &Store{dir: dir, fullName: fullName, token: token, branch: "main"}
}

// Dir is the local clone's path (exposed for diagnostics).
func (s *Store) Dir() string { return s.dir }

// FullName is the owner/repo this store is backed by.
func (s *Store) FullName() string { return s.fullName }

// git runs one git command in the clone, with credentials injected per invocation rather than written
// into the remote URL — a token must never end up in .git/config, where every later log would leak it.
func (s *Store) git(ctx context.Context, args ...string) (string, error) {
	var token string
	full := []string{"-C", s.dir, "-c", "user.name=Mercury", "-c", "user.email=mercury@holistic.local"}
	if !s.plain {
		t, err := s.token()
		if err != nil {
			return "", fmt.Errorf("kein GitHub-Token für das Axiom-Repository: %w", err)
		}
		token = t
		full = append(full, "-c", "credential.helper=",
			"-c", "http.https://github.com/.extraheader=Authorization: Basic "+basicAuth(token))
	}
	cmd := exec.CommandContext(ctx, "git", append(full, args...)...)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
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
		if force {
			return err
		}
		return nil // a read may serve the local state through a network blip
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
	args := []string{}
	url := s.fullName // a local path remote (tests) is used verbatim
	if !s.plain {
		t, err := s.token()
		if err != nil {
			return fmt.Errorf("kein GitHub-Token für das Axiom-Repository: %w", err)
		}
		token = t
		args = append(args, "-c", "credential.helper=",
			"-c", "http.https://github.com/.extraheader=Authorization: Basic "+basicAuth(token))
		url = "https://github.com/" + s.fullName + ".git"
	}
	args = append(args, "clone", "--quiet", url, s.dir)
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	var errb strings.Builder
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		return errors.New(redact(strings.TrimSpace(errb.String()), token))
	}
	return nil
}

// List returns every record path under prefix (empty = all), sorted — the same flat, sorted shape the
// tree projection is built from.
func (s *Store) List(ctx context.Context, prefix string) ([]string, error) {
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
	if err := safePath(path); err != nil {
		return nil, false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensure(ctx, false); err != nil {
		return nil, false, err
	}
	b, err := os.ReadFile(filepath.Join(s.dir, filepath.FromSlash(path)))
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
	if err := safePath(path); err != nil {
		return err
	}
	return s.write(ctx, message, actor, func() error {
		abs := filepath.Join(s.dir, filepath.FromSlash(path))
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
	if err := safePath(path); err != nil {
		return err
	}
	return s.write(ctx, message, actor, func() error {
		abs := filepath.Join(s.dir, filepath.FromSlash(path))
		if _, err := os.Stat(abs); os.IsNotExist(err) {
			return ErrNotFound
		}
		return os.RemoveAll(abs)
	})
}

// Move relocates a record or a category. Its content travels with the path, so an axiom keeps its id
// (and every run referencing it) across a re-filing.
func (s *Store) Move(ctx context.Context, from, to, message, actor string) error {
	if err := safePath(from); err != nil {
		return err
	}
	if err := safePath(to); err != nil {
		return err
	}
	return s.write(ctx, message, actor, func() error {
		src := filepath.Join(s.dir, filepath.FromSlash(from))
		dst := filepath.Join(s.dir, filepath.FromSlash(to))
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

// write is the single write path: refresh → apply → commit → push, retried once against a remote that
// moved underneath us. A change that produces no diff commits nothing (idempotent, like the rollout).
func (s *Store) write(ctx context.Context, message, actor string, apply func() error) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	for attempt := 0; attempt < 2; attempt++ {
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
			full += "\n\nGeändert von: " + actor
		}
		if _, err := s.git(ctx, "commit", "-m", full); err != nil {
			return err
		}
		if _, err := s.git(ctx, "push", "origin", "HEAD:"+s.branch); err == nil {
			s.lastFetch = time.Time{} // force the next read to see our own commit's remote state
			return nil
		} else if attempt == 1 {
			return err
		}
		// Someone else pushed first: drop our commit, take their state, and apply again.
		if _, rerr := s.git(ctx, "reset", "--hard", "HEAD~1"); rerr != nil {
			return rerr
		}
	}
	return errors.New("push nach zwei Versuchen abgelehnt")
}

// safePath rejects anything that could escape the repository or hit its git directory.
func safePath(p string) error {
	if p == "" || strings.HasPrefix(p, "/") || strings.Contains(p, "..") || strings.HasPrefix(p, ".git") {
		return fmt.Errorf("ungültiger Pfad %q", p)
	}
	return nil
}

func basicAuth(token string) string {
	return base64Std("x-access-token:" + token)
}

// redact removes the token from any message that is about to be logged or returned.
func redact(msg, token string) string {
	if token == "" {
		return msg
	}
	return strings.ReplaceAll(strings.ReplaceAll(msg, token, "***"), base64Std("x-access-token:"+token), "***")
}

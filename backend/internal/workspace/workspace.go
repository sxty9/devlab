// Package workspace manages per-user git working trees: /var/lib/devlab/workspaces/<user>/<repo>
// (env DEVLAB_WORKSPACES). Each user gets their own full clone per repo, cloned on first access
// with that user's GitHub token. A per-user-per-repo mutex serializes mutating ops so concurrent
// browser tabs cannot race the index.
//
// The OAuth token is NEVER persisted: the remote URL stays tokenless and credentials are injected
// out-of-band per network op (see gitops.go). After every network op .git/config is checked to
// guarantee the token did not leak in.
package workspace

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sync"

	"devlab/backend/internal/statepath"
)

var (
	userRe = regexp.MustCompile(`^[a-z_][a-z0-9_-]*$`)
	repoRe = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)
)

// Manager owns the workspace base dir and the per-user-per-repo lock table.
type Manager struct {
	base  string
	mu    sync.Mutex
	locks map[string]*sync.Mutex
}

// NewManager builds the manager below the state root (DEVLAB_WORKSPACES overrides).
func NewManager(p *statepath.Paths) *Manager {
	base := os.Getenv("DEVLAB_WORKSPACES")
	if base == "" && p != nil {
		base = p.Workspaces()
	}
	return &Manager{base: base, locks: map[string]*sync.Mutex{}}
}

// lockFor returns the mutex guarding one user's checkout of one repo.
func (m *Manager) lockFor(user, repo string) *sync.Mutex {
	key := user + "/" + repo
	m.mu.Lock()
	defer m.mu.Unlock()
	l, ok := m.locks[key]
	if !ok {
		l = &sync.Mutex{}
		m.locks[key] = l
	}
	return l
}

// Lock acquires the per-user-per-repo mutex; callers defer the returned unlock. Used by the API
// layer to serialize a whole mutate→refresh sequence.
func (m *Manager) Lock(user, repo string) (func(), error) {
	if !userRe.MatchString(user) || !repoRe.MatchString(repo) {
		return nil, fmt.Errorf("workspace: invalid user/repo")
	}
	l := m.lockFor(user, repo)
	l.Lock()
	return l.Unlock, nil
}

// Path resolves the working-tree path for a user/repo (validated; does not check existence).
func (m *Manager) Path(user, repo string) (string, error) {
	if !userRe.MatchString(user) || !repoRe.MatchString(repo) {
		return "", fmt.Errorf("workspace: invalid user/repo %q/%q", user, repo)
	}
	return filepath.Join(m.base, user, repo), nil
}

// Exists reports whether the user already has this repo cloned.
func (m *Manager) Exists(user, repo string) bool {
	wt, err := m.Path(user, repo)
	if err != nil {
		return false
	}
	return isGitDir(wt)
}

// Ensure returns the working-tree path, cloning on first access (no network if already present).
// fullName is "owner/repo"; token authorizes the clone of private repos and is never persisted.
// perUser=true provisions the user's workspace root (owned by the user) and clones AS the user via
// the wrapper; false (dev-bypass) clones directly as the service user.
func (m *Manager) Ensure(ctx context.Context, user, repo, fullName, token string, perUser bool) (string, error) {
	wt, err := m.Path(user, repo)
	if err != nil {
		return "", err
	}
	unlock, err := m.Lock(user, repo)
	if err != nil {
		return "", err
	}
	defer unlock()

	if isGitDir(wt) {
		return wt, nil
	}
	if perUser {
		if err := provisionWorkspace(user); err != nil { // root helper: create + chown the user's root
			return "", err
		}
	} else if err := os.MkdirAll(filepath.Dir(wt), 0o700); err != nil {
		return "", fmt.Errorf("workspace: mkdir: %w", err)
	}
	e := Executor{User: user, PerUser: perUser}
	if err := e.clone(ctx, wt, "https://github.com/"+fullName+".git", token); err != nil {
		if !perUser {
			_ = os.RemoveAll(wt) // leave no half-clone (git clone self-cleans in the per-user path)
		}
		return "", err
	}
	return wt, nil
}

func isGitDir(wt string) bool {
	fi, err := os.Stat(filepath.Join(wt, ".git"))
	return err == nil && (fi.IsDir() || fi.Mode().IsRegular())
}

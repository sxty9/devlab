// Package comments is DevLab's per-repo threaded discussion store for the Vision Catalog. Comments
// are discussion metadata (not repo source), shared across a repo's collaborators via the shared
// DevLab server — so they are keyed by repo (not by user) and stored server-side as plain JSON
// (atomic tmp+rename), mirroring package links minus the encryption. Authorship is stamped by the
// caller from the authenticated session, never trusted from the client.
package comments

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"sync"
	"time"

	"devlab/backend/internal/model"
)

// repoRe bounds the repo id to safe filename characters (defends the per-repo file path).
var repoRe = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// Store persists per-repo comment threads under a directory, one JSON file per repo.
type Store struct {
	dir   string
	mu    sync.Mutex
	locks map[string]*sync.Mutex
}

// NewStore builds the store from DEVLAB_COMMENTS (default /var/lib/devlab/comments), dir 0700.
func NewStore() (*Store, error) {
	dir := os.Getenv("DEVLAB_COMMENTS")
	if dir == "" {
		dir = "/var/lib/devlab/comments"
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("comments: mkdir %s: %w", dir, err)
	}
	return &Store{dir: dir, locks: map[string]*sync.Mutex{}}, nil
}

func (s *Store) lockFor(repo string) *sync.Mutex {
	s.mu.Lock()
	defer s.mu.Unlock()
	l, ok := s.locks[repo]
	if !ok {
		l = &sync.Mutex{}
		s.locks[repo] = l
	}
	return l
}

func (s *Store) path(repo string) (string, error) {
	if !repoRe.MatchString(repo) {
		return "", fmt.Errorf("comments: invalid repo %q", repo)
	}
	return filepath.Join(s.dir, repo+".json"), nil
}

type file struct {
	Comments []model.Comment `json:"comments"`
}

func (s *Store) read(repo string) (*file, error) {
	p, err := s.path(repo)
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(p)
	if err != nil {
		if os.IsNotExist(err) {
			return &file{}, nil
		}
		return nil, err
	}
	var f file
	if err := json.Unmarshal(b, &f); err != nil {
		return nil, fmt.Errorf("comments: parse %s: %w", p, err)
	}
	return &f, nil
}

func (s *Store) writeFile(repo string, f *file) error {
	p, err := s.path(repo)
	if err != nil {
		return err
	}
	b, err := json.Marshal(f)
	if err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, p); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// List returns a repo's comments (optionally only those attached to path), oldest first.
func (s *Store) List(repo, path string) ([]model.Comment, error) {
	l := s.lockFor(repo)
	l.Lock()
	defer l.Unlock()
	f, err := s.read(repo)
	if err != nil {
		return nil, err
	}
	out := make([]model.Comment, 0, len(f.Comments))
	for _, c := range f.Comments {
		if path == "" || c.Path == path {
			out = append(out, c)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt < out[j].CreatedAt })
	return out, nil
}

// Add stamps a fresh id + CreatedAt and appends the comment. A reply (ParentID set) must point at
// an existing comment on the same path. Returns the stored comment.
func (s *Store) Add(repo string, c model.Comment, now time.Time) (model.Comment, error) {
	l := s.lockFor(repo)
	l.Lock()
	defer l.Unlock()
	f, err := s.read(repo)
	if err != nil {
		return model.Comment{}, err
	}
	if c.ParentID != "" {
		ok := false
		for _, e := range f.Comments {
			if e.ID == c.ParentID && e.Path == c.Path {
				ok = true
				break
			}
		}
		if !ok {
			return model.Comment{}, fmt.Errorf("comments: parent not found")
		}
	}
	c.ID = randHex()
	c.CreatedAt = now.UTC().Format(time.RFC3339)
	f.Comments = append(f.Comments, c)
	if err := s.writeFile(repo, f); err != nil {
		return model.Comment{}, err
	}
	return c, nil
}

// Delete removes a comment and cascades to its descendant replies so no orphaned thread remains.
// authorize is called with the located target comment under the store lock; returning an error aborts
// the delete before anything is written — so WHO may delete is the caller's policy decision, made
// outside this passive pool, yet still evaluated atomically with the delete (no observable in-between
// state, no time-of-check/time-of-use gap). A nil authorize deletes unconditionally. Idempotent when
// the id is already gone (authorize is not consulted — there is nothing to delete).
func (s *Store) Delete(repo, id string, authorize func(model.Comment) error) error {
	l := s.lockFor(repo)
	l.Lock()
	defer l.Unlock()
	f, err := s.read(repo)
	if err != nil {
		return err
	}
	var target *model.Comment
	for i := range f.Comments {
		if f.Comments[i].ID == id {
			target = &f.Comments[i]
			break
		}
	}
	if target == nil {
		return nil
	}
	if authorize != nil {
		if err := authorize(*target); err != nil {
			return err
		}
	}
	remove := map[string]bool{id: true}
	for changed := true; changed; {
		changed = false
		for _, c := range f.Comments {
			if !remove[c.ID] && c.ParentID != "" && remove[c.ParentID] {
				remove[c.ID] = true
				changed = true
			}
		}
	}
	var out []model.Comment
	for _, c := range f.Comments {
		if !remove[c.ID] {
			out = append(out, c)
		}
	}
	f.Comments = out
	return s.writeFile(repo, f)
}

func randHex() string {
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		return ""
	}
	return hex.EncodeToString(b)
}

// Package chats persists the repo-scoped AI-assistant transcript per user+repo, so a conversation
// survives a reload. Plain JSON (not secret), atomic tmp+rename, nested <user>/<repo>.json under
// /var/lib/devlab/chats — mirroring the workspace layout. Mirrors package links minus encryption.
package chats

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sync"

	"devlab/backend/internal/fsatomic"
	"devlab/backend/internal/model"
)

var (
	userRe = regexp.MustCompile(`^[a-z_][a-z0-9_-]*$`)
	repoRe = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)
)

const maxMessages = 500 // cap transcript length so a runaway history can't grow unbounded

// Store persists per-user-per-repo transcripts.
type Store struct {
	dir   string
	mu    sync.Mutex
	locks map[string]*sync.Mutex
}

// NewStore builds the store from DEVLAB_CHATS (default /var/lib/devlab/chats), dir 0700.
func NewStore() (*Store, error) {
	dir := os.Getenv("DEVLAB_CHATS")
	if dir == "" {
		dir = "/var/lib/devlab/chats"
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("chats: mkdir %s: %w", dir, err)
	}
	return &Store{dir: dir, locks: map[string]*sync.Mutex{}}, nil
}

func (s *Store) lockFor(key string) *sync.Mutex {
	s.mu.Lock()
	defer s.mu.Unlock()
	l, ok := s.locks[key]
	if !ok {
		l = &sync.Mutex{}
		s.locks[key] = l
	}
	return l
}

func (s *Store) path(user, repo string) (string, error) {
	if !userRe.MatchString(user) || !repoRe.MatchString(repo) {
		return "", fmt.Errorf("chats: invalid user/repo")
	}
	return filepath.Join(s.dir, user, repo+".json"), nil
}

type file struct {
	Messages []model.AiMessage `json:"messages"`
}

// Get returns the stored transcript (empty when none).
func (s *Store) Get(user, repo string) ([]model.AiMessage, error) {
	l := s.lockFor(user + "/" + repo)
	l.Lock()
	defer l.Unlock()
	p, err := s.path(user, repo)
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(p)
	if err != nil {
		if os.IsNotExist(err) {
			return []model.AiMessage{}, nil
		}
		return nil, err
	}
	var f file
	if err := json.Unmarshal(b, &f); err != nil {
		return nil, fmt.Errorf("chats: parse %s: %w", p, err)
	}
	if f.Messages == nil {
		f.Messages = []model.AiMessage{}
	}
	return f.Messages, nil
}

// Put replaces the transcript (trimmed to the tail when over the cap).
func (s *Store) Put(user, repo string, msgs []model.AiMessage) error {
	l := s.lockFor(user + "/" + repo)
	l.Lock()
	defer l.Unlock()
	p, err := s.path(user, repo)
	if err != nil {
		return err
	}
	if len(msgs) > maxMessages {
		msgs = msgs[len(msgs)-maxMessages:]
	}
	return fsatomic.WriteJSON(p, file{Messages: msgs})
}

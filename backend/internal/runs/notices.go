package runs

import (
	"crypto/rand"
	"encoding/base32"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// A Notice records ONE outcome of the automatic axiom→run assignment: either an axiom set was assigned
// to a run (Kind == NoticeAssigned) or the assignment could not be carried out (Kind == NoticeFailed).
// The store is a pure, passive pool — it holds these records and nothing more; the assigner writes them
// and the API reads them, but the pool itself neither plans nor interprets. It exists so the user is
// informed of every automatic assignment (and every failure) in a portioned, raw-log-free feed.
type Notice struct {
	ID   string    `json:"id"`
	At   time.Time `json:"at"`
	Kind string    `json:"kind"` // NoticeAssigned | NoticeFailed

	// assigned only
	RunID   string `json:"runId,omitempty"`
	RunName string `json:"runName,omitempty"`
	NewRun  bool   `json:"newRun,omitempty"` // a fresh run was created (vs. an existing run extended)

	// the axioms this notice is about — ids plus a title snapshot so the feed reads without a live lookup
	AxiomIDs []string `json:"axiomIds"`
	Axioms   []string `json:"axioms"`

	// failed only — a short, human reason (never a raw log)
	Reason string `json:"reason,omitempty"`
}

const (
	NoticeAssigned = "assigned"
	NoticeFailed   = "failed"
)

// noticeCap bounds the pool so the feed stays portioned: only the newest noticeCap notices are kept.
const noticeCap = 50

// NoticeStore persists the auto-assignment feed (a small JSON file, same discipline as the runs and
// PR stores: atomic tmp+rename, 0600, missing → empty).
type NoticeStore struct {
	path string
	mu   sync.Mutex
}

func NewNoticeStore() *NoticeStore { return &NoticeStore{path: noticesPath()} }

func noticesPath() string {
	if p := os.Getenv("DEVLAB_MERCURY_RUNS_NOTICES"); p != "" {
		return p
	}
	return filepath.Join("/var/lib/devlab/mercury", "runs-notices.json")
}

// NewNoticeID mints an unguessable notice id (front-matter of the feed file, never a path segment).
func NewNoticeID() string {
	var b [10]byte
	_, _ = rand.Read(b[:])
	return "ntc_" + strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b[:]))
}

type noticeFile struct {
	Notices []Notice `json:"notices"`
}

func (s *NoticeStore) load() ([]Notice, error) {
	b, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var f noticeFile
	if err := json.Unmarshal(b, &f); err != nil {
		return nil, err
	}
	return f.Notices, nil
}

// List returns all notices, newest first (missing store → empty, never nil).
func (s *NoticeStore) List() ([]Notice, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cur, err := s.load()
	if err != nil {
		return []Notice{}, err
	}
	if cur == nil {
		return []Notice{}, nil
	}
	return cur, nil
}

// Add records a notice at the front of the feed (newest first) and trims to noticeCap so the pool stays
// portioned. The store owns the record's identity (id + timestamp); the caller supplies only the
// meaning. Nil slices are normalized to empty so a reader never faces a JSON null.
func (s *NoticeStore) Add(n Notice) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cur, err := s.load()
	if err != nil {
		return err
	}
	if n.ID == "" {
		n.ID = NewNoticeID()
	}
	if n.At.IsZero() {
		n.At = time.Now().UTC()
	}
	if n.AxiomIDs == nil {
		n.AxiomIDs = []string{}
	}
	if n.Axioms == nil {
		n.Axioms = []string{}
	}
	next := append([]Notice{n}, cur...)
	if len(next) > noticeCap {
		next = next[:noticeCap]
	}
	return s.save(next)
}

// Dismiss drops one notice by id (idempotent — an unknown id is a no-op).
func (s *NoticeStore) Dismiss(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cur, err := s.load()
	if err != nil {
		return err
	}
	out := make([]Notice, 0, len(cur))
	for _, n := range cur {
		if n.ID == id {
			continue
		}
		out = append(out, n)
	}
	return s.save(out)
}

// Clear empties the feed.
func (s *NoticeStore) Clear() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.save(nil)
}

func (s *NoticeStore) save(notices []Notice) error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(noticeFile{Notices: notices}, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

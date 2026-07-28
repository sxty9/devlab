// Package axiomauthors is a PASSIVE, DevLab-local pool recording who created and last changed each
// axiom of the constitution. It exists because the constitution itself is stored in the shared,
// instance-neutral axioms repo (via aigentic's scheme graveyard): personal data — usernames — must
// NOT be written there, or every instance's constitution would carry one instance's people. So the
// authorship lives here instead, in the instance's runtime state, exactly where instance-specifics
// belong.
//
// It is keyed by the axiom's stable front-matter id, so a record's authorship survives a re-file or
// rename (scheme carries the id with the content). The pool holds data only and evaluates nothing:
// whether a write is a create (stamp both) or an edit (stamp only "updated") is decided by the
// caller through the Mutate closure — the passive-pool rule.
package axiomauthors

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"

	"devlab/backend/internal/statepath"
)

// Author is who created and who last changed one axiom, kept separate so the creator stays visible
// after someone else edits. Empty fields mean unknown — the pool never fabricates a person.
type Author struct {
	CreatedBy string    `json:"createdBy,omitempty"`
	CreatedAt time.Time `json:"createdAt,omitempty"`
	UpdatedBy string    `json:"updatedBy,omitempty"`
	UpdatedAt time.Time `json:"updatedAt,omitempty"`
}

// Store is the JSON-file pool (atomic tmp+rename, 0600, missing → empty), matching the other DevLab
// stores' conventions. A nil *Store is a valid no-op sink so a disabled pool never fails an axiom write.
type Store struct {
	path string
	mu   sync.Mutex
}

// NewStore builds the pool from the environment.
func NewStore(p *statepath.Paths) *Store { return &Store{path: authorsPath(p)} }

func authorsPath(p *statepath.Paths) string {
	if p := os.Getenv("DEVLAB_MERCURY_AXIOM_AUTHORS"); p != "" {
		return p
	}
	if p != nil {
		return p.AxiomAuthors()
	}
	return ""
}

type file struct {
	Authors map[string]Author `json:"authors"`
}

func (s *Store) load() (map[string]Author, error) {
	b, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]Author{}, nil
		}
		return nil, err
	}
	var f file
	if err := json.Unmarshal(b, &f); err != nil {
		return nil, err
	}
	if f.Authors == nil {
		f.Authors = map[string]Author{}
	}
	return f.Authors, nil
}

// Get returns the authorship recorded for an axiom id (zero value + false when none is recorded, so
// the caller surfaces it as unknown rather than a guessed person).
func (s *Store) Get(id string) (Author, bool) {
	if s == nil || id == "" {
		return Author{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	m, err := s.load()
	if err != nil {
		return Author{}, false
	}
	a, ok := m[id]
	return a, ok
}

// Mutate atomically updates one id's record: load → fn(current) → save, all under the lock. The
// caller's fn decides WHAT to stamp (a create sets both created and updated; an edit sets only
// updated, leaving an unknown creator untouched), so the pool applies no policy of its own. It is
// best-effort — a nil store, an empty id, or a load/save error is a silent no-op, because recording
// authorship must never fail the underlying axiom write.
func (s *Store) Mutate(id string, fn func(Author) Author) {
	if s == nil || id == "" || fn == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	m, err := s.load()
	if err != nil {
		return
	}
	m[id] = fn(m[id])
	_ = s.save(m)
}

func (s *Store) save(m map[string]Author) error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(file{Authors: m}, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

// Package runs holds the passive pools of the run domain: run DEFINITIONS (this file),
// execution results, deliveries, PRs, notices, attachments, history and axiom checks. A run is a
// definition only — execution state lives exclusively in the per-execution state document
// (package execstate); the definition carries no state flags (B-20).
//
// This package never imports the api layer and never schedules anything itself.
package runs

import (
	"crypto/rand"
	"encoding/base32"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync"
	"time"

	"devlab/backend/internal/fsatomic"
	"devlab/backend/internal/model"
	"devlab/backend/internal/statepath"
)

// ErrNotFound is returned when an operation names a run id that does not exist, so an unknown-id
// update/delete never rewrites the pool or appends a spurious history snapshot.
var ErrNotFound = errors.New("run not found")

// Target is one destination of a task: an existing repo, or — with Create — a repo to be created
// first (REQ-006.2). The chain creates the repo and sets branch protection in the same pass.
type Target struct {
	Repo   string `json:"repo"`
	Create bool   `json:"create,omitempty"`
}

// Tuning is a run's engine choice. Empty strings and a nil TimeBudget REFER to the service
// defaults (REQ-010.2) — resolution happens in exactly one place, EffectiveTuning. A present
// TimeBudget of 0 means "no budget" (explicitly unlimited).
type Tuning struct {
	Model        string          `json:"model,omitempty"`
	ModelVersion string          `json:"modelVersion,omitempty"`
	Effort       string          `json:"effort,omitempty"`
	TimeBudget   *model.Duration `json:"timeBudget,omitempty"`
}

// Run is one run definition — recurring and axiom-driven (kind "auto") or a concrete one-time
// task (kind "todo"). SLIMMED (B-20): no StartPending, no Suspended, no Done, no LastResult —
// every execution fact is a projection over the execution documents and results.
type Run struct {
	ID    string        `json:"id"`
	Kind  model.RunKind `json:"kind"`
	Title string        `json:"title"`
	Task  string        `json:"task,omitempty"`

	// kind=auto
	AxiomIDs []string      `json:"axiomIds,omitempty"`
	Schedule *ScheduleSpec `json:"schedule,omitempty"`
	Active   bool          `json:"active,omitempty"`

	// kind=todo
	Targets []Target   `json:"targets,omitempty"`
	DueAt   *time.Time `json:"dueAt,omitempty"`

	// Autonomy fixes HOW SELF-RELIANT the run works — when it stops to ask instead of deciding for
	// itself (model.AutonomyLevel). Empty RESOLVES to Autonomous, so an existing run keeps today's
	// never-ask behavior.
	Autonomy model.AutonomyLevel `json:"autonomy,omitempty"`

	Tuning Tuning `json:"tuning"`

	// PromptSnapshot is the ONE composed prompt (constitution included); PromptInputHash
	// fingerprints the composition inputs INCLUDING the title (REQ-003).
	PromptSnapshot  string `json:"promptSnapshot,omitempty"`
	PromptInputHash string `json:"promptInputHash,omitempty"`

	Attachments []AttachmentRef  `json:"attachments,omitempty"`
	Authorship  model.Authorship `json:"authorship"`
}

// RunInput is the create/update request shape (the ONE parse target of the runs handler and the
// run MCP tools).
type RunInput struct {
	Kind     model.RunKind `json:"kind,omitempty"`
	Title    string        `json:"title"`
	Task     string        `json:"task,omitempty"`
	AxiomIDs []string      `json:"axiomIds,omitempty"`
	Schedule *ScheduleSpec `json:"schedule,omitempty"`
	Active   *bool         `json:"active,omitempty"`
	Targets  []Target      `json:"targets,omitempty"`
	DueAt    *time.Time    `json:"dueAt,omitempty"`
	Autonomy model.AutonomyLevel `json:"autonomy,omitempty"`
	Tuning   *Tuning       `json:"tuning,omitempty"`
}

// AttachmentRef is metadata for one media file attached to a run. The bytes live in the passive
// attachment pool (AttachmentStore), keyed by (run id, attachment id); this record is the single
// source of truth for WHICH attachments exist.
type AttachmentRef struct {
	ID         string    `json:"id"`
	Filename   string    `json:"filename"`
	MIME       string    `json:"mime,omitempty"`
	Size       int64     `json:"size"`
	SHA256     string    `json:"sha256,omitempty"`
	UploadedAt time.Time `json:"uploadedAt"`
	UploadedBy string    `json:"uploadedBy,omitempty"`
}

type runFile struct {
	Runs []Run `json:"runs"`
}

// Store is the passive pool of run definitions: one JSON file, mutated under a mutex, each
// user-meaningful mutation snapshotting the full config into the history so any past
// constellation can be restored.
type Store struct {
	path string
	hist *History
	mu   sync.Mutex
}

// NewStore builds the store (and its history) below the given state root. The historical
// per-store env override (DEVLAB_MERCURY_RUNS) is honored first — a ported test seam.
func NewStore(p *statepath.Paths) *Store {
	return &Store{path: runsPath(p), hist: NewHistory(p)}
}

func runsPath(p *statepath.Paths) string {
	if v := os.Getenv("DEVLAB_MERCURY_RUNS"); v != "" {
		return v
	}
	if p != nil {
		return p.Runs()
	}
	return ""
}

// NewID mints a stable, unguessable run id (front-matter of the runs file, never a path segment).
func NewID() string {
	var b [10]byte
	_, _ = rand.Read(b[:])
	return "run_" + strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b[:]))
}

// NewAttachmentID mints an unguessable attachment id. It is used as a path segment in the
// attachment pool, so it is bounded to the same [a-z0-9] shape the pool's guard enforces.
func NewAttachmentID() string {
	var b [10]byte
	_, _ = rand.Read(b[:])
	return "att_" + strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b[:]))
}

func (s *Store) load() ([]Run, error) {
	b, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var f runFile
	if err := json.Unmarshal(b, &f); err != nil {
		return nil, err
	}
	return f.Runs, nil
}

// List returns all runs (missing store → empty).
func (s *Store) List() ([]Run, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.load()
}

// Get returns one run by id.
func (s *Store) Get(id string) (Run, bool, error) {
	runs, err := s.List()
	if err != nil {
		return Run{}, false, err
	}
	for _, r := range runs {
		if r.ID == id {
			return r, true, nil
		}
	}
	return Run{}, false, nil
}

// Put inserts or replaces one run (matched by ID) and snapshots the resulting config into the
// history. The actor recorded is the run's Authorship.Updated.
func (s *Store) Put(r Run) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cur, err := s.load()
	if err != nil {
		return err
	}
	replaced := false
	for i := range cur {
		if cur[i].ID == r.ID {
			cur[i] = r
			replaced = true
			break
		}
	}
	if !replaced {
		cur = append(cur, r)
	}
	if err := s.save(cur); err != nil {
		return err
	}
	s.hist.snapshot(actionFor(replaced), actorName(r.Authorship.Updated), cur) // best-effort
	return nil
}

func actionFor(replaced bool) string {
	if replaced {
		return "update"
	}
	return "create"
}

func actorName(a model.Actor) string {
	if a.Autonomous {
		return "autonomous"
	}
	return a.User
}

// Delete removes one run by id (ErrNotFound if absent) and snapshots the history.
func (s *Store) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cur, err := s.load()
	if err != nil {
		return err
	}
	next := cur[:0:0]
	found := false
	for _, r := range cur {
		if r.ID == id {
			found = true
			continue
		}
		next = append(next, r)
	}
	if !found {
		return ErrNotFound
	}
	if err := s.save(next); err != nil {
		return err
	}
	s.hist.snapshot("delete", "", next)
	return nil
}

// Patch applies a runtime-state mutation WITHOUT a history snapshot (e.g. snapshot
// recomposition after an axiom write) so the restore history stays a history of
// user-meaningful config edits.
func (s *Store) Patch(fn func([]Run) ([]Run, error)) ([]Run, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cur, err := s.load()
	if err != nil {
		return nil, err
	}
	next, err := fn(cur)
	if err != nil {
		return nil, err
	}
	if err := s.save(next); err != nil {
		return nil, err
	}
	return next, nil
}

// ReplaceAll replaces the whole set (history restore / reviewed AI proposals) and snapshots it.
func (s *Store) ReplaceAll(runs []Run, action string, by model.Actor) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.save(runs); err != nil {
		return err
	}
	s.hist.snapshot(action, actorName(by), runs)
	return nil
}

// History exposes the config-snapshot history for listing and restore.
func (s *Store) History() *History { return s.hist }

func (s *Store) save(runs []Run) error {
	return fsatomic.WriteJSON(s.path, runFile{Runs: runs})
}

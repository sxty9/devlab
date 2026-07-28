// Package execstate is the ONE persisted state machine per execution (S7) — it replaces the
// five separate stores of the old scheduler (B 1.3c). Exactly one document per execution lives
// at executions/<id>/state.json; admission, display, resume and the free signal are pure
// projections over these documents. The process is mortal; these documents are the truth.
package execstate

import (
	"time"

	"devlab/backend/internal/model"
	"devlab/backend/internal/statepath"
)

// RepoState is one repo's coarse progress inside an execution.
type RepoState string

const (
	RepoPending RepoState = "pending"
	RepoActive  RepoState = "active"
	RepoDone    RepoState = "done"
	RepoBlocked RepoState = "blocked"
)

// RepoProgress is one repo's progress row.
type RepoProgress struct {
	Repo  string         `json:"repo"`
	State RepoState      `json:"state"`
	Block *model.Backoff `json:"block,omitempty"`
}

// Transition is one recorded phase transition. Every HUMAN-triggered transition carries its
// actor and time (REQ-041, A2-3).
type Transition struct {
	To     model.ExecPhase `json:"to"`
	Reason string          `json:"reason,omitempty"`
	By     model.Actor     `json:"by"`
	At     time.Time       `json:"at"`
}

// Doc is THE per-execution state document.
type Doc struct {
	ID     string          `json:"id"`
	RunID  string          `json:"runId"`
	Kind   model.RunKind   `json:"kind"`
	Phase  model.ExecPhase `json:"phase"`
	Reason string          `json:"reason,omitempty"`
	// Pause is the ONE pause (W-C): deferred-by-user or usage-limit.
	Pause *model.PauseView `json:"pause,omitempty"`
	// Continuation is the exact continuation point for resume.
	Continuation *model.ContinuationView `json:"continuation,omitempty"`
	Repos        []RepoProgress          `json:"repos"`
	Overload     bool                    `json:"overload,omitempty"`
	Requested    model.Authorship        `json:"requested"`
	Transitions  []Transition            `json:"transitions"`
	// Rev guards the CAS update path — every Update increments it.
	Rev       int        `json:"rev"`
	CreatedAt time.Time  `json:"createdAt"`
	StartedAt *time.Time `json:"startedAt,omitempty"`
	UpdatedAt *time.Time `json:"updatedAt,omitempty"`
}

// Live reports whether the document is alive: queued|running|paused|blocked|interrupted.
func (d Doc) Live() bool {
	switch d.Phase {
	case model.PhaseQueued, model.PhaseRunning, model.PhasePaused, model.PhaseBlocked, model.PhaseInterrupted:
		return true
	}
	return false
}

// Store persists the documents. All mutation goes through Update — atomic, CAS over Rev, under
// the store mutex (the one mutation path).
type Store struct {
	paths *statepath.Paths
}

// Open opens the store below the state root.
func Open(p *statepath.Paths) (*Store, error) {
	panic("TODO(B2)")
}

// Create mints a new execution document in phase created.
func (s *Store) Create(runID string, kind model.RunKind, repos []string, overload bool, by model.Actor) (Doc, error) {
	panic("TODO(B2)")
}

// Update is the ONE mutation path: load, mutate, CAS-check Rev, atomic write, all under the
// store mutex.
func (s *Store) Update(id string, mutate func(*Doc) error) (Doc, error) {
	panic("TODO(B2)")
}

// Get returns one document by execution id.
func (s *Store) Get(id string) (Doc, bool, error) {
	panic("TODO(B2)")
}

// List returns every document.
func (s *Store) List() ([]Doc, error) {
	panic("TODO(B2)")
}

// Live returns the live documents (queued|running|paused|blocked|interrupted).
func (s *Store) Live() ([]Doc, error) {
	panic("TODO(B2)")
}

// LiveForRun returns the live document of one run. INVARIANT: per run there is at most ONE
// live document; a violation is a store error.
func (s *Store) LiveForRun(runID string) (*Doc, error) {
	panic("TODO(B2)")
}

// MarkInterruptedAtBoot turns every "running" document into "interrupted" (reason "process
// death", continuation preserved) — called BEFORE the first request is served (REQ-039.1).
// Returns how many documents it reconciled.
func (s *Store) MarkInterruptedAtBoot(now time.Time) (int, error) {
	panic("TODO(B2)")
}

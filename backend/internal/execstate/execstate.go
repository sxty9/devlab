// Package execstate is the ONE persisted state machine per execution (S7) — it replaces the
// five separate stores of the old scheduler (B 1.3c). Exactly one document per execution lives
// at executions/<id>/state.json; admission, display, resume and the free signal are pure
// projections over these documents. The process is mortal; these documents are the truth.
package execstate

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"devlab/backend/internal/fsatomic"
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

// SetPhase performs one recorded transition: it sets phase and reason and appends the
// transition to the protocol (A2-3) — the ONE way any mutation changes the phase, so the
// protocol never misses a step.
func (d *Doc) SetPhase(to model.ExecPhase, reason string, by model.Actor, at time.Time) {
	d.Phase = to
	d.Reason = reason
	d.Transitions = append(d.Transitions, Transition{To: to, Reason: reason, By: by, At: at})
}

// Repo returns the progress row for one repo (nil when the doc does not carry it).
func (d *Doc) Repo(repo string) *RepoProgress {
	for i := range d.Repos {
		if d.Repos[i].Repo == repo {
			return &d.Repos[i]
		}
	}
	return nil
}

// Store persists the documents. All mutation goes through Update — atomic, CAS over Rev, under
// the store mutex (the one mutation path).
type Store struct {
	paths *statepath.Paths
	mu    sync.Mutex
}

// Open opens the store below the state root.
func Open(p *statepath.Paths) (*Store, error) {
	if p == nil || p.Root == "" {
		return nil, errors.New("execstate: state root not configured")
	}
	if err := os.MkdirAll(p.Executions(), 0o700); err != nil {
		return nil, fmt.Errorf("execstate: executions dir: %w", err)
	}
	return &Store{paths: p}, nil
}

// Paths exposes the state paths the store was opened over (the scheduler derives the restart
// marker location from it — one root, no second wiring).
func (s *Store) Paths() *statepath.Paths { return s.paths }

func (s *Store) statePath(id string) string {
	return filepath.Join(s.paths.Execution(id), "state.json")
}

// mintID mints a time-sortable execution id (a filename-safe path segment; same shape as the
// result ids so state.json and result.json of one execution share their directory).
func mintID(t time.Time) string {
	return "exec_" + strings.ReplaceAll(t.UTC().Format("20060102-150405.000000000"), ".", "-")
}

// Create mints a new execution document in phase created.
func (s *Store) Create(runID string, kind model.RunKind, repos []string, overload bool, by model.Actor) (Doc, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC()
	id := mintID(now)
	for {
		if _, err := os.Stat(s.statePath(id)); os.IsNotExist(err) {
			break
		}
		now = now.Add(time.Nanosecond)
		id = mintID(now)
	}
	d := Doc{
		ID:        id,
		RunID:     runID,
		Kind:      kind,
		Overload:  overload,
		Requested: model.Authorship{Created: by, CreatedAt: now, Updated: by, UpdatedAt: now},
		Rev:       1,
		CreatedAt: now,
	}
	for _, r := range repos {
		d.Repos = append(d.Repos, RepoProgress{Repo: r, State: RepoPending})
	}
	d.SetPhase(model.PhaseCreated, "", by, now)
	if err := fsatomic.WriteJSON(s.statePath(id), d); err != nil {
		return Doc{}, err
	}
	return d, nil
}

// Update is the ONE mutation path: load, mutate, CAS-check Rev, atomic write, all under the
// store mutex.
func (s *Store) Update(id string, mutate func(*Doc) error) (Doc, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok, err := s.read(id)
	if err != nil {
		return Doc{}, err
	}
	if !ok {
		return Doc{}, fmt.Errorf("execstate: %w: %s", ErrNotFound, id)
	}
	rev := d.Rev
	if err := mutate(&d); err != nil {
		return Doc{}, err
	}
	if d.Rev != rev {
		// The revision belongs to the store, not the mutation — a mutate that touched it would
		// undermine the CAS discipline.
		return Doc{}, fmt.Errorf("execstate: revision conflict on %s (mutate changed rev %d→%d)", id, rev, d.Rev)
	}
	d.Rev = rev + 1
	now := time.Now().UTC()
	d.UpdatedAt = &now
	if err := fsatomic.WriteJSON(s.statePath(id), d); err != nil {
		return Doc{}, err
	}
	return d, nil
}

// ErrNotFound names an unknown execution id.
var ErrNotFound = errors.New("execution document not found")

func (s *Store) read(id string) (Doc, bool, error) {
	b, err := os.ReadFile(s.statePath(id))
	if err != nil {
		if os.IsNotExist(err) {
			return Doc{}, false, nil
		}
		return Doc{}, false, err
	}
	var d Doc
	if err := json.Unmarshal(b, &d); err != nil {
		// Fail closed: admission must never run blind over an unreadable state document.
		return Doc{}, false, fmt.Errorf("execstate: unreadable state document %s: %w", id, err)
	}
	return d, true, nil
}

// Get returns one document by execution id.
func (s *Store) Get(id string) (Doc, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.read(id)
}

// List returns every document, oldest first. A directory without a state document (e.g. an
// imported legacy result) is skipped; an unreadable state document is an error (fail closed —
// admission must not run blind).
func (s *Store) List() ([]Doc, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.listLocked()
}

func (s *Store) listLocked() ([]Doc, error) {
	entries, err := os.ReadDir(s.paths.Executions())
	if err != nil {
		if os.IsNotExist(err) {
			return []Doc{}, nil
		}
		return nil, err
	}
	out := []Doc{}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		d, ok, err := s.read(e.Name())
		if err != nil {
			return nil, err
		}
		if !ok {
			continue // no state document here (legacy import) — not ours
		}
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].ID < out[j].ID
		}
		return out[i].CreatedAt.Before(out[j].CreatedAt)
	})
	return out, nil
}

// Live returns the live documents (queued|running|paused|blocked|interrupted).
func (s *Store) Live() ([]Doc, error) {
	all, err := s.List()
	if err != nil {
		return nil, err
	}
	out := []Doc{}
	for _, d := range all {
		if d.Live() {
			out = append(out, d)
		}
	}
	return out, nil
}

// PausedIDs returns the ids of the executions that stand on the ONE pause right now (phase
// paused — deferred-by-user or usage-limit). A caller that must tell "still running" from
// "standing still" reads it here instead of inferring it from a missing end stamp.
func (s *Store) PausedIDs() (map[string]bool, error) {
	live, err := s.Live()
	if err != nil {
		return nil, err
	}
	out := map[string]bool{}
	for _, d := range live {
		if d.Phase == model.PhasePaused {
			out[d.ID] = true
		}
	}
	return out, nil
}

// LiveForRun returns the live document of one run. INVARIANT: per run there is at most ONE
// live document; a violation is a store error.
func (s *Store) LiveForRun(runID string) (*Doc, error) {
	live, err := s.Live()
	if err != nil {
		return nil, err
	}
	var found *Doc
	for i := range live {
		if live[i].RunID != runID {
			continue
		}
		if found != nil {
			return nil, fmt.Errorf("execstate: invariant violated: run %s has more than one live document (%s, %s)",
				runID, found.ID, live[i].ID)
		}
		d := live[i]
		found = &d
	}
	return found, nil
}

// MarkInterruptedAtBoot turns every "running" document into "interrupted" (reason "process
// death", continuation preserved) — called BEFORE the first request is served (REQ-039.1).
// Returns how many documents it reconciled.
func (s *Store) MarkInterruptedAtBoot(now time.Time) (int, error) {
	all, err := s.List()
	if err != nil {
		return 0, err
	}
	n := 0
	for _, d := range all {
		if d.Phase != model.PhaseRunning {
			continue
		}
		if _, err := s.Update(d.ID, func(doc *Doc) error {
			if doc.Phase != model.PhaseRunning {
				return nil
			}
			doc.SetPhase(model.PhaseInterrupted, "process death", model.Actor{Autonomous: true}, now)
			return nil
		}); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

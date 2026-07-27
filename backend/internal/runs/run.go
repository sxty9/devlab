// Package runs is the model for Mercury's "Automatische Läufe" — scheduled autonomous runs. A run is
// a named, recurring job whose Claude prompt is composed from its Axioms + all global Laufregeln and
// which (in the execution phase) runs across every Holistic repo. The composed prompt is stored as a
// SNAPSHOT because composition needs the scheme store, reachable only in a cookie-bearing request —
// so the background scheduler can fire with no live session.
//
// This package owns persistence and scheduling; it never imports the api layer. Execution is injected
// as an Executor interface, so there is no import cycle.
package runs

import (
	"crypto/rand"
	"encoding/base32"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// ErrNotFound is returned by a Mutate closure that matched no run, so Mutate aborts before writing —
// an unknown-id update/recompose/delete must not rewrite the file or append a spurious history snapshot.
var ErrNotFound = errors.New("run not found")

// Type discriminates the two things that share this machinery (store, scheduler, executor, results,
// history) rather than duplicating it:
//
//	auto — a recurring, axiom-driven run over ALL Holistic repos (Automatische Läufe)
//	todo — a ONE-TIME, manually planned concrete task against ONE repo (Konkrete ToDos): an ad-hoc fix
//	       or a newly planned service. Its prompt is the free-text task; no axioms or Laufregeln are
//	       folded in, because the constitution already reaches the agent via the repo's CLAUDE.md.
type Type string

const (
	TypeAuto Type = "auto"
	TypeTodo Type = "todo"
)

// NormalizeType folds the zero value (records predating ToDos) to auto, so a run's kind is always
// exactly TypeAuto or TypeTodo. It is the single rule behind the "" == auto convention the Run model
// documents, reused wherever a stored kind must be resolved (execution history, calendars).
func NormalizeType(t Type) Type {
	if t == TypeTodo {
		return TypeTodo
	}
	return TypeAuto
}

// Run is one scheduled autonomous run (Type auto) or one concrete one-time task (Type todo).
type Run struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Type    Type   `json:"type"` // "" is read as auto (records predating ToDos)
	Enabled bool   `json:"enabled"`

	// auto only
	Schedule Schedule `json:"schedule"`
	AxiomIDs []string `json:"axiomIds"`

	// todo only
	Task        string       `json:"task,omitempty"`        // the concrete task; the prompt is built from it
	Targets     []Target     `json:"targets,omitempty"`     // one or more destinations (existing and/or newly-created repos)
	Attachments []Attachment `json:"attachments,omitempty"` // media (images, documents) the agent must take into account
	DueAt       *time.Time   `json:"dueAt,omitempty"`       // optional one-time due date; nil = run it manually
	Done        bool         `json:"done,omitempty"`        // set after a successful execution — a ToDo fires once

	// Deprecated: the single-target fields. Retained ONLY to read ToDo records written before a ToDo
	// could reach several repos; new writes populate Targets instead, and TodoTargets() bridges the two.
	Repo        string     `json:"repo,omitempty"`
	NewRepo     string     `json:"newRepo,omitempty"`
	Prompt      string     `json:"prompt"`               // composed snapshot (Axiom bodies + all Laufregeln + preamble)
	PromptAt    time.Time  `json:"promptAt,omitempty"`   // when the snapshot was composed
	PromptHash  string     `json:"promptHash,omitempty"` // fingerprint of the scheme inputs → staleness detection
	CreatedAt   time.Time  `json:"createdAt"`
	UpdatedAt   time.Time  `json:"updatedAt"`
	// CreatedBy and UpdatedBy are the Holistic usernames of who first created this run/ToDo and who last
	// changed it — kept SEPARATE so the creator stays visible even after someone else edits it. They are
	// stamped from the same actor already flowing into Store.Mutate, so no second author signal is
	// introduced. Empty on records written before authorship was tracked: surfaced as "unknown", never
	// back-filled to a person (no invented history).
	CreatedBy   string     `json:"createdBy,omitempty"`
	UpdatedBy   string     `json:"updatedBy,omitempty"`
	NextFireAt  *time.Time `json:"nextFireAt,omitempty"`  // nil = not scheduled (disabled); persisted → survives a restart
	LastFiredAt *time.Time `json:"lastFiredAt,omitempty"` // nil = never fired
	LastResult  *ResultRef `json:"lastResult,omitempty"`

	// Suspended is set when an execution stopped on the Claude usage limit and is waiting to resume.
	// While set it OVERRIDES the schedule (the run does not fire on NextFireAt), and the scheduler
	// resumes the SAME execution — only the not-yet-done repos — once ResumeAt passes.
	Suspended *Suspension `json:"suspended,omitempty"`
}

// Suspension records that a run hit the usage limit and should resume automatically. It points at the
// open execution (ResultID) so resume continues it instead of starting fresh, and counts attempts so a
// pathological loop (limit hit every resume) eventually gives up rather than spinning forever.
type Suspension struct {
	ResumeAt time.Time `json:"resumeAt"`
	ResultID string    `json:"resultId"`
	Attempts int       `json:"attempts"`
	Reason   string    `json:"reason,omitempty"` // e.g. "usage-limit"
}

// IsTodo reports whether this is a one-time concrete task rather than a recurring axiom run.
func (r Run) IsTodo() bool { return r.Type == TypeTodo }

// Target is one destination of a ToDo: an existing Holistic repo (Repo — its id or name) or a repo to
// be created first (NewRepo — a newly planned service). Exactly one of the two is set. A ToDo carries a
// list of these, so one task can reach several repos in a single sweep.
type Target struct {
	Repo    string `json:"repo,omitempty"`
	NewRepo string `json:"newRepo,omitempty"`
}

// TodoTargets is the single source of truth for a ToDo's destinations: the Targets list as stored, or —
// for a record written before multi-target support — the legacy single Repo/NewRepo folded into one
// target. Every reader (validation, execution, display) goes through this so the two shapes never
// diverge.
func (r Run) TodoTargets() []Target {
	if len(r.Targets) > 0 {
		return r.Targets
	}
	if r.Repo != "" || r.NewRepo != "" {
		return []Target{{Repo: r.Repo, NewRepo: r.NewRepo}}
	}
	return nil
}

// Attachment is metadata for one media file attached to a ToDo (an image, a document, …). The bytes
// live in the passive attachment pool (see AttachmentStore), keyed by (run id, attachment id); this
// record — stored inline on the Run — is the single source of truth for WHICH attachments exist. A
// ToDo carries media so the agent can take it into account while implementing the task; the executor
// materializes the pool into the agent's workspace at run time.
type Attachment struct {
	ID         string    `json:"id"`
	Filename   string    `json:"filename"`
	MIME       string    `json:"mime,omitempty"`
	Size       int64     `json:"size"`
	SHA256     string    `json:"sha256,omitempty"`
	UploadedAt time.Time `json:"uploadedAt"`
	UploadedBy string    `json:"uploadedBy,omitempty"`
}

// ResultRef is a lightweight pointer to a stored execution result (with its token/cost totals so the
// history list can show consumption without loading the full result).
type ResultRef struct {
	ResultID     string     `json:"resultId"`
	At           time.Time  `json:"at"`
	OK           bool       `json:"ok"`
	RepoCount    int        `json:"repoCount"`
	InputTokens  int        `json:"inputTokens,omitempty"`
	OutputTokens int        `json:"outputTokens,omitempty"`
	CostUSD      float64    `json:"costUsd,omitempty"`
	Suspended    bool       `json:"suspended,omitempty"` // execution paused on the usage limit
	ResumeAt     *time.Time `json:"resumeAt,omitempty"`  // when the paused execution will resume
}

type file struct {
	Runs []Run `json:"runs"`
}

// Store is the single source of truth for run instances: a JSON file, mutated under a mutex, each
// mutation snapshotting the full resulting config into the history so any past constellation can be
// restored. Storage mirrors mercury/order.go (atomic tmp+rename, 0600, missing → empty).
type Store struct {
	path string
	hist *History
	mu   sync.Mutex
}

// NewStore builds the store (and its history) from the environment. It never errors — a missing file
// is an empty store, matching the order/comments stores.
func NewStore() *Store {
	return &Store{path: runsPath(), hist: NewHistory()}
}

func runsPath() string {
	if p := os.Getenv("DEVLAB_MERCURY_RUNS"); p != "" {
		return p
	}
	return filepath.Join("/var/lib/devlab/mercury", "runs.json")
}

// NewID mints a stable, unguessable run id (front-matter of the runs file, never a path segment).
func NewID() string {
	var b [10]byte
	_, _ = rand.Read(b[:])
	return "run_" + strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b[:]))
}

// NewAttachmentID mints an unguessable attachment id. It is used as a path segment in the attachment
// pool, so it is bounded to the same [a-z0-9] shape the pool's guard enforces.
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
	var f file
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

// Mutate is the write path for user-meaningful config edits: load → fn → atomic save → history
// snapshot, all under the mutex. action/actor are recorded in the snapshot. fn returns the new list.
func (s *Store) Mutate(action, actor string, fn func([]Run) ([]Run, error)) ([]Run, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	next, err := s.apply(fn)
	if err != nil {
		return nil, err
	}
	s.hist.snapshot(action, actor, next) // best-effort: a history write must never lose the mutation
	return next, nil
}

// Patch is Mutate WITHOUT a history snapshot — for runtime-state changes (advancing NextFireAt,
// attaching a result) that are not user config edits, so the restore history stays meaningful.
func (s *Store) Patch(fn func([]Run) ([]Run, error)) ([]Run, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.apply(fn)
}

// apply loads, applies fn, and atomically saves. The caller holds s.mu.
func (s *Store) apply(fn func([]Run) ([]Run, error)) ([]Run, error) {
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

// History exposes the config-snapshot history for listing and restore.
func (s *Store) History() *History { return s.hist }

func (s *Store) save(runs []Run) error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(file{Runs: runs}, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

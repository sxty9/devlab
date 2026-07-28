// Service settings — the single source of truth for slot capacity, the default time budget and
// the automerge window (C F3; W3). A passive, atomic JSON pool; environment values are ONLY the
// first-start seed (REQ-013.2): once the file exists, runtime wins.
package runs

import (
	"encoding/json"
	"os"
	"strings"
	"sync"
	"time"

	"devlab/backend/internal/fsatomic"
	"devlab/backend/internal/model"
	"devlab/backend/internal/statepath"
)

// Settings are the three service tunables. The default time budget lives EXACTLY here (W3).
type Settings struct {
	MaxConcurrency    int           `json:"maxConcurrency"`
	DefaultTimeBudget time.Duration `json:"defaultTimeBudget"`
	AutomergeWindow   time.Duration `json:"automergeWindow"`
}

// settingsFile is the on-disk form. Durations are stored as model.Duration strings ("3h") so the
// pool stays human-readable; the tolerant unmarshal also accepts legacy nanosecond integers. Who
// changed the settings last is recorded as a label (REQ-041), never evaluated here.
type settingsFile struct {
	MaxConcurrency    int            `json:"maxConcurrency"`
	DefaultTimeBudget model.Duration `json:"defaultTimeBudget"`
	AutomergeWindow   model.Duration `json:"automergeWindow"`
	Updated           model.Actor    `json:"updated"`
	UpdatedAt         time.Time      `json:"updatedAt"`
}

// SettingsStore is the passive settings pool (mercury/settings.json).
type SettingsStore struct {
	path string
	seed Settings
	mu   sync.Mutex
}

// NewSettingsStore builds the store below the state root. seed provides the first-start values
// (from env in cmd/devlabd); it is used only while no file exists.
func NewSettingsStore(p *statepath.Paths, seed Settings) *SettingsStore {
	path := os.Getenv("DEVLAB_MERCURY_SETTINGS")
	if path == "" && p != nil {
		path = p.Settings()
	}
	return &SettingsStore{path: path, seed: seed}
}

// Get returns the current settings (the seed while no file exists). A present-but-unreadable
// file is an error, never silently the seed — a runtime choice must not be shadowed.
func (s *SettingsStore) Get() (Settings, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return s.seed, nil
		}
		return Settings{}, err
	}
	var f settingsFile
	if err := json.Unmarshal(b, &f); err != nil {
		return Settings{}, err
	}
	return Settings{
		MaxConcurrency:    f.MaxConcurrency,
		DefaultTimeBudget: time.Duration(f.DefaultTimeBudget),
		AutomergeWindow:   time.Duration(f.AutomergeWindow),
	}, nil
}

// Put replaces the settings atomically, recording who changed them. From the first Put on, the
// stored values win over the env seed (REQ-013.2).
func (s *SettingsStore) Put(set Settings, by model.Actor) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return fsatomic.WriteJSON(s.path, settingsFile{
		MaxConcurrency:    set.MaxConcurrency,
		DefaultTimeBudget: model.Duration(set.DefaultTimeBudget),
		AutomergeWindow:   model.Duration(set.AutomergeWindow),
		Updated:           by,
		UpdatedAt:         time.Now().UTC(),
	})
}

// ResolvedTuning is a run's fully resolved engine choice — reference semantics applied.
type ResolvedTuning struct {
	Model           string
	ModelVersion    string
	Effort          string
	Budget          time.Duration
	BudgetIsDefault bool
}

// EffectiveTuning resolves a run's tuning against the service defaults — the ONE place the
// reference semantics live (REQ-010): a nil budget REFERS to the default (so a later default
// change reaches every referring run), a present zero budget means "no budget". A negative
// stored value (corrupt data; the API refuses it) resolves to "no budget" rather than an
// unusable negative timeout.
func EffectiveTuning(r Run, set Settings) ResolvedTuning {
	rt := ResolvedTuning{
		Model:        strings.TrimSpace(r.Tuning.Model),
		ModelVersion: strings.TrimSpace(r.Tuning.ModelVersion),
		Effort:       strings.TrimSpace(r.Tuning.Effort),
	}
	if r.Tuning.TimeBudget == nil {
		rt.Budget = set.DefaultTimeBudget
		rt.BudgetIsDefault = true
		return rt
	}
	rt.Budget = time.Duration(*r.Tuning.TimeBudget)
	if rt.Budget < 0 {
		rt.Budget = 0
	}
	return rt
}

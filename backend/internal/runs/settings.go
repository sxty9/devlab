// Service settings — the single source of truth for slot capacity, the default time budget and
// the automerge window (C F3; W3). A passive, atomic JSON pool; environment values are ONLY the
// first-start seed (REQ-013.2): once the file exists, runtime wins.
package runs

import (
	"os"
	"time"

	"devlab/backend/internal/model"
	"devlab/backend/internal/statepath"
)

// Settings are the three service tunables. The default time budget lives EXACTLY here (W3).
type Settings struct {
	MaxConcurrency    int           `json:"maxConcurrency"`
	DefaultTimeBudget time.Duration `json:"defaultTimeBudget"`
	AutomergeWindow   time.Duration `json:"automergeWindow"`
}

// SettingsStore is the passive settings pool (mercury/settings.json).
type SettingsStore struct {
	path string
	seed Settings
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

// Get returns the current settings (the seed while no file exists).
func (s *SettingsStore) Get() (Settings, error) {
	panic("TODO(B8)")
}

// Put replaces the settings atomically, recording who changed them.
func (s *SettingsStore) Put(set Settings, by model.Actor) error {
	panic("TODO(B8)")
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
// reference semantics live (REQ-010): empty fields refer to the default (so a later default
// change reaches every referring run), a present zero budget means "no budget".
func EffectiveTuning(r Run, set Settings) ResolvedTuning {
	panic("TODO(B8)")
}

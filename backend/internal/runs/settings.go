package runs

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
)

// RunSettings are the live, UI-adjustable settings of the runs subsystem. Currently just the number of
// execution slots (the concurrency cap). Persisted so a chosen value survives a restart. A zero value
// means "not set" — the caller then falls back to the env/default seed, so the environment value stays
// valid only until something is configured (req 13).
type RunSettings struct {
	MaxConcurrent int `json:"maxConcurrent,omitempty"` // 0 = not set → use the env/default seed
}

// SettingsStore persists RunSettings (a small JSON file, same discipline as the other run stores: atomic
// tmp+rename, 0600, missing → zero value). A passive pool: it stores and returns, the caller decides.
type SettingsStore struct {
	path string
	mu   sync.Mutex
}

func NewSettingsStore() *SettingsStore { return &SettingsStore{path: settingsPath()} }

func settingsPath() string {
	if p := os.Getenv("DEVLAB_MERCURY_RUNS_SETTINGS"); p != "" {
		return p
	}
	return filepath.Join("/var/lib/devlab/mercury", "runs-settings.json")
}

func (s *SettingsStore) load() (RunSettings, error) {
	b, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return RunSettings{}, nil
		}
		return RunSettings{}, err
	}
	var rs RunSettings
	if err := json.Unmarshal(b, &rs); err != nil {
		return RunSettings{}, err
	}
	return rs, nil
}

// Get returns the current settings (missing store → zero value).
func (s *SettingsStore) Get() (RunSettings, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.load()
}

// Set applies fn to the settings and saves atomically, returning the new value.
func (s *SettingsStore) Set(fn func(*RunSettings)) (RunSettings, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cur, err := s.load()
	if err != nil {
		return RunSettings{}, err
	}
	fn(&cur)
	return cur, s.save(cur)
}

func (s *SettingsStore) save(rs RunSettings) error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(rs, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

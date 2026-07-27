package runs

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// DefaultTimeBudgetFallback is the built-in service default agent time budget — the per-repo cap that
// applies to any run/todo that made no own choice, when nothing is configured. Three hours: a full
// build-from-scratch pass needs materially more than a small correction, and too tight a cap kills a
// run mid-implement and wastes the whole invested time. It is deliberately generous; the whole-sweep
// duration cap remains the outer bound.
const DefaultTimeBudgetFallback = 3 * time.Hour

// Config is the service-level configuration for run execution: the settings that govern every run
// unless a run overrides them. Today it carries one value, the default time budget, and it is the seam
// a central configuration surface reads and writes.
//
// It is stored in a passive pool (see ConfigStore) — the pool holds the raw value and never interprets
// it; resolving the effective budget (parsing, falling back to the env bootstrap, then to the built-in
// default) is policy and lives outside the pool.
type Config struct {
	// DefaultTimeBudget is the time budget applied to a run/todo that made no own choice. A run
	// REFERENCES this default — it stores no budget of its own — so changing it here moves every
	// unconfigured run at once; the default is never copied into a run. Format: a Go duration string
	// ("3h", "90m"); "0" removes the cap (only the whole-sweep duration then bounds a run); "" (unset)
	// falls back to the built-in DefaultTimeBudgetFallback (bridged through the DEVLAB_RUNS_AGENT_TIMEOUT
	// env bootstrap, if present).
	DefaultTimeBudget string `json:"defaultTimeBudget,omitempty"`
}

// ConfigStore is the single source of truth for the service configuration: one JSON file, mutated under
// a mutex, written atomically (tmp+rename, 0600, missing → zero Config). It mirrors the runs Store's
// storage idiom so the service's stores stay structurally uniform. It is a passive pool: Get/Set only.
type ConfigStore struct {
	path string
	mu   sync.Mutex
}

// NewConfigStore builds the store from the environment. It never errors — a missing file is the zero
// Config, matching the other Mercury stores.
func NewConfigStore() *ConfigStore { return &ConfigStore{path: configPath()} }

func configPath() string {
	if p := os.Getenv("DEVLAB_MERCURY_CONFIG"); p != "" {
		return p
	}
	return filepath.Join("/var/lib/devlab/mercury", "config.json")
}

// Get returns the stored configuration (missing store → zero Config).
func (s *ConfigStore) Get() (Config, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.load()
}

// Set replaces the stored configuration atomically.
func (s *ConfigStore) Set(c Config) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.save(c)
}

func (s *ConfigStore) load() (Config, error) {
	b, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return Config{}, nil
		}
		return Config{}, err
	}
	var c Config
	if err := json.Unmarshal(b, &c); err != nil {
		return Config{}, err
	}
	return c, nil
}

func (s *ConfigStore) save(c Config) error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

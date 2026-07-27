package runs

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
)

// Settings is the runner's service-level configuration — the one central place for values that belong
// to the SERVICE rather than to any single run. It exists so a service default (today: the per-repo
// time budget every run follows unless it made its own choice) can be changed at runtime, without a
// restart and without editing an environment variable, and without copying the value into each run.
//
// It is deliberately a small struct with a growable Config, not a per-key bag: further service defaults
// join Config here rather than spawning a second settings store (one central config surface, not many).
//
// Like the other Mercury stores it is a pure passive pool: it holds the values and interprets nothing.
// What the default MEANS — how a run's effective budget is derived from it, and the built-in fallback —
// lives outside, in the executor. Reference-not-copy is a property of that resolution: a run without its
// own budget resolves against the live Settings value each fire, so changing it here changes every such
// run's next pass, and no run stores a stale copy.
type Settings struct {
	path string
	mu   sync.Mutex
}

// Config is the on-disk and wire shape of the service settings.
type Config struct {
	// DefaultTimeBudget is the per-repo agent wall-clock cap a run FOLLOWS when it made no own choice.
	// A Go duration string ("3h"); "0" is a deliberate "no cap" default; "" is read as "use the built-in
	// default" (resolved by the executor), so clearing it resets to the built-in rather than removing the
	// concept of a default.
	DefaultTimeBudget string `json:"defaultTimeBudget"`
}

// NewSettings builds the store from the environment. Like the other Mercury stores it never errors on
// read: a missing file is an empty Config (the caller then applies the built-in default).
func NewSettings() *Settings {
	p := os.Getenv("DEVLAB_MERCURY_SETTINGS")
	if p == "" {
		p = filepath.Join(filepath.Dir(runsPath()), "settings.json")
	}
	return &Settings{path: p}
}

func (s *Settings) load() Config {
	var c Config
	if b, err := os.ReadFile(s.path); err == nil {
		_ = json.Unmarshal(b, &c)
	}
	return c
}

// Get returns the stored config (an empty Config when nothing is persisted yet). Atomic read.
func (s *Settings) Get() Config {
	if s == nil {
		return Config{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.load()
}

// Set persists a new config atomically (tmp + rename), so a read never observes a half-written file.
func (s *Settings) Set(c Config) error {
	if s == nil {
		return errors.New("settings store unavailable")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
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

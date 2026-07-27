package runs

import (
	"path/filepath"
	"testing"
)

// The settings store round-trips a service default and survives a reopen (it is the durable central
// config), and a fresh store is empty so the caller applies the built-in default.
func TestSettingsRoundTrip(t *testing.T) {
	t.Setenv("DEVLAB_MERCURY_SETTINGS", filepath.Join(t.TempDir(), "settings.json"))
	s := NewSettings()
	if got := s.Get(); got.DefaultTimeBudget != "" {
		t.Fatalf("fresh store should be empty, got %q", got.DefaultTimeBudget)
	}
	if err := s.Set(Config{DefaultTimeBudget: "3h"}); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if got := NewSettings().Get(); got.DefaultTimeBudget != "3h" {
		t.Fatalf("persisted value = %q, want 3h", got.DefaultTimeBudget)
	}
}

// A nil store is safe (the whole store family tolerates being disabled): Get is the zero Config, Set errors.
func TestSettingsNilSafe(t *testing.T) {
	var s *Settings
	if got := s.Get(); got != (Config{}) {
		t.Fatalf("nil store Get = %+v, want zero Config", got)
	}
	if err := s.Set(Config{DefaultTimeBudget: "3h"}); err == nil {
		t.Fatal("nil store Set should error")
	}
}

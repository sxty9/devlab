package api

import (
	"path/filepath"
	"testing"
	"time"

	"devlab/backend/internal/runs"
)

// TestDefaultTimeBudgetResolution pins the resolution ladder: configured value beats the env bootstrap
// beats the built-in three-hour fallback; "0" is a service-wide no-cap; a malformed configured value
// falls through rather than being trusted.
func TestDefaultTimeBudgetResolution(t *testing.T) {
	t.Setenv("DEVLAB_MERCURY_CONFIG", filepath.Join(t.TempDir(), "config.json"))
	s := &Server{config: runs.NewConfigStore()}

	// 1. Nothing configured, no env → built-in three-hour fallback.
	if d, label := s.defaultTimeBudget(); d != runs.DefaultTimeBudgetFallback || label != "3h" {
		t.Fatalf("fallback: got %v/%q, want 3h", d, label)
	}

	// 2. Env bootstrap (the cap first shipped as DEVLAB_RUNS_AGENT_TIMEOUT) is honoured when unconfigured.
	t.Setenv("DEVLAB_RUNS_AGENT_TIMEOUT", "90m")
	if d, label := s.defaultTimeBudget(); d != 90*time.Minute || label != "1h30m" {
		t.Fatalf("env bootstrap: got %v/%q, want 90m", d, label)
	}

	// 3. A configured value overrides the env.
	if err := s.config.Set(runs.Config{DefaultTimeBudget: "2h"}); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if d, label := s.defaultTimeBudget(); d != 2*time.Hour || label != "2h" {
		t.Fatalf("configured: got %v/%q, want 2h", d, label)
	}

	// 4. "0" is a deliberate service-wide no-cap.
	if err := s.config.Set(runs.Config{DefaultTimeBudget: "0"}); err != nil {
		t.Fatalf("Set 0: %v", err)
	}
	if d, label := s.defaultTimeBudget(); d != 0 || label != "0" {
		t.Fatalf("no-cap: got %v/%q, want 0", d, label)
	}

	// 5. A malformed stored value is not trusted — fall through to the env.
	if err := s.config.Set(runs.Config{DefaultTimeBudget: "garbage"}); err != nil {
		t.Fatalf("Set garbage: %v", err)
	}
	if d, _ := s.defaultTimeBudget(); d != 90*time.Minute {
		t.Fatalf("malformed config should fall through to env, got %v", d)
	}
}

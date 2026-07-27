package api

import (
	"path/filepath"
	"testing"
	"time"

	"devlab/backend/internal/runs"
)

func TestHumanBudget(t *testing.T) {
	cases := []struct {
		d    time.Duration
		want string
	}{
		{0, "0"},
		{-5 * time.Minute, "0"},
		{3 * time.Hour, "3h"},
		{90 * time.Minute, "1h30m"},
		{30 * time.Minute, "30m"},
		{2 * time.Hour, "2h"},
	}
	for _, c := range cases {
		if got := humanBudget(c.d); got != c.want {
			t.Errorf("humanBudget(%s) = %q, want %q", c.d, got, c.want)
		}
	}
}

func TestParseBudget(t *testing.T) {
	for _, v := range []string{"", "  ", "nope", "-1h"} {
		if _, ok := parseBudget(v); ok {
			t.Errorf("parseBudget(%q) usable, want unusable", v)
		}
	}
	if d, ok := parseBudget("0"); !ok || d != 0 {
		t.Errorf(`parseBudget("0") = (%s,%v), want (0,true)`, d, ok)
	}
	if d, ok := parseBudget("3h"); !ok || d != 3*time.Hour {
		t.Errorf(`parseBudget("3h") = (%s,%v), want (3h,true)`, d, ok)
	}
	if d, ok := parseBudget("  90m  "); !ok || d != 90*time.Minute {
		t.Errorf(`parseBudget("  90m  ") = (%s,%v), want (90m,true)`, d, ok)
	}
}

// serviceDefaultBudget: a persisted Settings value wins (the runtime knob), else the legacy env override,
// else the built-in three hours — and an explicit "0" is a real no-cap default, not a fall-through.
func TestServiceDefaultBudget(t *testing.T) {
	if defaultAgentTimeout != 3*time.Hour {
		t.Fatalf("built-in default is %s, want 3h", defaultAgentTimeout)
	}
	t.Run("built-in when nothing set", func(t *testing.T) {
		t.Setenv("DEVLAB_RUNS_AGENT_TIMEOUT", "")
		if d := serviceDefaultBudget(nil); d != defaultAgentTimeout {
			t.Fatalf("got %s, want built-in %s", d, defaultAgentTimeout)
		}
	})
	t.Run("env override when no settings value", func(t *testing.T) {
		t.Setenv("DEVLAB_MERCURY_SETTINGS", filepath.Join(t.TempDir(), "settings.json"))
		t.Setenv("DEVLAB_RUNS_AGENT_TIMEOUT", "45m")
		if d := serviceDefaultBudget(runs.NewSettings()); d != 45*time.Minute {
			t.Fatalf("got %s, want env 45m", d)
		}
	})
	t.Run("settings value wins over env", func(t *testing.T) {
		t.Setenv("DEVLAB_MERCURY_SETTINGS", filepath.Join(t.TempDir(), "settings.json"))
		t.Setenv("DEVLAB_RUNS_AGENT_TIMEOUT", "45m")
		st := runs.NewSettings()
		if err := st.Set(runs.Config{DefaultTimeBudget: "2h"}); err != nil {
			t.Fatal(err)
		}
		if d := serviceDefaultBudget(st); d != 2*time.Hour {
			t.Fatalf("got %s, want settings 2h", d)
		}
	})
	t.Run("explicit no-cap default", func(t *testing.T) {
		t.Setenv("DEVLAB_MERCURY_SETTINGS", filepath.Join(t.TempDir(), "settings.json"))
		t.Setenv("DEVLAB_RUNS_AGENT_TIMEOUT", "45m")
		st := runs.NewSettings()
		if err := st.Set(runs.Config{DefaultTimeBudget: "0"}); err != nil {
			t.Fatal(err)
		}
		if d := serviceDefaultBudget(st); d != 0 {
			t.Fatalf("got %s, want 0 (no cap)", d)
		}
	})
}

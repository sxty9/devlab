package runs

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"devlab/backend/internal/model"
)

func newTestSettings(t *testing.T, seed Settings) *SettingsStore {
	t.Helper()
	t.Setenv("DEVLAB_MERCURY_SETTINGS", filepath.Join(t.TempDir(), "settings.json"))
	return NewSettingsStore(nil, seed)
}

// The env seed carries only the FIRST start: while no file exists Get returns the seed; the
// first Put persists a runtime choice and from then on runtime wins over any seed
// (REQ-013.2 — env is a start value, the runtime configuration is the truth).
func TestSettingsSeedThenRuntimeWins(t *testing.T) {
	seed := Settings{MaxConcurrency: 1, DefaultTimeBudget: 3 * time.Hour, AutomergeWindow: 720 * time.Hour}
	s := newTestSettings(t, seed)

	got, err := s.Get()
	if err != nil || got != seed {
		t.Fatalf("Get before any Put = %+v, %v; want the seed %+v", got, err, seed)
	}

	chosen := Settings{MaxConcurrency: 4, DefaultTimeBudget: 2 * time.Hour, AutomergeWindow: 24 * time.Hour}
	if err := s.Put(chosen, model.Actor{User: "admin"}); err != nil {
		t.Fatal(err)
	}
	got, err = s.Get()
	if err != nil || got != chosen {
		t.Fatalf("Get after Put = %+v, %v; want %+v", got, err, chosen)
	}

	// A fresh store over the same file with a DIFFERENT seed still returns the stored values —
	// the seed never shadows a runtime choice.
	s2 := &SettingsStore{path: s.path, seed: Settings{MaxConcurrency: 99}}
	got, err = s2.Get()
	if err != nil || got != chosen {
		t.Fatalf("Get with new seed = %+v, %v; want the stored %+v", got, err, chosen)
	}
}

// The stored form is human-readable (duration strings, not nanosecond blobs) and records who
// changed the settings (REQ-041 — a label on the pool, evaluated nowhere here).
func TestSettingsFileFormAndAuthor(t *testing.T) {
	s := newTestSettings(t, Settings{})
	if err := s.Put(Settings{MaxConcurrency: 2, DefaultTimeBudget: 3 * time.Hour}, model.Actor{User: "alice"}); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(s.path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"3h0m0s"`) {
		t.Errorf("duration not stored as a string: %s", b)
	}
	if !strings.Contains(string(b), `"alice"`) {
		t.Errorf("author not recorded: %s", b)
	}
}

// A present-but-corrupt file is an error, never silently the seed: a runtime choice must not
// be shadowed by a parse failure.
func TestSettingsCorruptFileIsAnError(t *testing.T) {
	s := newTestSettings(t, Settings{MaxConcurrency: 1})
	if err := os.WriteFile(s.path, []byte("{ torn"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(); err == nil {
		t.Fatal("Get over a corrupt file must error, not fall back to the seed")
	}
}

// Reference semantics (REQ-010.2): a run without its own choice FOLLOWS the default — changing
// the default changes what the run resolves to; an explicit choice is stable against default
// changes and recognizable as its own (BudgetIsDefault=false).
func TestEffectiveTuningReferenceSemantics(t *testing.T) {
	referring := Run{ID: "run_a", Kind: model.KindAuto} // no TimeBudget → refers
	chosen := model.Duration(90 * time.Minute)
	explicit := Run{ID: "run_b", Kind: model.KindTodo, Tuning: Tuning{TimeBudget: &chosen}}

	before := Settings{DefaultTimeBudget: 3 * time.Hour}
	after := Settings{DefaultTimeBudget: time.Hour}

	if rt := EffectiveTuning(referring, before); rt.Budget != 3*time.Hour || !rt.BudgetIsDefault {
		t.Fatalf("referring run before change: %+v", rt)
	}
	if rt := EffectiveTuning(referring, after); rt.Budget != time.Hour || !rt.BudgetIsDefault {
		t.Fatalf("a default change must reach the referring run: %+v", rt)
	}
	if rt := EffectiveTuning(explicit, after); rt.Budget != 90*time.Minute || rt.BudgetIsDefault {
		t.Fatalf("an explicit choice must survive a default change and read as its own: %+v", rt)
	}
}

// "No budget" is a legitimate explicit value (REQ-010.3): a PRESENT zero resolves to zero and
// is NOT the default reference — only the execution's overall duration then bounds the run.
func TestEffectiveTuningNoBudget(t *testing.T) {
	zero := model.Duration(0)
	r := Run{ID: "run_a", Tuning: Tuning{TimeBudget: &zero}}
	rt := EffectiveTuning(r, Settings{DefaultTimeBudget: 3 * time.Hour})
	if rt.Budget != 0 || rt.BudgetIsDefault {
		t.Fatalf(`"no budget" must resolve to 0 and not read as the default: %+v`, rt)
	}
	// A negative stored value (corrupt data; the API refuses it) degrades to "no budget",
	// never to a negative timeout.
	neg := model.Duration(-time.Hour)
	rt = EffectiveTuning(Run{Tuning: Tuning{TimeBudget: &neg}}, Settings{})
	if rt.Budget != 0 {
		t.Fatalf("negative budget must clamp to 0: %+v", rt)
	}
}

// Model, version and effort resolve as the run's own choice (trimmed); empty means "the
// engine default" and stays empty — there is no second store to consult (REQ-009: the chosen
// model is exactly what reaches the execution call).
func TestEffectiveTuningModelChoiceReachesTheCall(t *testing.T) {
	r := Run{Tuning: Tuning{Model: " claude-fable-5 ", ModelVersion: "20260115", Effort: "ultracode"}}
	rt := EffectiveTuning(r, Settings{})
	if rt.Model != "claude-fable-5" || rt.ModelVersion != "20260115" || rt.Effort != "ultracode" {
		t.Fatalf("chosen engine values must resolve verbatim: %+v", rt)
	}
	if rt := EffectiveTuning(Run{}, Settings{}); rt.Model != "" || rt.Effort != "" {
		t.Fatalf("no choice must stay empty (engine default), got %+v", rt)
	}
}

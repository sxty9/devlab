package api

import (
	"context"
	"strings"
	"testing"
	"time"

	"devlab/backend/internal/runs"
)

// A run/todo may carry an optional model + effort; both are guarded and trimmed. Empty is always fine
// (it selects the runner default), "ultracode" is accepted as the maximal effort tier, and anything
// outside the ladder or an arg-smuggling model string is rejected.
func TestValidateTuning(t *testing.T) {
	cases := []struct {
		name       string
		model      string
		effort     string
		wantOK     bool
		wantModel  string // trimmed value after a successful validate
		wantEffort string
	}{
		{name: "both empty", model: "", effort: "", wantOK: true},
		{name: "alias model + max", model: "opus", effort: "max", wantOK: true, wantModel: "opus", wantEffort: "max"},
		{name: "full id + ultracode", model: "claude-opus-4-8", effort: "ultracode", wantOK: true, wantModel: "claude-opus-4-8", wantEffort: "ultracode"},
		{name: "fable id", model: "claude-fable-5", effort: "low", wantOK: true, wantModel: "claude-fable-5", wantEffort: "low"},
		{name: "trims surrounding space", model: "  sonnet  ", effort: "  high  ", wantOK: true, wantModel: "sonnet", wantEffort: "high"},
		{name: "unknown effort", model: "opus", effort: "turbo", wantOK: false},
		{name: "effort with spaces smuggled", model: "opus", effort: "max max", wantOK: false},
		{name: "model with a flag", model: "opus --dangerously", effort: "", wantOK: false},
		{name: "model with a slash", model: "../etc", effort: "", wantOK: false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			b := &runBody{Model: c.model, Effort: c.effort}
			code, _ := validateTuning(b)
			if (code == 0) != c.wantOK {
				t.Fatalf("validateTuning(model=%q effort=%q) code=%d, wantOK=%v", c.model, c.effort, code, c.wantOK)
			}
			if c.wantOK {
				if b.Model != c.wantModel || b.Effort != c.wantEffort {
					t.Fatalf("after validate: model=%q effort=%q, want model=%q effort=%q", b.Model, b.Effort, c.wantModel, c.wantEffort)
				}
			}
		})
	}
}

// The optional time budget is guarded like model/effort: empty follows the service default, "off"/"none"/0
// canonicalise to a single "off" no-cap token, a positive duration up to a day passes through, and a typo
// or an absurd value is rejected with a clear message rather than silently stored.
func TestValidateBudget(t *testing.T) {
	cases := []struct {
		in     string
		wantOK bool
		want   string // canonical value after a successful validate
	}{
		{in: "", wantOK: true, want: ""},
		{in: "3h", wantOK: true, want: "3h"},
		{in: "90m", wantOK: true, want: "90m"},
		{in: "  6h  ", wantOK: true, want: "6h"},
		{in: "24h", wantOK: true, want: "24h"},
		{in: "off", wantOK: true, want: "off"},
		{in: "None", wantOK: true, want: "off"},
		{in: "0", wantOK: true, want: "off"},
		{in: "0s", wantOK: true, want: "off"},
		{in: "-1h", wantOK: false},
		{in: "48h", wantOK: false}, // beyond maxAgentBudget
		{in: "banana", wantOK: false},
		{in: "3 h", wantOK: false},
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			b := &runBody{TimeBudget: c.in}
			code, _ := validateTuning(b)
			if (code == 0) != c.wantOK {
				t.Fatalf("validateTuning(budget=%q) code=%d, wantOK=%v", c.in, code, c.wantOK)
			}
			if c.wantOK && b.TimeBudget != c.want {
				t.Fatalf("after validate: budget=%q, want %q", b.TimeBudget, c.want)
			}
		})
	}
}

// budgetFor resolves the per-repo budget: an empty choice REFERENCES the service default (3h here, with no
// env override) without being marked an override, an explicit duration or "off" overrides it, and a value
// that somehow reached the store unparseable falls back to the default rather than erroring.
func TestBudgetFor(t *testing.T) {
	cases := []struct {
		budget       string
		wantDur      time.Duration
		wantExplicit bool
	}{
		{budget: "", wantDur: defaultAgentTimeout, wantExplicit: false},
		{budget: "6h", wantDur: 6 * time.Hour, wantExplicit: true},
		{budget: "90m", wantDur: 90 * time.Minute, wantExplicit: true},
		{budget: "off", wantDur: 0, wantExplicit: true},
		{budget: "0", wantDur: 0, wantExplicit: true},
		{budget: "garbage", wantDur: defaultAgentTimeout, wantExplicit: false},
	}
	for _, c := range cases {
		t.Run(c.budget, func(t *testing.T) {
			d, explicit := budgetFor(runs.Run{TimeBudget: c.budget})
			if d != c.wantDur || explicit != c.wantExplicit {
				t.Fatalf("budgetFor(%q) = (%s, %v), want (%s, %v)", c.budget, d, explicit, c.wantDur, c.wantExplicit)
			}
		})
	}
}

// budgetLabel renders the resolved budget as the compact token the Result carries and the UI formats.
func TestBudgetLabel(t *testing.T) {
	cases := []struct {
		d    time.Duration
		want string
	}{
		{3 * time.Hour, "3h"},
		{90 * time.Minute, "1h30m"},
		{45 * time.Minute, "45m"},
		{2 * time.Hour, "2h"},
		{0, "off"},
	}
	for _, c := range cases {
		if got := budgetLabel(c.d); got != c.want {
			t.Fatalf("budgetLabel(%s) = %q, want %q", c.d, got, c.want)
		}
	}
}

// budgetOverrun names an exceeded budget ONLY when the context was cancelled by the budget's own timeout
// (cause errBudgetExceeded) — so the whole-sweep cap and a deliberate abort, which cancel with other
// causes, are never mislabelled as a budget overrun (req 4).
func TestBudgetOverrun(t *testing.T) {
	t.Run("budget cause names the value", func(t *testing.T) {
		ctx, cancel := context.WithCancelCause(context.Background())
		cancel(errBudgetExceeded)
		msg := budgetOverrun(ctx, 3*time.Hour)
		if !strings.Contains(msg, "Zeitbudget") || !strings.Contains(msg, "3h") {
			t.Fatalf("budgetOverrun on a budget cancel = %q, want it to name the budget and 3h", msg)
		}
	})
	t.Run("a plain cancel is not a budget overrun", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if msg := budgetOverrun(ctx, 3*time.Hour); msg != "" {
			t.Fatalf("budgetOverrun on a plain cancel = %q, want empty", msg)
		}
	})
}

// resolve turns the run's (possibly empty) tuning into the concrete CLI model/effort + preamble the
// executor drives: empty falls back to the historical opus / max, an explicit choice passes through, and
// "ultracode" maps to max effort plus the multi-agent directive folded into the system prompt.
func TestAgentTuningResolve(t *testing.T) {
	t.Run("empty falls back to opus/max, plain preamble", func(t *testing.T) {
		model, effort, preamble := agentTuning{}.resolve()
		if model != "opus" || effort != "max" {
			t.Fatalf("empty tuning resolved to model=%q effort=%q, want opus/max", model, effort)
		}
		if preamble != runnerPreamble {
			t.Fatalf("empty tuning must not alter the preamble")
		}
	})
	t.Run("explicit model + effort pass through", func(t *testing.T) {
		model, effort, preamble := agentTuning{model: "claude-fable-5", effort: "high"}.resolve()
		if model != "claude-fable-5" || effort != "high" {
			t.Fatalf("resolved to model=%q effort=%q, want claude-fable-5/high", model, effort)
		}
		if preamble != runnerPreamble {
			t.Fatalf("non-ultracode effort must not alter the preamble")
		}
	})
	t.Run("ultracode maps to max + orchestration directive", func(t *testing.T) {
		model, effort, preamble := agentTuning{effort: "ultracode"}.resolve()
		if model != "opus" || effort != "max" {
			t.Fatalf("ultracode resolved to model=%q effort=%q, want opus/max", model, effort)
		}
		if !strings.Contains(preamble, ultracodeDirective) {
			t.Fatalf("ultracode must fold its directive into the preamble")
		}
	})
	// Defense in depth: resolve() guards the bypassPermissions argv, so a value that reached the store
	// unvalidated (hand-edited runs.json, a future writer that skips validateTuning) must NOT be forwarded
	// verbatim to the CLI — it falls back to the safe default instead of smuggling a flag or bogus tier.
	t.Run("non-conforming model/effort fall back to the default", func(t *testing.T) {
		model, effort, preamble := agentTuning{model: "evil --dangerously-skip", effort: "turbo"}.resolve()
		if model != "opus" || effort != "max" {
			t.Fatalf("garbage tuning resolved to model=%q effort=%q, want opus/max", model, effort)
		}
		if preamble != runnerPreamble {
			t.Fatalf("a rejected effort must not alter the preamble")
		}
	})
}

// agentArgs / streamAgentArgs carry the resolved model + effort onto the CLI so a run is driven by the
// engine it selected — not the old hard-coded opus/max.
func TestAgentArgsCarryTuning(t *testing.T) {
	t.Run("buffered", func(t *testing.T) {
		args := agentArgs("do it", "plan", tuningFor(runs.Run{Model: "sonnet", Effort: "low"}))
		assertFlag(t, args, "--model", "sonnet")
		assertFlag(t, args, "--effort", "low")
	})
	t.Run("streaming", func(t *testing.T) {
		args := streamAgentArgs("do it", "bypassPermissions", tuningFor(runs.Run{Model: "sonnet", Effort: "low"}))
		assertFlag(t, args, "--model", "sonnet")
		assertFlag(t, args, "--effort", "low")
	})
	t.Run("default run keeps opus/max", func(t *testing.T) {
		args := agentArgs("do it", "plan", tuningFor(runs.Run{}))
		assertFlag(t, args, "--model", "opus")
		assertFlag(t, args, "--effort", "max")
	})
}

// assertFlag checks that args contains flag immediately followed by want.
func assertFlag(t *testing.T, args []string, flag, want string) {
	t.Helper()
	for i, a := range args {
		if a == flag {
			if i+1 >= len(args) || args[i+1] != want {
				t.Fatalf("%s = %q, want %q", flag, valueAfter(args, i), want)
			}
			return
		}
	}
	t.Fatalf("flag %s not present in %v", flag, args)
}

func valueAfter(args []string, i int) string {
	if i+1 < len(args) {
		return args[i+1]
	}
	return ""
}

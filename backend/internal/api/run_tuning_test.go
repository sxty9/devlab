package api

import (
	"strings"
	"testing"

	"devlab/backend/internal/runs"
)

// A run/todo may carry an optional model + effort + time budget; all are guarded and trimmed. Empty is
// always fine (it selects the runner/service default), "ultracode" is accepted as the maximal effort
// tier, and anything outside the ladder or an arg-smuggling model string is rejected.
func TestValidateTuning(t *testing.T) {
	cases := []struct {
		name       string
		model      string
		effort     string
		budget     string
		wantOK     bool
		wantModel  string // trimmed value after a successful validate
		wantEffort string
		wantBudget string
	}{
		{name: "all empty", model: "", effort: "", budget: "", wantOK: true},
		{name: "alias model + max", model: "opus", effort: "max", wantOK: true, wantModel: "opus", wantEffort: "max"},
		{name: "full id + ultracode", model: "claude-opus-4-8", effort: "ultracode", wantOK: true, wantModel: "claude-opus-4-8", wantEffort: "ultracode"},
		{name: "fable id", model: "claude-fable-5", effort: "low", wantOK: true, wantModel: "claude-fable-5", wantEffort: "low"},
		{name: "trims surrounding space", model: "  sonnet  ", effort: "  high  ", budget: "  3h  ", wantOK: true, wantModel: "sonnet", wantEffort: "high", wantBudget: "3h"},
		{name: "explicit no budget", budget: "0", wantOK: true, wantBudget: "0"},
		{name: "duration budget", budget: "90m", wantOK: true, wantBudget: "90m"},
		{name: "unknown effort", model: "opus", effort: "turbo", wantOK: false},
		{name: "effort with spaces smuggled", model: "opus", effort: "max max", wantOK: false},
		{name: "model with a flag", model: "opus --dangerously", effort: "", wantOK: false},
		{name: "model with a slash", model: "../etc", effort: "", wantOK: false},
		{name: "budget not a duration", budget: "soon", wantOK: false},
		{name: "negative budget", budget: "-1h", wantOK: false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			b := &runBody{Model: c.model, Effort: c.effort, TimeBudget: c.budget}
			code, _ := validateTuning(b)
			if (code == 0) != c.wantOK {
				t.Fatalf("validateTuning(model=%q effort=%q budget=%q) code=%d, wantOK=%v", c.model, c.effort, c.budget, code, c.wantOK)
			}
			if c.wantOK {
				if b.Model != c.wantModel || b.Effort != c.wantEffort || b.TimeBudget != c.wantBudget {
					t.Fatalf("after validate: model=%q effort=%q budget=%q, want model=%q effort=%q budget=%q",
						b.Model, b.Effort, b.TimeBudget, c.wantModel, c.wantEffort, c.wantBudget)
				}
			}
		})
	}
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

package api

import (
	"context"
	"errors"
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

// The time budget is the third tunable: an optional per-repo cap that is trimmed and canonicalized like
// its siblings. Empty is the service default, a Go duration caps the pass, every no-cap spelling folds to
// the canonical "off", and an unparseable or non-positive value is rejected (never silently defaulted).
func TestValidateTimeBudget(t *testing.T) {
	cases := []struct {
		name       string
		in         string
		wantOK     bool
		wantBudget string // canonical value after a successful validate
	}{
		{name: "empty is the default", in: "", wantOK: true, wantBudget: ""},
		{name: "duration in hours", in: "2h", wantOK: true, wantBudget: "2h"},
		{name: "duration in minutes", in: "90m", wantOK: true, wantBudget: "90m"},
		{name: "compound duration", in: "1h30m", wantOK: true, wantBudget: "1h30m"},
		{name: "trims surrounding space", in: "  3h  ", wantOK: true, wantBudget: "3h"},
		{name: "off is no-cap", in: "off", wantOK: true, wantBudget: "off"},
		{name: "none folds to off", in: "none", wantOK: true, wantBudget: "off"},
		{name: "zero folds to off", in: "0", wantOK: true, wantBudget: "off"},
		{name: "zero seconds folds to off", in: "0s", wantOK: true, wantBudget: "off"},
		{name: "off is case-insensitive", in: "OFF", wantOK: true, wantBudget: "off"},
		{name: "negative rejected", in: "-2h", wantOK: false},
		{name: "unparseable rejected", in: "soon", wantOK: false},
		{name: "smuggled flag rejected", in: "2h --dangerously", wantOK: false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			b := &runBody{TimeBudget: c.in}
			code, _ := validateTuning(b)
			if (code == 0) != c.wantOK {
				t.Fatalf("validateTuning(timeBudget=%q) code=%d, wantOK=%v", c.in, code, c.wantOK)
			}
			if c.wantOK && b.TimeBudget != c.wantBudget {
				t.Fatalf("after validate: timeBudget=%q, want %q", b.TimeBudget, c.wantBudget)
			}
		})
	}
}

// budgetFor resolves a run's (possibly empty) TimeBudget into the concrete per-repo cap: an explicit
// duration passes through, every no-cap spelling resolves to 0 (no cap), and anything unparseable falls
// back to the service default rather than running unbounded.
func TestBudgetFor(t *testing.T) {
	t.Setenv("DEVLAB_RUNS_AGENT_TIMEOUT", "3h") // pin the service default for the fallback cases
	cases := []struct {
		name   string
		budget string
		want   time.Duration
	}{
		{name: "explicit hours", budget: "2h", want: 2 * time.Hour},
		{name: "explicit minutes", budget: "90m", want: 90 * time.Minute},
		{name: "off is no cap", budget: "off", want: 0},
		{name: "none is no cap", budget: "none", want: 0},
		{name: "0 is no cap", budget: "0", want: 0},
		{name: "0s is no cap", budget: "0s", want: 0},
		{name: "garbage falls back to the default", budget: "soon", want: 3 * time.Hour},
		{name: "empty follows the default", budget: "", want: 3 * time.Hour},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := budgetFor(runs.Run{TimeBudget: c.budget}); got != c.want {
				t.Fatalf("budgetFor(%q) = %v, want %v", c.budget, got, c.want)
			}
		})
	}
}

// referenced-not-copied: an empty budget tracks the service default at RUN TIME (so moving the default
// moves every un-chosen run at once), while an explicit choice is pinned and survives a default change.
func TestBudgetForReferencedNotCopied(t *testing.T) {
	t.Setenv("DEVLAB_RUNS_AGENT_TIMEOUT", "2h")
	if got := budgetFor(runs.Run{}); got != 2*time.Hour {
		t.Fatalf("un-chosen budget = %v, want to follow the 2h default", got)
	}
	if got := budgetFor(runs.Run{TimeBudget: "5h"}); got != 5*time.Hour {
		t.Fatalf("explicit 5h = %v, want pinned 5h", got)
	}
	t.Setenv("DEVLAB_RUNS_AGENT_TIMEOUT", "4h") // the default moves…
	if got := budgetFor(runs.Run{}); got != 4*time.Hour {
		t.Fatalf("un-chosen budget after default change = %v, want to follow the new 4h default", got)
	}
	if got := budgetFor(runs.Run{TimeBudget: "5h"}); got != 5*time.Hour {
		t.Fatalf("explicit 5h after default change = %v, want still-pinned 5h", got)
	}
}

// humanizeDuration is the single formatter behind the stamped Result.TimeBudget and the honest overrun
// message, so a compact duration and the no-cap "off" render identically wherever the budget is shown.
func TestHumanizeDuration(t *testing.T) {
	cases := []struct {
		d    time.Duration
		want string
	}{
		{3 * time.Hour, "3h"},
		{2 * time.Hour, "2h"},
		{90 * time.Minute, "1h30m"},
		{45 * time.Minute, "45m"},
		{0, "off"},
		{-time.Hour, "off"},
	}
	for _, c := range cases {
		if got := humanizeDuration(c.d); got != c.want {
			t.Errorf("humanizeDuration(%v) = %q, want %q", c.d, got, c.want)
		}
	}
}

// A budget overrun is reported honestly — named as the spent budget with the value that applied — while a
// deliberate abort or any other cancel keeps the underlying error, so the two are never conflated.
func TestBudgetAwareError(t *testing.T) {
	base := errors.New("signal: killed")
	t.Run("budget overrun is named with its value", func(t *testing.T) {
		actx, cancel := context.WithTimeoutCause(context.Background(), -time.Second, errBudgetExceeded)
		defer cancel()
		<-actx.Done()
		if got := budgetAwareError(actx, 3*time.Hour, base); got != "Zeitbudget überschritten (3h)" {
			t.Fatalf("overrun = %q, want the honest Zeitbudget message", got)
		}
	})
	t.Run("a deliberate abort keeps the raw error", func(t *testing.T) {
		actx, cancel := context.WithCancelCause(context.Background())
		cancel(runs.ErrRunAborted)
		if got := budgetAwareError(actx, 3*time.Hour, base); got != base.Error() {
			t.Fatalf("abort = %q, want the raw error %q", got, base.Error())
		}
	})
	t.Run("a plain (non-context) error passes through", func(t *testing.T) {
		if got := budgetAwareError(context.Background(), 3*time.Hour, base); got != base.Error() {
			t.Fatalf("plain error = %q, want %q", got, base.Error())
		}
	})
}

// A time-budget overrun must KEEP the streamed transcript on the step — "what was reached until then"
// (req 4) — headed as partial progress, instead of clobbering it with the raw kill error. The honest
// "Zeitbudget überschritten" naming lives on the repo-level Error, so it is deliberately not repeated here.
func TestAgentStepFailBudgetKeepsTranscript(t *testing.T) {
	rr := &runs.RepoResult{Steps: []runs.Step{{Name: "implement", Running: true}}}
	ag := &agentStep{rr: rr, saver: &liveSaver{do: func() {}}, idx: 0}
	// stream what the live agent would have emitted before the cap fired
	ag.tr.push([]byte(`{"type":"assistant","message":{"content":[{"type":"text","text":"scaffolded the service"}]}}`))
	ag.tr.push([]byte(`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Edit","input":{"file_path":"main.go"}}]}}`))

	ag.failBudget()

	s := rr.Steps[0]
	if s.Running || s.Status != runs.StepFailed {
		t.Fatalf("failBudget: running=%v status=%v, want stopped + failed", s.Running, s.Status)
	}
	if !strings.Contains(s.Log, "scaffolded the service") || !strings.Contains(s.Log, "main.go") {
		t.Fatalf("failBudget dropped the partial transcript: %q", s.Log)
	}
	if !strings.Contains(s.Log, "Bis dahin erreicht") {
		t.Fatalf("failBudget must head the transcript as partial progress: %q", s.Log)
	}
	// It must NOT leak the raw kill error — that technical text is exactly what req 4 forbids surfacing.
	if strings.Contains(strings.ToLower(s.Log), "signal: killed") || strings.Contains(strings.ToLower(s.Log), "agent run failed") {
		t.Fatalf("failBudget leaked a raw kill error: %q", s.Log)
	}
}

// With nothing streamed before the cap fired, failBudget leaves the step log empty — the repo-level overrun
// banner alone then states it, and the step shows no misleading content (never a raw deadline error).
func TestAgentStepFailBudgetEmptyTranscript(t *testing.T) {
	rr := &runs.RepoResult{Steps: []runs.Step{{Name: "implement", Running: true}}}
	ag := &agentStep{rr: rr, saver: &liveSaver{do: func() {}}, idx: 0}
	ag.failBudget()
	s := rr.Steps[0]
	if s.Running || s.Status != runs.StepFailed {
		t.Fatalf("failBudget(empty): running=%v status=%v, want stopped + failed", s.Running, s.Status)
	}
	if s.Log != "" {
		t.Fatalf("failBudget(empty) log = %q, want empty", s.Log)
	}
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

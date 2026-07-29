package api

// The agentic path runs the user's own claude CLI in their workspace; what DevLab owns is how it
// reads that run's self-report: the answer, the session to continue with, the model that served
// it (labeling duty, D 26) and the tokens it consumed (the ONE usage pool, cross-cutting 5).

import (
	"strings"
	"testing"

	"devlab/backend/internal/telemetry"
)

// TestParseClaudeResultReportsModelAndUsage: model and tokens come from the run's own report —
// the per-model breakdown wins over the single `model` field because a run may hand off between
// models, and its numbers are the ones actually consumed.
func TestParseClaudeResultReportsModelAndUsage(t *testing.T) {
	out := []byte(`{
	  "result": "  did the thing  ",
	  "session_id": "sess-42",
	  "is_error": false,
	  "total_cost_usd": 0.42,
	  "num_turns": 3,
	  "model": "claude-opus-4-8",
	  "usage": {"input_tokens": 10, "output_tokens": 5},
	  "modelUsage": {
	    "claude-opus-4-8": {"inputTokens": 800, "outputTokens": 120},
	    "claude-haiku-4-8": {"inputTokens": 200, "outputTokens": 30}
	  }
	}`)
	reply, meta := parseClaudeResult(out)
	if reply.Output != "did the thing" {
		t.Errorf("output = %q", reply.Output)
	}
	if reply.SessionID != "sess-42" || reply.NumTurns != 3 || reply.CostUSD != 0.42 || reply.IsError {
		t.Errorf("reply = %+v", reply)
	}
	if meta.Model != "claude-haiku-4-8, claude-opus-4-8" {
		t.Errorf("model label = %q, want every model that served the run, in a stable order", meta.Model)
	}
	if meta.In != 1000 || meta.Out != 150 {
		t.Errorf("usage = in %d out %d, want the per-model totals", meta.In, meta.Out)
	}
}

// TestParseClaudeResultFallsBackHonestly: without a per-model breakdown the single reported model
// and the plain usage block are used; a silent run says so instead of returning an empty answer.
func TestParseClaudeResultFallsBackHonestly(t *testing.T) {
	reply, meta := parseClaudeResult([]byte(`{"result":"","model":"claude-fable-5","usage":{"input_tokens":7,"output_tokens":2}}`))
	if !strings.Contains(reply.Output, "no textual output") {
		t.Errorf("a silent run must say so, got %q", reply.Output)
	}
	if meta.Model != "claude-fable-5" || meta.In != 7 || meta.Out != 2 {
		t.Errorf("meta = %+v", meta)
	}

	// Garbage in: nothing is invented — no model, no tokens.
	_, meta = parseClaudeResult([]byte("not json"))
	if meta.Model != "" || meta.In != 0 || meta.Out != 0 {
		t.Errorf("unparsable output must yield no claims, got %+v", meta)
	}
}

// TestAgentModes: the three offered modes map onto the CLI's permission modes; anything else
// falls back to the edit-accepting default rather than to autonomy.
func TestAgentModes(t *testing.T) {
	want := map[string]string{"plan": "plan", "auto": "acceptEdits", "full": "bypassPermissions"}
	for in, out := range want {
		if agentModeMap[in] != out {
			t.Errorf("mode %q maps to %q, want %q", in, agentModeMap[in], out)
		}
	}
	if agentModeMap["bypassPermissions"] != "" {
		t.Error("a raw CLI mode must not be selectable from the request")
	}
}

// TestAgentUsageIsRecordedInTheOnePool: the agent's consumption is recorded through the same
// entry as the assistant's, with the model it reported.
func TestAgentUsageIsRecordedInTheOnePool(t *testing.T) {
	s, repo := devBypassServer(t)
	_, meta := parseClaudeResult([]byte(`{"result":"ok","modelUsage":{"claude-opus-4-8":{"inputTokens":300,"outputTokens":40}}}`))
	s.recordAiUsage(telemetry.UsageSample{
		Source: usageSourceAgent,
		User:   "dev",
		Repo:   repo,
		Model:  answerModel(meta.Model, "claude-opus-4-8"),
		In:     meta.In,
		Out:    meta.Out,
	})
	assertUsageSample(t, s, usageSourceAgent, "claude-opus-4-8", 300, 40)
}

// TestAgentErrorsAreActionable: a CLI failure becomes a sentence the user can act on, clipped.
func TestAgentErrorsAreActionable(t *testing.T) {
	cases := map[string]string{
		"claude: command not installed": "isn't installed",
		"invalid api key provided":      "isn't signed in",
		strings.Repeat("boom ", 200):    "Agent run failed",
	}
	for in, want := range cases {
		got := agentError(errString(in))
		if !strings.Contains(got, want) {
			t.Errorf("agentError(%.20q) = %q, want it to contain %q", in, got, want)
		}
		if len(got) > 340 { // 300 clipped bytes + the ellipsis + the lead-in sentence
			t.Errorf("agentError must clip verbose failures, got %d bytes", len(got))
		}
	}
}

type errString string

func (e errString) Error() string { return string(e) }

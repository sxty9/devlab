package executor

import (
	"strings"
	"testing"

	"devlab/backend/internal/model"
)

// F7: live usage is deduped by message id (each turn recurs once per content block with the
// SAME id and usage — summing raw events would multi-count).
func TestCompactStreamDedupesUsageByMessageID(t *testing.T) {
	in := script(
		assistantEvent("m1", "part one", 100, 10),
		assistantEvent("m1", "part two", 100, 10), // same turn, repeated event
		assistantEvent("m2", "next turn", 150, 20),
	)
	var climbs []model.UsageView
	out, err := compactStreamFull(strings.NewReader(in), streamEmit{
		usage: func(u model.UsageView, _ int) { climbs = append(climbs, u) },
	})
	if err != nil {
		t.Fatal(err)
	}
	// m1 counted once (100/10), then m1+m2 (250/30) — never 200/20.
	if len(climbs) != 2 {
		t.Fatalf("climbs = %d, want 2 (%+v)", len(climbs), climbs)
	}
	if climbs[0].InputTokens != 100 || climbs[1].InputTokens != 250 || climbs[1].OutputTokens != 30 {
		t.Fatalf("dedupe broken: %+v", climbs)
	}
	// No final result event: the live estimate is the honest figure.
	if out.SawResult || out.Usage.InputTokens != 250 {
		t.Fatalf("kill fallback wrong: %+v", out)
	}
}

// The final result event is authoritative: usage (incl. cache reads), cost, report text.
func TestCompactStreamFinalResultWins(t *testing.T) {
	in := script(
		assistantEvent("m1", "working", 10, 5),
		`{"type":"result","subtype":"success","is_error":false,"result":"the report","num_turns":4,"total_cost_usd":0.5,"usage":{"input_tokens":100,"output_tokens":40,"cache_read_input_tokens":900}}`,
	)
	out, err := compactStreamFull(strings.NewReader(in), streamEmit{})
	if err != nil {
		t.Fatal(err)
	}
	if !out.SawResult || out.ResultText != "the report" || out.IsError {
		t.Fatalf("final result lost: %+v", out)
	}
	if out.Usage.InputTokens != 1000 || out.Usage.OutputTokens != 40 || out.Usage.CostUSD != 0.5 {
		t.Fatalf("authoritative usage wrong: %+v", out.Usage)
	}
	if out.Turns != 4 {
		t.Fatalf("turns = %d", out.Turns)
	}
	if out.RawFinal == nil || !strings.Contains(string(out.RawFinal), `"type":"result"`) {
		t.Fatalf("raw final line lost")
	}
}

// F11: the transcript is compact (assistant text + tool lines), streams line by line, and its
// tail survives however the invocation ends.
func TestCompactStreamTranscript(t *testing.T) {
	in := script(
		`{"type":"system","subtype":"init"}`, // bookkeeping — invisible
		assistantEvent("m1", "let me look", 10, 2),
		toolEvent("m2", "Bash", "go build ./...", 20, 4),
		`not json at all`,                                                              // tolerated
		`{"type":"user","message":{"content":[{"type":"tool_result","content":"…"}]}}`, // invisible
	)
	var lines []string
	out, err := compactStreamFull(strings.NewReader(in), streamEmit{
		transcript: func(text string) { lines = append(lines, text) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 2 {
		t.Fatalf("visible lines = %d (%q)", len(lines), lines)
	}
	if lines[0] != "let me look" || lines[1] != "→ Bash go build ./..." {
		t.Fatalf("compaction wrong: %q", lines)
	}
	if !strings.Contains(out.TranscriptTail, "let me look") || !strings.Contains(out.TranscriptTail, "→ Bash") {
		t.Fatalf("tail incomplete: %q", out.TranscriptTail)
	}
}

// The public CompactStream contract: transcript into the sink as JSON journal lines, usage
// climbing, settled view returned.
func TestCompactStreamPublicContract(t *testing.T) {
	sink := newFakeSink()
	in := script(
		assistantEvent("m1", "hello", 10, 5),
		resultEventLine("done", false, 50, 20, 0.1),
	)
	u, err := CompactStream(strings.NewReader(in), sink, "org/app")
	if err != nil {
		t.Fatal(err)
	}
	if u.InputTokens != 50 || u.OutputTokens != 20 || u.CostUSD != 0.1 {
		t.Fatalf("settled usage wrong: %+v", u)
	}
	if len(sink.transcripts["org/app"]) != 1 {
		t.Fatalf("transcript lines = %d", len(sink.transcripts["org/app"]))
	}
	// Journal lines are self-contained JSON.
	if line := sink.transcripts["org/app"][0]; !strings.Contains(line, `"text":"hello"`) || !strings.HasPrefix(line, "{") {
		t.Fatalf("journal line not JSON: %q", line)
	}
	if len(sink.usages) == 0 {
		t.Fatalf("no live usage")
	}
}

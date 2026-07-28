package api

import (
	"fmt"
	"strings"
	"testing"

	"devlab/backend/internal/mercury"
	"devlab/backend/internal/runs"
)

// A representative stream-json (NDJSON) transcript from the claude CLI: init, assistant text, a tool
// use, a tool result, then the final result summary carrying cost/usage/output.
const streamNDJSON = `{"type":"system","subtype":"init","session_id":"s1","tools":["Bash","Edit"],"model":"opus"}
{"type":"assistant","message":{"content":[{"type":"text","text":"Let me inspect the failing test."}]},"session_id":"s1"}
{"type":"assistant","message":{"content":[{"type":"tool_use","id":"t1","name":"Bash","input":{"command":"go test ./...\nsecond line"}}]},"session_id":"s1"}
{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"t1","content":"FAIL"}]}}
{"type":"assistant","message":{"content":[{"type":"tool_use","id":"t2","name":"Edit","input":{"file_path":"backend/internal/runs/run.go"}}]},"session_id":"s1"}
{"type":"result","subtype":"success","is_error":false,"result":"Fixed the bug in run.go.","session_id":"s1","total_cost_usd":0.1234,"num_turns":7,"usage":{"input_tokens":100,"output_tokens":50,"cache_creation_input_tokens":10,"cache_read_input_tokens":5}}`

// The buffered (--output-format json) shape: a single object identical to the stream's result event.
const bufferedJSON = `{"type":"result","subtype":"success","is_error":false,"result":"Fixed the bug in run.go.","session_id":"s1","total_cost_usd":0.1234,"num_turns":7,"usage":{"input_tokens":100,"output_tokens":50,"cache_creation_input_tokens":10,"cache_read_input_tokens":5}}`

func TestResultEventExtractsFinalFromStream(t *testing.T) {
	final := resultEvent([]byte(streamNDJSON))
	// The money path parsers must see cost/usage/output exactly as in the buffered case.
	u := parseClaudeUsage(final)
	if u.cost != 0.1234 || u.turns != 7 {
		t.Fatalf("usage cost/turns wrong: %+v", u)
	}
	if u.in != 115 || u.out != 50 { // 100 + 10 + 5 cache, 50 out
		t.Fatalf("usage tokens wrong: in=%d out=%d", u.in, u.out)
	}
	if got := parseClaudeResult(final).Output; got != "Fixed the bug in run.go." {
		t.Fatalf("result output wrong: %q", got)
	}
}

func TestResultEventPassesBufferedThrough(t *testing.T) {
	final := resultEvent([]byte(bufferedJSON))
	u := parseClaudeUsage(final)
	if u.cost != 0.1234 || u.in != 115 || u.out != 50 {
		t.Fatalf("buffered usage wrong: %+v", u)
	}
	if got := parseClaudeResult(final).Output; got != "Fixed the bug in run.go." {
		t.Fatalf("buffered output wrong: %q", got)
	}
}

func TestResultEventFallsBackWhenNoResultLine(t *testing.T) {
	// A stream that died before the result summary → keep the whole output so error scanning still works.
	partial := `{"type":"system","subtype":"init"}
{"type":"assistant","message":{"content":[{"type":"text","text":"hi"}]}}`
	if got := string(resultEvent([]byte(partial))); got != partial {
		t.Fatalf("expected whole output back, got %q", got)
	}
}

// The usage-limit detector must still fire on a STREAM whose final result event flags the limit — proof
// that extracting the result line before DetectUsageLimit keeps the suspend/resume machinery working
// under streaming (a raw NDJSON blob would not json.Unmarshal and the limit would be missed).
func TestStreamUsageLimitStillDetected(t *testing.T) {
	stream := `{"type":"system","subtype":"init"}
{"type":"assistant","message":{"content":[{"type":"text","text":"working"}]}}
{"type":"result","subtype":"error","is_error":true,"result":"Claude usage limit reached|1799999999","total_cost_usd":0}`
	final := resultEvent([]byte(stream))
	limited, resetAt, hasReset := mercury.DetectUsageLimit(final, nil)
	if !limited {
		t.Fatal("expected usage limit to be detected from the stream's result event")
	}
	if !hasReset || resetAt.IsZero() {
		t.Fatalf("expected a parsed reset time, got hasReset=%v at=%v", hasReset, resetAt)
	}
}

func TestTranscriptRendersTextAndTools(t *testing.T) {
	var tr transcript
	changed := 0
	for _, line := range strings.Split(streamNDJSON, "\n") {
		if tr.push([]byte(line)) {
			changed++
		}
	}
	out := tr.clipped()
	if !strings.Contains(out, "Let me inspect the failing test.") {
		t.Errorf("assistant text missing:\n%s", out)
	}
	if !strings.Contains(out, "→ Bash go test ./... …") { // multi-line command collapsed to its first line
		t.Errorf("tool_use line missing/incorrect:\n%s", out)
	}
	if !strings.Contains(out, "→ Edit backend/internal/runs/run.go") {
		t.Errorf("edit tool line missing:\n%s", out)
	}
	// system, tool_result and the final result event contribute nothing to the transcript.
	if changed != 3 {
		t.Errorf("expected 3 visible events (2 tool_use + 1 text), got %d", changed)
	}
}

// assistantUsageLine renders one stream-json assistant event carrying token usage — the shape the runner
// folds live. input adds up as input_tokens + cache_creation + cache_read (mirrors parseClaudeUsage).
func assistantUsageLine(id string, in, cacheCreate, cacheRead, out int) []byte {
	return []byte(fmt.Sprintf(
		`{"type":"assistant","message":{"id":%q,"usage":{"input_tokens":%d,"cache_creation_input_tokens":%d,"cache_read_input_tokens":%d,"output_tokens":%d}}}`,
		id, in, cacheCreate, cacheRead, out))
}

// Live token tracking must (1) climb the repo counters as assistant events stream in, (2) dedupe by
// message id — the claude CLI repeats one turn's usage once per content block — and (3) settle to the
// authoritative final result event without double-counting the live estimate. This pins all three.
func TestAgentStepFoldsLiveUsageDedupedThenSettles(t *testing.T) {
	rr := &runs.RepoResult{Repo: "r"}
	ag := beginAgentStep(rr, &liveSaver{do: func() {}}, "implement")

	// Turn 1, two content blocks → same message id, identical usage. Fold ONCE (in = 10+100+200 = 310).
	if !ag.foldUsage(assistantUsageLine("m1", 10, 100, 200, 4)) {
		t.Fatal("first assistant event should move the live totals")
	}
	if ag.foldUsage(assistantUsageLine("m1", 10, 100, 200, 4)) {
		t.Fatal("a repeated message id must NOT move the totals again (dedupe by id)")
	}
	if rr.InputTokens != 310 || rr.OutputTokens != 4 || rr.NumTurns != 1 {
		t.Fatalf("turn 1 folded wrong: in=%d out=%d turns=%d", rr.InputTokens, rr.OutputTokens, rr.NumTurns)
	}

	// Turn 2, a new message id → summed on top (in = 20+0+300 = 320).
	if !ag.foldUsage(assistantUsageLine("m2", 20, 0, 300, 7)) {
		t.Fatal("a new message id should move the live totals")
	}
	if rr.InputTokens != 630 || rr.OutputTokens != 11 || rr.NumTurns != 2 {
		t.Fatalf("turn 2 folded wrong: in=%d out=%d turns=%d", rr.InputTokens, rr.OutputTokens, rr.NumTurns)
	}

	// Non-usage lines (a tool result, the init event) are ignored.
	if ag.foldUsage([]byte(`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"t1"}]}}`)) {
		t.Fatal("a non-assistant line must not move the totals")
	}

	// Settle to the authoritative result event: it REPLACES the running estimate (no double count) and
	// brings in cost, which the per-turn stream never carries. Fixture: in=115, out=50, cost=0.1234, turns=7.
	ag.settle(resultEvent([]byte(streamNDJSON)))
	if rr.InputTokens != 115 || rr.OutputTokens != 50 || rr.NumTurns != 7 {
		t.Fatalf("settle must replace the live estimate, not add to it: in=%d out=%d turns=%d", rr.InputTokens, rr.OutputTokens, rr.NumTurns)
	}
	if rr.CostUSD != 0.1234 {
		t.Fatalf("settle must bring in the final cost: %v", rr.CostUSD)
	}
}

func TestTranscriptClipsToRecentWindow(t *testing.T) {
	var tr transcript
	long := strings.Repeat("x", 40000)
	tr.push([]byte(`{"type":"assistant","message":{"content":[{"type":"text","text":"` + long + `"}]}}`))
	got := tr.clipped()
	if len([]rune(got)) > 16000+2 { // window + the "…\n" marker
		t.Fatalf("transcript not clipped: %d runes", len([]rune(got)))
	}
	if !strings.HasPrefix(got, "…") {
		t.Errorf("expected a clip marker prefix")
	}
}

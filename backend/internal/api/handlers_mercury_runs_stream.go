package api

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
)

// Live-follow plumbing for the autonomous runner. To let a run be watched "as if in a Claude Code
// session", the executor drives the claude CLI in --output-format stream-json (NDJSON, one event per
// line) instead of the buffered --output-format json, and turns those events into a compact transcript
// that streams into the running step's log. The FINAL result event of the stream is the very object the
// buffered path produced, so resultEvent() extracts it and the existing parsers (parseClaudeResult /
// parseClaudeUsage / mercury.DetectUsageLimit) keep working unchanged — the money path is untouched.
//
// Streaming is the default; DEVLAB_RUNS_STREAM=off reverts to the buffered call as a runtime safety
// valve (no redeploy) should a CLI version ever mishandle stream-json.

// streamEnabled reports whether the runner streams the agent (default on).
func streamEnabled() bool {
	return !strings.EqualFold(strings.TrimSpace(os.Getenv("DEVLAB_RUNS_STREAM")), "off")
}

// streamAgentArgs is agentArgs in streaming form: stream-json needs --verbose in -p mode so every event
// is emitted as it happens. Everything else matches agentArgs so behaviour is identical bar the wire
// format.
func streamAgentArgs(prompt, mode string) []string {
	return []string{
		"-p", prompt,
		"--output-format", "stream-json",
		"--verbose",
		"--permission-mode", mode,
		"--model", "opus",
		"--effort", "max",
		"--append-system-prompt", runnerPreamble,
	}
}

// resultEvent returns the final Claude "result" event from an agent's stdout, so the buffered parsers
// can consume it regardless of output format:
//   - buffered (--output-format json): a single JSON object → returned unchanged.
//   - streaming (--output-format stream-json): NDJSON whose last {"type":"result"} line is the summary
//     (cost, usage, final text) → that line is returned.
//
// Falls back to the whole output when no result line is present (a hard failure before the summary), so
// DetectUsageLimit still scans whatever the agent emitted.
func resultEvent(out []byte) []byte {
	trimmed := bytes.TrimSpace(out)
	if len(trimmed) == 0 {
		return out
	}
	if bytes.IndexByte(trimmed, '\n') < 0 {
		return trimmed // single line → buffered object (or a lone event)
	}
	var found []byte
	for _, line := range bytes.Split(trimmed, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 || line[0] != '{' {
			continue
		}
		var probe struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(line, &probe) == nil && probe.Type == "result" {
			found = line // keep scanning — the LAST result line wins
		}
	}
	if found != nil {
		return found
	}
	return out
}

// transcript accumulates a compact, human-readable narrative of a streaming agent run — the assistant's
// text and the tools it invokes — for display in the live step log. Tool results and bookkeeping events
// are omitted to keep it readable and portioned; the whole thing is clipped to a recent window.
type transcript struct {
	b strings.Builder
}

// push folds one NDJSON stream-json line into the transcript. It returns true when the line added
// something visible, so the caller only re-saves the live result on real progress.
func (t *transcript) push(line []byte) bool {
	line = bytes.TrimSpace(line)
	if len(line) == 0 || line[0] != '{' {
		return false
	}
	var ev struct {
		Type    string `json:"type"`
		Message struct {
			Content []struct {
				Type  string          `json:"type"`
				Text  string          `json:"text"`
				Name  string          `json:"name"`
				Input json.RawMessage `json:"input"`
			} `json:"content"`
		} `json:"message"`
	}
	if json.Unmarshal(line, &ev) != nil || ev.Type != "assistant" {
		return false
	}
	changed := false
	for _, c := range ev.Message.Content {
		switch c.Type {
		case "text":
			if s := strings.TrimSpace(c.Text); s != "" {
				t.writeBlock(s)
				changed = true
			}
		case "tool_use":
			t.writeBlock("→ " + toolLine(c.Name, c.Input))
			changed = true
		}
	}
	return changed
}

func (t *transcript) writeBlock(s string) {
	if t.b.Len() > 0 {
		t.b.WriteString("\n\n")
	}
	t.b.WriteString(s)
}

// clipped returns the transcript bounded to a recent window (keep the tail — that is where the action
// is), so the live result document stays portioned however long the agent talks.
func (t *transcript) clipped() string {
	const max = 16000
	r := []rune(t.b.String())
	if len(r) > max {
		return "…\n" + string(r[len(r)-max:])
	}
	return string(r)
}

// toolLine renders a tool invocation as "Name <most-telling-argument>", e.g. "Bash go test ./…" or
// "Edit backend/internal/runs/run.go" — the one field that says what the agent is doing.
func toolLine(name string, input json.RawMessage) string {
	var m map[string]any
	_ = json.Unmarshal(input, &m)
	for _, k := range []string{"command", "file_path", "path", "pattern", "url", "query", "description", "prompt"} {
		if v, ok := m[k].(string); ok && strings.TrimSpace(v) != "" {
			return name + " " + oneLine(v, 160)
		}
	}
	return name
}

// oneLine collapses s to a single clipped line for a compact tool summary.
func oneLine(s string, max int) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = strings.TrimSpace(s[:i]) + " …"
	}
	r := []rune(s)
	if len(r) > max {
		return string(r[:max]) + "…"
	}
	return string(r)
}

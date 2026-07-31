// Stream compaction (F7/F11, ported from the retired stream handler): the agent runs in
// --output-format stream-json (NDJSON, one event per line); this file turns those events into
//   - a compact, human-readable transcript that reaches the sink LINE BY LINE as it happens —
//     so the transcript survives a kill (F11), and
//   - live token counters that climb WHILE the agent works (deduped by message id), settled to
//     the authoritative figures of the final result event (F7).
package executor

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"strings"
	"time"

	"devlab/backend/internal/model"
)

// streamOutcome is one agent invocation's digest.
type streamOutcome struct {
	// Usage is the invocation's consumption: the authoritative final-result figures when the
	// stream carried them, else the live (deduped) estimate.
	Usage model.UsageView
	// Turns counts distinct assistant messages.
	Turns int
	// ResultText is the final result event's text (the agent's report).
	ResultText string
	// IsError is the final result event's is_error.
	IsError bool
	// SawResult reports whether a final result event arrived at all (false on a kill).
	SawResult bool
	// RawFinal is the raw final result event line (input for mercury.DetectUsageLimit); nil
	// when none arrived.
	RawFinal []byte
	// TranscriptTail is the clipped compact transcript — the honest result of a failed or
	// killed agent step (F11).
	TranscriptTail string
}

// streamEmit receives live progress: each visible transcript block and every climb of the
// live token counters (invocation-relative).
type streamEmit struct {
	transcript func(text string)
	usage      func(u model.UsageView, turns int)
}

// CompactStream compacts the agent's stream-json into transcript lines and live usage
// (F7/F11): every visible block reaches the sink immediately (as a JSON journal line), so the
// transcript survives a kill; the returned view is the invocation's settled consumption.
func CompactStream(r io.Reader, sink Sink, repo string) (model.UsageView, error) {
	out, err := compactStreamFull(r, streamEmit{
		transcript: func(text string) { sink.Transcript(repo, transcriptLine(text)) },
		usage:      func(u model.UsageView, _ int) { sink.Usage(u) },
	})
	return out.Usage, err
}

// transcriptLine renders one visible block as a self-contained JSON journal line.
func transcriptLine(text string) []byte {
	b, _ := json.Marshal(struct {
		At   string `json:"at"`
		Text string `json:"text"`
	}{At: time.Now().UTC().Format(time.RFC3339), Text: text})
	return b
}

// compactStreamFull is the full digest behind CompactStream. It never fails the invocation on
// a malformed line (tolerant reader); the only error is the reader's own.
func compactStreamFull(r io.Reader, emit streamEmit) (streamOutcome, error) {
	var out streamOutcome
	var tail transcriptBuf
	byMsg := map[string]tokenPair{}
	var live tokenPair

	br := bufio.NewReaderSize(r, 64<<10)
	var readErr error
	for {
		line, err := br.ReadBytes('\n')
		if len(line) > 0 {
			l := bytes.TrimSpace(line)
			if len(l) > 0 {
				// Transcript blocks (assistant text + tool invocations).
				for _, block := range visibleBlocks(l) {
					tail.push(block)
					if emit.transcript != nil {
						emit.transcript(block)
					}
				}
				// Live token climb, deduped by message id (each turn recurs once per content
				// block with identical usage — summing raw events would multi-count).
				if id, u, ok := streamUsage(l); ok {
					byMsg[id] = u
					var sum tokenPair
					for _, v := range byMsg {
						sum.in += v.in
						sum.out += v.out
					}
					if sum != live {
						live = sum
						if emit.usage != nil {
							emit.usage(model.UsageView{InputTokens: live.in, OutputTokens: live.out}, len(byMsg))
						}
					}
				}
				if fin, ok := finalResult(l); ok {
					out.SawResult = true
					out.RawFinal = append([]byte(nil), l...)
					out.ResultText = fin.Result
					out.IsError = fin.IsError
					out.Usage = model.UsageView{
						InputTokens:  fin.Usage.InputTokens + fin.Usage.CacheCreationInputTokens + fin.Usage.CacheReadInputTokens,
						OutputTokens: fin.Usage.OutputTokens,
						CostUSD:      fin.TotalCostUSD,
					}
					if fin.NumTurns > 0 {
						out.Turns = fin.NumTurns
					}
				}
			}
		}
		if err != nil {
			if err != io.EOF {
				readErr = err
			}
			break
		}
	}

	if !out.SawResult {
		// Killed before the summary: the live estimate is the honest figure (F7/F11).
		out.Usage = model.UsageView{InputTokens: live.in, OutputTokens: live.out}
	}
	if out.Turns == 0 {
		out.Turns = len(byMsg)
	}
	out.TranscriptTail = tail.clipped()
	return out, readErr
}

type tokenPair struct{ in, out int64 }

// visibleBlocks extracts the human-visible blocks of one stream-json line: assistant text and
// tool invocations ("→ Name most-telling-argument"). Bookkeeping events yield nothing.
func visibleBlocks(line []byte) []string {
	if len(line) == 0 || line[0] != '{' {
		return nil
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
		return nil
	}
	var blocks []string
	for _, c := range ev.Message.Content {
		switch c.Type {
		case "text":
			if s := strings.TrimSpace(c.Text); s != "" {
				blocks = append(blocks, s)
			}
		case "tool_use":
			blocks = append(blocks, "→ "+toolLine(c.Name, c.Input))
		}
	}
	return blocks
}

// streamUsage pulls one assistant event's message id and running token usage out of a
// stream-json line. Claude repeats the SAME message.id AND usage once per content block, so a
// live accumulator must dedupe by id — never sum raw events (ported F7 lesson).
func streamUsage(line []byte) (id string, u tokenPair, ok bool) {
	if len(line) == 0 || line[0] != '{' {
		return "", tokenPair{}, false
	}
	var ev struct {
		Type    string `json:"type"`
		Message struct {
			ID    string `json:"id"`
			Usage struct {
				InputTokens              int64 `json:"input_tokens"`
				OutputTokens             int64 `json:"output_tokens"`
				CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
				CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
			} `json:"usage"`
		} `json:"message"`
	}
	if json.Unmarshal(line, &ev) != nil || ev.Type != "assistant" || ev.Message.ID == "" {
		return "", tokenPair{}, false
	}
	u = tokenPair{
		in:  ev.Message.Usage.InputTokens + ev.Message.Usage.CacheCreationInputTokens + ev.Message.Usage.CacheReadInputTokens,
		out: ev.Message.Usage.OutputTokens,
	}
	if u.in == 0 && u.out == 0 {
		return "", tokenPair{}, false
	}
	return ev.Message.ID, u, true
}

// finalResultEvent is the summary line of a stream-json run — the same object the buffered
// output format produces.
type finalResultEvent struct {
	Type         string  `json:"type"`
	Subtype      string  `json:"subtype"`
	IsError      bool    `json:"is_error"`
	Result       string  `json:"result"`
	NumTurns     int     `json:"num_turns"`
	TotalCostUSD float64 `json:"total_cost_usd"`
	Usage        struct {
		InputTokens              int64 `json:"input_tokens"`
		OutputTokens             int64 `json:"output_tokens"`
		CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
		CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
	} `json:"usage"`
}

func finalResult(line []byte) (finalResultEvent, bool) {
	if len(line) == 0 || line[0] != '{' {
		return finalResultEvent{}, false
	}
	var fin finalResultEvent
	if json.Unmarshal(line, &fin) != nil || fin.Type != "result" {
		return finalResultEvent{}, false
	}
	return fin, true
}

// toolLine renders a tool invocation as "Name <most-telling-argument>" — the one field that
// says what the agent is doing (ported).
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

// transcriptBuf accumulates the compact transcript, clipped to a recent window.
type transcriptBuf struct{ b strings.Builder }

func (t *transcriptBuf) push(block string) {
	if t.b.Len() > 0 {
		t.b.WriteString("\n\n")
	}
	t.b.WriteString(block)
}

func (t *transcriptBuf) clipped() string { return clip(t.b.String()) }

// ── RepoCtx glue ─────────────────────────────────────────────────────────────────────────

// transcriptEmitter wires one agent invocation into the sink: transcript lines immediately
// (F11), the usage climbing EXECUTION-cumulatively (F7) — the live estimate rides on top of
// the settled total of previous invocations.
func (rc *RepoCtx) transcriptEmitter() streamEmit {
	base := *rc.usage
	return streamEmit{
		transcript: func(text string) { rc.Sink.Transcript(rc.Repo, transcriptLine(text)) },
		usage: func(u model.UsageView, _ int) {
			rc.Sink.Usage(model.UsageView{
				InputTokens:  base.in + u.InputTokens,
				OutputTokens: base.out + u.OutputTokens,
				CostUSD:      base.cost,
			})
		},
	}
}

// settleUsage folds one invocation's settled consumption into the execution total and emits
// the authoritative cumulative figure.
func (rc *RepoCtx) settleUsage(out streamOutcome) {
	rc.usage.in += out.Usage.InputTokens
	rc.usage.out += out.Usage.OutputTokens
	rc.usage.cost += out.Usage.CostUSD
	rc.Sink.Usage(rc.usage.view())
}

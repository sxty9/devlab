package mercury

import (
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// A long autonomous run can exhaust the Claude subscription's rolling usage window mid-execution. That
// is not a failure of the work — it is a "come back later" signal. DetectUsageLimit turns a Claude CLI
// outcome into that signal (plus, when the CLI tells us, WHEN the window resets), so the scheduler can
// suspend the run and resume it automatically instead of burning the remaining repos on the same wall.

// limitSignature matches the phrases the Claude CLI uses when the account has hit its usage/rate window.
// Kept deliberately specific: a SUCCESSFUL report that merely discusses "rate limiting" in the code must
// never trip this (DetectUsageLimit additionally gates on the run having actually failed).
var limitSignature = regexp.MustCompile(`(?i)usage limit|rate limit|limit reached|too many requests|\b429\b|quota (?:exceeded|reached)`)

// resetEpochRe pulls the reset instant the CLI appends after a pipe, e.g. "usage limit reached|1735689600"
// (seconds) or a 13-digit millisecond epoch.
var resetEpochRe = regexp.MustCompile(`\|\s*(\d{10,13})\b`)

// resetISORe pulls an ISO-8601 timestamp if the message carries one instead of an epoch.
var resetISORe = regexp.MustCompile(`\d{4}-\d{2}-\d{2}[T ]\d{2}:\d{2}(:\d{2})?(?:Z|[+-]\d{2}:?\d{2})?`)

// DetectUsageLimit reports whether a Claude CLI outcome (its raw --output-format json stdout and the
// error returned by Executor.Agent) is a usage/rate-limit stop, and — when the message says so — the
// instant the window resets. It only ever fires for a FAILED run (a non-nil err, or a result JSON with
// is_error=true): a successful agent report that happens to mention rate limits does not count.
func DetectUsageLimit(out []byte, err error) (limited bool, resetAt time.Time, hasReset bool) {
	failed := err != nil
	var text strings.Builder
	if err != nil {
		text.WriteString(err.Error())
		text.WriteByte('\n')
	}
	// The err==nil, is_error=true path carries the message in the result JSON's "result" field.
	var raw struct {
		IsError bool   `json:"is_error"`
		Result  string `json:"result"`
		Subtype string `json:"subtype"`
	}
	if json.Unmarshal(out, &raw) == nil {
		if raw.IsError {
			failed = true
		}
		text.WriteString(raw.Result)
		text.WriteByte('\n')
		text.WriteString(raw.Subtype)
	}
	if !failed {
		return false, time.Time{}, false
	}
	s := text.String()
	if !limitSignature.MatchString(s) {
		return false, time.Time{}, false
	}
	if t, ok := parseResetTime(s); ok {
		return true, t, true
	}
	return true, time.Time{}, false
}

// parseResetTime extracts the reset instant from a limit message: first a piped epoch (seconds or
// milliseconds), then an ISO-8601 timestamp. Returns ok=false when the message carries neither.
func parseResetTime(s string) (time.Time, bool) {
	if m := resetEpochRe.FindStringSubmatch(s); m != nil {
		n, err := strconv.ParseInt(m[1], 10, 64)
		if err == nil && n > 0 {
			if len(m[1]) >= 13 { // milliseconds
				return time.Unix(n/1000, (n%1000)*int64(time.Millisecond)).UTC(), true
			}
			return time.Unix(n, 0).UTC(), true
		}
	}
	if m := resetISORe.FindString(s); m != "" {
		for _, layout := range []string{time.RFC3339, "2006-01-02T15:04:05", "2006-01-02 15:04:05", "2006-01-02T15:04", "2006-01-02 15:04"} {
			if t, err := time.Parse(layout, m); err == nil {
				return t.UTC(), true
			}
		}
	}
	return time.Time{}, false
}

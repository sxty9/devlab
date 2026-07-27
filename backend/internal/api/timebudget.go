package api

import (
	"fmt"
	"strings"
	"time"
)

// Time-budget helpers shared by the write path (validation), the central config resolution, and the
// executor. One definition each so a budget is parsed and spelled the same everywhere — the store, the
// config, the execution stamp and the honest timeout message never disagree on what "3h" means.

// parseBudget interprets a stored/config time-budget string as a duration. "" is not a value here
// (callers resolve the default first); "0" and any valid non-negative Go duration parse; ok is false
// for anything malformed so the caller can fall back to a safe default rather than trust it. A parsed
// 0 means "no cap".
func parseBudget(s string) (d time.Duration, ok bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, false
	}
	d, err := time.ParseDuration(s)
	if err != nil || d < 0 {
		return 0, false
	}
	return d, true
}

// compactDuration renders a duration in a short, human form ("3h", "90m" → "1h30m", "45m"), rounded to
// the minute — budgets are coarse, so seconds are noise. It is the canonical spelling a budget is
// stored and displayed in.
func compactDuration(d time.Duration) string {
	if d < time.Minute {
		return "0m"
	}
	d = d.Truncate(time.Minute) // budgets are coarse; drop the seconds noise
	h := d / time.Hour
	m := (d % time.Hour) / time.Minute
	switch {
	case h > 0 && m > 0:
		return fmt.Sprintf("%dh%dm", h, m)
	case h > 0:
		return fmt.Sprintf("%dh", h)
	default:
		return fmt.Sprintf("%dm", m)
	}
}

// budgetLabel is the display/stamp form of a resolved budget: "0" for no cap (only the whole-sweep
// duration bounds the run), else the compact duration. It is what the execution result records so the
// Ausführungsansicht can name the budget that actually applied.
func budgetLabel(d time.Duration) string {
	if d <= 0 {
		return "0"
	}
	return compactDuration(d)
}

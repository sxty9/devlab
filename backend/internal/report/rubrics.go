package report

import (
	"sort"
	"time"

	"devlab/backend/internal/runs"
)

// Notices is the read side of the notice pool the Reporter needs for its rubrics.
// *runs.NoticeStore satisfies it. The pool stays passive: it hands over its records, and the
// grouping below — which finding belongs in which rubric — happens here, outside it.
type Notices interface {
	List() ([]runs.Notice, error)
}

// Rubric titles (REQ-042.6). The daily report names the three kinds of finding a run day can
// leave behind beyond its executions: work that did not reach the user, a deliberately overridden
// safeguard, and a safeguard that drifted.
const (
	RubricDeliveryAlarms = "Delivery alarms"
	RubricOverrides      = "Emergency-route overrides"
	RubricProtection     = "Protection deviations"
)

// rubricOf maps a notice kind onto its rubric. A kind that is absent here is not a report
// finding: it is a hint for the notice panel (an automatic assignment, a startup reconcile, a
// restart) and stays out of the mail, which reports the day's work and its alarms — not the whole
// feed.
func rubricOf(kind string) string {
	switch kind {
	case runs.NoticeDeliveryAlarm, runs.NoticeDeliveryBlocked,
		runs.NoticeDeliverySelfCheck, runs.NoticeStructureViolation,
		// A held maintenance is work that did not reach the user for the whole day — the daily
		// report is exactly where that belongs.
		runs.NoticeDeliveryHeld,
		// Production not reaching the user is a delivery alarm too: a merged delivery still stuck
		// before production, and the standard branch silently drifting ahead of what production runs.
		runs.NoticeProdUndelivered, runs.NoticeProdDrift,
		// A pull request merged onto a stale base never reached the standard branch — the same
		// class of "the work did not land where the ledger claims" finding.
		runs.NoticeMisdelivered:
		return RubricDeliveryAlarms
	case runs.NoticeAdminOverride:
		return RubricOverrides
	case runs.NoticeProtectionDeviation:
		return RubricProtection
	}
	return ""
}

// rubricOrder is the order the rubrics appear in — the order REQ-042.6 names them.
var rubricOrder = []string{RubricDeliveryAlarms, RubricOverrides, RubricProtection}

// RubricLine is one finding as the report states it: which repository it concerns, what happened,
// what resolves it, and — for a bundled notice — how often it occurred over which period. A raw
// log never appears; details live in the UI.
type RubricLine struct {
	Repo     string
	Text     string
	NextStep string
	Count    int
	FirstAt  time.Time
	LastAt   time.Time
}

// Rubric is one titled group of findings.
type Rubric struct {
	Title string
	Lines []RubricLine
}

// BuildRubrics groups the notices that concern one report day into the report's rubrics
// (REQ-042.6). A notice counts for the day when its period touches that day — a bundled notice
// that started earlier and is still recurring is reported on every day it kept happening, with
// its full count and period, because that is what actually occurred. Empty rubrics are left out:
// an alarm-free day states no alarms rather than three empty headings.
func BuildRubrics(notices []runs.Notice, day string, loc *time.Location) []Rubric {
	if loc == nil {
		loc = time.UTC
	}
	byTitle := map[string][]RubricLine{}
	for _, n := range notices {
		title := rubricOf(n.Kind)
		if title == "" || !touchesDay(n, day, loc) {
			continue
		}
		byTitle[title] = append(byTitle[title], RubricLine{
			Repo: n.Repo, Text: n.Message(), NextStep: n.NextStep,
			Count: n.Count, FirstAt: n.FirstAt, LastAt: n.LastAt,
		})
	}
	out := make([]Rubric, 0, len(rubricOrder))
	for _, title := range rubricOrder {
		lines := byTitle[title]
		if len(lines) == 0 {
			continue
		}
		sort.Slice(lines, func(i, j int) bool { return lines[i].LastAt.After(lines[j].LastAt) })
		out = append(out, Rubric{Title: title, Lines: lines})
	}
	return out
}

// touchesDay reports whether a notice's period [FirstAt, LastAt] intersects the given report day.
func touchesDay(n runs.Notice, day string, loc *time.Location) bool {
	first, last := n.FirstAt, n.LastAt
	if first.IsZero() {
		first = n.At
	}
	if last.IsZero() {
		last = first
	}
	return dayKey(first.In(loc)) <= day && dayKey(last.In(loc)) >= day
}

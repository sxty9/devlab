// The printed protocol: what the import will do, what it found already done, and what it
// refuses. It is PORTIONED — one line per record it touches, a count for the bulk it leaves
// alone — because a wall of raw records is not a protocol anyone reads.
package main

import (
	"fmt"
	"io"
	"strings"

	"devlab/backend/internal/model"
	"devlab/backend/internal/runs"
)

// titleWidth bounds a printed title so one record stays one line.
const titleWidth = 56

func writeProtocol(w io.Writer, p *plan, dry bool) {
	fmt.Fprintln(w, "devlab-migrate — one-time data import (S15)")
	fmt.Fprintln(w)
	fmt.Fprintf(w, "  state root   %s\n", p.stateRoot)
	fmt.Fprintf(w, "  export       %s (%d records)\n", p.inputPath, p.records)
	if dry {
		fmt.Fprintln(w, "  mode         dry run — nothing is written")
	} else {
		fmt.Fprintln(w, "  mode         import")
	}
	fmt.Fprintln(w)

	if len(p.refusals) > 0 {
		fmt.Fprintf(w, "REFUSED (%d) — no record is imported while any of these stands\n", len(p.refusals))
		for _, r := range p.refusals {
			fmt.Fprintf(w, "  ! %s\n", r)
		}
		fmt.Fprintln(w)
	}

	fmt.Fprintf(w, "automatic runs (%d) — created WITHOUT axiom assignment; uncovered stays visible\n", len(p.autoRuns))
	for _, r := range p.autoRuns {
		state := "inactive"
		if r.Active {
			state = "active"
		}
		fmt.Fprintf(w, "  + %-22s %-20s %-8s %s\n", r.ID, scheduleLine(r.Schedule), state, clip(r.Title))
	}

	fmt.Fprintf(w, "open tasks (%d) — foreign repositories; fed in, not started\n", len(p.openTodos))
	for _, r := range p.openTodos {
		fmt.Fprintf(w, "  + %-22s %-29s %s\n", r.ID, targetLine(r.Targets), clip(r.Title))
	}

	fmt.Fprintf(w, "history entries (%d) — completed foreign tasks, original metadata, no run definition\n", len(p.history))
	for _, res := range p.history {
		fmt.Fprintf(w, "  + %-32s %-19s %s\n", res.ID, repoLine(res.Repos), clip(res.RunTitle))
	}

	fmt.Fprintf(w, "own-repository records skipped (%d) — their substance is the acceptance matrix\n", p.skippedOwn)

	fmt.Fprintln(w, "legacy execution archive")
	switch {
	case p.arch.dir == "":
		fmt.Fprintln(w, "  none on this instance")
	default:
		fmt.Fprintf(w, "  %s — %d files, %d to import into executions/\n", p.arch.dir, p.arch.files, len(p.arch.imports))
		for _, u := range p.arch.unmatched {
			fmt.Fprintf(w, "  ? %s — not readable as an execution; kept verbatim\n", u)
		}
		fmt.Fprintf(w, "  moved aside afterwards to %s (nothing is deleted; the tolerant read stops listing it twice)\n", p.arch.movedTo)
	}

	fmt.Fprintf(w, "migration protocol M1–M8 (%d items to record)\n", len(p.notices))
	for _, o := range p.notices {
		fmt.Fprintf(w, "  + %s\n", o.Label)
	}

	if len(p.lapsedDue) > 0 {
		fmt.Fprintf(w, "due dates dropped (%d) — already lapsed; imported without one so nothing starts by itself\n", len(p.lapsedDue))
		for _, l := range p.lapsedDue {
			fmt.Fprintf(w, "  ~ %s\n", l)
		}
	}

	fmt.Fprintf(w, "already present — runs %d · history entries %d · protocol items %d\n",
		len(p.presentRuns), len(p.presentHistory), p.presentNotices)
	if p.empty() {
		fmt.Fprintln(w, "nothing to do.")
	}
}

// scheduleLine renders a recurrence compactly ("weekly 07:00 Mon").
func scheduleLine(s *runs.ScheduleSpec) string {
	if s == nil {
		return "-"
	}
	out := string(s.Kind) + " " + s.TimeOfDay
	days := make([]string, 0, len(s.Weekdays))
	for _, d := range s.Weekdays {
		days = append(days, d.String()[:3])
	}
	if len(days) > 0 {
		out += " " + strings.Join(days, ",")
	}
	return out
}

// targetLine names a task's destinations, marking the ones to be created.
func targetLine(ts []runs.Target) string {
	out := make([]string, 0, len(ts))
	for _, t := range ts {
		if t.Create {
			out = append(out, t.Repo+" (new)")
			continue
		}
		out = append(out, t.Repo)
	}
	if len(out) == 0 {
		return "-"
	}
	return strings.Join(out, ",")
}

func repoLine(rs []model.RepoPipeline) string {
	out := make([]string, 0, len(rs))
	for _, r := range rs {
		out = append(out, r.Repo)
	}
	if len(out) == 0 {
		return "-"
	}
	return strings.Join(out, ",")
}

// clip bounds a title to one line (counted in characters, not bytes — titles are not ASCII).
func clip(s string) string {
	r := []rune(strings.TrimSpace(strings.ReplaceAll(s, "\n", " ")))
	if len(r) <= titleWidth {
		return string(r)
	}
	return strings.TrimSpace(string(r[:titleWidth-1])) + "…"
}

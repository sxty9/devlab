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

	fmt.Fprintf(w, "automatic runs (%d) — created INACTIVE and WITHOUT axiom assignment; uncovered stays visible\n", len(p.autoRuns))
	for _, r := range p.autoRuns {
		state := "inactive"
		if r.Active {
			state = "active"
		}
		fmt.Fprintf(w, "  + %-22s %-20s %-8s %s\n", r.ID, scheduleLine(r.Schedule), state, clip(r.Title))
	}
	if len(p.heldInactive) > 0 {
		fmt.Fprintf(w, "  activation gate (%d) — these were switched ON in the export and are imported OFF:\n",
			len(p.heldInactive))
		fmt.Fprintln(w, "  without axioms there is no prompt, and a run without a prompt would execute the bare preamble.")
		for _, h := range p.heldInactive {
			fmt.Fprintf(w, "  ~ %s\n", h)
		}
		fmt.Fprintln(w, "  assign the axioms first (a constitution write triggers it), then switch each run on.")
	}

	fmt.Fprintf(w, "open tasks (%d) — foreign repositories, prompt composed; fed in, not started\n", len(p.openTodos))
	for _, r := range p.openTodos {
		fmt.Fprintf(w, "  + %-22s %-29s %s\n", r.ID, targetLine(r.Targets), clip(r.Title))
	}

	fmt.Fprintf(w, "history entries (%d) — completed foreign tasks, original metadata, no run definition\n", len(p.history))
	for _, res := range p.history {
		fmt.Fprintf(w, "  + %-32s %-9s %-19s %s\n", res.ID, outcomeLine(res.Repos), repoLine(res.Repos), clip(res.RunTitle))
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

	writeTakeover(w, p)

	fmt.Fprintf(w, "migration protocol (%d items to record) — M1–M8 plus the activation gate\n", len(p.notices))
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

// writeTakeover reports what the import CARRIES OVER rather than adds: which pool held how many
// pre-rebuild records, where their verbatim copy went, and what the rebuild has no reader for. Every
// set-aside artifact is printed with its find location — a copy whose whereabouts are not written
// down is indistinguishable from a deletion.
func writeTakeover(w io.Writer, p *plan) {
	fmt.Fprintln(w, "pre-rebuild stock taken over")

	switch {
	case !p.pool.takenOver() && len(p.pool.newForm) == 0:
		fmt.Fprintln(w, "  run pool             empty — nothing to carry over")
	case !p.pool.takenOver():
		fmt.Fprintf(w, "  run pool             %s — %d records, all in the rebuilt form; nothing to carry over\n",
			p.pool.path, len(p.pool.newForm))
	default:
		fmt.Fprintf(w, "  run pool             %s\n", p.pool.path)
		fmt.Fprintf(w, "    %d records in the PRE-REBUILD form, %d in the rebuilt form, %d undecidable\n",
			len(p.pool.legacy), len(p.pool.newForm), len(p.pool.undecidable))
		fmt.Fprintf(w, "    set aside verbatim to %s, then the pool is written with %d records in the rebuilt form\n",
			p.pool.aside, len(p.poolAfter()))
		fmt.Fprintln(w, "    the pre-rebuild records share their ids with the imported ones; they are told apart by SHAPE,")
		fmt.Fprintln(w, "    which is why they neither count as already imported nor stay behind in the pool")
		for _, u := range p.pool.undecidable {
			fmt.Fprintf(w, "    ? %s — in neither shape; set aside with the rest, never interpreted\n", u)
		}
	}

	if p.ledger.count() > 0 || len(p.ledger.undecidable) > 0 {
		fmt.Fprintf(w, "  delivery ledger      %s\n", p.ledger.path)
		fmt.Fprintf(w, "    %d records converted (%d merged · %d closed · %d open); set aside verbatim to %s first\n",
			p.ledger.count(), p.ledger.merged, p.ledger.closed, p.ledger.open, p.ledger.aside)
		fmt.Fprintln(w, "    the source recorded a status WORD; the rebuilt record expresses merged and closed as times,")
		fmt.Fprintln(w, "    so an unconverted record reads as OPEN and the next pull request stacks onto it")
		if p.ledger.merged+p.ledger.closed > 0 {
			fmt.Fprintln(w, "    the outcome TIME is the delivery's own creation time: the source carried no second timestamp")
		}
		for _, u := range p.ledger.undecidable {
			fmt.Fprintf(w, "    ? %s — in neither shape; left untouched and not converted\n", u)
		}
	}

	switch {
	case len(p.snaps.moved) > 0:
		fmt.Fprintf(w, "  config snapshots     %s — %d of %d hold pre-rebuild run records\n",
			p.snaps.dir, len(p.snaps.moved), len(p.snaps.moved)+p.snaps.kept)
		fmt.Fprintf(w, "    moved to %s; a restore would write them back into the pool verbatim\n", p.snaps.to)
	case p.snaps.kept > 0:
		fmt.Fprintf(w, "  config snapshots     %s — %d, all in the rebuilt form; restorable\n", p.snaps.dir, p.snaps.kept)
	}

	for _, o := range p.orphans {
		fmt.Fprintf(w, "  no reader            %s → %s\n", o.from, o.to)
		fmt.Fprintf(w, "    %s\n", o.why)
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

// outcomeLine states the recorded outcome of a history entry the way the entry itself carries it —
// read off the one derivation, never re-decided here.
func outcomeLine(rs []model.RepoPipeline) string {
	if len(rs) == 0 {
		return "no repos"
	}
	for _, r := range rs {
		done, ok := model.PipelineSucceeded(r.Stages)
		if !done {
			return "open"
		}
		if !ok {
			return "failed"
		}
	}
	return "succeeded"
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

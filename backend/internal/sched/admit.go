// Pure admission rules (REQ-013/014/015) — unit-testable without a process. The persisted
// execution documents ARE the concurrency state; these functions only project them.
package sched

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"devlab/backend/internal/execstate"
	"devlab/backend/internal/model"
	"devlab/backend/internal/runs"
)

// AdmitDecision is the pure admission verdict.
type AdmitDecision struct {
	Admit    bool
	Overload bool
	Queue    bool
	Reason   string
	// BusyRepos are the candidate's targets currently claimed elsewhere — they go to the BACK
	// of the worklist, never dropped (REQ-014.3).
	BusyRepos  []string
	Suggestion *model.DeferSuggestion
	// DeferFirst names the execution the caller asked to defer (placement "defer") — the
	// scheduler defers it, then re-admits.
	DeferFirst string
}

// repoKey normalises a repo identifier so claims collide iff they name the same repo.
func repoKey(repo string) string { return strings.ToLower(strings.TrimSpace(repo)) }

// claimTargets are the repos one document claims for admission ordering: every repo not done
// yet (finished repos stay finished and hold nothing).
func claimTargets(d execstate.Doc) []string {
	out := []string{}
	for _, r := range d.Repos {
		if r.State != execstate.RepoDone {
			out = append(out, r.Repo)
		}
	}
	return out
}

// claimedRepos maps repo key → execution id over the RUNNING documents. A todo claims all its
// unfinished targets for its whole execution; an all-repos auto run claims ONLY the repo it is
// actively working (REQ-014.2). Queued, paused, blocked and interrupted documents hold no
// repo — a deferred execution frees its repositories along with its slot.
func claimedRepos(docs []execstate.Doc) map[string]string {
	claims := map[string]string{}
	for _, d := range docs {
		if d.Phase != model.PhaseRunning {
			continue
		}
		for _, r := range d.Repos {
			claim := false
			switch d.Kind {
			case model.KindTodo:
				claim = r.State != execstate.RepoDone
			default: // auto: only the repo currently in work
				claim = r.State == execstate.RepoActive
			}
			if claim {
				claims[repoKey(r.Repo)] = d.ID
			}
		}
	}
	return claims
}

// Admit is the pure admission rule (unit-testable without a process): capacity cap; overload
// only on a cap block, at most ONE living overload (never summing), never on a repo conflict;
// repo exclusivity is hard; busy repos queue at the BACK, never skipped (REQ-013/014/015).
func Admit(docs []execstate.Doc, set runs.Settings, targets []string, placement *Placement) (AdmitDecision, error) {
	capSlots := set.MaxConcurrency
	if capSlots <= 0 {
		capSlots = DefaultCapacity
	}
	kind := ""
	if placement != nil {
		kind = placement.Kind
	}
	switch kind {
	case "", PlacementQueue, PlacementDefer, PlacementOverload:
	default:
		return AdmitDecision{}, fmt.Errorf("unknown placement kind %q", kind)
	}

	claims := claimedRepos(docs)
	occupied := 0
	overloadAlive := false
	for _, d := range docs {
		if d.Phase == model.PhaseRunning && !d.Overload {
			occupied++
		}
		if d.Live() && d.Overload {
			overloadAlive = true
		}
	}

	// Repo exclusivity first — it is the harder rule: no urgency crosses it.
	busy := []string{}
	conflicting := map[string]bool{}
	for _, t := range targets {
		if id, taken := claims[repoKey(t)]; taken {
			busy = append(busy, t)
			conflicting[id] = true
		}
	}
	if len(busy) > 0 {
		d := AdmitDecision{
			Reason:    "target repository busy: " + strings.Join(busy, ", "),
			BusyRepos: busy,
		}
		switch kind {
		case PlacementOverload:
			d.Reason = "an overload cannot cross repository exclusivity — " + d.Reason
			return d, nil
		case PlacementDefer:
			d.DeferFirst = placement.DeferExecutionID
			if d.DeferFirst == "" {
				return AdmitDecision{}, fmt.Errorf("placement defer needs the execution to defer")
			}
			return d, nil
		case PlacementQueue:
			d.Queue = true
			return d, nil
		default:
			d.Suggestion = suggestAmong(docs, conflicting, true)
			return d, nil
		}
	}

	if occupied >= capSlots {
		d := AdmitDecision{Reason: fmt.Sprintf("all %d slots occupied", capSlots)}
		switch kind {
		case PlacementOverload:
			if overloadAlive {
				d.Reason = "an overload is already active — overloads never sum (one temporary extra slot at most)"
				return d, nil
			}
			d.Admit = true
			d.Overload = true
			d.Reason = ""
			return d, nil
		case PlacementDefer:
			d.DeferFirst = placement.DeferExecutionID
			if d.DeferFirst == "" {
				return AdmitDecision{}, fmt.Errorf("placement defer needs the execution to defer")
			}
			return d, nil
		case PlacementQueue:
			d.Queue = true
			return d, nil
		default:
			d.Suggestion = SuggestDefer(docs)
			return d, nil
		}
	}

	return AdmitDecision{Admit: true}, nil
}

// SuggestDefer proposes which execution to defer (C F3 deferScore) — with its reason. Lower
// score is the better target; ties break toward the YOUNGER execution (least effort invested).
// The explicit user choice always outranks the suggestion (REQ-015.4).
func SuggestDefer(docs []execstate.Doc) *model.DeferSuggestion {
	return suggestAmong(docs, nil, false)
}

// suggestAmong scores the running documents (restricted to `only` when non-nil — the
// conflicting executions of a repo block, which are the only ones whose defer frees the way).
func suggestAmong(docs []execstate.Doc, only map[string]bool, repoBlock bool) *model.DeferSuggestion {
	type cand struct {
		d     execstate.Doc
		score int
		why   string
	}
	cands := []cand{}
	for _, d := range docs {
		if d.Phase != model.PhaseRunning {
			continue
		}
		if only != nil && !only[d.ID] {
			continue
		}
		score, why := deferScore(d, repoBlock)
		cands = append(cands, cand{d: d, score: score, why: why})
	}
	if len(cands) == 0 {
		return nil
	}
	sort.SliceStable(cands, func(i, j int) bool {
		if cands[i].score != cands[j].score {
			return cands[i].score < cands[j].score
		}
		return startedAt(cands[i].d).After(startedAt(cands[j].d)) // younger first
	})
	best := cands[0]
	return &model.DeferSuggestion{ExecutionID: best.d.ID, Reason: best.why, Score: best.score}
}

// deferScore ranks one running document for deferral — LOWER is a better target. A document
// between repositories loses nothing (0); one mid-repository would discard that repository's
// in-flight work (1000, always worse). On a repo block the conflicting execution carries its
// own justification: deferring it frees the repository the start needs.
func deferScore(d execstate.Doc, repoBlock bool) (int, string) {
	activeRepo := ""
	done := 0
	for _, r := range d.Repos {
		switch r.State {
		case execstate.RepoActive:
			activeRepo = r.Repo
		case execstate.RepoDone:
			done++
		}
	}
	if repoBlock {
		return 0, "occupies a repository this start needs — deferring it frees the way"
	}
	if activeRepo != "" {
		return 1000, fmt.Sprintf("is mid-repository %q — deferring now would set aside in-flight work", activeRepo)
	}
	why := "is between repositories — deferring loses no in-flight work"
	if done > 0 {
		why += fmt.Sprintf("; %d finished repositories stay finished", done)
	}
	return 0, why
}

func startedAt(d execstate.Doc) time.Time {
	if d.StartedAt != nil {
		return *d.StartedAt
	}
	return d.CreatedAt
}

// IsDue is THE ONE place "due" is decided. The schedule progresses at the ACTUAL start — the
// execution document written at queued→running is the progression record, so lastStart derives
// from the documents (and results); a LIVE document for the run suppresses any new fire on the
// caller's side (no double start, no lost fire). A missed window fires once (catch-up), never
// N times, because the next fire is always computed forward from the last actual start.
func IsDue(r runs.Run, lastStart *time.Time, now time.Time) bool {
	if r.Kind == model.KindTodo {
		// A todo fires once at its due time: any earlier actual start means only a manual
		// re-trigger (or the resume path) touches it again.
		return r.DueAt != nil && !r.DueAt.After(now) && lastStart == nil
	}
	if !r.Active || r.Schedule == nil {
		return false
	}
	anchor := r.Authorship.CreatedAt
	if lastStart != nil {
		anchor = *lastStart
	}
	next := runs.NextFire(*r.Schedule, anchor)
	if next.IsZero() {
		return false
	}
	return !next.After(now)
}

package report

import (
	"context"
	"fmt"
	"log"
	"time"

	"devlab/backend/internal/live"
	"devlab/backend/internal/model"
	"devlab/backend/internal/runs"
)

// NoticeSink is the write side of the notice pool the self-check needs. *runs.NoticeStore
// satisfies it: recording goes through Coalesce, so a finding that keeps holding does not produce
// one hint per pass but ONE hint with a count and a period (REQ-032.5).
type NoticeSink interface {
	Coalesce(n runs.Notice) (runs.Notice, error)
}

// defaultSelfCheckWindow is the period the self-check looks back over: long enough that a single
// quiet evening is not an alarm, short enough that a broken delivery path is named the same week.
const defaultSelfCheckWindow = 72 * time.Hour

// SelfCheck is the service watching its own outcome (REQ-031.4): when executions kept CHANGING
// code over a period but nothing was ever delivered, that is a finding the user must hear of their
// own accord — the individual runs each look plausible, only the period reveals the pattern.
//
// It only ever reports what it observed (how long, and that nothing was delivered); whether the
// cause is a broken delivery path, a missing setup or a deliberate pause is not judged here.
type SelfCheck struct {
	execs   Executions
	notices NoticeSink
	pub     live.Publisher

	window time.Duration
	now    func() time.Time
	logf   func(string, ...any)
}

// SelfCheckConfig configures a SelfCheck. Window defaults to defaultSelfCheckWindow.
type SelfCheckConfig struct {
	Execs   Executions
	Notices NoticeSink
	Pub     live.Publisher
	Window  time.Duration
	Now     func() time.Time
	Logf    func(string, ...any)
}

// NewSelfCheck builds the self-check, filling the real clock, the standard logger and the default
// window.
func NewSelfCheck(c SelfCheckConfig) *SelfCheck {
	sc := &SelfCheck{
		execs: c.Execs, notices: c.Notices, pub: c.Pub,
		window: c.Window, now: c.Now, logf: c.Logf,
	}
	if sc.window <= 0 {
		sc.window = defaultSelfCheckWindow
	}
	if sc.now == nil {
		sc.now = time.Now
	}
	if sc.logf == nil {
		sc.logf = log.Printf
	}
	return sc
}

// Run ticks the self-check on an interval until ctx is done (shared ticker, loop.go).
func (sc *SelfCheck) Run(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = time.Hour
	}
	ticker{
		label:    "delivery self-check",
		interval: interval,
		logf:     sc.logf,
		tick:     func(context.Context) { _, _ = sc.Tick() },
	}.run(ctx)
}

// Tick performs one pass and reports whether the finding holds. It raises the notice through
// Coalesce, so repeated passes over the same unresolved situation bundle into one hint whose count
// and period say how long it has been true. It is silent in every other case: no executions at
// all, no changes, at least one delivery — and it says nothing at all about the pre-rebuild
// archive (writtenInThisVocabulary).
func (sc *SelfCheck) Tick() (bool, error) {
	if sc.execs == nil || sc.notices == nil {
		return false, nil
	}
	results, err := sc.execs.List()
	if err != nil {
		sc.logf("devlabd: self-check list executions: %v", err)
		return false, err
	}
	now := sc.now().UTC()
	cutoff := now.Add(-sc.window)

	changed, delivered := 0, 0
	for _, res := range results {
		if !writtenInThisVocabulary(res) || !inWindow(res, cutoff) {
			continue
		}
		if resultChangedCode(res) {
			changed++
		}
		if resultDelivered(res) {
			delivered++
		}
	}
	if changed == 0 || delivered > 0 {
		return false, nil
	}

	n := runs.Notice{
		Kind: runs.NoticeDeliverySelfCheck,
		// The wording is STABLE (no varying counts) so recurring passes bundle into one hint
		// instead of a new row each time; how often and how long is what Count/FirstAt/LastAt say.
		Text: fmt.Sprintf("changes were implemented but nothing was delivered in the last %s",
			humanWindow(sc.window)),
		NextStep: "check the delivery path (deliver-dev) of the affected repositories and resume",
		LastAt:   now,
	}
	if _, err := sc.notices.Coalesce(n); err != nil {
		sc.logf("devlabd: self-check notice write failed: %v", err)
		return true, err
	}
	if sc.pub != nil {
		sc.pub.Publish(live.TopicNotices)
	}
	return true, nil
}

// writtenInThisVocabulary reports whether an execution's stages are written in the vocabulary the
// comparisons below use. A document from the pre-rebuild archive is NOT (REQ-027.3): the tolerant
// reader carries its stage names verbatim, so it says `dev-deploy` where the chain says
// `deliver-dev` and `pr` where it says `pull-request`. Compared strictly, every archived record
// therefore reads as "implement ran, nothing was delivered" — and the very first start of a
// rebuilt instance raised exactly that finding over a past that is closed.
//
// Translating the old names would not repair it either: "delivered" meant something else there (a
// push plus an opened pull request, not a dev delivery), so a mapping would trade a wrong finding
// for a guessed one. And the finding's next step — check the delivery path and RESUME — has no
// addressee in the archive: nothing in it can be resumed.
//
// So the archive is neither translated nor judged, it is left out: this check watches THIS
// system's outcome, and an archived record is evidence neither for it nor against it.
func writtenInThisVocabulary(res runs.Result) bool { return !res.Legacy }

// inWindow reports whether an execution belongs to the observed period: by when it finished, or —
// while it is still unfinished — by when it started.
func inWindow(res runs.Result, cutoff time.Time) bool {
	t := res.StartedAt
	if res.EndedAt != nil {
		t = *res.EndedAt
	}
	return !t.Before(cutoff)
}

// resultChangedCode reports whether an execution actually changed something: a repository whose
// implement stage ran. Read off the server stage array, never a stored flag (K-3).
func resultChangedCode(res runs.Result) bool {
	return anyStageExecuted(res, model.StageImplement)
}

// resultDelivered reports whether anything of an execution reached the user: a dev delivery that
// ran, or deliveries that have since been merged.
func resultDelivered(res runs.Result) bool {
	if res.MergedAt != nil {
		return true
	}
	return anyStageExecuted(res, model.StageDeliverDev)
}

// anyStageExecuted compares stage names STRICTLY against the chain's vocabulary, which is only
// meaningful for a document written in it — the archive's retired names would never match and the
// absence would read as a finding. Tick therefore filters with writtenInThisVocabulary (Legacy)
// before it asks, and this function answers for nothing else.
func anyStageExecuted(res runs.Result, stage model.Stage) bool {
	for _, rp := range res.Repos {
		for _, sv := range rp.Stages {
			if sv.Stage == stage && sv.State == model.StepExecuted {
				return true
			}
		}
	}
	return false
}

// humanWindow renders a period the way the report states it ("72h" rather than "72h0m0s").
func humanWindow(d time.Duration) string {
	if d%time.Hour == 0 {
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return d.String()
}

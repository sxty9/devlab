package report

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sort"
	"strings"
	"time"

	"devlab/backend/internal/faultclass"
	"devlab/backend/internal/live"
	"devlab/backend/internal/model"
	"devlab/backend/internal/runs"
)

// Executions is the read side of the result store the Reporter needs: every stored result
// document (each already carrying its per-repo stage array). *runs.ResultStore satisfies it.
type Executions interface {
	List() ([]runs.Result, error)
}

// PausedExecutions names the executions that stand on the ONE pause right now, by execution id.
// The pause lives on the state document, not on the result, so the Reporter reads it through this
// seam instead of guessing from an unfinished result. nil is allowed: the report then names no
// pause rather than an invented one.
type PausedExecutions interface {
	PausedIDs() (map[string]bool, error)
}

// Sender delivers a composed report to a recipient. The production implementation wraps the mailer
// (maild); tests substitute a fake. It is the single seam through which mail leaves the Reporter, so
// a send failure is observed in exactly one place. A Sender that KNOWS its fault cannot be mended by
// repetition wraps ErrPermanent around it.
type Sender interface {
	Send(ctx context.Context, recipient string, c Content) error
}

// ErrPermanent is how a Sender NAMES a fault no repetition can fix — a mail path that is
// misconfigured rather than momentarily unavailable. It mirrors faultclass.ErrSatisfied: the
// classification itself stays with faultclass, and a seam that knows more than an error string can
// convey says so by wrapping this sentinel. A send that fails with it earns exactly ONE attempt and
// a named, blocked end (K-5) instead of a retry every pass for ever.
var ErrPermanent = errors.New("permanent send fault")

// The ONE fault classification and the ONE growing-interval schedule (K-5) — the same two functions
// every other retry in the system runs through. Named here once so this package borrows the policy
// rather than growing a second one.
var (
	classify       = faultclass.Classify
	advanceBackoff = faultclass.Next
)

// maxSendAttempts caps ONE retry episode of a day's report (K-5): after this many failed sends the
// day is blocked — with reason, time and attempts — and waits for an explicit resumption instead of
// being retried for ever. An explicitly resumed day starts a fresh episode.
const maxSendAttempts = 5

// NoticePool is the notice pool as the Reporter uses it: it READS the day's findings for the report's
// rubrics and WRITES the one hint a blocked delivery is worth — through Coalesce, so a fault that
// keeps holding becomes ONE record with a count and a period instead of a row per pass (REQ-032.5).
// *runs.NoticeStore satisfies it.
type NoticePool interface {
	Notices
	NoticeSink
}

// Reporter drives the daily run report. Each pass it buckets executions by the day they belong to and,
// for every CLOSED day (strictly before today) that had at least one execution and has not already
// been delivered, composes and sends exactly one report to the run owner. A restart, a second pass, or
// a later-finishing run never produces a second email for a day, because the ledger seals a delivered
// day.
//
// A failed send is recorded visibly and retried with GROWING intervals; a fault no repetition can fix
// — and a transient one that exhausts its attempts — ends the day honestly as `blocked` with reason,
// time and attempts, waiting for the explicit resumption (K-5). Nothing is ever attempted once per
// pass for ever, and a broken mail path is stated ONCE, bundled, instead of on every pass.
//
// It only ever reports closed days: a report for day D is sent once D is over, so no execution can
// still land on D after its report — a run that finishes the next day is attributed to that next day
// (executions are bucketed by when they finished), which is exactly where the requirement wants it.
type Reporter struct {
	recipient string
	execs     Executions
	paused    PausedExecutions
	notices   NoticePool
	ledger    *Ledger
	sender    Sender
	pub       live.Publisher

	now      func() time.Time
	loc      *time.Location
	lookback int    // how many closed days back a NEW report may still be sent for (default 1 = yesterday)
	linkBase string // app base URL for the UI deep link ("" ⇒ the report points to the UI in words only)
	logf     func(string, ...any)
}

// Config configures a Reporter. recipient is the Holistic user the runs are assigned to (the owner);
// an empty recipient makes the Reporter inert. Notices feeds the report's rubrics (REQ-042.6) and
// takes the hint a blocked delivery raises; it may be nil — the report then carries its executions
// without rubrics and a block stays visible in the delivery record alone.
type Config struct {
	Recipient string
	Execs     Executions
	// Paused names the currently paused executions (optional — nil ⇒ no pause is claimed).
	Paused  PausedExecutions
	Notices NoticePool
	Ledger  *Ledger
	Sender  Sender
	// Pub ticks the ONE live stream after a delivery record changes, so a failed or blocked send
	// appears in the surface without a reload (REQ-034). Optional.
	Pub      live.Publisher
	Now      func() time.Time
	Loc      *time.Location
	Lookback int
	LinkBase string
	Logf     func(string, ...any)
}

// NewReporter builds a Reporter, filling sensible defaults (real clock, server-local timezone, a
// one-day lookback, the standard logger).
func NewReporter(c Config) *Reporter {
	rp := &Reporter{
		recipient: strings.TrimSpace(c.Recipient),
		execs:     c.Execs,
		paused:    c.Paused,
		notices:   c.Notices,
		ledger:    c.Ledger,
		sender:    c.Sender,
		pub:       c.Pub,
		now:       c.Now,
		loc:       c.Loc,
		lookback:  c.Lookback,
		linkBase:  strings.TrimSpace(c.LinkBase),
		logf:      c.Logf,
	}
	if rp.now == nil {
		rp.now = time.Now
	}
	if rp.loc == nil {
		rp.loc = time.Local
	}
	if rp.lookback <= 0 {
		rp.lookback = 1
	}
	if rp.logf == nil {
		rp.logf = log.Printf
	}
	return rp
}

// Run ticks the Reporter on an interval until ctx is done, through the shared ticker (loop.go) —
// panic-isolated, with one pass at startup so a report due right after a restart is not delayed.
func (rp *Reporter) Run(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = 10 * time.Minute
	}
	ticker{
		label:    "daily-report reporter (recipient=" + rp.recipient + ")",
		interval: interval,
		logf:     rp.logf,
		tick:     rp.tick,
	}.run(ctx)
}

// tick performs one pass: it is the whole decision, kept free of I/O timing so tests can drive it
// directly with a fixed clock.
func (rp *Reporter) tick(ctx context.Context) {
	if rp.recipient == "" || rp.execs == nil || rp.ledger == nil || rp.sender == nil {
		return
	}
	now := rp.now().In(rp.loc)
	today := dayKey(now)

	summaries, err := rp.execs.List()
	if err != nil {
		rp.logf("devlabd: daily-report list executions: %v", err)
		return
	}

	byDay := map[string][]runs.Result{}
	for _, s := range summaries {
		byDay[rp.reportDay(s)] = append(byDay[rp.reportDay(s)], s)
	}

	// Candidate days = closed days with executions inside the lookback window (fresh reports), PLUS any
	// closed day whose record is still undelivered AND due again. Due is the whole retry policy (K-5):
	// a blocked day waits for its explicit resumption and a day inside its growing backoff interval
	// waits for that interval — neither is picked up here, however old it is.
	earliest := dayKey(now.AddDate(0, 0, -rp.lookback))
	cand := map[string]bool{}
	for d := range byDay {
		if d < today && d >= earliest {
			cand[d] = true
		}
	}
	if recs, err := rp.ledger.List(); err == nil {
		for _, r := range recs {
			if r.Recipient == rp.recipient && r.Day < today && due(r, now) {
				cand[r.Day] = true
			}
		}
	}

	days := make([]string, 0, len(cand))
	for d := range cand {
		days = append(days, d)
	}
	sort.Strings(days) // chronological (YYYY-MM-DD sorts by date)

	for _, d := range days {
		rp.deliverDay(ctx, d, byDay[d], now)
	}
}

// deliverDay sends (or seals, or records a fault for) exactly one report for day d. It is idempotent
// per day: a day already sent is skipped, so a second pass or a restart cannot double-send.
func (rp *Reporter) deliverDay(ctx context.Context, day string, summaries []runs.Result, now time.Time) {
	rec, _, err := rp.ledger.Get(rp.recipient, day)
	if err != nil {
		rp.logf("devlabd: daily-report ledger read %s: %v", day, err)
		return
	}
	if !due(rec, now) {
		return // sealed, blocked, or waiting out its backoff interval — the ONE gate, see due()
	}
	if len(summaries) == 0 {
		return // a day without any execution produces no message
	}

	items := rp.buildItems(summaries)
	content := Compose(day, items, rp.rubrics(day), rp.dayLink())

	at := now
	if err := rp.sender.Send(ctx, rp.recipient, content); err != nil {
		rp.recordFault(rec, day, len(items), err, at)
		return
	}
	rp.logf("devlabd: daily report %s for %s sent (%d executions)", day, rp.recipient, len(items))
	rp.put(Record{
		Recipient: rp.recipient, Day: day, Status: StatusSent, Executions: len(items),
		Attempts: rec.Attempts + 1, SentAt: &at, LastAttempt: &at,
	})
}

// due decides whether a day's report may be attempted at t — the ONE gate every pass runs through
// (K-5): a delivered day is sealed for ever; a BLOCKED day waits for its explicit resumption and
// never for another silent pass; a day inside its growing backoff interval waits for that interval to
// pass. Everything else is due. This silence is what replaces the old attempt-per-pass.
func due(rec Record, t time.Time) bool {
	switch rec.Status {
	case StatusSent, StatusBlocked:
		return false
	}
	return rec.Backoff == nil || !t.Before(rec.Backoff.NextAt)
}

// recordFault carries ONE failed send through the classification and persists the outcome at the
// day's record (K-5):
//
//   - a PERMANENT fault — a misconfiguration or a rejection repetition cannot change — ends the day
//     after this single, named attempt: blocked, class permanent, with no next attempt due;
//   - a TRANSIENT one backs off with a growing interval and only blocks once the attempt cap is
//     reached.
//
// Either way the outcome is a state the delivery record states honestly (reason, time, attempts), and
// a fresh block raises exactly ONE hint — bundled by the pool, so a fault that holds across several
// undelivered days is one record with a count and a period rather than a row per day and pass.
func (rp *Reporter) recordFault(rec Record, day string, executions int, cause error, now time.Time) {
	class := sendClass(cause)
	b := model.Backoff{}
	if rec.Backoff != nil {
		b = *rec.Backoff
	}
	b, blocked := advanceBackoff(b, now, attemptCap(class))
	b.Reason, b.Class = cause.Error(), class.String()
	if blocked {
		// A blocked record has NO next attempt: it waits for a person, not for a timer. A future
		// moment left here would state something that is never going to happen.
		b.NextAt = time.Time{}
	}

	next := Record{
		Recipient: rp.recipient, Day: day, Status: StatusFailed, Executions: executions,
		Attempts: rec.Attempts + 1, LastAttempt: &now, LastError: cause.Error(), Backoff: &b,
	}
	if blocked {
		next.Status = StatusBlocked
	}
	rp.put(next)

	if next.Status == StatusBlocked {
		rp.logf("devlabd: daily report %s for %s BLOCKED after %s (%s fault) — awaiting an explicit resume: %v",
			day, rp.recipient, attemptPhrase(b.Attempts), b.Class, cause)
		rp.raiseBlocked(cause, now)
		return
	}
	rp.logf("devlabd: daily report %s for %s failed (attempt %d of %d, %s fault, next attempt %s): %v",
		day, rp.recipient, b.Attempts, maxSendAttempts, b.Class, b.NextAt.UTC().Format(time.RFC3339), cause)
}

// sendClass classifies ONE failed send. The classification is faultclass's — this is only where the
// sending seam's own knowledge enters it: a Sender that KNOWS its fault is a misconfiguration wraps
// ErrPermanent, exactly as a caller wraps faultclass.ErrSatisfied to say a goal already holds.
func sendClass(err error) faultclass.Class {
	if errors.Is(err, ErrPermanent) {
		return faultclass.Permanent
	}
	return classify(err)
}

// attemptCap is the attempt budget a fault class earns: a permanent fault exactly one attempt, a
// transient one the episode cap (K-5). The class decides the budget; the growing intervals and the
// blocking itself stay with the ONE backoff schedule.
func attemptCap(class faultclass.Class) int {
	if class == faultclass.Permanent {
		return 1
	}
	return maxSendAttempts
}

// raiseBlocked raises the ONE hint a blocked report delivery is worth. The wording is STABLE — no
// day, no attempt count — so the pool's bundling folds a fault that holds across several undelivered
// days into ONE record; how often and over what period is what that record's count and period say
// (REQ-032.5). Without a pool the block stays visible in the delivery record alone.
func (rp *Reporter) raiseBlocked(cause error, now time.Time) {
	if rp.notices == nil {
		return
	}
	if _, err := rp.notices.Coalesce(runs.Notice{
		Kind:     runs.NoticeDeliveryBlocked,
		Text:     fmt.Sprintf("the daily run report could not be delivered: %v", cause),
		NextStep: "repair the mail path, then resume the report delivery",
		LastAt:   now,
	}); err != nil {
		rp.logf("devlabd: daily-report block hint not recorded: %v", err)
	}
}

// put persists a delivery record and ticks the ONE live stream, so a failure, a block and a delivery
// each reach the surface without a reload. A store that cannot be written is named, never swallowed.
func (rp *Reporter) put(rec Record) {
	if err := rp.ledger.Put(rec); err != nil {
		rp.logf("devlabd: daily-report ledger write %s: %v", rec.Day, err)
		return
	}
	if rp.pub != nil {
		rp.pub.Publish(live.TopicNotices)
	}
}

// Resume is the EXPLICIT resumption K-5 requires: it lifts the block on blocked day-reports and hands
// them back to the retry path — nothing in the system resumes itself. day names one day; an empty day
// resumes every blocked one. The record keeps its history (total attempts, reason, times) and only
// loses its retry EPISODE, which is what earns the resumed day a fresh set of attempts on the next
// pass. It returns the records it actually changed, so a caller can name what it resumed instead of
// claiming more.
func Resume(l *Ledger, day string) ([]Record, error) {
	if l == nil {
		return nil, nil
	}
	all, err := l.List()
	if err != nil {
		return nil, err
	}
	resumed := []Record{}
	for _, r := range all {
		if r.Status != StatusBlocked || (day != "" && r.Day != day) {
			continue
		}
		changed := false
		rec, found, err := l.Update(r.Recipient, r.Day, func(rec *Record) {
			if rec.Status != StatusBlocked {
				return // a concurrent pass already moved this day on: its state wins
			}
			rec.Status, rec.Backoff, changed = StatusFailed, nil, true
		})
		if err != nil {
			return resumed, err
		}
		if found && changed {
			resumed = append(resumed, rec)
		}
	}
	return resumed, nil
}

// rubrics reads the day's findings out of the notice pool (REQ-042.6). An unreadable pool costs
// the report its rubrics, never the report itself — the executions are the substance, the rubrics
// the addition.
func (rp *Reporter) rubrics(day string) []Rubric {
	if rp.notices == nil {
		return nil
	}
	list, err := rp.notices.List()
	if err != nil {
		rp.logf("devlabd: daily-report notices for %s: %v", day, err)
		return nil
	}
	return BuildRubrics(list, day, rp.loc)
}

// buildItems turns a day's results (oldest first) into report items — each result already
// carries its per-repo stage array (the one source of stage truth). Whether an unfinished
// execution merely runs or STANDS on the shared pause is read off the state documents, never
// guessed from the missing end stamp.
func (rp *Reporter) buildItems(summaries []runs.Result) []Item {
	ordered := append([]runs.Result(nil), summaries...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].StartedAt.Before(ordered[j].StartedAt) })

	paused := map[string]bool{}
	if rp.paused != nil {
		if ids, err := rp.paused.PausedIDs(); err == nil {
			paused = ids
		} else {
			rp.logf("devlabd: daily-report pause lookup: %v", err)
		}
	}

	items := make([]Item, 0, len(ordered))
	for _, s := range ordered {
		it := Item{
			RunName:   runName(s),
			TypeLabel: typeLabel(s.Kind),
			StartedAt: s.StartedAt,
			Finished:  s.EndedAt != nil,
			OK:        resultOK(s),
			Paused:    s.EndedAt == nil && paused[s.ID],
			InTokens:  int(s.Usage.InputTokens),
			OutTokens: int(s.Usage.OutputTokens),
			CostUSD:   s.Usage.CostUSD,
		}
		for _, rr := range s.Repos {
			it.Repos = append(it.Repos, RepoLine{Repo: rr.Repo, Stage: deliveryStage(rr)})
		}
		items = append(items, it)
	}
	return items
}

// resultOK reads success off the stage arrays: every repo done and succeeded (the honest
// success formula, REQ-030.4). An empty result is no success.
func resultOK(res runs.Result) bool {
	if len(res.Repos) == 0 {
		return false
	}
	for _, rp := range res.Repos {
		done, ok := model.PipelineSucceeded(rp.Stages)
		if !done || !ok {
			return false
		}
	}
	return true
}

// reportDay is the calendar day an execution belongs to: the day it FINISHED (so a run that finishes
// after its start day is reported on the day it actually completed), falling back to its start day
// while it is still unfinished.
func (rp *Reporter) reportDay(s runs.Result) string {
	t := s.StartedAt
	if s.EndedAt != nil {
		t = *s.EndedAt
	}
	return dayKey(t.In(rp.loc))
}

func (rp *Reporter) dayLink() string {
	if rp.linkBase == "" {
		return ""
	}
	return strings.TrimRight(rp.linkBase, "/") + "/#/mercury"
}

// dayKey renders a time as its "YYYY-MM-DD" report day (in whatever location the time already carries).
func dayKey(t time.Time) string { return t.Format("2006-01-02") }

// attemptPhrase states an attempt count the way a person reads it ("1 attempt", "5 attempts").
func attemptPhrase(n int) string {
	if n == 1 {
		return "1 attempt"
	}
	return fmt.Sprintf("%d attempts", n)
}

func runName(s runs.Result) string {
	if n := strings.TrimSpace(s.RunTitle); n != "" {
		return n
	}
	if s.RunID != "" {
		return "Run " + s.RunID
	}
	return "Run"
}

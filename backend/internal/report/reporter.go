package report

import (
	"context"
	"log"
	"sort"
	"strings"
	"time"

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
// a send failure is observed in exactly one place.
type Sender interface {
	Send(ctx context.Context, recipient string, c Content) error
}

// Reporter drives the daily run report. Each pass it buckets executions by the day they belong to and,
// for every CLOSED day (strictly before today) that had at least one execution and has not already
// been delivered, composes and sends exactly one report to the run owner. A restart, a second pass, or
// a later-finishing run never produces a second email for a day, because the ledger seals a delivered
// day; a failed send is recorded visibly and retried on the next pass until it goes through.
//
// It only ever reports closed days: a report for day D is sent once D is over, so no execution can
// still land on D after its report — a run that finishes the next day is attributed to that next day
// (executions are bucketed by when they finished), which is exactly where the requirement wants it.
type Reporter struct {
	recipient string
	execs     Executions
	paused    PausedExecutions
	notices   Notices
	ledger    *Ledger
	sender    Sender

	now      func() time.Time
	loc      *time.Location
	lookback int    // how many closed days back a NEW report may still be sent for (default 1 = yesterday)
	linkBase string // app base URL for the UI deep link ("" ⇒ the report points to the UI in words only)
	logf     func(string, ...any)
}

// Config configures a Reporter. recipient is the Holistic user the runs are assigned to (the owner);
// an empty recipient makes the Reporter inert. Notices feeds the report's rubrics (REQ-042.6) and
// may be nil — the report then carries its executions without them.
type Config struct {
	Recipient string
	Execs     Executions
	// Paused names the currently paused executions (optional — nil ⇒ no pause is claimed).
	Paused   PausedExecutions
	Notices  Notices
	Ledger   *Ledger
	Sender   Sender
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
	// closed day still carrying an undelivered ledger record (a failed send to retry, however old).
	earliest := dayKey(now.AddDate(0, 0, -rp.lookback))
	cand := map[string]bool{}
	for d := range byDay {
		if d < today && d >= earliest {
			cand[d] = true
		}
	}
	if recs, err := rp.ledger.List(); err == nil {
		for _, r := range recs {
			if r.Recipient == rp.recipient && r.Status != StatusSent && r.Day < today {
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

// deliverDay sends (or seals, or records a failure for) exactly one report for day d. It is idempotent
// per day: a day already sent is skipped, so a second pass or a restart cannot double-send.
func (rp *Reporter) deliverDay(ctx context.Context, day string, summaries []runs.Result, now time.Time) {
	rec, _, err := rp.ledger.Get(rp.recipient, day)
	if err != nil {
		rp.logf("devlabd: daily-report ledger read %s: %v", day, err)
		return
	}
	if rec.Status == StatusSent {
		return // sealed — one email per day and recipient
	}
	if len(summaries) == 0 {
		return // a day without any execution produces no message
	}

	items := rp.buildItems(summaries)
	content := Compose(day, items, rp.rubrics(day), rp.dayLink())

	at := now
	attempts := rec.Attempts + 1
	if err := rp.sender.Send(ctx, rp.recipient, content); err != nil {
		rp.logf("devlabd: daily report %s for %s FAILED (attempt %d): %v", day, rp.recipient, attempts, err)
		_ = rp.ledger.Put(Record{
			Recipient: rp.recipient, Day: day, Status: StatusFailed, Executions: len(items),
			Attempts: attempts, LastAttempt: &at, LastError: err.Error(),
		})
		return
	}
	rp.logf("devlabd: daily report %s for %s sent (%d executions)", day, rp.recipient, len(items))
	_ = rp.ledger.Put(Record{
		Recipient: rp.recipient, Day: day, Status: StatusSent, Executions: len(items),
		Attempts: attempts, SentAt: &at, LastAttempt: &at,
	})
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

func runName(s runs.Result) string {
	if n := strings.TrimSpace(s.RunTitle); n != "" {
		return n
	}
	if s.RunID != "" {
		return "Run " + s.RunID
	}
	return "Run"
}

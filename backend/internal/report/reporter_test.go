package report

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"devlab/backend/internal/github"
	"devlab/backend/internal/model"
	"devlab/backend/internal/runs"
)

// --- fakes -----------------------------------------------------------------

type fakeExecs struct {
	summaries []runs.Result
}

func (f *fakeExecs) List() ([]runs.Result, error) { return f.summaries, nil }

type sentMail struct {
	recipient string
	content   Content
}

type fakeSender struct {
	failFirst int   // fail the first N Send calls, then succeed
	failAll   bool  // fail EVERY call — the fault that does not mend itself
	failWith  error // the fault a failing call reports (nil ⇒ a plain transient outage)
	attempts  int
	sent      []sentMail
}

func (f *fakeSender) Send(_ context.Context, recipient string, c Content) error {
	f.attempts++
	if f.failAll || f.attempts <= f.failFirst {
		if f.failWith != nil {
			return f.failWith
		}
		return errors.New("mail service down")
	}
	f.sent = append(f.sent, sentMail{recipient, c})
	return nil
}

// clock is a movable test clock. Passes are driven by advancing it, which is the only way a growing
// backoff interval becomes observable at all — a fixed clock cannot tell "retried too early" from
// "retried on time".
type clock struct{ t time.Time }

func (c *clock) now() time.Time      { return c.t }
func (c *clock) add(d time.Duration) { c.t = c.t.Add(d) }

// logSpy captures every line the Reporter logs, so the volume over simulated hours can be measured
// (K-5: one bundled message instead of dozens of identical repetitions).
type logSpy struct{ lines []string }

func (l *logSpy) logf(format string, args ...any) {
	l.lines = append(l.lines, fmt.Sprintf(format, args...))
}

func (l *logSpy) containing(sub string) []string {
	var out []string
	for _, ln := range l.lines {
		if strings.Contains(ln, sub) {
			out = append(out, ln)
		}
	}
	return out
}

// --- helpers ---------------------------------------------------------------

func at(day int, hour int) time.Time {
	return time.Date(2026, 7, day, hour, 0, 0, 0, time.UTC)
}

// summary builds a finished, successful execution result.
func summary(runID, name string, finished time.Time) runs.Result {
	end := finished
	return runs.Result{
		RunID: runID, RunTitle: name, ID: "r-" + runID,
		StartedAt: finished.Add(-30 * time.Minute), EndedAt: &end,
		Repos: []model.RepoPipeline{{
			Repo:   "svc",
			Stages: []model.StageView{{Stage: model.StageImplement, State: model.StepExecuted}},
		}},
		Usage: model.UsageView{InputTokens: 1000, OutputTokens: 200, CostUSD: 0.05},
	}
}

func newReporter(t *testing.T, execs Executions, sender Sender, ledger *Ledger, now time.Time) *Reporter {
	t.Helper()
	return NewReporter(Config{
		Recipient: "owner", Execs: execs, Ledger: ledger, Sender: sender,
		Now: func() time.Time { return now }, Loc: time.UTC, Lookback: 3,
		Logf: func(string, ...any) {},
	})
}

// harness is the K-5 measuring rig: a Reporter over a movable clock, a real notice pool and a captured
// log — the three places the retry policy has to be honest in (record, hint, log).
type harness struct {
	clk    *clock
	ledger *Ledger
	sender *fakeSender
	pool   *runs.NoticeStore
	logs   *logSpy
	rp     *Reporter
}

func newHarness(t *testing.T, sender *fakeSender, start time.Time, work ...runs.Result) *harness {
	t.Helper()
	h := &harness{
		clk:    &clock{t: start},
		ledger: NewLedgerAt(filepath.Join(t.TempDir(), "l.json")),
		sender: sender,
		pool:   noticePool(t),
		logs:   &logSpy{},
	}
	h.rp = NewReporter(Config{
		Recipient: "owner",
		Execs:     &fakeExecs{summaries: work},
		Notices:   h.pool,
		Ledger:    h.ledger,
		Sender:    sender,
		Now:       h.clk.now,
		Loc:       time.UTC, Lookback: 3, Logf: h.logs.logf,
	})
	return h
}

// pass performs one Reporter pass at the current clock.
func (h *harness) pass() { h.rp.tick(context.Background()) }

// hours drives the loop the way the daemon does: one pass every `every` over `span` of simulated
// time. This is the measurement K-5 asks for — a fault that holds must not produce a pass-by-pass
// repetition over hours.
func (h *harness) hours(span, every time.Duration) {
	for elapsed := time.Duration(0); elapsed < span; elapsed += every {
		h.pass()
		h.clk.add(every)
	}
}

func (h *harness) record(t *testing.T, day string) Record {
	t.Helper()
	rec, ok, err := h.ledger.Get("owner", day)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatalf("no delivery record for %s", day)
	}
	return rec
}

// blockHints returns the pool's blocked-delivery records — the hint a blocked report raises.
func (h *harness) blockHints(t *testing.T) []runs.Notice {
	t.Helper()
	all, err := h.pool.List()
	if err != nil {
		t.Fatal(err)
	}
	var out []runs.Notice
	for _, n := range all {
		if n.Kind == runs.NoticeDeliveryBlocked {
			out = append(out, n)
		}
	}
	return out
}

// --- the required behaviours ----------------------------------------------

// A day on which nothing ran produces no email — neither an empty store, nor a day whose only
// executions belong to the still-open current day.
func TestNoExecutionsNoEmail(t *testing.T) {
	ledger := NewLedgerAt(filepath.Join(t.TempDir(), "l.json"))
	sender := &fakeSender{}

	// (a) empty store.
	rp := newReporter(t, &fakeExecs{}, sender, ledger, at(27, 1))
	rp.tick(context.Background())

	// (b) executions exist, but only for today (not a closed day).
	execs := &fakeExecs{summaries: []runs.Result{summary("a", "Today run", at(27, 9))}}
	rp2 := newReporter(t, execs, sender, ledger, at(27, 23))
	rp2.tick(context.Background())

	if len(sender.sent) != 0 {
		t.Fatalf("no email expected, got %d", len(sender.sent))
	}
}

// A closed day with several executions produces exactly one email covering all of them.
func TestMultipleExecutionsOneEmail(t *testing.T) {
	ledger := NewLedgerAt(filepath.Join(t.TempDir(), "l.json"))
	sender := &fakeSender{}
	execs := &fakeExecs{summaries: []runs.Result{
		summary("a", "Nightly axioms", at(26, 20)),
		summary("b", "Fix login", at(26, 22)),
	}}
	rp := newReporter(t, execs, sender, ledger, at(27, 0)) // just after midnight into the 27th

	rp.tick(context.Background())
	rp.tick(context.Background()) // a second pass in the same day must not send again

	if len(sender.sent) != 1 {
		t.Fatalf("want exactly 1 email, got %d", len(sender.sent))
	}
	body := sender.sent[0].content.Text
	if !strings.Contains(body, "Nightly axioms") || !strings.Contains(body, "Fix login") {
		t.Errorf("the one email should cover both executions:\n%s", body)
	}
	if rec, ok, _ := ledger.Get("owner", "2026-07-26"); !ok || rec.Status != StatusSent || rec.Executions != 2 {
		t.Errorf("ledger not sealed correctly: %+v ok=%v", rec, ok)
	}
}

// A restart on the same day (a fresh Reporter over the same ledger) sends no second email.
func TestRestartSameDayNoSecondEmail(t *testing.T) {
	dir := t.TempDir()
	ledgerPath := filepath.Join(dir, "l.json")
	execs := &fakeExecs{summaries: []runs.Result{summary("a", "Nightly", at(26, 20))}}

	sender1 := &fakeSender{}
	rp1 := newReporter(t, execs, sender1, NewLedgerAt(ledgerPath), at(27, 0))
	rp1.tick(context.Background())
	if len(sender1.sent) != 1 {
		t.Fatalf("first run should send once, got %d", len(sender1.sent))
	}

	// Process restarts: brand-new Reporter and brand-new Ledger handle, same file, same day.
	sender2 := &fakeSender{}
	rp2 := newReporter(t, execs, sender2, NewLedgerAt(ledgerPath), at(27, 6))
	rp2.tick(context.Background())
	if len(sender2.sent) != 0 {
		t.Fatalf("restart on the same day must not resend, got %d", len(sender2.sent))
	}
}

// A failed send is recorded visibly and retried once its backoff interval has passed — and once it
// goes through the day is sealed, so it is never duplicated.
func TestFailedSendIsVisibleAndRetriedWithoutDuplicate(t *testing.T) {
	// The first attempt fails, then the mail service recovers.
	h := newHarness(t, &fakeSender{failFirst: 1}, at(27, 0), summary("a", "Nightly", at(26, 20)))

	// Pass 1: the send fails. It is visible in the ledger as a failure with the reason, not silent.
	h.pass()
	rec := h.record(t, "2026-07-26")
	if rec.Status != StatusFailed || rec.LastError == "" || rec.Attempts != 1 {
		t.Fatalf("failure not visible in ledger: %+v", rec)
	}
	if len(h.sender.sent) != 0 {
		t.Fatalf("nothing should have been delivered yet")
	}
	if rec.Backoff == nil || !rec.Backoff.NextAt.After(h.clk.now()) {
		t.Fatalf("a failed send must name WHEN it may be retried: %+v", rec.Backoff)
	}

	// A pass inside the interval does not attempt again — that is the growing distance, not a skip.
	h.pass()
	if h.sender.attempts != 1 {
		t.Fatalf("a pass inside the backoff interval must not attempt again, attempts=%d", h.sender.attempts)
	}

	// Once the named moment has come the retry runs, succeeds and seals the day.
	h.clk.t = rec.Backoff.NextAt
	h.pass()
	rec = h.record(t, "2026-07-26")
	if rec.Status != StatusSent || rec.Attempts != 2 || rec.LastError != "" || rec.Backoff != nil {
		t.Fatalf("retry did not seal cleanly: %+v", rec)
	}
	if len(h.sender.sent) != 1 {
		t.Fatalf("want exactly one delivery after retry, got %d", len(h.sender.sent))
	}

	// Sealed — no duplicate however many more passes run, over however many hours.
	h.hours(24*time.Hour, 10*time.Minute)
	if len(h.sender.sent) != 1 {
		t.Fatalf("sealed day must never duplicate, got %d", len(h.sender.sent))
	}
}

// K-5/REQ-032.1 — a PERMANENT fault gets exactly ONE attempt with a named end. The production case is
// the unreadable internal mail secret: a misconfiguration, wrapped as permanent by the mail seam. The
// old reporter re-sent it on every pass for every undelivered day, for ever.
func TestPermanentFaultGetsOneAttemptAndBlocks(t *testing.T) {
	cause := fmt.Errorf("%w: %w", ErrPermanent, errors.New("mailer: no internal secret configured"))
	h := newHarness(t, &fakeSender{failAll: true, failWith: cause}, at(27, 0), summary("a", "Nightly", at(26, 20)))

	// A full day of passes at the daemon's rhythm: 144 opportunities to repeat the mistake.
	h.hours(24*time.Hour, 10*time.Minute)

	if h.sender.attempts != 1 {
		t.Fatalf("a permanent fault must be attempted exactly once, got %d attempts", h.sender.attempts)
	}
	rec := h.record(t, "2026-07-26")
	if rec.Status != StatusBlocked {
		t.Fatalf("status = %q, want %q", rec.Status, StatusBlocked)
	}
	if rec.Backoff == nil || rec.Backoff.Class != "permanent" || rec.Backoff.Attempts != 1 {
		t.Fatalf("the block must name its class and attempts: %+v", rec.Backoff)
	}
	if !rec.Backoff.NextAt.IsZero() {
		t.Errorf("a blocked day must not claim a next attempt: %v", rec.Backoff.NextAt)
	}
	if rec.LastError == "" || rec.LastAttempt == nil || rec.Attempts != 1 {
		t.Errorf("the block must state reason, time and attempts: %+v", rec)
	}

	// The log names the end ONCE — not 144 times.
	blocked := h.logs.containing("BLOCKED")
	if len(blocked) != 1 {
		t.Fatalf("want exactly one named end in the log, got %d:\n%s", len(blocked), strings.Join(h.logs.lines, "\n"))
	}
	if len(h.logs.lines) != 1 {
		t.Errorf("a blocked day must stay silent afterwards, got %d log lines:\n%s",
			len(h.logs.lines), strings.Join(h.logs.lines, "\n"))
	}
	// And exactly ONE hint the user can act on.
	if hints := h.blockHints(t); len(hints) != 1 || hints[0].NextStep == "" {
		t.Fatalf("want one actionable blocked hint, got %+v", hints)
	}
}

// K-5 — the fault classes faultclass itself names (404/403/422) reach the same named end through the
// same path: no repetition, a blocked record, one hint.
func TestRejectedSendIsPermanentByItsStatus(t *testing.T) {
	h := newHarness(t, &fakeSender{failAll: true, failWith: &github.StatusError{Status: 422, Msg: "recipient rejected"}},
		at(27, 0), summary("a", "Nightly", at(26, 20)))

	h.hours(6*time.Hour, 10*time.Minute)

	if h.sender.attempts != 1 {
		t.Fatalf("a 422 must be attempted exactly once, got %d", h.sender.attempts)
	}
	if rec := h.record(t, "2026-07-26"); rec.Status != StatusBlocked || rec.Backoff.Class != "permanent" {
		t.Fatalf("record = %+v / %+v", rec, rec.Backoff)
	}
}

// K-5/REQ-032.3 — a TRANSIENT fault backs off with provably growing intervals and ends in `blocked`
// after the attempt cap, with reason, time and attempts. It never keeps trying for ever.
func TestTransientFaultBacksOffThenBlocks(t *testing.T) {
	h := newHarness(t, &fakeSender{failAll: true}, at(27, 0), summary("a", "Nightly", at(26, 20)))

	var gaps []time.Duration
	for attempt := 1; attempt <= maxSendAttempts; attempt++ {
		h.pass()
		rec := h.record(t, "2026-07-26")
		if h.sender.attempts != attempt {
			t.Fatalf("attempt %d: sender saw %d attempts", attempt, h.sender.attempts)
		}
		if rec.Backoff == nil || rec.Backoff.Attempts != attempt || rec.Backoff.Class != "transient" {
			t.Fatalf("attempt %d: backoff = %+v", attempt, rec.Backoff)
		}
		if attempt == maxSendAttempts {
			break
		}
		if rec.Status != StatusFailed {
			t.Fatalf("attempt %d of %d must not block yet: %+v", attempt, maxSendAttempts, rec)
		}
		gap := rec.Backoff.NextAt.Sub(rec.Backoff.LastAt)
		gaps = append(gaps, gap)
		h.clk.t = rec.Backoff.NextAt // wait exactly as long as the record asks
	}

	// The distances grow — that is what makes the retry a backoff and not a hammer.
	for i := 1; i < len(gaps); i++ {
		if gaps[i] <= gaps[i-1] {
			t.Fatalf("the retry intervals do not grow: %v", gaps)
		}
	}
	rec := h.record(t, "2026-07-26")
	if rec.Status != StatusBlocked || rec.Attempts != maxSendAttempts {
		t.Fatalf("the exhausted day must be blocked with its attempts: %+v", rec)
	}
	if rec.LastError == "" || rec.LastAttempt == nil || !rec.Backoff.NextAt.IsZero() {
		t.Fatalf("the block must state reason and time and claim no next attempt: %+v / %+v", rec, rec.Backoff)
	}

	// And it stays put: hours of further passes add nothing.
	before := h.sender.attempts
	h.hours(24*time.Hour, 10*time.Minute)
	if h.sender.attempts != before {
		t.Fatalf("a blocked day must not be retried, attempts %d → %d", before, h.sender.attempts)
	}
	if hints := h.blockHints(t); len(hints) != 1 {
		t.Fatalf("want exactly one blocked hint, got %d", len(hints))
	}
}

// K-5 measurement — the same fault over SEVERAL undelivered days and hours of passes produces ONE
// bundled hint with a count and a period, not a row (or a log line) per day and pass. The old reporter
// wrote ~144 identical FAILED lines per day AND per undelivered day.
func TestABrokenMailPathIsReportedOnceNotPerPass(t *testing.T) {
	cause := fmt.Errorf("%w: %w", ErrPermanent, errors.New("mailer: no internal secret configured"))
	h := newHarness(t, &fakeSender{failAll: true, failWith: cause}, at(27, 0),
		summary("a", "Nightly", at(24, 20)),
		summary("b", "Nightly", at(25, 20)),
		summary("c", "Nightly", at(26, 20)))

	h.hours(24*time.Hour, 10*time.Minute) // 144 passes over three undelivered days

	if h.sender.attempts != 3 {
		t.Fatalf("three undelivered days must cost three attempts in total, got %d", h.sender.attempts)
	}
	for _, day := range []string{"2026-07-24", "2026-07-25", "2026-07-26"} {
		if rec := h.record(t, day); rec.Status != StatusBlocked {
			t.Errorf("%s: status = %q, want blocked", day, rec.Status)
		}
	}
	hints := h.blockHints(t)
	if len(hints) != 1 {
		t.Fatalf("want ONE bundled hint, got %d: %+v", len(hints), hints)
	}
	if hints[0].Count != 3 {
		t.Errorf("the hint must count the occurrences, got %d", hints[0].Count)
	}
	if len(h.logs.lines) != 3 {
		t.Errorf("want one log line per blocked day, got %d:\n%s", len(h.logs.lines), strings.Join(h.logs.lines, "\n"))
	}

	// A day later the operator resumes, the fault still holds — and the SAME record grows its count
	// and stretches its period instead of a second row appearing (REQ-032.5).
	if _, err := Resume(h.ledger, ""); err != nil {
		t.Fatal(err)
	}
	h.hours(24*time.Hour, 10*time.Minute)

	hints = h.blockHints(t)
	if len(hints) != 1 {
		t.Fatalf("the recurring fault must stay ONE record, got %d", len(hints))
	}
	if hints[0].Count != 6 || !hints[0].LastAt.After(hints[0].FirstAt) {
		t.Errorf("the hint must carry the count AND the period: count=%d %s..%s",
			hints[0].Count, hints[0].FirstAt, hints[0].LastAt)
	}
}

// K-5/REQ-032.3 — a blocked delivery is resumed EXPLICITLY, and only then does it try again. The
// resumed day keeps its history and earns a fresh set of attempts.
func TestBlockedDeliveryResumesOnlyWhenAsked(t *testing.T) {
	sender := &fakeSender{failAll: true}
	h := newHarness(t, sender, at(27, 0), summary("a", "Nightly", at(26, 20)))
	h.hours(2*time.Hour, time.Minute) // fails, backs off, blocks
	if rec := h.record(t, "2026-07-26"); rec.Status != StatusBlocked {
		t.Fatalf("precondition: the day must be blocked, got %+v", rec)
	}
	spent := sender.attempts

	// Resuming a day that is NOT blocked changes nothing and claims nothing.
	if resumed, err := Resume(h.ledger, "2026-07-25"); err != nil || len(resumed) != 0 {
		t.Fatalf("resume of an unblocked day = %+v, %v", resumed, err)
	}

	// The explicit resumption hands the day back to the retry path.
	resumed, err := Resume(h.ledger, "2026-07-26")
	if err != nil {
		t.Fatal(err)
	}
	if len(resumed) != 1 || resumed[0].Status != StatusFailed || resumed[0].Backoff != nil {
		t.Fatalf("resumed = %+v", resumed)
	}
	if resumed[0].Attempts != spent || resumed[0].LastError == "" {
		t.Errorf("a resumed day keeps its history: %+v", resumed[0])
	}

	// It now tries again — and, the mail path being repaired, gets through.
	sender.failAll = false
	h.pass()
	if len(sender.sent) != 1 {
		t.Fatalf("the resumed day must be attempted again, deliveries = %d", len(sender.sent))
	}
	rec := h.record(t, "2026-07-26")
	if rec.Status != StatusSent || rec.Attempts != spent+1 {
		t.Fatalf("the resumed day did not seal: %+v", rec)
	}
}

// A resumption without a named day lifts EVERY blocked day — the one gesture the surface needs when a
// mail path was broken for a while.
func TestResumeWithoutADayLiftsEveryBlockedDay(t *testing.T) {
	cause := fmt.Errorf("%w: %w", ErrPermanent, errors.New("mailer: no internal secret configured"))
	h := newHarness(t, &fakeSender{failAll: true, failWith: cause}, at(27, 0),
		summary("a", "Nightly", at(25, 20)), summary("b", "Nightly", at(26, 20)))
	h.pass()

	resumed, err := Resume(h.ledger, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(resumed) != 2 {
		t.Fatalf("want both blocked days resumed, got %+v", resumed)
	}
	for _, day := range []string{"2026-07-25", "2026-07-26"} {
		if rec := h.record(t, day); rec.Status != StatusFailed || rec.Backoff != nil {
			t.Errorf("%s: %+v", day, rec)
		}
	}
	// Idempotent: nothing is blocked any more, so a second resumption claims nothing.
	if again, err := Resume(h.ledger, ""); err != nil || len(again) != 0 {
		t.Fatalf("second resume = %+v, %v", again, err)
	}
}

// A day the ledger reports as sent is never resumable — the seal outlives every gesture.
func TestResumeNeverReopensADeliveredDay(t *testing.T) {
	h := newHarness(t, &fakeSender{}, at(27, 0), summary("a", "Nightly", at(26, 20)))
	h.pass()
	if rec := h.record(t, "2026-07-26"); rec.Status != StatusSent {
		t.Fatalf("precondition: %+v", rec)
	}
	if resumed, err := Resume(h.ledger, ""); err != nil || len(resumed) != 0 {
		t.Fatalf("a delivered day must not be resumable: %+v %v", resumed, err)
	}
	h.hours(24*time.Hour, 10*time.Minute)
	if len(h.sender.sent) != 1 {
		t.Fatalf("deliveries = %d, want 1", len(h.sender.sent))
	}
}

// An unwritable notice pool costs the block its hint, never the honest record: the blocked state and
// its reason stay in the ledger, and the pass does not fall over.
func TestBlockSurvivesAnUnwritableNoticePool(t *testing.T) {
	cause := fmt.Errorf("%w: %w", ErrPermanent, errors.New("mailer: no internal secret configured"))
	ledger := NewLedgerAt(filepath.Join(t.TempDir(), "l.json"))
	logs := &logSpy{}
	rp := NewReporter(Config{
		Recipient: "owner",
		Execs:     &fakeExecs{summaries: []runs.Result{summary("a", "Nightly", at(26, 20))}},
		Notices:   brokenNotices{},
		Ledger:    ledger, Sender: &fakeSender{failAll: true, failWith: cause},
		Now: func() time.Time { return at(27, 0) },
		Loc: time.UTC, Lookback: 3, Logf: logs.logf,
	})
	rp.tick(context.Background())

	rec, ok, err := ledger.Get("owner", "2026-07-26")
	if err != nil || !ok || rec.Status != StatusBlocked || rec.LastError == "" {
		t.Fatalf("the block must be recorded regardless of the pool: %+v ok=%v err=%v", rec, ok, err)
	}
	if len(logs.containing("hint not recorded")) != 1 {
		t.Errorf("the unwritable pool must be named once: %s", strings.Join(logs.lines, "\n"))
	}
}

// A run that finishes the day AFTER it started is reported on the day it completed, not the day it
// began — and it does not reopen the earlier, already-sent report.
func TestRunFinishingNextDayGoesToNextDayReport(t *testing.T) {
	ledger := NewLedgerAt(filepath.Join(t.TempDir(), "l.json"))
	sender := &fakeSender{}

	a := summary("a", "Same-day run", at(26, 20)) // started & finished the 26th
	bEnd := at(27, 2)
	b := runs.Result{RunID: "b", RunTitle: "Carry-over migration", ID: "r-b",
		StartedAt: at(26, 23), EndedAt: &bEnd} // started 26th, finished 27th
	execs := &fakeExecs{summaries: []runs.Result{a, b}}

	// End of the 26th (now in the 27th): only the same-day run is reported for the 26th.
	rp := newReporter(t, execs, sender, ledger, at(27, 5))
	rp.tick(context.Background())
	if len(sender.sent) != 1 || sender.sent[0].content.Subject == "" {
		t.Fatalf("want one report for the 26th, got %d", len(sender.sent))
	}
	if !strings.Contains(sender.sent[0].content.Subject, "2026-07-26") {
		t.Errorf("first report should be for 2026-07-26: %q", sender.sent[0].content.Subject)
	}
	if strings.Contains(sender.sent[0].content.Text, "Carry-over migration") {
		t.Errorf("the carry-over run must NOT be in the 26th's report")
	}

	// End of the 27th (now in the 28th): the carry-over run appears in the 27th's report.
	rp2 := newReporter(t, execs, sender, ledger, at(28, 5))
	rp2.tick(context.Background())
	if len(sender.sent) != 2 {
		t.Fatalf("want a second report for the 27th, got %d", len(sender.sent))
	}
	second := sender.sent[1].content
	if !strings.Contains(second.Subject, "2026-07-27") || !strings.Contains(second.Text, "Carry-over migration") {
		t.Errorf("carry-over run should appear in the 27th's report: %q\n%s", second.Subject, second.Text)
	}
}

// buildItems fills per-repo delivery stages straight off the result's stage arrays.
func TestBuildItemsFillsRepoStages(t *testing.T) {
	s := summary("a", "Run A", at(26, 20))
	s.Repos = []model.RepoPipeline{
		{Repo: "devlab", Stages: []model.StageView{
			{Stage: model.StageImplement, State: model.StepExecuted},
			{Stage: model.StageDeliverDev, State: model.StepExecuted},
		}},
		{Repo: "aigentic", Stages: []model.StageView{
			{Stage: model.StageImplement, State: model.StepExecuted},
			{Stage: model.StagePullRequest, State: model.StepExecuted},
		}},
	}
	rp := newReporter(t, &fakeExecs{summaries: []runs.Result{s}}, &fakeSender{}, NewLedgerAt(filepath.Join(t.TempDir(), "l.json")), at(27, 0))
	items := rp.buildItems([]runs.Result{s})
	if len(items) != 1 || len(items[0].Repos) != 2 {
		t.Fatalf("items=%+v", items)
	}
	if items[0].Repos[0].Stage != "deployed" || items[0].Repos[1].Stage != "PR opened" {
		t.Errorf("stages = %+v", items[0].Repos)
	}
}

// fakePaused answers the pause seam over a fixed id set.
type fakePaused struct {
	ids map[string]bool
	err error
}

func (f fakePaused) PausedIDs() (map[string]bool, error) { return f.ids, f.err }

// An unfinished execution is only named as paused when a state DOCUMENT says so — the missing end
// stamp alone never earns the word (REQ-040.4: the one pause vocabulary, stated where it is true).
func TestPausedIsReadOffTheDocumentsNotGuessed(t *testing.T) {
	running := summary("a", "Still running", at(26, 20))
	running.ID = "exec_running"
	running.EndedAt = nil
	standing := summary("b", "Standing on the limit", at(26, 21))
	standing.ID = "exec_paused"
	standing.EndedAt = nil

	ledger := NewLedgerAt(filepath.Join(t.TempDir(), "l.json"))
	all := []runs.Result{running, standing}

	// (a) no pause seam wired ⇒ nothing claims a pause.
	rp := newReporter(t, &fakeExecs{summaries: all}, &fakeSender{}, ledger, at(27, 0))
	for _, it := range rp.buildItems(all) {
		if it.Paused {
			t.Errorf("%s claimed a pause without a document saying so", it.RunName)
		}
	}

	// (b) the documents name exactly one paused execution.
	rp2 := NewReporter(Config{
		Recipient: "owner", Execs: &fakeExecs{summaries: all}, Ledger: ledger, Sender: &fakeSender{},
		Paused: fakePaused{ids: map[string]bool{"exec_paused": true}},
		Now:    func() time.Time { return at(27, 0) }, Loc: time.UTC, Lookback: 3,
		Logf: func(string, ...any) {},
	})
	items := rp2.buildItems(all)
	if len(items) != 2 {
		t.Fatalf("items = %d, want 2", len(items))
	}
	if items[0].Paused {
		t.Errorf("the running execution must not read as paused")
	}
	if !items[1].Paused {
		t.Errorf("the execution whose document is paused must say so")
	}
	if body := Compose("2026-07-26", items, nil, "").Text; !strings.Contains(body, "paused on the usage limit") {
		t.Errorf("the report never names the pause:\n%s", body)
	}

	// (c) a FINISHED execution never reads as paused, even if a stale document lingers.
	done := summary("c", "Finished", at(26, 22))
	done.ID = "exec_paused"
	if rp2.buildItems([]runs.Result{done})[0].Paused {
		t.Errorf("a finished execution must not read as paused")
	}
}

// A day whose report never went out and whose executions have since left the results pool cannot be
// composed from anything. It used to be skipped SILENTLY on every pass — the record stayed
// undelivered for ever while the resume told the operator "the next pass tries again", a promise the
// pass could not keep. Measured on the live record of 2026-07-26 (51 attempts, executions archived).
func TestADayWhoseExecutionsAreGoneIsSettledInsteadOfSkippedSilently(t *testing.T) {
	// A first pass with material, and a sender that refuses: the day ends up undelivered.
	sender := &fakeSender{failAll: true, failWith: errors.New("mailer: 400 Bad Request: A from user is required")}
	h := newHarness(t, sender, at(28, 9), summary("run_a", "A", at(27, 12)))
	h.pass()
	rec, _, err := h.ledger.Get("owner", "2026-07-27")
	if err != nil {
		t.Fatal(err)
	}
	if rec.Status != StatusFailed {
		t.Fatalf("the day must be undelivered first, is %q", rec.Status)
	}

	// Now the material is gone (the results pool no longer holds that day) and the day is due again.
	h.rp = NewReporter(Config{
		Recipient: "owner", Execs: &fakeExecs{}, Notices: h.pool, Ledger: h.ledger,
		Sender: sender, Now: h.clk.now, Loc: time.UTC, Lookback: 3, Logf: h.logs.logf,
	})
	h.clk.t = at(28, 23)
	h.pass()

	rec, _, err = h.ledger.Get("owner", "2026-07-27")
	if err != nil {
		t.Fatal(err)
	}
	if rec.Status != StatusBlocked {
		t.Fatalf("status = %q, want blocked — a day that can never be composed is not pending work", rec.Status)
	}
	if !strings.Contains(rec.LastError, "no longer in the results pool") {
		t.Fatalf("the reason is not named: %q", rec.LastError)
	}
	if rec.Backoff == nil || rec.Backoff.Class != "permanent" || !rec.Backoff.NextAt.IsZero() {
		t.Fatalf("a next attempt is promised that will never come: %+v", rec.Backoff)
	}
	if len(h.logs.containing("no longer in the results pool")) == 0 {
		t.Fatalf("the outcome was silent:\n%s", strings.Join(h.logs.lines, "\n"))
	}

	// And it does not loop: a further pass adds no second outcome.
	before := len(h.logs.lines)
	h.clk.t = at(29, 9)
	h.pass()
	if got := len(h.logs.lines) - before; got != 0 {
		t.Fatalf("the blocked day speaks again on the next pass (%d lines) — that is the loop it replaces", got)
	}
}

// A day that never had a record and has no executions stays correctly silent — the fix above must not
// turn ordinary quiet days into blocked records.
func TestADayWithoutAnyRecordStaysSilent(t *testing.T) {
	h := newHarness(t, &fakeSender{}, at(28, 9))
	h.pass()
	if _, ok, _ := h.ledger.Get("owner", "2026-07-27"); ok {
		t.Fatal("a day nobody ever reported on grew a record")
	}
	if len(h.logs.lines) != 0 {
		t.Fatalf("an empty day was not silent:\n%s", strings.Join(h.logs.lines, "\n"))
	}
}

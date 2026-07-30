package report

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

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
	failFirst int // fail the first N Send calls, then succeed
	attempts  int
	sent      []sentMail
}

func (f *fakeSender) Send(_ context.Context, recipient string, c Content) error {
	f.attempts++
	if f.attempts <= f.failFirst {
		return errors.New("mail service down")
	}
	f.sent = append(f.sent, sentMail{recipient, c})
	return nil
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

// A failed send is recorded visibly and retried on the next pass — and once it goes through the day is
// sealed, so it is never duplicated.
func TestFailedSendIsVisibleAndRetriedWithoutDuplicate(t *testing.T) {
	ledger := NewLedgerAt(filepath.Join(t.TempDir(), "l.json"))
	sender := &fakeSender{failFirst: 1} // first attempt fails, then the mail service recovers
	execs := &fakeExecs{summaries: []runs.Result{summary("a", "Nightly", at(26, 20))}}
	rp := newReporter(t, execs, sender, ledger, at(27, 0))

	// Pass 1: the send fails. It is visible in the ledger as a failure with the reason, not silent.
	rp.tick(context.Background())
	rec, ok, _ := ledger.Get("owner", "2026-07-26")
	if !ok || rec.Status != StatusFailed || rec.LastError == "" || rec.Attempts != 1 {
		t.Fatalf("failure not visible in ledger: %+v ok=%v", rec, ok)
	}
	if len(sender.sent) != 0 {
		t.Fatalf("nothing should have been delivered yet")
	}

	// Pass 2: retried, now succeeds and is sealed.
	rp.tick(context.Background())
	rec, _, _ = ledger.Get("owner", "2026-07-26")
	if rec.Status != StatusSent || rec.Attempts != 2 || rec.LastError != "" {
		t.Fatalf("retry did not seal cleanly: %+v", rec)
	}
	if len(sender.sent) != 1 {
		t.Fatalf("want exactly one delivery after retry, got %d", len(sender.sent))
	}

	// Pass 3: sealed — no duplicate however many more passes run.
	rp.tick(context.Background())
	if len(sender.sent) != 1 {
		t.Fatalf("sealed day must never duplicate, got %d", len(sender.sent))
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

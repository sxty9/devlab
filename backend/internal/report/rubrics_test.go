package report

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"devlab/backend/internal/model"
	"devlab/backend/internal/runs"
)

// noticePool builds a real notice pool over a temp file — the fixtures below are exactly the
// records the delivery chain (B3), the delivery keeper (B4) and the deploy path raise.
func noticePool(t *testing.T) *runs.NoticeStore {
	t.Helper()
	t.Setenv("DEVLAB_MERCURY_RUNS_NOTICES", filepath.Join(t.TempDir(), "notices.json"))
	return runs.NewNoticeStore(nil)
}

// REQ-042.6: the day's findings are grouped into the three named rubrics, straight out of the
// notice pool. Hints that are not report findings (an automatic assignment) stay out of the mail,
// and a finding from another day is not attributed to this one.
func TestBuildRubricsGroupsTheDaysFindings(t *testing.T) {
	pool := noticePool(t)
	day := time.Date(2026, 7, 26, 9, 0, 0, 0, time.UTC)

	// B3: work implemented, dev delivery missing (K-4/REQ-031) — recurring, hence bundled.
	for i := 0; i < 3; i++ {
		if _, err := pool.Coalesce(runs.Notice{
			Kind: runs.NoticeDeliveryAlarm, Repo: "devlab",
			Text:     "implemented without dev delivery: dev delivery did not run",
			NextStep: "fix the delivery path and resume the execution",
			LastAt:   day.Add(time.Duration(i) * time.Hour),
		}); err != nil {
			t.Fatal(err)
		}
	}
	// B5/B3: the repository cannot go down the generic delivery path (REQ-028.4).
	mustNotice(t, pool, runs.Notice{
		Kind: runs.NoticeStructureViolation, Repo: "presentr",
		Text:   "code-structure violation: the service does not fit the generic delivery path",
		LastAt: day,
	})
	// B4: the emergency route was taken (REQ-033.4).
	mustNotice(t, pool, runs.Notice{
		Kind:   runs.NoticeAdminOverride,
		Text:   "ada overrode the delivery-origin protection for PR #12: hotfix for the broken login",
		LastAt: day,
	})
	// B4: a protection drifted and was restored (REQ-033.5).
	mustNotice(t, pool, runs.Notice{
		Kind: runs.NoticeProtectionDeviation, Repo: "aigentic",
		Text:   "branch protection deviated (required review off) and was restored",
		LastAt: day,
	})
	// Not a report finding: the automatic axiom assignment belongs in the panel.
	mustNotice(t, pool, runs.Notice{Kind: runs.NoticeAssigned, RunName: "Architecture", LastAt: day})
	// Another day's finding.
	mustNotice(t, pool, runs.Notice{
		Kind: runs.NoticeProtectionDeviation, Repo: "studiq", Text: "older deviation",
		FirstAt: day.AddDate(0, 0, -3), LastAt: day.AddDate(0, 0, -3),
	})

	list, err := pool.List()
	if err != nil {
		t.Fatal(err)
	}
	rubrics := BuildRubrics(list, "2026-07-26", time.UTC)

	if len(rubrics) != 3 {
		t.Fatalf("want the three named rubrics, got %d: %+v", len(rubrics), rubrics)
	}
	if rubrics[0].Title != RubricDeliveryAlarms || rubrics[1].Title != RubricOverrides || rubrics[2].Title != RubricProtection {
		t.Errorf("rubric order = %q/%q/%q", rubrics[0].Title, rubrics[1].Title, rubrics[2].Title)
	}
	if len(rubrics[0].Lines) != 2 {
		t.Errorf("delivery alarms = %+v, want the alarm and the structure violation", rubrics[0].Lines)
	}
	// The bundled alarm carries its count and its period (REQ-032.5).
	var bundled RubricLine
	for _, l := range rubrics[0].Lines {
		if l.Count > 1 {
			bundled = l
		}
	}
	if bundled.Count != 3 || !bundled.FirstAt.Equal(day) || !bundled.LastAt.Equal(day.Add(2*time.Hour)) {
		t.Errorf("bundled alarm = %+v", bundled)
	}
	if len(rubrics[2].Lines) != 1 || rubrics[2].Lines[0].Repo != "aigentic" {
		t.Errorf("protection rubric must carry only this day's deviation: %+v", rubrics[2].Lines)
	}
}

// The delivery keeper raises its findings in the older shape — the wording in `Reason`, the
// repository folded into it, no separate Repo field. Those records must reach their rubric with
// their wording intact: the report reads a notice through Message(), not through one chosen field.
func TestBuildRubricsReadsTheOlderNoticeShape(t *testing.T) {
	day := time.Date(2026, 7, 26, 9, 0, 0, 0, time.UTC)
	list := []runs.Notice{
		{Kind: runs.NoticeProtectionDeviation, Reason: "o/drifted: branch protection deviated (review off) and was restored",
			Count: 1, FirstAt: day, LastAt: day},
		{Kind: runs.NoticeAdminOverride, Reason: "ada overrode the delivery-origin protection for PR #12: hotfix",
			Count: 1, FirstAt: day, LastAt: day},
	}
	rubrics := BuildRubrics(list, "2026-07-26", time.UTC)
	if len(rubrics) != 2 {
		t.Fatalf("both findings must land in their rubric, got %+v", rubrics)
	}
	for _, r := range rubrics {
		if len(r.Lines) != 1 || !strings.Contains(r.Lines[0].Text, "protection") {
			t.Errorf("%s: the wording must survive, got %+v", r.Title, r.Lines)
		}
	}
}

// A finding that started earlier and is still recurring is reported on the day it kept happening —
// what occurred, not where the record was first written.
func TestBuildRubricsCountsAStillRecurringFinding(t *testing.T) {
	first := time.Date(2026, 7, 24, 6, 0, 0, 0, time.UTC)
	n := runs.Notice{
		Kind: runs.NoticeDeliveryBlocked, Repo: "o/x", Text: "pull request #7 is blocked: 502 from origin",
		Count: 40, FirstAt: first, LastAt: time.Date(2026, 7, 26, 22, 0, 0, 0, time.UTC),
	}
	if got := BuildRubrics([]runs.Notice{n}, "2026-07-26", time.UTC); len(got) != 1 || got[0].Lines[0].Count != 40 {
		t.Errorf("a still-recurring blockade belongs in the day's rubric with its full count: %+v", got)
	}
	if got := BuildRubrics([]runs.Notice{n}, "2026-07-20", time.UTC); len(got) != 0 {
		t.Errorf("a day before the finding must stay clean: %+v", got)
	}
}

// The whole way through: the pool's records reach the mail. The reporter reads the notices it was
// given and the composed message names all three rubrics.
func TestReportCarriesTheRubrics(t *testing.T) {
	pool := noticePool(t)
	day := time.Date(2026, 7, 26, 20, 0, 0, 0, time.UTC)
	mustNotice(t, pool, runs.Notice{
		Kind: runs.NoticeDeliveryAlarm, Repo: "devlab", Text: "implemented without dev delivery", LastAt: day,
	})
	mustNotice(t, pool, runs.Notice{Kind: runs.NoticeAdminOverride, Text: "ada overrode the protection for PR #12", LastAt: day})
	mustNotice(t, pool, runs.Notice{
		Kind: runs.NoticeProtectionDeviation, Repo: "aigentic", Text: "branch protection deviated and was restored", LastAt: day,
	})

	sender := &fakeSender{}
	rp := NewReporter(Config{
		Recipient: "owner",
		Execs:     &fakeExecs{summaries: []runs.Result{summary("a", "Nightly axioms", day)}},
		Notices:   pool,
		Ledger:    NewLedgerAt(filepath.Join(t.TempDir(), "l.json")),
		Sender:    sender,
		Now:       func() time.Time { return at(27, 1) },
		Loc:       time.UTC, Lookback: 3, Logf: func(string, ...any) {},
	})
	rp.tick(tctx())

	if len(sender.sent) != 1 {
		t.Fatalf("want one report, got %d", len(sender.sent))
	}
	body := strings.ToLower(sender.sent[0].content.Text)
	for _, want := range []string{RubricDeliveryAlarms, RubricOverrides, RubricProtection} {
		if !strings.Contains(body, strings.ToLower(want)) {
			t.Errorf("the report must carry the %q rubric:\n%s", want, body)
		}
	}
}

// An unreadable notice pool costs the report its rubrics, never the report itself.
func TestReportSurvivesAnUnreadableNoticePool(t *testing.T) {
	sender := &fakeSender{}
	rp := NewReporter(Config{
		Recipient: "owner",
		Execs:     &fakeExecs{summaries: []runs.Result{summary("a", "Nightly", at(26, 20))}},
		Notices:   brokenNotices{},
		Ledger:    NewLedgerAt(filepath.Join(t.TempDir(), "l.json")),
		Sender:    sender,
		Now:       func() time.Time { return at(27, 1) },
		Loc:       time.UTC, Lookback: 3, Logf: func(string, ...any) {},
	})
	rp.tick(tctx())
	if len(sender.sent) != 1 {
		t.Fatalf("the report must still go out, got %d", len(sender.sent))
	}
}

// brokenNotices is a pool that can neither be read nor written — the worst case for the report:
// neither its rubrics nor a block hint can pass through it, and neither may cost the report itself.
type brokenNotices struct{}

func (brokenNotices) List() ([]runs.Notice, error) { return nil, errBroken }

func (brokenNotices) Coalesce(runs.Notice) (runs.Notice, error) { return runs.Notice{}, errBroken }

var errBroken = errNotice("notice pool unreadable")

type errNotice string

func (e errNotice) Error() string { return string(e) }

func mustNotice(t *testing.T, pool *runs.NoticeStore, n runs.Notice) {
	t.Helper()
	if _, err := pool.Coalesce(n); err != nil {
		t.Fatal(err)
	}
}

// stageResult builds a finished result whose repo reached exactly the given stages.
func stageResult(id string, finished time.Time, repo string, stages ...model.StageView) runs.Result {
	end := finished
	return runs.Result{
		RunID: id, RunTitle: "Run " + id, ID: "r-" + id,
		StartedAt: finished.Add(-time.Hour), EndedAt: &end,
		Repos: []model.RepoPipeline{{Repo: repo, Stages: stages}},
	}
}

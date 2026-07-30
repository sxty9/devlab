package report

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"devlab/backend/internal/live"
	"devlab/backend/internal/model"
	"devlab/backend/internal/runs"
)

func tctx() context.Context { return context.Background() }

type topicSpy struct{ topics []live.Topic }

func (s *topicSpy) Publish(t live.Topic) { s.topics = append(s.topics, t) }

func newSelfCheck(t *testing.T, pool *runs.NoticeStore, execs Executions, now time.Time, spy live.Publisher) *SelfCheck {
	t.Helper()
	return NewSelfCheck(SelfCheckConfig{
		Execs: execs, Notices: pool, Pub: spy, Window: 72 * time.Hour,
		Now: func() time.Time { return now }, Logf: func(string, ...any) {},
	})
}

// REQ-031.4: over a period with runs that CHANGED code but never delivered, the self-check fires —
// as a persistent hint that names the period and the next step. Repeated passes bundle into ONE
// hint whose count and period say how long it has held (REQ-032.5), never a new row per pass.
func TestSelfCheckFiresOnChangesWithoutDelivery(t *testing.T) {
	pool := noticePool(t)
	now := time.Date(2026, 7, 26, 20, 0, 0, 0, time.UTC)
	spy := &topicSpy{}
	execs := &fakeExecs{summaries: []runs.Result{
		stageResult("a", now.Add(-40*time.Hour), "devlab",
			model.StageView{Stage: model.StageImplement, State: model.StepExecuted},
			model.StageView{Stage: model.StageDeliverDev, State: model.StepFailed, Reason: "delivery not yet set up"}),
		stageResult("b", now.Add(-10*time.Hour), "aigentic",
			model.StageView{Stage: model.StageImplement, State: model.StepExecuted},
			model.StageView{Stage: model.StageDeliverDev, State: model.StepNotExecuted, Reason: "publish failed"}),
	}}

	sc := newSelfCheck(t, pool, execs, now, spy)
	fired, err := sc.Tick()
	if err != nil {
		t.Fatal(err)
	}
	if !fired {
		t.Fatal("changes without any delivery over the window must fire the self-check")
	}
	list, _ := pool.List()
	if len(list) != 1 {
		t.Fatalf("want exactly one hint, got %d: %+v", len(list), list)
	}
	n := list[0]
	if n.Kind != runs.NoticeDeliverySelfCheck {
		t.Errorf("kind = %q", n.Kind)
	}
	if !strings.Contains(n.Text, "72h") || !strings.Contains(n.Text, "nothing was delivered") {
		t.Errorf("the hint must name the period and the finding: %q", n.Text)
	}
	if n.NextStep == "" {
		t.Error("the hint must name the next step")
	}
	if n.Read {
		t.Error("the hint arrives unread — it stays until read")
	}
	if len(spy.topics) != 1 || spy.topics[0] != live.TopicNotices {
		t.Errorf("the notices topic must tick after the write, got %v", spy.topics)
	}

	// Two more passes while nothing changed: ONE hint, count 3.
	if _, err := sc.Tick(); err != nil {
		t.Fatal(err)
	}
	if _, err := sc.Tick(); err != nil {
		t.Fatal(err)
	}
	list, _ = pool.List()
	if len(list) != 1 || list[0].Count != 3 {
		t.Fatalf("repeated passes must bundle into one hint, got %d records: %+v", len(list), list)
	}
}

// The self-check stays silent where there is nothing to report: no executions at all, executions
// that changed nothing, and — the important one — a period in which something WAS delivered.
func TestSelfCheckStaysSilentWhenThereIsNothingToReport(t *testing.T) {
	now := time.Date(2026, 7, 26, 20, 0, 0, 0, time.UTC)

	cases := []struct {
		name  string
		execs *fakeExecs
	}{
		{"no executions", &fakeExecs{}},
		{"nothing changed", &fakeExecs{summaries: []runs.Result{
			stageResult("a", now.Add(-5*time.Hour), "devlab",
				model.StageView{Stage: model.StagePreflight, State: model.StepExecuted},
				model.StageView{Stage: model.StageImplement, State: model.StepNotApplicable, Evidence: "already delivered"}),
		}}},
		{"something was delivered", &fakeExecs{summaries: []runs.Result{
			stageResult("a", now.Add(-30*time.Hour), "devlab",
				model.StageView{Stage: model.StageImplement, State: model.StepExecuted},
				model.StageView{Stage: model.StageDeliverDev, State: model.StepFailed, Reason: "gate red"}),
			stageResult("b", now.Add(-4*time.Hour), "aigentic",
				model.StageView{Stage: model.StageImplement, State: model.StepExecuted},
				model.StageView{Stage: model.StageDeliverDev, State: model.StepExecuted}),
		}}},
		{"changes lie outside the window", &fakeExecs{summaries: []runs.Result{
			stageResult("a", now.Add(-100*time.Hour), "devlab",
				model.StageView{Stage: model.StageImplement, State: model.StepExecuted},
				model.StageView{Stage: model.StageDeliverDev, State: model.StepFailed, Reason: "gate red"}),
		}}},
	}
	for _, c := range cases {
		pool := noticePool(t)
		sc := newSelfCheck(t, pool, c.execs, now, nil)
		fired, err := sc.Tick()
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if fired {
			t.Errorf("%s: the self-check must stay silent", c.name)
		}
		if list, _ := pool.List(); len(list) != 0 {
			t.Errorf("%s: no hint expected, got %+v", c.name, list)
		}
	}
}

// A merged delivery counts as delivered even when the dev stage is not part of the stage array
// (the work reached the default branch — that is delivery).
func TestSelfCheckCountsMergedDeliveriesAsDelivered(t *testing.T) {
	now := time.Date(2026, 7, 26, 20, 0, 0, 0, time.UTC)
	merged := now.Add(-2 * time.Hour)
	res := stageResult("a", now.Add(-3*time.Hour), "devlab",
		model.StageView{Stage: model.StageImplement, State: model.StepExecuted},
		model.StageView{Stage: model.StagePullRequest, State: model.StepExecuted})
	res.MergedAt = &merged

	pool := noticePool(t)
	sc := newSelfCheck(t, pool, &fakeExecs{summaries: []runs.Result{res}}, now, nil)
	if fired, _ := sc.Tick(); fired {
		t.Error("a merged delivery is a delivery — the self-check must stay silent")
	}
}

// The self-check's hint flows into the daily report's delivery-alarm rubric: one reporting path,
// not a second one for the self-check.
func TestSelfCheckHintReachesTheReportRubric(t *testing.T) {
	pool := noticePool(t)
	now := time.Date(2026, 7, 26, 20, 0, 0, 0, time.UTC)
	sc := newSelfCheck(t, pool, &fakeExecs{summaries: []runs.Result{
		stageResult("a", now.Add(-2*time.Hour), "devlab",
			model.StageView{Stage: model.StageImplement, State: model.StepExecuted},
			model.StageView{Stage: model.StageDeliverDev, State: model.StepFailed, Reason: "no delivery path"}),
	}}, now, nil)
	if fired, err := sc.Tick(); err != nil || !fired {
		t.Fatalf("Tick: fired=%v err=%v", fired, err)
	}

	list, _ := pool.List()
	rubrics := BuildRubrics(list, "2026-07-26", time.UTC)
	if len(rubrics) != 1 || rubrics[0].Title != RubricDeliveryAlarms {
		t.Fatalf("the self-check finding belongs in the delivery-alarm rubric, got %+v", rubrics)
	}
}

// BLOCKER: the first start of a rebuilt instance raised "changes were implemented but nothing was
// delivered" out of the pre-rebuild ARCHIVE. The archive carries its stage names in the retired
// vocabulary (`dev-deploy`, `push`, `pr`), so the strict comparison never saw a delivery and every
// archived execution read as implemented-and-undelivered — a finding whose next step ("resume")
// has no addressee in a closed past.
//
// The fixture below is one real archived document with the instance stripped out of it (generic
// run id, generic repository, example.invalid links); its SHAPE — per-step `ok` booleans, no
// `status` field, exactly those four step names — is the archive's, and it is read here through
// the production tolerant reader, not through a hand-built Result.
func TestSelfCheckSaysNothingAboutTheArchive(t *testing.T) {
	store := archiveStore(t)
	all, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 || !all[0].Legacy {
		t.Fatalf("the fixture must read as ONE archived execution, got %d: %+v", len(all), all)
	}
	// The premise the blocker rests on: the archive DID implement, and its delivery step is
	// spelled in the retired vocabulary — so a strict comparison finds no delivery at all.
	if !resultChangedCode(all[0]) {
		t.Fatal("the fixture must carry an executed implement stage — otherwise it cannot reproduce the finding")
	}
	if resultDelivered(all[0]) {
		t.Fatal("the fixture's delivery stage must be the RETIRED name — otherwise the case is not the blocker's")
	}

	// now sits inside the default window of the archived record, exactly as the first start did.
	now := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
	pool := noticePool(t)
	sc := newSelfCheck(t, pool, store, now, nil)
	fired, err := sc.Tick()
	if err != nil {
		t.Fatal(err)
	}
	if fired {
		t.Error("the self-check must raise no finding over the archive — its vocabulary does not cover it")
	}
	if list, _ := pool.List(); len(list) != 0 {
		t.Errorf("no hint expected over an archived-only period, got %+v", list)
	}
}

// The control: the SAME situation written in the chain's own vocabulary must still fire. Without
// it, "silent over the archive" could equally be a self-check that stopped working.
func TestSelfCheckStillFiresOnTheSameSituationInTheCurrentVocabulary(t *testing.T) {
	now := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
	current := stageResult("a", now.Add(-6*time.Hour), "service-a",
		model.StageView{Stage: model.StageImplement, State: model.StepExecuted},
		model.StageView{Stage: model.StageDeliverDev, State: model.StepFailed, Reason: "delivery path broken"})

	pool := noticePool(t)
	sc := newSelfCheck(t, pool, &fakeExecs{summaries: []runs.Result{current}}, now, nil)
	if fired, err := sc.Tick(); err != nil || !fired {
		t.Fatalf("a rebuilt execution that implemented and did not deliver must still fire: fired=%v err=%v", fired, err)
	}
}

// An archived record must not silence a REAL finding either: the archive is left out of both
// counters, so a rebuilt execution beside it is judged exactly as if the archive were not there.
func TestSelfCheckArchiveNeitherRaisesNorSilences(t *testing.T) {
	now := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
	store := archiveStore(t)
	archived, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	rebuilt := stageResult("a", now.Add(-6*time.Hour), "service-a",
		model.StageView{Stage: model.StageImplement, State: model.StepExecuted},
		model.StageView{Stage: model.StageDeliverDev, State: model.StepFailed, Reason: "delivery path broken"})

	pool := noticePool(t)
	sc := newSelfCheck(t, pool, &fakeExecs{summaries: append(archived, rebuilt)}, now, nil)
	if fired, err := sc.Tick(); err != nil || !fired {
		t.Fatalf("the archive must not swallow a finding the rebuilt execution carries: fired=%v err=%v", fired, err)
	}
}

// archiveStore opens the production result store over the archive fixture — the same tolerant
// reader the daemon uses, so the test proves the path, not a copy of it.
func archiveStore(t *testing.T) *runs.ResultStore {
	t.Helper()
	t.Setenv("DEVLAB_MERCURY_EXECUTIONS", filepath.Join(t.TempDir(), "executions"))
	t.Setenv("DEVLAB_MERCURY_RUNS_RESULTS", filepath.Join("testdata", "legacy-archive"))
	return runs.NewResultStore(nil)
}

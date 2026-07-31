package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"devlab/backend/internal/auth"
	"devlab/backend/internal/mailer"
	"devlab/backend/internal/model"
	"devlab/backend/internal/report"
	"devlab/backend/internal/runs"
	"devlab/backend/internal/statepath"
)

// reportFixture wires the reporting side over real pools below a temp state root.
func reportFixture(t *testing.T) *Server {
	t.Helper()
	dir := t.TempDir()
	paths := &statepath.Paths{Root: dir}
	t.Setenv("DEVLAB_MERCURY_EXECUTIONS", paths.Executions())
	t.Setenv("DEVLAB_MERCURY_RUNS_NOTICES", paths.NoticesFile())
	t.Setenv("DEVLAB_MERCURY_REPORTS", paths.DailyReports())
	return &Server{
		paths:        paths,
		results:      runs.NewResultStore(paths),
		runNotices:   runs.NewNoticeStore(nil),
		reportLedger: report.NewLedger(paths),
	}
}

// GET report-status surfaces the delivery record of each day — newest first — so a FAILED send is
// visible in the UI instead of vanishing (REQ-042.5).
func TestRunsReportStatusSurfacesFailures(t *testing.T) {
	s := reportFixture(t)
	at := time.Date(2026, 7, 26, 23, 0, 0, 0, time.UTC)
	if err := s.reportLedger.Put(report.Record{
		Recipient: "ada", Day: "2026-07-25", Status: report.StatusSent, Executions: 2, Attempts: 1, SentAt: &at,
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.reportLedger.Put(report.Record{
		Recipient: "ada", Day: "2026-07-26", Status: report.StatusFailed, Executions: 1, Attempts: 3,
		LastAttempt: &at, LastError: "mail service down",
	}); err != nil {
		t.Fatal(err)
	}

	w := httptest.NewRecorder()
	s.runsReportStatus(w, httptest.NewRequest("GET", "/api/mercury/runs/report-status", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", w.Code, w.Body.String())
	}
	var body struct{ Records []report.Record }
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Records) != 2 || body.Records[0].Day != "2026-07-26" {
		t.Fatalf("records = %+v, want newest day first", body.Records)
	}
	if body.Records[0].Status != report.StatusFailed || body.Records[0].LastError == "" || body.Records[0].Attempts != 3 {
		t.Errorf("a failed send must stay visible with its reason and attempts: %+v", body.Records[0])
	}
}

// An empty ledger answers an empty LIST, never null — the UI renders a defined empty state.
func TestRunsReportStatusEmptyIsAList(t *testing.T) {
	s := reportFixture(t)
	w := httptest.NewRecorder()
	s.runsReportStatus(w, httptest.NewRequest("GET", "/api/mercury/runs/report-status", nil))
	if got := w.Body.String(); got != "{\"records\":[]}\n" {
		t.Errorf("empty status = %q, want an empty list", got)
	}
}

// B-15 fail-soft: without a readable internal secret the mail seam returns a NAMED error — it
// never panics and never invents a delivery. K-5 sharpens it: an unreadable secret is a
// MISCONFIGURATION, so the seam names the fault PERMANENT. Without that name the reporter would
// treat a broken mail path as a passing hiccup and compose a send on every pass, for every
// undelivered day, for ever.
func TestMailSenderFailsSoftWithoutSecret(t *testing.T) {
	t.Setenv("DEVLAB_MAIL_INTERNAL_SECRET", "")
	t.Setenv("DEVLAB_MAIL_INTERNAL_SECRET_FILE", filepath.Join(t.TempDir(), "absent"))

	err := mailSender{}.Send(context.Background(), "ada", report.Content{Subject: "s", Text: "t"})
	if err == nil {
		t.Fatal("a send without a secret must fail, not silently succeed")
	}
	if !errors.Is(err, mailer.ErrNoSecret) {
		t.Errorf("the failure must be the named one, got %v", err)
	}
	if !errors.Is(err, report.ErrPermanent) {
		t.Errorf("a misconfigured mail path must be named permanent, got %v", err)
	}
}

// K-5 — the ONE access point resumes a blocked delivery EXPLICITLY: POST lifts the block on the named
// day, answers with what it actually resumed, and hands the day back to the retry path. Nothing else
// unblocks it, so a permanently broken mail path waits for a person.
func TestRunsReportStatusResumesABlockedDay(t *testing.T) {
	s := reportFixture(t)
	failed := time.Date(2026, 7, 27, 0, 5, 0, 0, time.UTC)
	blocked := report.Record{
		Recipient: "ada", Day: "2026-07-26", Status: report.StatusBlocked, Executions: 2, Attempts: 1,
		LastAttempt: &failed, LastError: "mailer: no internal secret configured",
		Backoff: &model.Backoff{
			Class: "permanent", Attempts: 1, Reason: "mailer: no internal secret configured",
			FirstAt: failed, LastAt: failed,
		},
	}
	if err := s.reportLedger.Put(blocked); err != nil {
		t.Fatal(err)
	}
	// A second, untouched day proves the resumption stays with the day it was asked for.
	if err := s.reportLedger.Put(report.Record{
		Recipient: "ada", Day: "2026-07-25", Status: report.StatusBlocked, Attempts: 5,
	}); err != nil {
		t.Fatal(err)
	}

	// The blocked state is visible first — with its reason, its time and its attempts.
	w := httptest.NewRecorder()
	s.runsReportStatus(w, httptest.NewRequest("GET", "/api/mercury/runs/report-status", nil))
	var status struct{ Records []report.Record }
	if err := json.Unmarshal(w.Body.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	if len(status.Records) != 2 || status.Records[0].Status != report.StatusBlocked ||
		status.Records[0].Backoff == nil || status.Records[0].Backoff.Class != "permanent" {
		t.Fatalf("a blocked delivery must be recognisable: %+v", status.Records)
	}

	// Then it is resumed by name.
	w = httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/mercury/runs/report-status", strings.NewReader(`{"day":"2026-07-26"}`))
	s.runsReportStatus(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", w.Code, w.Body.String())
	}
	var body struct{ Resumed []string }
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Resumed) != 1 || body.Resumed[0] != "2026-07-26" {
		t.Fatalf("resumed = %+v, want the one named day", body.Resumed)
	}
	rec, _, _ := s.reportLedger.Get("ada", "2026-07-26")
	if rec.Status != report.StatusFailed || rec.Backoff != nil || rec.Attempts != 1 {
		t.Errorf("the resumed day must be back in the retry path, keeping its history: %+v", rec)
	}
	if other, _, _ := s.reportLedger.Get("ada", "2026-07-25"); other.Status != report.StatusBlocked {
		t.Errorf("the day nobody named must stay blocked: %+v", other)
	}
}

// A resumption with no body lifts every blocked day; one that finds nothing blocked says so instead of
// claiming a success.
func TestRunsReportStatusResumeIsHonestAndIdempotent(t *testing.T) {
	s := reportFixture(t)
	for _, day := range []string{"2026-07-25", "2026-07-26"} {
		if err := s.reportLedger.Put(report.Record{
			Recipient: "ada", Day: day, Status: report.StatusBlocked, Attempts: 5, LastError: "mail service down",
		}); err != nil {
			t.Fatal(err)
		}
	}

	resumed := func() []string {
		t.Helper()
		w := httptest.NewRecorder()
		s.runsReportStatus(w, httptest.NewRequest("POST", "/api/mercury/runs/report-status", nil))
		if w.Code != http.StatusOK {
			t.Fatalf("status = %d: %s", w.Code, w.Body.String())
		}
		var body struct{ Resumed []string }
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		return body.Resumed
	}

	// A malformed body is NAMED, never silently taken as "resume everything".
	w := httptest.NewRecorder()
	s.runsReportStatus(w, httptest.NewRequest("POST", "/api/mercury/runs/report-status", strings.NewReader("{day")))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("a malformed body = %d, want 400: %s", w.Code, w.Body.String())
	}
	if rec, _, _ := s.reportLedger.Get("ada", "2026-07-26"); rec.Status != report.StatusBlocked {
		t.Fatalf("a refused request must change nothing: %+v", rec)
	}

	if got := resumed(); len(got) != 2 {
		t.Fatalf("resumed = %+v, want both blocked days", got)
	}
	if got := resumed(); len(got) != 0 {
		t.Errorf("a second resumption must claim nothing, got %+v", got)
	}
}

// REQ-043 — an agent performs the SAME resumption as the surface: the tool's argument becomes the
// access point's body, and behind it stands the one handler the UI calls. No second path exists.
func TestReportResumeToolReachesTheSameResumption(t *testing.T) {
	s := reportFixture(t)
	if err := s.reportLedger.Put(report.Record{
		Recipient: "ada", Day: "2026-07-26", Status: report.StatusBlocked, Attempts: 1,
		LastError: "mailer: no internal secret configured",
	}); err != nil {
		t.Fatal(err)
	}

	var row mcpTool
	for _, r := range mcpToolRows() {
		if r.Name == "report_resume" {
			row = r
		}
	}
	if row.Name == "" {
		t.Fatal("the resume capability is missing from the MCP tool table")
	}
	u := withRight()
	req, err := mcpRequest(context.Background(), row, &u, mcpCallInfo{}, json.RawMessage(`{"day":"2026-07-26"}`))
	if err != nil {
		t.Fatal(err)
	}

	w := httptest.NewRecorder()
	s.runsReportStatus(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", w.Code, w.Body.String())
	}
	var body struct{ Resumed []string }
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Resumed) != 1 || body.Resumed[0] != "2026-07-26" {
		t.Fatalf("resumed = %+v", body.Resumed)
	}
	if rec, _, _ := s.reportLedger.Get("ada", "2026-07-26"); rec.Status != report.StatusFailed {
		t.Errorf("the agent path did not resume the day: %+v", rec)
	}
}

// REQ-040.3 — the resumption is REACHABLE: the ONE route table binds POST beside GET on the access
// point. Unauthenticated here, so the honest answer is 401 in both directions; an unbound method would
// fall through the table and answer 404 — a handler nobody can call.
func TestReportStatusRouteAcceptsTheResume(t *testing.T) {
	h := (&Server{v: auth.New()}).Handler()
	for _, method := range []string{"GET", "POST"} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(method, "/api/mercury/runs/report-status", nil))
		if w.Code == http.StatusNotFound || w.Code == http.StatusMethodNotAllowed {
			t.Errorf("%s report-status is not routed (%d) — the access exists but nothing can reach it", method, w.Code)
			continue
		}
		if w.Code != http.StatusUnauthorized {
			t.Errorf("%s report-status = %d, want the guard's 401", method, w.Code)
		}
	}
}

// StartReporter is the ONE reporting boot step: the self-check runs even without a provisioned run
// user (it writes hints, it needs no mailbox), while the mail report stays off. REQ-031.4 —
// changes without any delivery over the window reach the user of their own accord.
func TestStartReporterArmsTheSelfCheckWithoutARunUser(t *testing.T) {
	s := reportFixture(t)
	t.Setenv("DEVLAB_RUNS_USER", "")
	t.Setenv("DEVLAB_MERCURY_SELFCHECK_WINDOW", "72h")
	t.Setenv("DEVLAB_MERCURY_SELFCHECK_INTERVAL", "50ms")

	now := time.Now().UTC()
	ended := now.Add(-2 * time.Hour)
	if err := s.results.Put(runs.Result{
		ID: "exec_1", RunID: "run_a", RunTitle: "Nightly", Kind: model.KindAuto,
		StartedAt: ended.Add(-time.Hour), EndedAt: &ended,
		Repos: []model.RepoPipeline{{Repo: "devlab", Stages: []model.StageView{
			{Stage: model.StageImplement, State: model.StepExecuted},
			{Stage: model.StageDeliverDev, State: model.StepFailed, Reason: "delivery not yet set up"},
		}}},
	}); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.StartReporter(ctx)

	deadline := time.Now().Add(3 * time.Second)
	for {
		list, err := s.runNotices.List()
		if err != nil {
			t.Fatal(err)
		}
		if len(list) > 0 {
			if list[0].Kind != runs.NoticeDeliverySelfCheck {
				t.Fatalf("hint = %+v, want the delivery self-check", list[0])
			}
			if list[0].NextStep == "" || list[0].Read {
				t.Errorf("the hint must name a next step and arrive unread: %+v", list[0])
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("the self-check produced no hint")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// The (kind, text) bridge lands in the SAME pool the panel and the report read — and bundles a
// repeated one-liner instead of writing it again (REQ-032.5). Without a pool there is no bridge, so
// the scheduler keeps its logging default rather than dropping events into nothing.
func TestNoticeFuncBridgesOneLinersIntoThePool(t *testing.T) {
	s := reportFixture(t)
	notify := s.NoticeFunc()
	if notify == nil {
		t.Fatal("with a pool there must be a bridge")
	}
	notify("restart-requested", "restart requested — running executions drain out (deadline 2026-07-26T23:00:00Z)")
	notify("restart-requested", "restart requested — running executions drain out (deadline 2026-07-26T23:00:00Z)")
	notify("execution-abandoned", "execution exec_9 of run run_a was abandoned (older than the resume window)")

	list, err := s.runNotices.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Fatalf("want 2 records (the repeat bundled), got %d: %+v", len(list), list)
	}
	for _, n := range list {
		if n.Message() == "" || n.Kind == "" {
			t.Errorf("every bridged notice must carry kind and text: %+v", n)
		}
		if n.Kind == "restart-requested" && n.Count != 2 {
			t.Errorf("the repeated one-liner must bundle, got count %d", n.Count)
		}
	}

	if (&Server{}).NoticeFunc() != nil {
		t.Error("without a pool there must be no bridge")
	}
}

// envDuration takes what operation configures and falls back — loudly in the log, never silently
// disabling a pass — on nonsense.
func TestEnvDurationFallsBack(t *testing.T) {
	t.Setenv("DEVLAB_TEST_DURATION", "45m")
	if got := envDuration("DEVLAB_TEST_DURATION", time.Hour); got != 45*time.Minute {
		t.Errorf("got %s, want 45m", got)
	}
	t.Setenv("DEVLAB_TEST_DURATION", "eventually")
	if got := envDuration("DEVLAB_TEST_DURATION", time.Hour); got != time.Hour {
		t.Errorf("got %s, want the default", got)
	}
	t.Setenv("DEVLAB_TEST_DURATION", "")
	if got := envDuration("DEVLAB_TEST_DURATION", 2*time.Hour); got != 2*time.Hour {
		t.Errorf("got %s, want the default", got)
	}
}

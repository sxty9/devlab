package runs

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"devlab/backend/internal/model"
	"devlab/backend/internal/statepath"
)

// The pre-rebuild archive must stay VIEWABLE (REQ-027.3): every legacy result — including one
// with an empty step list, a legacy ok-bool step and a still-"running" live repo — maps onto a
// defined new-format document. Legacy states are display-only and are never produced anew.
func TestLegacyResultsReadTolerantly(t *testing.T) {
	dir := t.TempDir()
	legacyDir := filepath.Join(dir, "runs-results", "run_legacy1")
	if err := os.MkdirAll(legacyDir, 0o700); err != nil {
		t.Fatal(err)
	}
	fixture, err := os.ReadFile(filepath.Join("testdata", "legacy-result.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacyDir, "2026-07-20T02-00-00.000Z.json"), fixture, 0o600); err != nil {
		t.Fatal(err)
	}
	// A torn file must be skipped, never fatal.
	if err := os.WriteFile(filepath.Join(legacyDir, "torn.json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}

	t.Setenv("DEVLAB_MERCURY_RUNS_RESULTS", filepath.Join(dir, "runs-results"))
	rs := NewResultStore(&statepath.Paths{Root: dir})

	all, err := rs.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("List = %d results, want exactly the one parsable legacy document", len(all))
	}
	res := all[0]
	if !res.Legacy {
		t.Error("a document from the archive must carry the legacy provenance flag")
	}
	if res.ID != "2026-07-20T02-00-00.000Z" || res.RunID != "run_legacy1" || res.Kind != model.KindAuto {
		t.Errorf("identity mapped wrong: %+v", res)
	}
	if res.Requested.Created.Autonomous != true || res.Requested.Created.OnBehalfOf != "owner" {
		t.Errorf("legacy trigger must map to autonomous authorship, got %+v", res.Requested.Created)
	}
	if res.EndedAt == nil || !res.EndedAt.Equal(time.Date(2026, 7, 20, 2, 41, 12, 0, time.UTC)) {
		t.Errorf("finishedAt must map to EndedAt, got %v", res.EndedAt)
	}
	if len(res.Repos) != 3 {
		t.Fatalf("repos = %d, want 3 (two completed + the live one)", len(res.Repos))
	}

	// Repo svc-a: legacy statuses map onto the four terminal states; the legacy ok-bool step
	// ("push") maps to executed; not-applicable keeps its reason.
	a := res.Repos[0]
	wantStates := []model.StepState{model.StepExecuted, model.StepExecuted, model.StepNotApplicable, model.StepExecuted, model.StepExecuted}
	for i, want := range wantStates {
		if a.Stages[i].State != want {
			t.Errorf("svc-a stage %d = %s, want %s", i, a.Stages[i].State, want)
		}
	}
	if !a.Done || !a.Succeeded {
		t.Error("svc-a with no failed/not-executed stage must derive done+succeeded")
	}

	// Repo svc-b: failed before any step ran — still a defined, renderable state (no black box).
	b := res.Repos[1]
	if len(b.Stages) == 0 || b.Stages[0].State != model.StepFailed || b.Stages[0].Reason == "" {
		t.Errorf("svc-b must render a defined failed state with its reason, got %+v", b.Stages)
	}
	if b.Succeeded {
		t.Error("svc-b must not read as succeeded")
	}

	// Live repo svc-c: the step the old store left RUNNING is closed as aborted. The archive is a
	// closed past — a stage carried as running would make the surface pulse for a repository where
	// nothing runs — and the mandatory reason names what the archive recorded.
	c := res.Repos[2]
	if c.Stages[0].State != model.StepFailed || !strings.Contains(c.Stages[0].Reason, "aborted") {
		t.Errorf("svc-c: the step left running must be closed as aborted with its reason, got %+v", c.Stages[0])
	}
	if !strings.Contains(c.Stages[0].Reason, "2026-07-20T02:40:00Z") {
		t.Errorf("svc-c: the aborted step must name the moment the archive recorded, got %q", c.Stages[0].Reason)
	}
	if !c.Done || c.Succeeded {
		t.Errorf("svc-c must read as done (nothing runs in an archive) but never as succeeded: done=%v succeeded=%v", c.Done, c.Succeeded)
	}

	// Get by id finds the legacy document too.
	got, ok, err := rs.Get("2026-07-20T02-00-00.000Z")
	if err != nil || !ok || got.RunID != "run_legacy1" {
		t.Fatalf("Get(legacy id) = %+v %v %v", got, ok, err)
	}
}

// A record the pre-rebuild store left WITHOUT a finishing time must not fall between the two lists:
// the history selector drops an execution that never ended (derive.go: ExecutionCompleted) while the
// history's counter still counts every record it read — so an unstamped record was invisible in the
// list and claimed in the number at the same time. An archived execution has ENDED; the four
// documents below are the four forms the imported archive actually holds among its unstamped records
// (a live block cut off mid-step; a live block whose recorded steps all ended; a record the source
// flagged ok with complete chains; a record naming no repository at all), with neutral names.
func TestArchiveRecordWithoutFinishingTimeIsEndedAndCounted(t *testing.T) {
	const (
		cutOffMidStep = `{"runId":"run_a","resultId":"r_cut","runName":"Sweep","type":"todo",
			"startedAt":"2026-07-27T19:42:15Z","finishedAt":"0001-01-01T00:00:00Z","ok":false,
			"live":{"repo":"svc-a","steps":[{"name":"implement","status":"","running":true,"at":"2026-07-28T10:37:49Z"}]}}`
		stepsAllEnded = `{"runId":"run_b","resultId":"r_ended","runName":"Sweep","type":"todo",
			"startedAt":"2026-07-28T06:45:18Z","finishedAt":"0001-01-01T00:00:00Z","ok":false,
			"live":{"repo":"svc-a","steps":[
				{"name":"fold","status":"not-applicable","at":"2026-07-28T06:45:20Z"},
				{"name":"implement","status":"ok","at":"2026-07-28T06:45:21Z"}]}}`
		flaggedOK = `{"runId":"run_c","resultId":"r_ok","runName":"Sweep",
			"startedAt":"2026-07-22T02:33:05Z","finishedAt":"0001-01-01T00:00:00Z","ok":true,
			"repos":[{"repo":"svc-a","ok":true,"steps":[
				{"name":"implement","status":"ok","at":"2026-07-22T02:48:19Z"},
				{"name":"push","status":"ok","at":"2026-07-22T02:48:21Z"},
				{"name":"pr","status":"ok","at":"2026-07-22T02:48:22Z"}]}]}`
		noRepoAtAll = `{"runId":"run_d","resultId":"r_bare","runName":"Sweep","type":"todo",
			"startedAt":"2026-07-27T16:20:42Z","finishedAt":"0001-01-01T00:00:00Z","ok":false,"repos":[]}`
	)
	rs := archiveStore(t, map[string]string{
		"run_a/r_cut.json": cutOffMidStep, "run_b/r_ended.json": stepsAllEnded,
		"run_c/r_ok.json": flaggedOK, "run_d/r_bare.json": noRepoAtAll,
	})
	all, err := rs.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(all) != 4 {
		t.Fatalf("List = %d records, want the four archive documents", len(all))
	}
	byID := map[string]Result{}
	for _, res := range all {
		byID[res.ID] = res
		// THE invariant, for every archive record: it ended, no stage is still transient, and the
		// document says where its end came from. Whatever the history counter counts, the list shows.
		if res.EndedAt == nil {
			t.Errorf("%s: an archived execution without an end stamp stays out of the history while the counter counts it", res.ID)
		}
		if res.Report == "" {
			t.Errorf("%s: the substituted end is not stated anywhere — the record would claim a recorded finish", res.ID)
		}
		for _, rp := range res.Repos {
			for _, sv := range rp.Stages {
				if !sv.State.Terminal() {
					t.Errorf("%s/%s: stage %q is %q — nothing in a closed archive is transient", res.ID, rp.Repo, sv.Stage, sv.State)
				}
			}
			if !rp.Done {
				t.Errorf("%s/%s: an archived pipeline must read as done", res.ID, rp.Repo)
			}
		}
	}

	// Cut off mid-step: the end is the last instant the record itself carries, the step is aborted,
	// and the cutoff stage keeps the truncated chain from reading as a completed one.
	cut := byID["r_cut"]
	if want := time.Date(2026, 7, 28, 10, 37, 49, 0, time.UTC); !cut.EndedAt.Equal(want) {
		t.Errorf("r_cut end = %v, want the last recorded step stamp %v", cut.EndedAt, want)
	}
	if cut.Repos[0].Succeeded {
		t.Error("r_cut: a chain cut off mid-step must never read as succeeded")
	}
	if !strings.Contains(cut.Report, "2026-07-28T10:37:49Z") || !strings.Contains(cut.Report, "standing in for the missing stamp") {
		t.Errorf("r_cut: the report must name the substituted end and say it stands in:\n%s", cut.Report)
	}

	// Every recorded step ended, yet the record was cut off: without the cutoff stage this is the
	// false green — a truncated chain reading as a full success (K-4).
	ended := byID["r_ended"]
	if ended.Repos[0].Succeeded {
		t.Error("r_ended: a record without a finishing time must not read as a completed chain")
	}
	last := ended.Repos[0].Stages[len(ended.Repos[0].Stages)-1]
	if last.Stage != stageArchivedCutoff || last.State != model.StepNotExecuted || last.Reason == "" {
		t.Errorf("r_ended: the cutoff stage is missing or unexplained: %+v", last)
	}

	// The source flagged the record ok and its chains are complete: only the STAMP is missing, so
	// nothing is turned red — a false failure is as much a lie as a false success.
	ok := byID["r_ok"]
	if !ok.Repos[0].Succeeded {
		t.Errorf("r_ok: a recorded success must survive the missing stamp: %+v", ok.Repos[0].Stages)
	}
	if want := time.Date(2026, 7, 22, 2, 48, 22, 0, time.UTC); !ok.EndedAt.Equal(want) {
		t.Errorf("r_ok end = %v, want %v", ok.EndedAt, want)
	}

	// Nothing recorded at all: the end can only be the start, and the report says exactly that
	// instead of implying a duration nobody measured.
	bare := byID["r_bare"]
	if !bare.EndedAt.Equal(bare.StartedAt) {
		t.Errorf("r_bare end = %v, want its own start %v", bare.EndedAt, bare.StartedAt)
	}
	if !strings.Contains(bare.Report, "no later instant") || !strings.Contains(bare.Report, "names no repository") {
		t.Errorf("r_bare: the report must state that nothing later and no repository was recorded:\n%s", bare.Report)
	}
}

// archiveStore writes the given legacy documents (path relative to the archive root) and opens a
// store over them — the ONE seam the archive is read through.
func archiveStore(t *testing.T, docs map[string]string) *ResultStore {
	t.Helper()
	root := t.TempDir()
	archive := filepath.Join(root, "runs-results")
	for rel, body := range docs {
		p := filepath.Join(archive, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("DEVLAB_MERCURY_RUNS_RESULTS", archive)
	t.Setenv("DEVLAB_MERCURY_EXECUTIONS", filepath.Join(root, "executions"))
	return NewResultStore(&statepath.Paths{Root: root})
}

// New-format documents and the transcript journal round-trip through the store.
func TestResultStoreNewFormatAndTranscript(t *testing.T) {
	rs := NewResultStore(&statepath.Paths{Root: t.TempDir()})
	res := Result{
		ID: "exec_x", RunID: "run_1", Kind: model.KindTodo, StartedAt: time.Now().UTC(),
		Repos: []model.RepoPipeline{{
			Repo: "svc-a",
			Stages: []model.StageView{
				{Stage: model.StagePreflight, State: model.StepExecuted},
				{Stage: model.StageImplement, State: model.StepRunning},
			},
		}},
	}
	if err := rs.Put(res); err != nil {
		t.Fatalf("Put: %v", err)
	}
	got, ok, err := rs.Get("exec_x")
	if err != nil || !ok {
		t.Fatalf("Get: %v %v", ok, err)
	}
	if got.Repos[0].Done {
		t.Error("a pipeline with a running stage must not derive done")
	}
	forRun, err := rs.ForRun("run_1")
	if err != nil || len(forRun) != 1 {
		t.Fatalf("ForRun = %v, %v", forRun, err)
	}
	if err := rs.AppendTranscript("exec_x", []byte(`{"t":1}`)); err != nil {
		t.Fatal(err)
	}
	if err := rs.AppendTranscript("exec_x", []byte(`{"t":2}`)); err != nil {
		t.Fatal(err)
	}
	// The file is 16 bytes; a 10-byte window cuts mid-line, and the torn first line is dropped
	// so the tail starts at a line boundary.
	tail, err := rs.ReadTranscriptTail("exec_x", 10)
	if err != nil {
		t.Fatal(err)
	}
	if tail != "{\"t\":2}\n" {
		t.Errorf("tail = %q, want the last full journal line", tail)
	}
}

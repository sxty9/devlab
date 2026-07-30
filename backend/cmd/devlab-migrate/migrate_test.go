// Tests of the one-time import. The fixture under testdata/ is ANONYMIZED: the real export is
// instance data and stays outside the repository. It carries the same SHAPE (both target forms,
// the older singular repository field, summary-only executions, an unusable-free schedule set)
// and the same classification cases: automatic runs, own-repository records, foreign records
// open and completed.
package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"devlab/backend/internal/executor"
	"devlab/backend/internal/mercury"
	"devlab/backend/internal/model"
	"devlab/backend/internal/preflight"
	"devlab/backend/internal/runs"
	"devlab/backend/internal/sched"
	"devlab/backend/internal/statepath"
)

const exportFixture = "testdata/export.json"

// migrateNow is a fixed clock: the protocol and the schedule anchor must not depend on the hour
// the test runs.
var migrateNow = time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)

func stateRoot(t *testing.T) *statepath.Paths {
	t.Helper()
	root := t.TempDir()
	t.Setenv("DEVLAB_STATE_DIR", root)
	// The per-pool env seams must not leak in from the developer's shell — every seam the import
	// mirrors, so a set variable can never make a test read one pool and write another.
	for _, seam := range []string{
		"DEVLAB_MERCURY_RUNS", "DEVLAB_MERCURY_EXECUTIONS", "DEVLAB_MERCURY_RUNS_RESULTS",
		"DEVLAB_MERCURY_RUNS_NOTICES", "DEVLAB_MERCURY_RUNS_PRS", "DEVLAB_MERCURY_RUNS_DELIVERIES",
		"DEVLAB_MERCURY_RUNS_HISTORY", "DEVLAB_MERCURY_SETTINGS",
	} {
		t.Setenv(seam, "")
	}
	p, err := statepath.FromEnv()
	if err != nil {
		t.Fatalf("state root: %v", err)
	}
	return p
}

func migrate(t *testing.T, p *statepath.Paths) *plan {
	t.Helper()
	m := newMigrator(p, ownRepo, migrateNow)
	pl, err := m.plan(exportFixture)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if len(pl.refusals) > 0 {
		t.Fatalf("unexpected refusals: %v", pl.refusals)
	}
	if err := m.apply(pl); err != nil {
		t.Fatalf("apply: %v", err)
	}
	return pl
}

// installArchive copies the legacy archive fixture into the state root — the import MOVES the
// archive aside, so it must never work on the repository's copy.
func installArchive(t *testing.T, p *statepath.Paths) {
	t.Helper()
	src := "testdata/archive"
	err := filepath.WalkDir(src, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, rerr := filepath.Rel(src, path)
		if rerr != nil {
			return rerr
		}
		dst := filepath.Join(p.LegacyResults(), rel)
		if d.IsDir() {
			return os.MkdirAll(dst, 0o700)
		}
		b, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		return os.WriteFile(dst, b, 0o600)
	})
	if err != nil {
		t.Fatalf("install archive: %v", err)
	}
}

// ── the foreign records: 1 open task, the completed ones as history entries ──────────────────

// The foreign records are fed in as required: the open one as an executable task with its
// original metadata, the completed ones as history entries — and NOT as run definitions, so a
// finished task never shows up as open again (S15 step 1).
func TestForeignRecordsOpenTaskAndHistoryEntries(t *testing.T) {
	p := stateRoot(t)
	pl := migrate(t, p)

	if len(pl.openTodos) != 1 {
		t.Fatalf("expected exactly one open foreign task, got %d", len(pl.openTodos))
	}
	if len(pl.history) != 3 {
		t.Fatalf("expected three completed foreign records as history entries, got %d", len(pl.history))
	}

	all, err := runs.NewStore(p).List()
	if err != nil {
		t.Fatalf("run pool: %v", err)
	}
	byID := map[string]runs.Run{}
	for _, r := range all {
		byID[r.ID] = r
	}
	open, ok := byID["run_foreign_open"]
	if !ok {
		t.Fatal("the open foreign task was not imported")
	}
	if open.Kind != model.KindTodo || open.Task == "" {
		t.Errorf("open task imported without kind/task: %+v", open)
	}
	if len(open.Targets) != 1 || open.Targets[0].Repo != "alpha" || open.Targets[0].Create {
		t.Errorf("targets not carried faithfully: %+v", open.Targets)
	}
	if !open.Authorship.CreatedAt.Equal(time.Date(2026, 7, 19, 19, 0, 0, 0, time.UTC)) {
		t.Errorf("original creation time not carried: %v", open.Authorship.CreatedAt)
	}
	if open.Authorship.Created.User != "" || open.Authorship.Created.Autonomous {
		t.Errorf("the export names no author — unknown must not be back-filled: %+v", open.Authorship.Created)
	}
	for _, id := range []string{"run_foreign_done1", "run_foreign_done2", "run_foreign_done3"} {
		if _, exists := byID[id]; exists {
			t.Errorf("completed foreign record %s must not become a run definition", id)
		}
	}

	// History: the entries are there, carry the original metadata, and claim no stage.
	results, err := runs.NewResultStore(p).List()
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	res, found := findResult(results, "2026-07-14T14-52-28.048904010Z")
	if !found {
		t.Fatal("history entry of the completed foreign record is missing")
	}
	if res.RunTitle != "extract the shared library out of gamma" || res.RunID != "run_foreign_done2" {
		t.Errorf("original identity not carried: %+v", res)
	}
	if !res.Legacy {
		t.Error("an imported pre-rebuild execution must be marked as legacy provenance")
	}
	if res.EndedAt == nil {
		t.Error("a completed history entry needs an end time, otherwise it stays in the open list")
	}
	if res.Usage.InputTokens != 22905150 || res.Usage.CostUSD == 0 {
		t.Errorf("original usage not carried: %+v", res.Usage)
	}
	if res.Prompt == "" {
		t.Error("the prompt the execution was driven by must be carried")
	}
	if len(res.Repos) != 2 {
		t.Fatalf("both target repositories must be named: %+v", res.Repos)
	}
	for _, rp := range res.Repos {
		if len(rp.Stages) != 1 || rp.Stages[0].Stage != stageArchivedOutcome {
			t.Errorf("the entry must carry exactly the recorded outcome and no chain stage: %+v", rp)
		}
	}
	if !strings.Contains(res.Report, "no per-stage detail") {
		t.Errorf("the entry must state what the source did not carry:\n%s", res.Report)
	}
	if !strings.Contains(res.Report, "note.jpg") {
		t.Errorf("attachment metadata of a history entry must be preserved:\n%s", res.Report)
	}
	// The failed one stays failed: the recorded outcome is not prettied up.
	failed, found := findResult(results, "2026-07-16T18-00-12.382259604Z")
	if !found {
		t.Fatal("the failed foreign record is missing from the history")
	}
	if !strings.Contains(failed.Report, "failed") {
		t.Errorf("a failed original outcome must stay failed:\n%s", failed.Report)
	}
}

// An imported history entry is an ARCHIVE state, not a corpse: it carries the outcome the export
// recorded in a stage of its own — `archived-outcome`, terminal, never a chain stage — so the one
// derivation makes the entry done and its success the recorded one.
//
// Without it every entry stays "neither done nor successful", which the surface can only label
// "incomplete" in red although the source recorded a success, and which makes it offer a re-run
// for an execution that has no run definition to re-run.
func TestImportedHistoryEntryCarriesTheRecordedOutcome(t *testing.T) {
	p := stateRoot(t)
	migrate(t, p)
	results, err := runs.NewResultStore(p).List()
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	chain := map[model.Stage]bool{}
	for _, s := range model.ChainStages() {
		chain[s] = true
	}
	for _, c := range []struct {
		id        string
		state     model.StepState
		succeeded bool
	}{
		{"2026-07-12T11-49-52.236015668Z", model.StepExecuted, true},
		{"2026-07-14T14-52-28.048904010Z", model.StepExecuted, true},
		{"2026-07-16T18-00-12.382259604Z", model.StepFailed, false},
	} {
		res, found := findResult(results, c.id)
		if !found {
			t.Errorf("history entry %s is missing", c.id)
			continue
		}
		if len(res.Repos) == 0 {
			t.Errorf("%s names no repository", c.id)
			continue
		}
		for _, rp := range res.Repos {
			if len(rp.Stages) != 1 {
				t.Errorf("%s/%s: expected exactly the recorded outcome, got %+v", c.id, rp.Repo, rp.Stages)
				continue
			}
			sv := rp.Stages[0]
			if sv.Stage != stageArchivedOutcome || chain[sv.Stage] {
				t.Errorf("%s/%s: the archive state must be its own name, not a chain stage: %q", c.id, rp.Repo, sv.Stage)
			}
			if sv.State != c.state {
				t.Errorf("%s/%s: recorded outcome %q, want %q", c.id, rp.Repo, sv.State, c.state)
			}
			if !sv.State.Terminal() {
				t.Errorf("%s/%s: an archived entry must END somewhere: %q", c.id, rp.Repo, sv.State)
			}
			if sv.EndedAt == nil || !sv.EndedAt.Equal(res.StartedAt) {
				t.Errorf("%s/%s: the recorded instant must be carried: %+v", c.id, rp.Repo, sv)
			}
			// The contract: a failure states its reason; the note sits in the log otherwise.
			if c.state == model.StepFailed && strings.TrimSpace(sv.Reason) == "" {
				t.Errorf("%s/%s: a failed stage must carry its reason", c.id, rp.Repo)
			}
			if c.state == model.StepExecuted && strings.TrimSpace(sv.Log) == "" {
				t.Errorf("%s/%s: the entry must say where its outcome comes from", c.id, rp.Repo)
			}
			if !rp.Done {
				t.Errorf("%s/%s: a completed archive entry is done — otherwise it reads as incomplete", c.id, rp.Repo)
			}
			if rp.Succeeded != c.succeeded {
				t.Errorf("%s/%s: succeeded=%v, want %v (the recorded outcome, neither prettied up nor sunk)",
					c.id, rp.Repo, rp.Succeeded, c.succeeded)
			}
		}
		if !strings.Contains(res.Report, "No run definition stands behind this entry") {
			t.Errorf("%s: the entry must state that nothing can be started from it:\n%s", c.id, res.Report)
		}
	}
}

// A completed history entry counts as history — and its (absent) run never appears in the open
// list. This is the selector the surface uses, so the assertion is the user-visible one.
func TestImportedHistoryEntryIsHistoryNotOpen(t *testing.T) {
	p := stateRoot(t)
	migrate(t, p)

	rs, results := runs.NewStore(p), runs.NewResultStore(p)
	defs, err := rs.List()
	if err != nil {
		t.Fatalf("run pool: %v", err)
	}
	hist, err := results.List()
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	open, history := runs.SplitOpenHistory(defs, hist, nil)
	for _, r := range open {
		if strings.HasPrefix(r.ID, "run_foreign_done") {
			t.Errorf("completed foreign record %s shows up as open", r.ID)
		}
	}
	got := 0
	for _, res := range history {
		if strings.HasPrefix(res.RunID, "run_foreign_done") {
			got++
		}
	}
	if got != 3 {
		t.Errorf("expected 3 imported executions in the history, got %d", got)
	}
	// The one open foreign task is open, and it is the only imported task in that list.
	openIDs := []string{}
	for _, r := range open {
		if r.Kind == model.KindTodo {
			openIDs = append(openIDs, r.ID)
		}
	}
	if len(openIDs) != 1 || openIDs[0] != "run_foreign_open" {
		t.Errorf("expected exactly the one open foreign task, got %v", openIDs)
	}
}

// The records aiming at the service's own repository are NOT fed back as tasks: their substance
// is the acceptance matrix (S15 step 2). The count is protocolled, not silently dropped.
func TestOwnRepositoryRecordsAreNotFedBack(t *testing.T) {
	p := stateRoot(t)
	pl := migrate(t, p)
	if pl.skippedOwn != 2 {
		t.Fatalf("expected the two own-repository records to be skipped, got %d", pl.skippedOwn)
	}
	defs, err := runs.NewStore(p).List()
	if err != nil {
		t.Fatalf("run pool: %v", err)
	}
	for _, r := range defs {
		if strings.HasPrefix(r.ID, "run_own") {
			t.Errorf("own-repository record %s must not be imported", r.ID)
		}
	}
	results, err := runs.NewResultStore(p).List()
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	if _, found := findResult(results, "2026-07-03T09-00-00.000000000Z"); found {
		t.Error("an own-repository record must not be imported as a history entry either")
	}
	var buf bytes.Buffer
	writeProtocol(&buf, pl, false)
	if !strings.Contains(buf.String(), "own-repository records skipped (2)") {
		t.Errorf("the protocol must state the skipped count:\n%s", buf.String())
	}
}

// ── the automatic runs: created, uncovered, and not due at once ──────────────────────────────

// The automatic runs are created WITHOUT an axiom assignment — uncovered stays visible, and the
// one planning path assigns them on the first constitution write (B-10).
func TestAutomaticRunsAreCreatedUncovered(t *testing.T) {
	p := stateRoot(t)
	pl := migrate(t, p)
	if len(pl.autoRuns) != 2 {
		t.Fatalf("expected the fixture's two automatic runs, got %d", len(pl.autoRuns))
	}
	defs, err := runs.NewStore(p).List()
	if err != nil {
		t.Fatalf("run pool: %v", err)
	}
	seen := 0
	for _, r := range defs {
		if r.Kind != model.KindAuto {
			continue
		}
		seen++
		if len(r.AxiomIDs) != 0 {
			t.Errorf("run %s was imported WITH an axiom assignment: %v", r.ID, r.AxiomIDs)
		}
		if r.PromptSnapshot != "" || r.PromptInputHash != "" {
			t.Errorf("run %s carries a stale prompt snapshot — the one composition path must fill it", r.ID)
		}
		if r.Schedule == nil {
			t.Errorf("run %s lost its recurrence", r.ID)
			continue
		}
		if err := r.Schedule.Valid(); err != nil {
			t.Errorf("run %s got an unusable schedule: %v", r.ID, err)
		}
		if r.Active {
			t.Errorf("run %s is active while it carries neither axioms nor a prompt", r.ID)
		}
		if !r.Authorship.Created.Autonomous {
			t.Errorf("run %s: a record the import creates is not a person's act: %+v", r.ID, r.Authorship.Created)
		}
	}
	if seen != 2 {
		t.Fatalf("expected 2 automatic runs in the pool, got %d", seen)
	}
}

// THE decisive property of the import: nothing it creates can be started by the scheduler while
// it carries no prompt that names a subject. The check runs against sched.IsDue — the one place
// "due" is decided — a week and a day after the import, so every weekly window has passed.
//
// Without this, the imported recurring runs are due with a 0-byte snapshot, and an execution
// hands the agent the division-of-labor preamble alone: "YOU implement the task", no task,
// "never end with a question", "do not shrink the task" — an autonomous sweep over every target
// repository with no subject at all.
func TestNothingImportedIsDueWithoutAPromptThatNamesItsSubject(t *testing.T) {
	p := stateRoot(t)
	migrate(t, p)
	defs, err := runs.NewStore(p).List()
	if err != nil {
		t.Fatalf("run pool: %v", err)
	}
	if len(defs) == 0 {
		t.Fatal("nothing was imported at all")
	}
	later := migrateNow.Add(8 * 24 * time.Hour)
	for _, r := range defs {
		subject := strings.TrimSpace(r.PromptSnapshot) != "" &&
			(r.Kind == model.KindTodo || len(r.AxiomIDs) > 0)
		if sched.IsDue(r, nil, later) && !subject {
			t.Errorf("%s run %s is due %s after the import with %d axioms and a %d-byte prompt",
				r.Kind, r.ID, later.Sub(migrateNow), len(r.AxiomIDs), len(r.PromptSnapshot))
		}
	}
}

// The bar itself, over the exact record shapes the import must never write. It is stated as a
// table so a future change to the planning that reintroduces one of them fails here, in one line,
// instead of on the instance.
func TestARecordReadyToFireWithoutASubjectIsRefused(t *testing.T) {
	weekly := &runs.ScheduleSpec{Kind: runs.Weekly, TimeOfDay: "07:00", Weekdays: []time.Weekday{time.Monday}}
	due := migrateNow.Add(24 * time.Hour)
	for _, c := range []struct {
		name   string
		run    runs.Run
		refuse bool
	}{
		{"active recurring run without axioms or prompt", runs.Run{
			ID: "r1", Kind: model.KindAuto, Title: "sweep", Schedule: weekly, Active: true}, true},
		{"active recurring run with a prompt but no axioms", runs.Run{
			ID: "r2", Kind: model.KindAuto, Title: "sweep", Schedule: weekly, Active: true,
			PromptSnapshot: "# run\n\nno axioms beneath the heading"}, true},
		{"inactive recurring run without either", runs.Run{
			ID: "r3", Kind: model.KindAuto, Title: "sweep", Schedule: weekly}, false},
		{"active recurring run with axioms and a prompt", runs.Run{
			ID: "r4", Kind: model.KindAuto, Title: "sweep", Schedule: weekly, Active: true,
			AxiomIDs: []string{"ax_reuse"}, PromptSnapshot: "# run\n\n### Reuse\nbody"}, false},
		{"task with a due date and no prompt", runs.Run{
			ID: "r5", Kind: model.KindTodo, Title: "task", Task: "do it", DueAt: &due}, true},
		{"task with a due date and a prompt over an empty task", runs.Run{
			ID: "r6", Kind: model.KindTodo, Title: "task", DueAt: &due,
			PromptSnapshot: "# Konkretes ToDo: task\n\n## Aufgabe\n\n"}, true},
		{"task without a due date and without a prompt", runs.Run{
			ID: "r7", Kind: model.KindTodo, Title: "task", Task: "do it"}, false},
		{"task with a due date, a task text and a prompt", runs.Run{
			ID: "r8", Kind: model.KindTodo, Title: "task", Task: "do it", DueAt: &due,
			PromptSnapshot: "# Konkretes ToDo: task\n\n## Aufgabe\n\ndo it"}, false},
	} {
		why := unsubstantiated(c.run)
		if c.refuse && why == "" {
			t.Errorf("%s: must be refused, was accepted", c.name)
		}
		if !c.refuse && why != "" {
			t.Errorf("%s: must be accepted, was refused (%s)", c.name, why)
		}
	}
}

// And the bar end to end: a record the export delivers ready to fire without a subject aborts the
// whole import by name — nothing at all is written.
func TestATaskThatWouldFireWithoutATaskTextRefusesTheWholeImport(t *testing.T) {
	p := stateRoot(t)
	in := filepath.Join(t.TempDir(), "export.json")
	if err := os.WriteFile(in, []byte(`{"runs":[
	  {"id":"run_empty","name":"a task with no task text","type":"todo",
	   "targets":[{"repo":"alpha"}],"dueAt":"2026-08-30T08:00:00Z",
	   "createdAt":"2026-07-01T08:00:00Z","updatedAt":"2026-07-01T08:00:00Z"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	m := newMigrator(p, ownRepo, migrateNow)
	pl, err := m.plan(in)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if len(pl.refusals) != 1 || !strings.Contains(pl.refusals[0], "run_empty") ||
		!strings.Contains(pl.refusals[0], "no task text") {
		t.Fatalf("expected one named refusal, got %v", pl.refusals)
	}
	if err := m.apply(pl); err == nil {
		t.Fatal("apply must refuse a record that would fire without a subject")
	}
	if _, err := os.Stat(p.Runs()); !os.IsNotExist(err) {
		t.Error("a refused import must not have written the run pool")
	}
}

// The imported task carries the prompt an execution actually hands the agent: composed HERE
// through the one composition path, from the task's own text. Assembled with the runtime addenda
// it must NAME the task — the preamble alone is an order to implement nothing.
func TestOpenTaskCarriesTheComposedPromptTheAgentReceives(t *testing.T) {
	p := stateRoot(t)
	migrate(t, p)
	defs, err := runs.NewStore(p).List()
	if err != nil {
		t.Fatalf("run pool: %v", err)
	}
	var todo runs.Run
	for _, r := range defs {
		if r.ID == "run_foreign_open" {
			todo = r
		}
	}
	if todo.ID == "" {
		t.Fatal("the open foreign task was not imported")
	}
	if strings.TrimSpace(todo.PromptSnapshot) == "" {
		t.Fatal("the imported task carries no prompt snapshot — its execution would run the bare preamble")
	}
	if todo.PromptInputHash == "" {
		t.Error("the snapshot must carry its input fingerprint, or no constitution write could tell it drifted")
	}
	for _, want := range []string{todo.Title, "Switch the imports and prove the check passes."} {
		if !strings.Contains(todo.PromptSnapshot, want) {
			t.Errorf("the composed snapshot does not carry %q:\n%s", want, todo.PromptSnapshot)
		}
	}
	// The import runs OFFLINE, so the constitution wording cannot be read here. That gap is NAMED in
	// the prompt rather than presented as an empty constitution (REQ-002.1 honesty), and the
	// fingerprint records "not read" — so the first constitution write recomposes the task in full.
	if !strings.Contains(todo.PromptSnapshot, "nicht gelesen") {
		t.Errorf("an offline import must name the missing constitution wording:\n%s", todo.PromptSnapshot)
	}
	withWording := todo
	runs.ComposeInto(&withWording, runs.Catalog{ByID: map[string]mercury.RunAxiom{
		"ax_1": {ID: "ax_1", Titel: "Minimalism", Body: "Keep it small."},
	}})
	if withWording.PromptInputHash == todo.PromptInputHash {
		t.Error("the fingerprint must tell 'constitution not read' apart from 'constitution read'")
	}
	if !strings.Contains(withWording.PromptSnapshot, "Keep it small.") {
		t.Errorf("recomposing with the constitution must fold in the wording:\n%s", withWording.PromptSnapshot)
	}
	full := executor.AssemblePrompt(todo.PromptSnapshot, preflight.Finding{State: model.TaskNotImplemented}, "", "")
	if !strings.Contains(full, "Switch the imports and prove the check passes.") {
		t.Errorf("the assembled execution prompt names no task:\n%s", full)
	}
}

// The activation gate is recorded as a protocol item AND printed, so the operator cannot miss why
// seven switched-on runs arrived switched off.
func TestActivationGateIsRecordedAndProtocolled(t *testing.T) {
	p := stateRoot(t)
	pl := migrate(t, p)
	if len(pl.heldInactive) != 2 {
		t.Fatalf("both runs that were switched on in the export must be named, got %v", pl.heldInactive)
	}
	var buf bytes.Buffer
	writeProtocol(&buf, pl, false)
	for _, want := range []string{"activation gate (2)", "assign the axioms first"} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("the protocol is missing %q:\n%s", want, buf.String())
		}
	}
	list, err := runs.NewNoticeStore(p).List()
	if err != nil {
		t.Fatalf("notice pool: %v", err)
	}
	gate := activationGate()
	for _, n := range list {
		if n.ID != noticeIDPrefix+gate.Key {
			continue
		}
		if !strings.Contains(n.Message(), "switched OFF") || !strings.Contains(n.NextStep, "Assign the axioms") {
			t.Errorf("the gate record must name the required unlock: %+v", n)
		}
		return
	}
	t.Error("the activation gate was not recorded in the notice pool")
}

// Weekday and time-of-day survive the import — the recurrence is data, not a default.
func TestAutomaticRunKeepsItsRecurrence(t *testing.T) {
	p := stateRoot(t)
	migrate(t, p)
	defs, _ := runs.NewStore(p).List()
	for _, r := range defs {
		switch r.ID {
		case "run_auto1":
			if r.Schedule.Kind != runs.Weekly || r.Schedule.TimeOfDay != "07:00" ||
				len(r.Schedule.Weekdays) != 1 || r.Schedule.Weekdays[0] != time.Monday {
				t.Errorf("weekly recurrence not carried: %+v", r.Schedule)
			}
		case "run_auto2":
			if r.Schedule.Kind != runs.Daily || r.Schedule.TimeOfDay != "06:30" || len(r.Schedule.Weekdays) != 0 {
				t.Errorf("daily recurrence not carried: %+v", r.Schedule)
			}
		}
	}
}

// An unusable recurrence is REFUSED by name and nothing at all is written: a run that looks
// active while never firing is the silent trap the rebuild removes.
func TestUnusableScheduleRefusesTheWholeImport(t *testing.T) {
	p := stateRoot(t)
	bad := filepath.Join(t.TempDir(), "export.json")
	if err := os.WriteFile(bad, []byte(`{"runs":[
	  {"id":"run_bad","name":"broken recurrence","type":"","enabled":true,
	   "schedule":{"kind":"monthly","timeOfDay":"99:99"}}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	m := newMigrator(p, ownRepo, migrateNow)
	pl, err := m.plan(bad)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if len(pl.refusals) != 1 || !strings.Contains(pl.refusals[0], "run_bad") {
		t.Fatalf("expected one named refusal, got %v", pl.refusals)
	}
	if err := m.apply(pl); err == nil {
		t.Fatal("apply must refuse while a record cannot be mapped")
	}
	if _, err := os.Stat(p.Runs()); !os.IsNotExist(err) {
		t.Error("a refused import must not have written the run pool")
	}
}

// An identifier from the export becomes a directory name. A traversal attempt is refused, never
// normalized away.
func TestUnsafeIdentifiersAreRefused(t *testing.T) {
	p := stateRoot(t)
	bad := filepath.Join(t.TempDir(), "export.json")
	if err := os.WriteFile(bad, []byte(`{"runs":[
	  {"id":"..","name":"traversal","type":"todo","targets":[{"repo":"alpha"}]},
	  {"id":"run_ok","name":"summary with a traversal id","type":"todo","done":true,
	   "targets":[{"repo":"alpha"}],
	   "lastResult":{"resultId":"../../escape","at":"2026-07-01T00:00:00Z","ok":true}}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	m := newMigrator(p, ownRepo, migrateNow)
	pl, err := m.plan(bad)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if len(pl.refusals) != 2 {
		t.Fatalf("both unsafe identifiers must be refused, got %v", pl.refusals)
	}
	if err := m.apply(pl); err == nil {
		t.Fatal("apply must refuse unsafe identifiers")
	}
}

// A record marked completed but carrying no execution summary is REFUSED by name: it can be
// neither a faithful history entry nor an open task, and dropping it silently would lose it.
func TestCompletedRecordWithoutSummaryIsRefused(t *testing.T) {
	p := stateRoot(t)
	in := filepath.Join(t.TempDir(), "export.json")
	if err := os.WriteFile(in, []byte(`{"runs":[
	  {"id":"run_nosummary","name":"completed without a record","type":"todo","done":true,
	   "targets":[{"repo":"alpha"}]}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	m := newMigrator(p, ownRepo, migrateNow)
	pl, err := m.plan(in)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if len(pl.refusals) != 1 || !strings.Contains(pl.refusals[0], "run_nosummary") {
		t.Fatalf("expected one named refusal, got %v", pl.refusals)
	}
	if err := m.apply(pl); err == nil {
		t.Fatal("apply must refuse")
	}
	if _, err := os.Stat(p.Runs()); !os.IsNotExist(err) {
		t.Error("a refused import must not have written the run pool")
	}
}

// A due date that has already lapsed is not carried: this phase feeds the foreign tasks in
// without starting them, and a past due date would fire on the first tick.
func TestLapsedDueDateIsDroppedAndProtocolled(t *testing.T) {
	p := stateRoot(t)
	in := filepath.Join(t.TempDir(), "export.json")
	if err := os.WriteFile(in, []byte(`{"runs":[
	  {"id":"run_late","name":"foreign task with a lapsed due date","type":"todo",
	   "targets":[{"repo":"alpha"}],"dueAt":"2026-07-01T08:00:00Z",
	   "createdAt":"2026-06-30T08:00:00Z","updatedAt":"2026-06-30T08:00:00Z"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	m := newMigrator(p, ownRepo, migrateNow)
	pl, err := m.plan(in)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if len(pl.lapsedDue) != 1 {
		t.Fatalf("the lapsed due date must be protocolled, got %v", pl.lapsedDue)
	}
	if pl.openTodos[0].DueAt != nil {
		t.Error("a lapsed due date must not be carried")
	}
	if err := m.apply(pl); err != nil {
		t.Fatalf("apply: %v", err)
	}
	var buf bytes.Buffer
	writeProtocol(&buf, pl, false)
	if !strings.Contains(buf.String(), "due dates dropped (1)") {
		t.Errorf("the protocol must name the dropped due date:\n%s", buf.String())
	}
}

// ── the legacy execution archive (REQ-027.3) ─────────────────────────────────────────────────

// REQ-027.3, first half: the archived executions are imported tolerantly and a legacy state such
// as "skipped because of a setting" stays VIEWABLE. An unreadable file is named instead of
// failing the import, and the archive is moved aside afterwards so one execution is never listed
// twice.
func TestLegacyArchiveImportKeepsLegacyStatesViewable(t *testing.T) {
	p := stateRoot(t)
	installArchive(t, p)
	pl := migrate(t, p)

	// Two readable archived executions: the hand-written one below, plus the record derived from
	// the real archive that carries its real-world traits (takeover_test.go).
	if len(pl.arch.imports) != 2 {
		t.Fatalf("expected the two readable archived executions, got %d", len(pl.arch.imports))
	}
	if len(pl.arch.unmatched) != 1 || pl.arch.unmatched[0] != "broken" {
		t.Fatalf("the unreadable archive file must be named, got %v", pl.arch.unmatched)
	}

	res, found, err := runs.NewResultStore(p).Get("2026-07-05T02-00-00.000000000Z")
	if err != nil || !found {
		t.Fatalf("archived execution not readable after the import (found=%v): %v", found, err)
	}
	if !res.Legacy {
		t.Error("provenance lost: an imported archived execution stays marked legacy")
	}
	if len(res.Repos) != 1 || len(res.Repos[0].Stages) != 5 {
		t.Fatalf("the archived stage detail was not carried: %+v", res.Repos)
	}
	var skipped *model.StageView
	for i := range res.Repos[0].Stages {
		if res.Repos[0].Stages[i].Stage == model.Stage("dev-deploy") {
			skipped = &res.Repos[0].Stages[i]
		}
	}
	if skipped == nil {
		t.Fatal("the legacy stage is gone")
	}
	if skipped.State != model.StepNotApplicable || !strings.Contains(skipped.Reason, "skipped because of a setting") {
		t.Errorf("the legacy state must stay displayable verbatim: %+v", skipped)
	}
	if res.Repos[0].Succeeded {
		t.Error("an archived execution with a not-executed stage is no success")
	}

	// Moved aside, nothing deleted, and the same execution is listed exactly once.
	if _, err := os.Stat(p.LegacyResults()); !os.IsNotExist(err) {
		t.Error("the archive must be moved aside once imported, or the tolerant read lists it twice")
	}
	moved := p.LegacyResults() + ".imported"
	if _, err := os.Stat(filepath.Join(moved, "run_legacy", "broken.json")); err != nil {
		t.Errorf("nothing may be deleted — the unreadable file must survive in %s: %v", moved, err)
	}
	all, err := runs.NewResultStore(p).List()
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	seen := 0
	for _, r := range all {
		if r.ID == "2026-07-05T02-00-00.000000000Z" {
			seen++
		}
	}
	if seen != 1 {
		t.Errorf("the archived execution appears %d times, expected exactly once", seen)
	}
}

// REQ-027.3, second half: a skipped state is never PRODUCED anew. Everything the import writes
// from the export carries no stage state at all — the only skip states in the tree are the ones
// the archive already held.
func TestNoSkippedStateIsEverProducedAnew(t *testing.T) {
	p := stateRoot(t)
	installArchive(t, p)
	pl := migrate(t, p)

	chain := map[model.Stage]bool{}
	for _, s := range model.ChainStages() {
		chain[s] = true
	}
	for _, res := range pl.history {
		for _, rp := range res.Repos {
			for _, sv := range rp.Stages {
				if chain[sv.Stage] {
					t.Errorf("history entry %s claims the chain stage %q: %+v", res.ID, sv.Stage, sv)
				}
			}
		}
	}
	for _, r := range pl.autoRuns {
		if r.PromptSnapshot != "" || r.PromptInputHash != "" {
			t.Errorf("recurring run %s was imported with a composition it has no axioms for", r.ID)
		}
	}
	for _, r := range pl.openTodos {
		// A task's snapshot is composed HERE, but from its own task text — never copied from the
		// export's aged string.
		if strings.Contains(r.PromptSnapshot, "old composed task prompt") {
			t.Errorf("task %s carries the export's stale composition instead of a fresh one", r.ID)
		}
	}
	// Across the whole imported history, every skip state traces back to the archive.
	all, err := runs.NewResultStore(p).List()
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	for _, res := range all {
		for _, rp := range res.Repos {
			for _, sv := range rp.Stages {
				if sv.State != model.StepNotApplicable && sv.State != model.StepNotExecuted {
					continue
				}
				if !res.Legacy {
					t.Errorf("execution %s carries a freshly produced %q state", res.ID, sv.State)
				}
			}
		}
	}
}

// An instance without an archive migrates just the same — the archive is optional data.
func TestImportWithoutArchive(t *testing.T) {
	p := stateRoot(t)
	pl := migrate(t, p)
	if pl.arch.dir != "" || len(pl.arch.imports) != 0 {
		t.Errorf("no archive on this instance, yet one was planned: %+v", pl.arch)
	}
	var buf bytes.Buffer
	writeProtocol(&buf, pl, false)
	if !strings.Contains(buf.String(), "none on this instance") {
		t.Errorf("the protocol must say the archive is absent:\n%s", buf.String())
	}
}

// ── the M1–M8 protocol ──────────────────────────────────────────────────────────────────────

// The one-off items are prepared as a notice protocol in the EXISTING notice pool — the
// migration adds no store of its own, and every item carries its next step.
func TestOneOffItemsArePreparedAsNotices(t *testing.T) {
	p := stateRoot(t)
	migrate(t, p)
	list, err := runs.NewNoticeStore(p).List()
	if err != nil {
		t.Fatalf("notice pool: %v", err)
	}
	byID := map[string]runs.Notice{}
	for _, n := range list {
		byID[n.ID] = n
	}
	if len(oneOffs()) != 8 {
		t.Fatalf("M1–M8 are eight items, got %d", len(oneOffs()))
	}
	items := protocolItems()
	if len(items) != 9 {
		t.Fatalf("the protocol is M1–M8 plus the activation gate, got %d items", len(items))
	}
	for _, o := range items {
		n, ok := byID[noticeIDPrefix+o.Key]
		if !ok {
			t.Errorf("item %s was not recorded", o.Label)
			continue
		}
		if n.Kind != noticeKindMigration {
			t.Errorf("item %s is not marked as a migration record: %q", o.Key, n.Kind)
		}
		if !strings.Contains(n.Message(), o.Label) {
			t.Errorf("item %s does not name itself in the feed: %q", o.Key, n.Message())
		}
		if strings.TrimSpace(n.NextStep) == "" {
			t.Errorf("item %s carries no next step", o.Key)
		}
		if !n.At.Equal(migrateNow) || n.Read {
			t.Errorf("item %s: the occurrence is pinned to the import and starts unread: %+v", o.Key, n)
		}
	}
}

// ── idempotence ─────────────────────────────────────────────────────────────────────────────

// A second run changes NOTHING — not a byte, not a timestamp. Idempotence is structural: every
// step asks whether its record is already there.
func TestSecondRunChangesNothing(t *testing.T) {
	p := stateRoot(t)
	installArchive(t, p)
	migrate(t, p)
	before := snapshot(t, p.Root)

	m := newMigrator(p, ownRepo, migrateNow.Add(48*time.Hour))
	pl, err := m.plan(exportFixture)
	if err != nil {
		t.Fatalf("second plan: %v", err)
	}
	if !pl.empty() {
		t.Fatalf("the second run still plans work: runs=%d todos=%d history=%d notices=%d archive=%d",
			len(pl.autoRuns), len(pl.openTodos), len(pl.history), len(pl.notices), len(pl.arch.imports))
	}
	if err := m.apply(pl); err != nil {
		t.Fatalf("second apply: %v", err)
	}
	after := snapshot(t, p.Root)
	if len(before) != len(after) {
		t.Fatalf("the second run changed the file set: %d → %d", len(before), len(after))
	}
	for path, sig := range before {
		if after[path] != sig {
			t.Errorf("the second run rewrote %s", path)
		}
	}
	if len(pl.presentRuns) != 3 || len(pl.presentHistory) != 3 || pl.presentNotices != 9 {
		t.Errorf("the second protocol must report what is already there, got runs=%v history=%v notices=%d",
			pl.presentRuns, pl.presentHistory, pl.presentNotices)
	}
}

// An automatic run that the assignment has meanwhile filled is recognized by its TITLE too — the
// identity rule of the planning path — so a repeated import never creates a twin.
func TestAutomaticRunIsNotDuplicatedUnderANewIdentity(t *testing.T) {
	p := stateRoot(t)
	store := runs.NewStore(p)
	if err := store.Put(runs.Run{
		ID: "run_fresh", Kind: model.KindAuto, Title: "reuse AND shared building blocks",
		AxiomIDs: []string{"ax_reuse"}, Active: true,
		Schedule: &runs.ScheduleSpec{Kind: runs.Weekly, TimeOfDay: "07:00", Weekdays: []time.Weekday{time.Monday}},
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	m := newMigrator(p, ownRepo, migrateNow)
	pl, err := m.plan(exportFixture)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	for _, r := range pl.autoRuns {
		if strings.EqualFold(r.Title, "reuse and shared building blocks") {
			t.Error("an automatic run of the same title must not be created twice")
		}
	}
	if err := m.apply(pl); err != nil {
		t.Fatalf("apply: %v", err)
	}
	defs, _ := store.List()
	titles := map[string]int{}
	for _, r := range defs {
		titles[strings.ToLower(r.Title)]++
	}
	if titles["reuse and shared building blocks"] != 1 {
		t.Errorf("expected one run with that title, got %d", titles["reuse and shared building blocks"])
	}
}

// ── helpers ─────────────────────────────────────────────────────────────────────────────────

func findResult(list []runs.Result, id string) (runs.Result, bool) {
	for _, r := range list {
		if r.ID == id {
			return r, true
		}
	}
	return runs.Result{}, false
}

// snapshot maps every file below root to its content hash and modification time, so "changed
// nothing" is checked against the bytes AND the timestamps.
func snapshot(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		fi, ierr := d.Info()
		if ierr != nil {
			return ierr
		}
		sum, _ := json.Marshal(struct {
			N int
			H string
			M time.Time
		}{len(b), string(b), fi.ModTime()})
		out[path] = string(sum)
		return nil
	})
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	return out
}

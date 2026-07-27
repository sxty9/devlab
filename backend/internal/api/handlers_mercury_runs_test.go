package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"devlab/backend/internal/runs"
)

// The execution history is one machinery shared by the Läufe and ToDos surfaces; ?type scopes it so
// each tab shows ONLY its own kind. A stamped result is trusted; an unstamped one (written before the
// stamp existed) falls back to its live run's kind. Both paths are exercised here.
func TestRunsExecutionsFilterByType(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("DEVLAB_MERCURY_RUNS", filepath.Join(dir, "runs.json"))
	t.Setenv("DEVLAB_MERCURY_RUNS_RESULTS", filepath.Join(dir, "res"))
	t.Setenv("DEVLAB_MERCURY_RUNS_HISTORY", filepath.Join(dir, "hist"))

	store, results := runs.NewStore(), runs.NewResults()
	// A todo whose run still exists but whose result predates the stamp → resolved via the live run.
	if _, err := store.Mutate("create", "t", func(cur []runs.Run) ([]runs.Run, error) {
		return append(cur, runs.Run{ID: "run_todo", Type: runs.TypeTodo, Name: "Todo"}), nil
	}); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	must := func(err error) {
		if err != nil {
			t.Fatal(err)
		}
	}
	must(results.Save(runs.Result{RunID: "run_todo", ResultID: runs.NewResultID(now), StartedAt: now})) // unstamped
	must(results.Save(runs.Result{RunID: "run_auto", ResultID: runs.NewResultID(now.Add(time.Second)), Type: runs.TypeAuto, StartedAt: now.Add(time.Second)}))
	must(results.Save(runs.Result{RunID: "run_gone", ResultID: runs.NewResultID(now.Add(2 * time.Second)), Type: runs.TypeTodo, StartedAt: now.Add(2 * time.Second)})) // stamped todo, run deleted

	s := &Server{runs: store, runResults: results}

	fetch := func(q string) []runs.ExecutionSummary {
		rec := httptest.NewRecorder()
		s.runsExecutions(rec, httptest.NewRequest(http.MethodGet, "/api/mercury/runs/executions"+q, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("status %d for %q", rec.Code, q)
		}
		var body struct {
			Executions []runs.ExecutionSummary `json:"executions"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		return body.Executions
	}

	if got := fetch(""); len(got) != 3 {
		t.Fatalf("no filter: want all 3, got %d", len(got))
	}
	todo := fetch("?type=todo")
	if len(todo) != 2 {
		t.Fatalf("type=todo: want 2 (unstamped-but-live + stamped-orphan), got %d", len(todo))
	}
	for _, e := range todo {
		if e.Type != runs.TypeTodo {
			t.Errorf("type=todo returned %s for %s (resolved kind must be handed back)", e.Type, e.RunID)
		}
	}
	auto := fetch("?type=auto")
	if len(auto) != 1 || auto[0].RunID != "run_auto" {
		t.Fatalf("type=auto: want just run_auto, got %+v", auto)
	}
}

// The calendar is the union of upcoming firings and past executions: a scheduled run contributes
// future occurrences (schedule set, no resultId) AND its completed executions fold back in as past
// occurrences (resultId + status, no schedule), each type-resolved and honouring ?type exactly like
// the History. This pins that union, the past/future split, and the status carry-through.
func TestRunsCalendarIsUnionOfPastAndUpcoming(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("DEVLAB_MERCURY_RUNS", filepath.Join(dir, "runs.json"))
	t.Setenv("DEVLAB_MERCURY_RUNS_RESULTS", filepath.Join(dir, "res"))
	t.Setenv("DEVLAB_MERCURY_RUNS_HISTORY", filepath.Join(dir, "hist"))

	store, results := runs.NewStore(), runs.NewResults()
	must := func(err error) {
		if err != nil {
			t.Fatal(err)
		}
	}
	// A recurring auto run (→ future firings) and a one-time ToDo due tomorrow (→ one future firing).
	due := time.Now().Add(24 * time.Hour)
	if _, err := store.Mutate("create", "t", func(cur []runs.Run) ([]runs.Run, error) {
		return append(cur,
			runs.Run{ID: "run_auto", Type: runs.TypeAuto, Name: "Nightly", Enabled: true, Schedule: runs.Schedule{Kind: runs.Daily, TimeOfDay: "03:00"}},
			runs.Run{ID: "run_todo", Type: runs.TypeTodo, Name: "Todo", Enabled: true, DueAt: &due},
		), nil
	}); err != nil {
		t.Fatal(err)
	}
	// Past executions: an OK and a failed auto run, and one OK ToDo run.
	okAt, failAt, todoAt := time.Now().Add(-48*time.Hour), time.Now().Add(-24*time.Hour), time.Now().Add(-72*time.Hour)
	must(results.Save(runs.Result{RunID: "run_auto", ResultID: runs.NewResultID(okAt), Type: runs.TypeAuto, StartedAt: okAt, OK: true}))
	must(results.Save(runs.Result{RunID: "run_auto", ResultID: runs.NewResultID(failAt), Type: runs.TypeAuto, StartedAt: failAt, OK: false}))
	must(results.Save(runs.Result{RunID: "run_todo", ResultID: runs.NewResultID(todoAt), Type: runs.TypeTodo, StartedAt: todoAt, OK: true}))

	s := &Server{runs: store, runResults: results}

	type occ struct {
		RunID    string `json:"runId"`
		Type     string `json:"type"`
		Schedule string `json:"schedule"`
		ResultID string `json:"resultId"`
		OK       bool   `json:"ok"`
	}
	fetch := func(q string) []occ {
		rec := httptest.NewRecorder()
		s.runsCalendar(rec, httptest.NewRequest(http.MethodGet, "/api/mercury/runs/calendar"+q, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("status %d for %q", rec.Code, q)
		}
		var body struct {
			Occurrences []occ `json:"occurrences"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		return body.Occurrences
	}
	// split partitions occurrences into past (a resultId) and future (a schedule, no resultId),
	// asserting every entry is exactly one of the two.
	split := func(occs []occ) (past, future []occ) {
		for _, o := range occs {
			switch {
			case o.ResultID != "":
				past = append(past, o)
			case o.Schedule != "":
				future = append(future, o)
			default:
				t.Errorf("occurrence is neither past nor future: %+v", o)
			}
		}
		return
	}

	past, future := split(fetch(""))
	if len(past) != 3 {
		t.Fatalf("union: want 3 past executions, got %d (%+v)", len(past), past)
	}
	if len(future) == 0 {
		t.Fatal("union: want upcoming firings too, got none")
	}
	// The failed execution's status must survive into the calendar (not silently OK).
	oks := 0
	for _, o := range past {
		if o.OK {
			oks++
		}
	}
	if oks != 2 {
		t.Fatalf("status carry-through: want 2 OK past execs, got %d (%+v)", oks, past)
	}

	// ?type scopes BOTH halves, exactly like the History.
	autoPast, autoFuture := split(fetch("?type=auto"))
	if len(autoPast) != 2 {
		t.Fatalf("type=auto: want 2 past, got %d", len(autoPast))
	}
	for _, o := range append(append([]occ{}, autoPast...), autoFuture...) {
		if o.Type != string(runs.TypeAuto) {
			t.Errorf("type=auto returned a %s occurrence", o.Type)
		}
	}
	todoPast, todoFuture := split(fetch("?type=todo"))
	if len(todoPast) != 1 || todoPast[0].RunID != "run_todo" {
		t.Fatalf("type=todo: want the one ToDo execution, got %+v", todoPast)
	}
	if len(todoFuture) != 1 {
		t.Fatalf("type=todo: want the one upcoming ToDo firing, got %d", len(todoFuture))
	}
}

// normalizeTargets is the single gate a ToDo's destinations pass through: it must accept several
// existing/new repos, collapse duplicates, and reject a malformed or empty set.
func TestNormalizeTargets(t *testing.T) {
	ok := func(in []runs.Target, wantN int) {
		t.Helper()
		out, code, msg := normalizeTargets(in)
		if code != 0 {
			t.Fatalf("normalizeTargets(%v) rejected: %d %q", in, code, msg)
		}
		if len(out) != wantN {
			t.Fatalf("normalizeTargets(%v) len=%d want %d (%v)", in, len(out), wantN, out)
		}
	}
	bad := func(in []runs.Target) {
		t.Helper()
		if _, code, _ := normalizeTargets(in); code != http.StatusBadRequest {
			t.Fatalf("normalizeTargets(%v) should be rejected, got code %d", in, code)
		}
	}

	// Several targets, mixing an existing repo and a new one, trimmed.
	ok([]runs.Target{{Repo: "devlab"}, {NewRepo: "brand-new"}, {Repo: " icaly "}}, 3)
	// Duplicates (same existing repo, same new repo) collapse to one each.
	ok([]runs.Target{{Repo: "a"}, {Repo: "a"}, {NewRepo: "b"}, {NewRepo: "b"}}, 2)
	// A single target still works (parity with the old one-target ToDo).
	ok([]runs.Target{{Repo: "a"}}, 1)

	bad(nil)                                       // no target at all
	bad([]runs.Target{})                           // empty
	bad([]runs.Target{{}})                         // neither repo nor newRepo
	bad([]runs.Target{{Repo: "a", NewRepo: "b"}})  // both set on one target
	bad([]runs.Target{{NewRepo: "in valid/name"}}) // illegal new-repo name
	bad([]runs.Target{{Repo: "ok"}, {}})           // one good, one empty → whole set rejected
}

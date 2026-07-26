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

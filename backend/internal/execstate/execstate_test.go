package execstate

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"devlab/backend/internal/model"
	"devlab/backend/internal/statepath"
)

func open(t *testing.T) *Store {
	t.Helper()
	s, err := Open(&statepath.Paths{Root: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestCreateGetListRoundtrip(t *testing.T) {
	s := open(t)
	by := model.Actor{User: "ada"}
	d, err := s.Create("run_1", model.KindTodo, []string{"alpha", "beta"}, false, by)
	if err != nil {
		t.Fatal(err)
	}
	if d.ID == "" || d.Phase != model.PhaseCreated || d.Rev != 1 {
		t.Fatalf("unexpected fresh doc: %+v", d)
	}
	if len(d.Repos) != 2 || d.Repos[0].State != RepoPending {
		t.Fatalf("repos not initialized pending: %+v", d.Repos)
	}
	if len(d.Transitions) != 1 || d.Transitions[0].To != model.PhaseCreated || d.Transitions[0].By != by {
		t.Fatalf("creation transition not recorded: %+v", d.Transitions)
	}
	got, ok, err := s.Get(d.ID)
	if err != nil || !ok {
		t.Fatalf("get: %v ok=%v", err, ok)
	}
	if got.RunID != "run_1" || got.Kind != model.KindTodo {
		t.Fatalf("roundtrip lost fields: %+v", got)
	}
	all, err := s.List()
	if err != nil || len(all) != 1 {
		t.Fatalf("list: %v n=%d", err, len(all))
	}
}

func TestCreateMintsUniqueIDs(t *testing.T) {
	s := open(t)
	seen := map[string]bool{}
	for i := 0; i < 5; i++ {
		d, err := s.Create("run_x", model.KindAuto, nil, false, model.Actor{Autonomous: true})
		if err != nil {
			t.Fatal(err)
		}
		if seen[d.ID] {
			t.Fatalf("duplicate execution id %s", d.ID)
		}
		seen[d.ID] = true
	}
}

func TestUpdateIsTheOneMutationPath(t *testing.T) {
	s := open(t)
	d, _ := s.Create("run_1", model.KindTodo, []string{"alpha"}, false, model.Actor{User: "ada"})

	got, err := s.Update(d.ID, func(doc *Doc) error {
		doc.SetPhase(model.PhaseQueued, "", model.Actor{User: "ada"}, time.Now())
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Phase != model.PhaseQueued || got.Rev != 2 {
		t.Fatalf("update did not bump rev / set phase: %+v", got)
	}
	if got.UpdatedAt == nil {
		t.Fatal("UpdatedAt not stamped")
	}
	if len(got.Transitions) != 2 {
		t.Fatalf("transition protocol incomplete: %+v", got.Transitions)
	}

	// A mutate that fiddles with Rev undermines CAS and is refused.
	if _, err := s.Update(d.ID, func(doc *Doc) error { doc.Rev = 99; return nil }); err == nil {
		t.Fatal("rev-tampering mutate must be refused")
	}

	// Unknown id errors.
	if _, err := s.Update("exec_missing", func(*Doc) error { return nil }); err == nil {
		t.Fatal("update of unknown id must error")
	}
}

func TestUpdateSerializesConcurrentMutations(t *testing.T) {
	s := open(t)
	d, _ := s.Create("run_1", model.KindTodo, nil, false, model.Actor{})
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = s.Update(d.ID, func(doc *Doc) error {
				doc.Reason = doc.Reason + "x"
				return nil
			})
		}()
	}
	wg.Wait()
	got, _, err := s.Get(d.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Rev != 21 {
		t.Fatalf("20 serialized updates must yield rev 21, got %d", got.Rev)
	}
	if len(got.Reason) != 20 {
		t.Fatalf("lost updates: reason length %d, want 20", len(got.Reason))
	}
}

func TestLiveAndLiveForRunInvariant(t *testing.T) {
	s := open(t)
	d1, _ := s.Create("run_1", model.KindTodo, nil, false, model.Actor{})
	d2, _ := s.Create("run_2", model.KindTodo, nil, false, model.Actor{})
	// created is NOT live; queued is.
	live, err := s.Live()
	if err != nil || len(live) != 0 {
		t.Fatalf("created docs must not be live: %v n=%d", err, len(live))
	}
	mustPhase(t, s, d1.ID, model.PhaseQueued)
	mustPhase(t, s, d2.ID, model.PhaseCompleted)
	live, err = s.Live()
	if err != nil || len(live) != 1 || live[0].ID != d1.ID {
		t.Fatalf("live projection wrong: %v %+v", err, live)
	}
	got, err := s.LiveForRun("run_1")
	if err != nil || got == nil || got.ID != d1.ID {
		t.Fatalf("LiveForRun: %v %+v", err, got)
	}
	none, err := s.LiveForRun("run_2")
	if err != nil || none != nil {
		t.Fatalf("completed run must have no live doc: %v %+v", err, none)
	}

	// Violating the one-live-doc invariant is an ERROR, never silent.
	d3, _ := s.Create("run_1", model.KindTodo, nil, false, model.Actor{})
	mustPhase(t, s, d3.ID, model.PhaseQueued)
	if _, err := s.LiveForRun("run_1"); err == nil {
		t.Fatal("two live docs for one run must surface as an error")
	}
}

func mustPhase(t *testing.T, s *Store, id string, p model.ExecPhase) {
	t.Helper()
	if _, err := s.Update(id, func(doc *Doc) error {
		doc.SetPhase(p, "", model.Actor{}, time.Now())
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// REQ-039.1 (store half): a document left "running" by a killed process is honestly
// terminalized to interrupted at boot — continuation and repo progress preserved.
func TestMarkInterruptedAtBoot(t *testing.T) {
	s := open(t)
	d1, _ := s.Create("run_1", model.KindAuto, []string{"alpha", "beta"}, false, model.Actor{})
	_, err := s.Update(d1.ID, func(doc *Doc) error {
		doc.SetPhase(model.PhaseRunning, "", model.Actor{}, time.Now())
		doc.Repos[0].State = RepoDone
		doc.Repos[1].State = RepoActive
		doc.Continuation = &model.ContinuationView{Repo: "beta", Stage: model.StageImplement}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	d2, _ := s.Create("run_2", model.KindTodo, nil, false, model.Actor{})
	mustPhase(t, s, d2.ID, model.PhaseCompleted)

	now := time.Now().UTC()
	n, err := s.MarkInterruptedAtBoot(now)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("reconciled %d docs, want 1", n)
	}
	got, _, _ := s.Get(d1.ID)
	if got.Phase != model.PhaseInterrupted || got.Reason != "process death" {
		t.Fatalf("running doc not interrupted: %+v", got)
	}
	if got.Continuation == nil || got.Continuation.Repo != "beta" {
		t.Fatal("continuation lost at boot reconcile")
	}
	if got.Repos[0].State != RepoDone {
		t.Fatal("finished repo lost its done state")
	}
	last := got.Transitions[len(got.Transitions)-1]
	if last.To != model.PhaseInterrupted || !last.By.Autonomous {
		t.Fatalf("interrupt transition not recorded autonomously: %+v", last)
	}
	// Idempotent: a second pass finds nothing running.
	n, err = s.MarkInterruptedAtBoot(now)
	if err != nil || n != 0 {
		t.Fatalf("second pass must be a no-op: %v n=%d", err, n)
	}
}

// A directory holding only an imported legacy result (no state.json) is not a state document
// and must not break listing; an unreadable state document fails CLOSED.
func TestListSkipsForeignDirsButFailsOnCorruptDocs(t *testing.T) {
	p := &statepath.Paths{Root: t.TempDir()}
	s, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(p.Execution("exec_legacy"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(p.Execution("exec_legacy"), "result.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	all, err := s.List()
	if err != nil || len(all) != 0 {
		t.Fatalf("legacy-only dir must be skipped: %v n=%d", err, len(all))
	}
	if err := os.WriteFile(filepath.Join(p.Execution("exec_legacy"), "state.json"), []byte("{torn"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.List(); err == nil {
		t.Fatal("a corrupt state document must fail closed, never be guessed over")
	}
}

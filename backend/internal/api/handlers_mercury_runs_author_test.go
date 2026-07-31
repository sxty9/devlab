package api

import (
	"bytes"
	"context"
	"devlab/backend/internal/axiomrepo"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"devlab/backend/internal/auth"
	"devlab/backend/internal/runs"
)

// Authorship of runs/ToDos: a create records WHO made it, an edit by someone else records the new
// editor while the original creator stays visible (created ≠ updated), the same actor already flows
// into the config history (the signal we reuse, not a second field), and a record written with no
// author is left unattributed — never fabricated into a person.
func TestRunAuthorship(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("DEVLAB_MERCURY_RUNS", filepath.Join(dir, "runs.json"))
	t.Setenv("DEVLAB_MERCURY_RUNS_HISTORY", filepath.Join(dir, "hist"))

	// Stub aigentic's grave/list so runCatalog returns an empty axiom set — a ToDo needs none.
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/grave/list") {
			_, _ = w.Write([]byte(`{"refs":[]}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer stub.Close()
	t.Setenv("DEVLAB_AIGENTIC_URL", stub.URL)

	// The run catalog reads the constitution, which is a real git repository — give the test an empty
	// throwaway one instead of a stubbed HTTP surface.
	remote := filepath.Join(dir, "remote.git")
	runGit(t, "", "git", "init", "--quiet", "--bare", "--initial-branch=main", remote)
	seed := filepath.Join(dir, "seed")
	runGit(t, "", "git", "clone", "--quiet", remote, seed)
	runGit(t, seed, "git", "-c", "user.name=t", "-c", "user.email=t@t", "commit", "--quiet", "--allow-empty", "-m", "init")
	runGit(t, seed, "git", "push", "--quiet", "origin", "HEAD:main")
	s := &Server{runs: runs.NewStore(nil),
		axioms: axiomrepo.New(filepath.Join(dir, "work"), remote, func() (string, error) { return "", nil })}

	call := func(h http.HandlerFunc, user, id string, body any) (*httptest.ResponseRecorder, runs.Run) {
		b, _ := json.Marshal(body)
		req := httptest.NewRequest(http.MethodPost, "/api/mercury/runs", bytes.NewReader(b))
		req = req.WithContext(context.WithValue(req.Context(), userCtxKey, &auth.User{Username: user}))
		if id != "" {
			req.SetPathValue("id", id)
		}
		rec := httptest.NewRecorder()
		h(rec, req)
		var run runs.Run
		_ = json.Unmarshal(rec.Body.Bytes(), &run)
		return rec, run
	}

	todo := map[string]any{
		"title": "Ship it", "kind": "todo", "task": "do the thing",
		"targets": []map[string]string{{"repo": "devlab"}},
	}

	// Create as alice → both createdBy and updatedBy are alice.
	rec, created := call(s.runCreate, "alice", "", todo)
	if rec.Code != http.StatusOK {
		t.Fatalf("create: status %d: %s", rec.Code, rec.Body)
	}
	if created.Authorship.Created.User != "alice" || created.Authorship.Updated.User != "alice" {
		t.Fatalf("create must stamp creator and editor = alice, got created=%q updated=%q",
			created.Authorship.Created.User, created.Authorship.Updated.User)
	}

	// Edit as bob → the last editor changes; the original creator remains visible.
	rec, updated := call(s.runUpdate, "bob", created.ID, todo)
	if rec.Code != http.StatusOK {
		t.Fatalf("update: status %d: %s", rec.Code, rec.Body)
	}
	if updated.Authorship.Created.User != "alice" {
		t.Errorf("update must preserve the creator: created=%q want alice", updated.Authorship.Created.User)
	}
	if updated.Authorship.Updated.User != "bob" {
		t.Errorf("update must record the last editor: updated=%q want bob", updated.Authorship.Updated.User)
	}

	// The config history carries the same actor per mutation — the existing author signal we reuse.
	snaps, err := s.runs.History().List()
	if err != nil {
		t.Fatal(err)
	}
	if len(snaps) < 2 || snaps[0].Actor != "bob" {
		t.Errorf("history must record the actor per mutation; newest snapshot actor want bob (got %d snapshots)", len(snaps))
	}

	// A record written with no author stays unattributed (surfaced as unknown), never
	// back-filled to a person (REQ-041).
	if _, err := s.runs.Patch(func(cur []runs.Run) ([]runs.Run, error) {
		return append(cur, runs.Run{ID: "run_legacy", Title: "Legacy"}), nil
	}); err != nil {
		t.Fatal(err)
	}
	got, ok, _ := s.runs.Get("run_legacy")
	if !ok || got.Authorship.Created.User != "" || got.Authorship.Updated.User != "" {
		t.Errorf("an author-less record must stay unattributed: %+v", got.Authorship)
	}
}

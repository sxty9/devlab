package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"devlab/backend/internal/axiomauthors"
	"devlab/backend/internal/axiomrepo"
	"devlab/backend/internal/mercury"
	"devlab/backend/internal/model"
	"devlab/backend/internal/runs"
)

// End-to-end over the REAL handlers and a REAL constitution repository: EVERY write kind —
// create, edit, rename, move, move-category, delete — recomposes the affected prompt snapshots
// in the same request (REQ-003; "stale" is structurally unreachable), and NONE of them opens a
// pull request anywhere (REQ-002.3): the server under test carries no GitHub client, no deliver
// wiring and no rollout — the constitution remote ends the day with its single branch and no
// side artefacts.

type writesFixture struct {
	s      *Server
	remote string
}

func newWritesFixture(t *testing.T) *writesFixture {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("DEVLAB_MERCURY_RUNS", filepath.Join(dir, "runs.json"))
	t.Setenv("DEVLAB_MERCURY_RUNS_HISTORY", filepath.Join(dir, "hist"))
	t.Setenv("DEVLAB_MERCURY_AXIOM_AUTHORS", filepath.Join(dir, "axiom-authors.json"))
	// aigentic is deliberately unreachable: the classifier fails fast and addAxiom parks the
	// record — the write itself (and its recomposition) must not depend on the model.
	t.Setenv("DEVLAB_AIGENTIC_URL", "http://127.0.0.1:1")

	root := t.TempDir()
	remote := filepath.Join(root, "remote.git")
	gitCmd(t, "", "init", "--quiet", "--bare", "--initial-branch=main", remote)
	seed := filepath.Join(root, "seed")
	gitCmd(t, "", "clone", "--quiet", remote, seed)
	gitCmd(t, seed, "-c", "user.name=t", "-c", "user.email=t@t", "commit", "--quiet", "--allow-empty", "-m", "init")
	gitCmd(t, seed, "push", "--quiet", "origin", "HEAD:main")

	axioms := axiomrepo.New(filepath.Join(root, "work"), remote, func() (string, error) { return "", nil })
	if err := axioms.Put(context.Background(), "axiome/misc/alpha.md",
		mercury.Render(mercury.Axiom{ID: "ax_alpha", Titel: "Alpha", Body: "Stay minimal."}), "seed", "t", false); err != nil {
		t.Fatal(err)
	}

	s := &Server{axioms: axioms, runs: runs.NewStore(nil), axiomAuthors: axiomauthors.NewStore(nil)}

	// One auto run referencing the seeded axiom, composed against the seeded catalog.
	cat := runs.Catalog{ByID: map[string]mercury.RunAxiom{
		"ax_alpha": {ID: "ax_alpha", Titel: "Alpha", Body: "Stay minimal."},
	}}
	r1 := runs.Run{ID: "run_1", Kind: model.KindAuto, Title: "Guard", Active: true, AxiomIDs: []string{"ax_alpha"}}
	runs.ComposeInto(&r1, cat)
	if _, err := s.runs.Patch(func([]runs.Run) ([]runs.Run, error) { return []runs.Run{r1}, nil }); err != nil {
		t.Fatal(err)
	}
	return &writesFixture{s: s, remote: remote}
}

// run1 reloads the observed run after a write.
func (f *writesFixture) run1(t *testing.T) runs.Run {
	t.Helper()
	all, err := f.s.runs.List()
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range all {
		if r.ID == "run_1" {
			return r
		}
	}
	t.Fatal("run_1 vanished")
	return runs.Run{}
}

func postJSON(t *testing.T, h http.HandlerFunc, method, target, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("%s %s: status %d, body %s", method, target, w.Code, w.Body.String())
	}
	return w
}

func TestEveryWriteKindRecomposesAndOpensNoPR(t *testing.T) {
	f := newWritesFixture(t)
	s := f.s

	before := f.run1(t)
	if !strings.Contains(before.PromptSnapshot, "Stay minimal.") {
		t.Fatalf("fixture snapshot must carry the axiom body:\n%s", before.PromptSnapshot)
	}

	// (1) CREATE — a new record in laeufe/ (a run rule folds into EVERY auto run's prompt). The
	// classifier is down, so the record parks under unsortiert — the write and its recomposition
	// must succeed regardless.
	w := postJSON(t, s.addAxiom, http.MethodPost, "/api/mercury/axiom",
		`{"titel":"Honest delivery","body":"Ship honestly.","section":"laeufe"}`)
	if !strings.Contains(w.Body.String(), "laeufe/unsortiert/") {
		t.Fatalf("unclassifiable record must be parked, got %s", w.Body.String())
	}
	afterAdd := f.run1(t)
	if !strings.Contains(afterAdd.PromptSnapshot, "Ship honestly.") {
		t.Errorf("create must recompose in the same request — the new run rule is missing:\n%s", afterAdd.PromptSnapshot)
	}
	if afterAdd.PromptInputHash == before.PromptInputHash {
		t.Error("create must change the composed input hash")
	}

	// (2) EDIT — the axiom's body changes; the snapshot follows in the same request.
	postJSON(t, s.editAxiom, http.MethodPut, "/api/mercury/axiom",
		`{"path":"axiome/misc/alpha.md","titel":"Alpha","body":"Stay strictly minimal."}`)
	afterEdit := f.run1(t)
	if !strings.Contains(afterEdit.PromptSnapshot, "Stay strictly minimal.") {
		t.Errorf("edit must recompose in the same request:\n%s", afterEdit.PromptSnapshot)
	}

	// (3) RENAME — a pure title change (body unchanged) re-slugs the record AND recomposes: the
	// input hash folds in titles (REQ-003), so a rename is never invisible.
	postJSON(t, s.editAxiom, http.MethodPut, "/api/mercury/axiom",
		`{"path":"axiome/misc/alpha.md","titel":"Alpha Maxim","body":"Stay strictly minimal."}`)
	afterRename := f.run1(t)
	if !strings.Contains(afterRename.PromptSnapshot, "Alpha Maxim") {
		t.Errorf("rename must recompose with the new title:\n%s", afterRename.PromptSnapshot)
	}
	if afterRename.PromptInputHash == afterEdit.PromptInputHash {
		t.Error("a pure rename must change the composed input hash")
	}

	// (4) MOVE — re-filing the parked rule OUT of laeufe/ removes it from every composition.
	postJSON(t, s.moveAxiom, http.MethodPost, "/api/mercury/move",
		`{"from":"laeufe/unsortiert/honest-delivery.md","to":"axiome/imported/honest-delivery.md"}`)
	afterMove := f.run1(t)
	if strings.Contains(afterMove.PromptSnapshot, "Ship honestly.") {
		t.Errorf("move must recompose — the re-filed record no longer folds in:\n%s", afterMove.PromptSnapshot)
	}

	// (5) MOVE CATEGORY — the whole category returns under laeufe/, so the rule folds in again.
	postJSON(t, s.moveCategory, http.MethodPost, "/api/mercury/move-category",
		`{"from":"axiome/imported","to":"laeufe/imported"}`)
	afterMoveCat := f.run1(t)
	if !strings.Contains(afterMoveCat.PromptSnapshot, "Ship honestly.") {
		t.Errorf("move-category must recompose:\n%s", afterMoveCat.PromptSnapshot)
	}

	// (6) DELETE — removing the rule recomposes it away.
	req := httptest.NewRequest(http.MethodDelete, "/api/mercury/axiom?path="+url.QueryEscape("laeufe/imported/honest-delivery.md"), nil)
	rec := httptest.NewRecorder()
	s.deleteAxiom(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("delete: status %d, body %s", rec.Code, rec.Body.String())
	}
	afterDelete := f.run1(t)
	if strings.Contains(afterDelete.PromptSnapshot, "Ship honestly.") {
		t.Errorf("delete must recompose:\n%s", afterDelete.PromptSnapshot)
	}

	// REQ-002.3 — n constitution writes, ZERO pull requests. The handlers ran to completion on a
	// server without any GitHub/PR/deliver wiring (a PR path would have panicked or errored), and
	// the constitution remote holds exactly its one branch with no PR/rollout artefacts.
	out, err := exec.Command("git", "-C", f.remote, "for-each-ref", "--format=%(refname:short)", "refs/heads").Output()
	if err != nil {
		t.Fatalf("for-each-ref: %v", err)
	}
	branches := strings.Fields(strings.TrimSpace(string(out)))
	if len(branches) != 1 || branches[0] != "main" {
		t.Errorf("constitution writes must never create branches or PRs, got branches %v", branches)
	}
	if prs, err := exec.Command("git", "-C", f.remote, "for-each-ref", "refs/pull").Output(); err == nil && strings.TrimSpace(string(prs)) != "" {
		t.Errorf("unexpected PR refs on the constitution remote: %s", prs)
	}
}

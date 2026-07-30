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

// REQ-002.1 end-to-end over the REAL store: the prompt of BOTH kinds carries the constitution in
// full wording — the axioms AND the Implementierungsregeln. This is the path the blocker ran
// through: the store scan has to read regeln/ at all, and a ToDo has to be recomposed by a
// constitution write just like an automatic run, because a repository's CLAUDE.md carries only the
// reference text now.
func TestBothPromptKindsCarryAxiomsAndImplementationRules(t *testing.T) {
	f := newWritesFixture(t)
	s := f.s

	// A ToDo joins the pool, composed against the same seeded store the fixture used.
	cat, _, err := s.runCatalog(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	todo := runs.Run{ID: "run_t", Kind: model.KindTodo, Title: "Fix login", Task: "Repair the reload bug."}
	runs.ComposeInto(&todo, cat)
	if _, err := s.runs.Patch(func(cur []runs.Run) ([]runs.Run, error) { return append(cur, todo), nil }); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(todo.PromptSnapshot, "Stay minimal.") {
		t.Fatalf("a todo prompt must carry the axiom wording:\n%s", todo.PromptSnapshot)
	}
	if strings.Contains(todo.PromptSnapshot, "CLAUDE.md") {
		t.Errorf("a todo prompt must not defer the wording to a repository file:\n%s", todo.PromptSnapshot)
	}

	// Write an Implementierungsregel through the real handler; the same request must recompose both.
	postJSON(t, s.addAxiom, http.MethodPost, "/api/mercury/axiom",
		`{"titel":"Ask first","body":"Ask before you guess.","section":"regeln"}`)

	all, err := s.runs.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 {
		t.Fatalf("both runs must still be in the pool, got %d", len(all))
	}
	for _, r := range all {
		if !strings.Contains(r.PromptSnapshot, "Stay minimal.") {
			t.Errorf("%s (%s) lost the axiom wording:\n%s", r.ID, r.Kind, r.PromptSnapshot)
		}
		if sec := sectionAt(r.PromptSnapshot, "Ask before you guess."); sec != "Verfassung — Implementierungsregeln" {
			t.Errorf("%s (%s) does not carry the implementation rule in its own section (got %q):\n%s",
				r.ID, r.Kind, sec, r.PromptSnapshot)
		}
	}
}

// sectionAt names the "## " heading a piece of prompt text sits under. Presence alone no longer
// tells a move apart from a no-op: every prompt carries the whole constitution, so a re-filed record
// moves between sections instead of appearing or disappearing.
func sectionAt(prompt, needle string) string {
	i := strings.Index(prompt, needle)
	if i < 0 {
		return "(absent)"
	}
	j := strings.LastIndex(prompt[:i], "\n## ")
	if j < 0 {
		return "(no heading)"
	}
	head := prompt[j+len("\n## "):]
	if k := strings.IndexByte(head, '\n'); k >= 0 {
		head = head[:k]
	}
	return head
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

	// (4) MOVE — re-filing the parked rule OUT of laeufe/ recomposes. The wording does NOT vanish:
	// every prompt carries the whole constitution (REQ-002.1), so a re-filed record changes SECTION,
	// from the run rules into the axioms. The section is what proves the move reached composition.
	postJSON(t, s.moveAxiom, http.MethodPost, "/api/mercury/move",
		`{"from":"laeufe/unsortiert/honest-delivery.md","to":"axiome/imported/honest-delivery.md"}`)
	afterMove := f.run1(t)
	if got := sectionAt(afterMove.PromptSnapshot, "Ship honestly."); got != "Verfassung — Axiome" {
		t.Errorf("move must recompose — the re-filed record sits under %q:\n%s", got, afterMove.PromptSnapshot)
	}
	if afterMove.PromptInputHash == afterRename.PromptInputHash {
		t.Error("a move must change the composed input hash")
	}

	// (5) MOVE CATEGORY — the whole category returns under laeufe/, so the record is a run rule again.
	postJSON(t, s.moveCategory, http.MethodPost, "/api/mercury/move-category",
		`{"from":"axiome/imported","to":"laeufe/imported"}`)
	afterMoveCat := f.run1(t)
	if got := sectionAt(afterMoveCat.PromptSnapshot, "Ship honestly."); got != "Laufregeln (gelten für den gesamten Lauf)" {
		t.Errorf("move-category must recompose — the record sits under %q:\n%s", got, afterMoveCat.PromptSnapshot)
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

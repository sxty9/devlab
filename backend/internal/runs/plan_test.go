package runs

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"devlab/backend/internal/mercury"
	"devlab/backend/internal/model"
)

func testCatalog() Catalog {
	return Catalog{
		ByID: map[string]mercury.RunAxiom{
			"ax_a": {ID: "ax_a", Titel: "Minimalism", Body: "Keep the surface small."},
			"ax_b": {ID: "ax_b", Titel: "Uniformity", Body: "One structure everywhere."},
		},
		Regeln:     []mercury.RunAxiom{{ID: "ir_1", Titel: "Ask first", Body: "Ask before you guess."}},
		Laufregeln: []mercury.RunAxiom{{ID: "lr_1", Titel: "Run rule", Body: "Work incrementally."}},
	}
}

// constitutionWording is what BOTH kinds of prompt have to carry verbatim (REQ-002.1): every axiom
// and every Implementierungsregel.
var constitutionWording = []string{
	"Minimalism", "Keep the surface small.",
	"Uniformity", "One structure everywhere.",
	"Ask first", "Ask before you guess.",
}

// ComposeInto is the ONE composition path (REQ-003): an auto run's snapshot carries the whole
// constitution plus every run rule, names its own axioms as the subject in the run's order, and
// fingerprints all of it — a pure rename recomposes to a different hash.
func TestComposeIntoAutoRun(t *testing.T) {
	cat := testCatalog()
	r := Run{ID: "run_1", Kind: model.KindAuto, Title: "Weekly sweep", AxiomIDs: []string{"ax_b", "ax_a", "ax_missing"}}
	ComposeInto(&r, cat)

	for _, want := range append([]string{"Weekly sweep", "Run rule", "Work incrementally."}, constitutionWording...) {
		if !strings.Contains(r.PromptSnapshot, want) {
			t.Errorf("snapshot missing %q", want)
		}
	}
	// The run's own axiom order governs the subject list (unknown ids drop out).
	subject := r.PromptSnapshot[strings.Index(r.PromptSnapshot, "Gegenstand dieses Laufs"):]
	if strings.Index(subject, "- Uniformity") > strings.Index(subject, "- Minimalism") {
		t.Errorf("the subject must follow the run's order (ax_b before ax_a):\n%s", subject)
	}
	if strings.Contains(subject, "ax_missing") {
		t.Error("an unknown axiom id must not appear in the subject")
	}
	if r.PromptInputHash == "" {
		t.Fatal("an auto run must carry the input hash")
	}

	// A pure title rename of an axiom changes the composed prompt heading — the hash MUST move
	// (REQ-003: hash includes the title).
	renamed := testCatalog()
	ax := renamed.ByID["ax_a"]
	ax.Titel = "Minimalism (renamed)"
	renamed.ByID["ax_a"] = ax
	r2 := Run{ID: "run_1", Kind: model.KindAuto, Title: "Weekly sweep", AxiomIDs: []string{"ax_b", "ax_a"}}
	ComposeInto(&r2, renamed)
	r3 := Run{ID: "run_1", Kind: model.KindAuto, Title: "Weekly sweep", AxiomIDs: []string{"ax_b", "ax_a"}}
	ComposeInto(&r3, testCatalog())
	if r2.PromptInputHash == r3.PromptInputHash {
		t.Error("a pure axiom rename must change the input hash")
	}
}

// A ToDo carries the constitution in full wording too (REQ-002.1). It is the way hand-started work
// runs, so the wording must reach it through the prompt — the repositories' CLAUDE.md only points
// back at the prompt, so a ToDo without the wording would have none at all.
func TestComposeIntoTodoCarriesTheConstitution(t *testing.T) {
	r := Run{
		ID: "run_t", Kind: model.KindTodo, Title: "Fix login", Task: "Repair the reload bug.",
		Attachments: []AttachmentRef{{ID: "att_1", Filename: "shot.png", MIME: "image/png"}},
	}
	ComposeInto(&r, testCatalog())
	for _, want := range append([]string{"Fix login", "Repair the reload bug.", "shot.png"}, constitutionWording...) {
		if !strings.Contains(r.PromptSnapshot, want) {
			t.Errorf("todo snapshot missing %q\n---\n%s", want, r.PromptSnapshot)
		}
	}
	if strings.Contains(r.PromptSnapshot, "CLAUDE.md") {
		t.Error("a todo prompt must not defer the wording to the repository's CLAUDE.md")
	}
	// Symmetry with the auto kind: both fingerprint their composition inputs, so a constitution
	// write can tell whether either has drifted.
	if r.PromptInputHash == "" {
		t.Error("a todo must carry the input hash too")
	}
	// A run rule governs how a RUN is conducted; it is not part of a one-time task's prompt.
	if strings.Contains(r.PromptSnapshot, "Work incrementally.") {
		t.Error("run rules must not fold into a todo prompt")
	}
}

// A catalog that never saw the store is NAMED in the prompt, not composed as an empty
// constitution — and it must never overwrite a prompt that already carries the wording.
func TestComposeIntoNamesAnUnscannedStore(t *testing.T) {
	if (Catalog{}).Scanned() {
		t.Error("the zero catalog must count as unscanned")
	}
	if !(Catalog{ByID: map[string]mercury.RunAxiom{}}).Scanned() {
		t.Error("a scanned but empty store must count as scanned (empty is not unreadable)")
	}

	r := Run{ID: "run_t", Kind: model.KindTodo, Title: "T", Task: "Do it."}
	ComposeInto(&r, Catalog{})
	if !strings.Contains(r.PromptSnapshot, "nicht gelesen") {
		t.Errorf("an unscanned store must be named in the prompt:\n%s", r.PromptSnapshot)
	}

	good := Run{ID: "run_t", Kind: model.KindTodo, Title: "T", Task: "Do it."}
	ComposeInto(&good, testCatalog())
	kept := RecomposeDrifted([]Run{good}, Catalog{})
	if kept[0].PromptSnapshot != good.PromptSnapshot {
		t.Error("an unscanned catalog must recompose nothing — a failed read may not damage a run")
	}
}

// The composed prompt is deterministic: the catalog's axiom map has no order, so composition
// imposes one. Two composes of the same store content must be byte-identical, or every reconcile
// would rewrite every run.
func TestComposeIntoIsDeterministic(t *testing.T) {
	first := Run{ID: "run_t", Kind: model.KindTodo, Title: "T", Task: "Do it."}
	ComposeInto(&first, testCatalog())
	for i := 0; i < 20; i++ {
		again := Run{ID: "run_t", Kind: model.KindTodo, Title: "T", Task: "Do it."}
		ComposeInto(&again, testCatalog())
		if again.PromptSnapshot != first.PromptSnapshot || again.PromptInputHash != first.PromptInputHash {
			t.Fatalf("composition is not deterministic (iteration %d)", i)
		}
	}
}

// An axiom change reaches the NEXT prompt of BOTH kinds without any intermediate step: no rollout,
// no refresh button — the write's recomposition is the whole mechanism (REQ-002.1 + REQ-003).
// Runs whose inputs did not move keep their snapshot byte for byte.
func TestRecomposeDriftedReachesBothKinds(t *testing.T) {
	cat := testCatalog()
	auto := Run{ID: "run_1", Kind: model.KindAuto, Title: "Sweep", AxiomIDs: []string{"ax_a"}}
	todo := Run{ID: "run_t", Kind: model.KindTodo, Title: "T", Task: "Do it."}
	ComposeInto(&auto, cat)
	ComposeInto(&todo, cat)

	// Nothing changed → nothing is rewritten.
	same := RecomposeDrifted([]Run{auto, todo}, testCatalog())
	if same[0].PromptSnapshot != auto.PromptSnapshot || same[1].PromptSnapshot != todo.PromptSnapshot ||
		same[0].PromptInputHash != auto.PromptInputHash || same[1].PromptInputHash != todo.PromptInputHash {
		t.Error("an unchanged catalog must leave every snapshot untouched")
	}

	// Edit an axiom the TODO never selected (a todo selects none) and the auto run does not carry
	// as its subject either — both prompts still carry it, so both must be recomposed.
	next := testCatalog()
	ax := next.ByID["ax_b"]
	ax.Body = "One structure everywhere, without exception."
	next.ByID["ax_b"] = ax

	out := RecomposeDrifted([]Run{auto, todo}, next)
	for _, r := range out {
		if !strings.Contains(r.PromptSnapshot, "without exception") {
			t.Errorf("%s did not pick up the axiom change:\n%s", r.ID, r.PromptSnapshot)
		}
		if r.PromptInputHash == "" {
			t.Errorf("%s lost its input hash", r.ID)
		}
	}
	if out[0].PromptInputHash == auto.PromptInputHash || out[1].PromptInputHash == todo.PromptInputHash {
		t.Error("a constitution change must move the fingerprint of both kinds")
	}

	// The same holds for an Implementierungsregel — it is part of the wording both kinds carry.
	ruleChanged := testCatalog()
	ruleChanged.Regeln[0].Body = "Ask before you guess, always."
	out = RecomposeDrifted([]Run{auto, todo}, ruleChanged)
	for _, r := range out {
		if !strings.Contains(r.PromptSnapshot, "always") {
			t.Errorf("%s did not pick up the implementation-rule change", r.ID)
		}
	}
}

// There is exactly ONE composition path, and this is the check that keeps it that way: only the
// composer renders constitution wording, only ComposeInto calls it, and only ComposeInto writes a
// prompt snapshot. A second path would be how the ToDo prompt drifted away from the law once.
func TestExactlyOneCompositionPath(t *testing.T) {
	root := moduleRoot(t)
	for _, tc := range []struct {
		what    string
		needle  string
		allowed []string
	}{
		{"renders constitution headings", `"## Verfassung`, []string{"internal/mercury/compose.go"}},
		{"calls the composer", "mercury.ComposePrompt(", []string{"internal/runs/plan.go"}},
		{"writes a prompt snapshot", ".PromptSnapshot =", []string{"internal/runs/plan.go"}},
		{"fingerprints composition inputs", "mercury.PromptInputsHash(", []string{"internal/runs/plan.go"}},
	} {
		hits := grepGoSources(t, root, tc.needle)
		if len(hits) == 0 {
			t.Errorf("%s: %q occurs nowhere — the check has lost its subject", tc.what, tc.needle)
		}
		for _, h := range hits {
			if !containsString(tc.allowed, h) {
				t.Errorf("%s: %q also occurs in %s — expected only %v", tc.what, tc.needle, h, tc.allowed)
			}
		}
	}
}

// moduleRoot is the directory holding go.mod (the backend module root).
func moduleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("no go.mod found above the test directory")
		}
		dir = parent
	}
}

// grepGoSources returns the module-relative paths of the non-test Go files containing needle.
func grepGoSources(t *testing.T, root, needle string) []string {
	t.Helper()
	var out []string
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		b, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		if strings.Contains(string(b), needle) {
			rel, _ := filepath.Rel(root, path)
			out = append(out, filepath.ToSlash(rel))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func containsString(in []string, want string) bool {
	for _, s := range in {
		if s == want {
			return true
		}
	}
	return false
}

// UpsertPlannedRun is the one planning fold (REQ-004): an existing auto run of the same title
// is EXTENDED (axioms deduped, snapshot recomposed) instead of duplicated; a todo sharing the
// title is never turned into an axiom carrier; anything else appends.
func TestUpsertPlannedRun(t *testing.T) {
	cat := testCatalog()
	now := time.Date(2026, 7, 26, 9, 0, 0, 0, time.UTC)
	by := model.Actor{Autonomous: true}

	existing := Run{ID: "run_1", Kind: model.KindAuto, Title: "Sweep", AxiomIDs: []string{"ax_a"}}
	todoTwin := Run{ID: "run_2", Kind: model.KindTodo, Title: "Sweep", Task: "unrelated"}

	// Same title (case-insensitive) → extend, dedup, recompose; no new run.
	out, affected, created := UpsertPlannedRun([]Run{existing, todoTwin}, Run{ID: "run_new", Kind: model.KindAuto, Title: "sweep", AxiomIDs: []string{"ax_a", "ax_b"}}, cat, now, by)
	if created {
		t.Fatal("an existing auto run of the same title must be extended, not duplicated")
	}
	if len(out) != 2 || affected.ID != "run_1" {
		t.Fatalf("fold result: len=%d affected=%s", len(out), affected.ID)
	}
	if got := out[0].AxiomIDs; len(got) != 2 || got[0] != "ax_a" || got[1] != "ax_b" {
		t.Fatalf("axioms not deduped/extended: %v", got)
	}
	if out[0].PromptSnapshot == "" || !strings.Contains(out[0].PromptSnapshot, "- Uniformity") {
		t.Fatal("the extended run must be recomposed in the same pass (REQ-004.3)")
	}
	if out[0].Authorship.Updated != by || !out[0].Authorship.UpdatedAt.Equal(now) {
		t.Fatalf("the fold must stamp the acting instance (REQ-041): %+v", out[0].Authorship)
	}
	// The todo twin stays untouched.
	if out[1].Kind != model.KindTodo || len(out[1].AxiomIDs) != 0 {
		t.Fatalf("a todo sharing the title must never become an axiom carrier: %+v", out[1])
	}

	// No title match → append as a new run.
	out2, _, created2 := UpsertPlannedRun([]Run{existing}, Run{ID: "run_new", Kind: model.KindAuto, Title: "Other", AxiomIDs: []string{"ax_b"}}, cat, now, by)
	if !created2 || len(out2) != 2 {
		t.Fatalf("unmatched plan must append: created=%v len=%d", created2, len(out2))
	}
}

func TestDedupStrings(t *testing.T) {
	got := DedupStrings([]string{" a ", "b", "a", "", "  ", "b", "c"})
	if len(got) != 3 || got[0] != "a" || got[1] != "b" || got[2] != "c" {
		t.Fatalf("DedupStrings = %v", got)
	}
}

// AxiomsFor keeps the run's order and silently drops unknown ids (a deleted axiom must not
// wedge composition).
func TestCatalogAxiomsFor(t *testing.T) {
	cat := testCatalog()
	got := cat.AxiomsFor([]string{"ax_b", "ax_gone", "ax_a"})
	if len(got) != 2 || got[0].ID != "ax_b" || got[1].ID != "ax_a" {
		t.Fatalf("AxiomsFor = %+v", got)
	}
}

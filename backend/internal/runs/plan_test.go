package runs

import (
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
		Laufregeln: []mercury.RunAxiom{{ID: "lr_1", Titel: "Run rule", Body: "Work incrementally."}},
	}
}

// ComposeInto is the ONE composition path (REQ-003): an auto run's snapshot folds in its axioms
// (in the run's own order) plus every global run rule, and the input hash covers the TITLES —
// a pure rename recomposes to a different fingerprint.
func TestComposeIntoAutoRun(t *testing.T) {
	cat := testCatalog()
	r := Run{ID: "run_1", Kind: model.KindAuto, Title: "Weekly sweep", AxiomIDs: []string{"ax_b", "ax_a", "ax_missing"}}
	ComposeInto(&r, cat)

	for _, want := range []string{"Weekly sweep", "Minimalism", "Uniformity", "Keep the surface small.", "Run rule", "Work incrementally."} {
		if !strings.Contains(r.PromptSnapshot, want) {
			t.Errorf("snapshot missing %q", want)
		}
	}
	// The run's own axiom order governs the composition.
	if strings.Index(r.PromptSnapshot, "Uniformity") > strings.Index(r.PromptSnapshot, "Keep the surface small.") {
		t.Error("axioms must compose in the run's order (ax_b before ax_a)")
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

// A todo composes from its own task text + attachment manifest; the constitution reaches the
// agent via CLAUDE.md, so no axioms fold in and no input hash applies.
func TestComposeIntoTodo(t *testing.T) {
	r := Run{
		ID: "run_t", Kind: model.KindTodo, Title: "Fix login", Task: "Repair the reload bug.",
		Attachments: []AttachmentRef{{ID: "att_1", Filename: "shot.png", MIME: "image/png"}},
	}
	ComposeInto(&r, testCatalog())
	for _, want := range []string{"Fix login", "Repair the reload bug.", "shot.png"} {
		if !strings.Contains(r.PromptSnapshot, want) {
			t.Errorf("todo snapshot missing %q", want)
		}
	}
	if strings.Contains(r.PromptSnapshot, "Minimalism") {
		t.Error("a todo must not fold axioms into its snapshot")
	}
	if r.PromptInputHash != "" {
		t.Error("a todo carries no axiom input hash")
	}
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
	if out[0].PromptSnapshot == "" || !strings.Contains(out[0].PromptSnapshot, "Uniformity") {
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

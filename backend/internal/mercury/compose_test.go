package mercury

import (
	"strings"
	"testing"
)

func TestComposeRunPrompt(t *testing.T) {
	axs := []RunAxiom{{ID: "ax_1", Titel: "Minimalism", Body: "Sei minimal."}}
	rules := []RunAxiom{{ID: "r_1", Titel: "Klärung", Body: "Keine Rückfragen."}}
	p := ComposeRunPrompt("Nachtlauf", axs, rules)
	for _, want := range []string{"Nachtlauf", "Minimalism", "Sei minimal.", "Klärung", "Keine Rückfragen.", "Laufregeln", "Axiome"} {
		if !strings.Contains(p, want) {
			t.Errorf("prompt missing %q\n---\n%s", want, p)
		}
	}
}

func TestComposeTodoPrompt(t *testing.T) {
	// Without attachments the media section must be absent.
	p := ComposeTodoPrompt("Fix Login", "Behebe den Bug.", "", nil)
	for _, want := range []string{"Fix Login", "Behebe den Bug.", "Aufgabe"} {
		if !strings.Contains(p, want) {
			t.Errorf("todo prompt missing %q\n%s", want, p)
		}
	}
	if strings.Contains(p, "Angehängte Medien") {
		t.Errorf("no-attachment prompt must not add a media section\n%s", p)
	}

	// With attachments: the section, each file's workspace path + type, and the do-not-commit note.
	atts := []TodoAttachment{
		{Filename: "mockup.png", MIME: "image/png"},
		{Filename: "spec.pdf", MIME: "application/pdf"},
	}
	p = ComposeTodoPrompt("Redesign", "Setze das Mockup um.", "", atts)
	for _, want := range []string{
		"Angehängte Medien",
		TodoAttachmentRel("mockup.png"), // .mercury/attachments/mockup.png
		"image/png",
		TodoAttachmentRel("spec.pdf"),
		"application/pdf",
		"committe sie nicht",
	} {
		if !strings.Contains(p, want) {
			t.Errorf("media prompt missing %q\n---\n%s", want, p)
		}
	}

	// A new-repo target keeps its scaffold note alongside the media block.
	p = ComposeTodoPrompt("Neuer Service", "Baue X.", "mein-service", atts)
	if !strings.Contains(p, "mein-service") || !strings.Contains(p, "Angehängte Medien") {
		t.Errorf("new-repo + media prompt missing parts\n%s", p)
	}
}

func TestRunInputsHash(t *testing.T) {
	a := []RunAxiom{{ID: "ax_1", Body: "x"}}
	r := []RunAxiom{{ID: "r_1", Body: "y"}}
	h1 := RunInputsHash(a, r)
	if h1 != RunInputsHash(a, r) {
		t.Error("hash not stable across calls")
	}
	// a pure re-title MUST change the hash: the title is the axiom's heading in the composed prompt,
	// so a rename genuinely changes the prompt and must not go unnoticed.
	if RunInputsHash([]RunAxiom{{ID: "ax_1", Titel: "New Title", Body: "x"}}, r) == h1 {
		t.Error("title change did not change the hash")
	}
	// a body change MUST change the hash (staleness)
	if RunInputsHash([]RunAxiom{{ID: "ax_1", Body: "z"}}, r) == h1 {
		t.Error("body change did not change the hash")
	}
	// order-independence
	two := []RunAxiom{{ID: "ax_1", Body: "x"}, {ID: "ax_2", Body: "w"}}
	twoRev := []RunAxiom{{ID: "ax_2", Body: "w"}, {ID: "ax_1", Body: "x"}}
	if RunInputsHash(two, r) != RunInputsHash(twoRev, r) {
		t.Error("hash depends on axiom order")
	}
}

// TestRepoScopeSection pins the incremental scope: an axiom with a recorded stand is scoped to the
// commits after it, one without a record is named explicitly as full-repository work (silence would
// leave the agent guessing), and the misleading word "Checkpoint" appears nowhere.
func TestRepoScopeSection(t *testing.T) {
	axioms := []RunAxiom{{ID: "ax_a", Titel: "Passive Speicher"}, {ID: "ax_b", Titel: "Atomare Zugriffe"}}
	out := RepoScopeSection(axioms, map[string]LastCheck{"ax_a": {Commit: "abc1234", At: "2026-07-20"}})

	for _, want := range []string{"Passive Speicher", "abc1234", "2026-07-20", "Atomare Zugriffe", "GESAMTE Repository"} {
		if !strings.Contains(out, want) {
			t.Errorf("scope section is missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(strings.ToLower(out), "checkpoint") {
		t.Errorf("the scope section must not use the misleading term 'Checkpoint':\n%s", out)
	}
	if RepoScopeSection(nil, nil) != "" {
		t.Error("a run without axioms must add no scope section")
	}
}

// TestComposeRunPromptHasNoCheckpointClaim pins that the preamble no longer asserts a record that
// nothing writes: it points at the scope section this package actually renders.
func TestComposeRunPromptHasNoCheckpointClaim(t *testing.T) {
	p := ComposeRunPrompt("Lauf", []RunAxiom{{ID: "ax_a", Titel: "A", Body: "b"}}, nil)
	if strings.Contains(strings.ToLower(p), "checkpoint") {
		t.Errorf("run prompt still claims a 'Checkpoint' record:\n%s", p)
	}
	if !strings.Contains(p, "Zuletzt geprüfter Stand") {
		t.Errorf("run prompt should point at the scope section:\n%s", p)
	}
}

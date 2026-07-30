package mercury

import (
	"strings"
	"testing"
)

func testCorpus() Corpus {
	return Corpus{
		Read: true,
		Axiome: []RunAxiom{
			{ID: "ax_1", Titel: "Minimalism", Body: "Sei minimal."},
			{ID: "ax_2", Titel: "Symmetrie", Body: "Sei symmetrisch."},
		},
		Regeln: []RunAxiom{{ID: "ir_1", Titel: "Klärung", Body: "Frage nach, wenn etwas unklar ist."}},
	}
}

// REQ-002.1 for BOTH kinds: the prompt of every execution carries the constitution — axioms AND
// Implementierungsregeln — in full wording. This is the regression test for the wording reaching a
// ToDo at all: before the repair a ToDo prompt carried none of it and pointed at the repository's
// CLAUDE.md instead, which since the abolished rollout only points back at the prompt.
func TestComposePromptCarriesTheConstitutionForBothKinds(t *testing.T) {
	for _, tc := range []struct {
		name string
		spec PromptSpec
	}{
		{"todo", PromptSpec{Todo: true, Title: "Fix Login", Task: "Behebe den Bug.", Corpus: testCorpus()}},
		{"auto", PromptSpec{
			Title:      "Nachtlauf",
			Corpus:     testCorpus(),
			Subject:    []RunAxiom{{ID: "ax_1", Titel: "Minimalism", Body: "Sei minimal."}},
			Laufregeln: []RunAxiom{{ID: "lr_1", Titel: "Bericht", Body: "Berichte ehrlich."}},
		}},
	} {
		p := ComposePrompt(tc.spec)
		for _, want := range []string{
			"Verfassung — Axiome",
			"Minimalism", "Sei minimal.",
			"Symmetrie", "Sei symmetrisch.",
			"Verfassung — Implementierungsregeln",
			"Klärung", "Frage nach, wenn etwas unklar ist.",
		} {
			if !strings.Contains(p, want) {
				t.Errorf("%s prompt is missing %q\n---\n%s", tc.name, want, p)
			}
		}
	}
}

// The composed prompt must not send the agent to the repository's CLAUDE.md for the wording: that
// file carries only the reference text now (claudemd.go), so such a pointer would be a circle in
// which the wording never appears.
func TestComposePromptNeverPointsAtTheRepositoryClaudeMD(t *testing.T) {
	for _, spec := range []PromptSpec{
		{Todo: true, Title: "T", Task: "Tu etwas.", Corpus: testCorpus()},
		{Title: "L", Corpus: testCorpus(), Subject: testCorpus().Axiome},
	} {
		p := ComposePrompt(spec)
		if strings.Contains(p, "CLAUDE.md") {
			t.Errorf("the prompt must carry the wording itself, not point at CLAUDE.md:\n%s", p)
		}
		if !strings.Contains(p, "Vorrang vor jeder anderen Fassung") {
			t.Errorf("the prompt must claim precedence over any other wording found in a repo:\n%s", p)
		}
	}
}

// An unreadable store is NAMED, never composed as an empty constitution: "no axioms" and "could not
// read the axioms" must not look the same to the agent (REQ-001.3 carried into the prompt).
func TestComposePromptNamesAnUnreadCorpus(t *testing.T) {
	p := ComposePrompt(PromptSpec{Todo: true, Title: "T", Task: "Tu etwas."}) // zero corpus = unread
	if !strings.Contains(p, "nicht gelesen") || !strings.Contains(p, "NICHT die Aussage, dass es keine Axiome gibt") {
		t.Errorf("an unread corpus must be named as such:\n%s", p)
	}
	if strings.Contains(p, "Verfassung — Axiome") {
		t.Errorf("an unread corpus must not render an empty axiom section:\n%s", p)
	}

	// A store that WAS read and is empty says exactly that — distinguishable from the above.
	empty := ComposePrompt(PromptSpec{Todo: true, Title: "T", Task: "Tu etwas.", Corpus: Corpus{Read: true}})
	if !strings.Contains(empty, "Der Bestand enthält derzeit keine Axiome.") ||
		!strings.Contains(empty, "Der Bestand enthält derzeit keine Implementierungsregeln.") {
		t.Errorf("a read but empty corpus must be stated as empty:\n%s", empty)
	}
	if strings.Contains(empty, "nicht gelesen") {
		t.Errorf("an empty corpus must not be reported as unreadable:\n%s", empty)
	}
}

// The wording stands exactly ONCE in a prompt: a run's subject names its axioms by title and leans
// on the constitution section above, instead of repeating the same bodies a second time.
func TestComposeRunPromptCarriesEachWordingOnce(t *testing.T) {
	p := ComposePrompt(PromptSpec{
		Title:   "Nachtlauf",
		Corpus:  testCorpus(),
		Subject: []RunAxiom{{ID: "ax_1", Titel: "Minimalism", Body: "Sei minimal."}},
	})
	if n := strings.Count(p, "Sei minimal."); n != 1 {
		t.Errorf("the axiom body must appear exactly once, got %d:\n%s", n, p)
	}
	if !strings.Contains(p, "Gegenstand dieses Laufs") || !strings.Contains(p, "- Minimalism") {
		t.Errorf("the run's subject must be named:\n%s", p)
	}
	// A run without axioms says so instead of leaving the reader to guess.
	bare := ComposePrompt(PromptSpec{Title: "Leer", Corpus: testCorpus()})
	if !strings.Contains(bare, "kein Axiom zugeordnet") {
		t.Errorf("a run without a subject must name that:\n%s", bare)
	}
}

// A ToDo keeps its task and its media block; without media no media section appears.
func TestComposeTodoPromptTaskAndMedia(t *testing.T) {
	p := ComposePrompt(PromptSpec{Todo: true, Title: "Fix Login", Task: "Behebe den Bug.", Corpus: testCorpus()})
	for _, want := range []string{"Konkretes ToDo: Fix Login", "Aufgabe", "Behebe den Bug."} {
		if !strings.Contains(p, want) {
			t.Errorf("todo prompt missing %q\n%s", want, p)
		}
	}
	if strings.Contains(p, "Angehängte Medien") {
		t.Errorf("no-attachment prompt must not add a media section\n%s", p)
	}

	p = ComposePrompt(PromptSpec{
		Todo: true, Title: "Redesign", Task: "Setze das Mockup um.", Corpus: testCorpus(),
		Attachments: []TodoAttachment{
			{Filename: "mockup.png", MIME: "image/png"},
			{Filename: "spec.pdf", MIME: "application/pdf"},
		},
	})
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
}

// PromptInputsHash is the drift fingerprint: it must move when ANYTHING the composer reads moves —
// including a corpus record the run does not have as its subject, because every prompt now carries
// the whole corpus. Store-wide section ORDER is normalized away; the run's own subject order is not.
func TestPromptInputsHash(t *testing.T) {
	base := PromptSpec{
		Todo: true, Title: "T", Task: "Tu etwas.", Corpus: testCorpus(),
	}
	h := PromptInputsHash(base)
	if h != PromptInputsHash(base) {
		t.Error("hash not stable across calls")
	}

	mutate := func(name string, fn func(*PromptSpec)) {
		s := base
		s.Corpus = testCorpus()
		fn(&s)
		if PromptInputsHash(s) == h {
			t.Errorf("%s did not change the hash", name)
		}
	}
	mutate("a corpus body change", func(s *PromptSpec) { s.Corpus.Axiome[1].Body = "anders" })
	mutate("a corpus title change (pure rename)", func(s *PromptSpec) { s.Corpus.Axiome[1].Titel = "Neu" })
	mutate("an implementation-rule change", func(s *PromptSpec) { s.Corpus.Regeln[0].Body = "anders" })
	mutate("a run title change", func(s *PromptSpec) { s.Title = "T2" })
	mutate("a task change", func(s *PromptSpec) { s.Task = "Tu etwas anderes." })
	mutate("an attachment change", func(s *PromptSpec) {
		s.Attachments = []TodoAttachment{{Filename: "a.png", MIME: "image/png"}}
	})
	mutate("losing the corpus", func(s *PromptSpec) { s.Corpus = Corpus{} })
	mutate("a laufregel change", func(s *PromptSpec) {
		s.Laufregeln = []RunAxiom{{ID: "lr_1", Body: "x"}}
	})

	// Store order is not prompt order: a re-ordered scan must not recompose every run.
	rev := base
	rev.Corpus = testCorpus()
	rev.Corpus.Axiome = []RunAxiom{rev.Corpus.Axiome[1], rev.Corpus.Axiome[0]}
	if PromptInputsHash(rev) != h {
		t.Error("a pure corpus re-order must not change the hash")
	}
	// The run's own subject order IS prompt order.
	s1 := base
	s1.Todo = false
	s1.Subject = testCorpus().Axiome
	s2 := s1
	s2.Subject = []RunAxiom{s1.Subject[1], s1.Subject[0]}
	if PromptInputsHash(s1) == PromptInputsHash(s2) {
		t.Error("a subject re-order must change the hash")
	}
	// The kinds are distinguishable even with identical inputs.
	auto := base
	auto.Todo = false
	if PromptInputsHash(auto) == h {
		t.Error("the run kind must be part of the fingerprint")
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

// The run preamble must not assert a record nothing writes, and it must stay TRUE whether or not the
// per-repo scope section is appended: it names the section conditionally and says what to do without
// it (the section is per repository, so it can only be appended at execution time).
func TestComposeRunPromptScopeClaimIsConditional(t *testing.T) {
	p := ComposePrompt(PromptSpec{Title: "Lauf", Corpus: testCorpus(), Subject: testCorpus().Axiome})
	if strings.Contains(strings.ToLower(p), "checkpoint") {
		t.Errorf("run prompt still claims a 'Checkpoint' record:\n%s", p)
	}
	if !strings.Contains(p, "Zuletzt geprüfter Stand") {
		t.Errorf("run prompt should name the scope section:\n%s", p)
	}
	if !strings.Contains(p, "fehlt er") {
		t.Errorf("run prompt must say what to do when the scope section is absent:\n%s", p)
	}
}

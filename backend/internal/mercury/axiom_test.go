package mercury

import (
	"errors"
	"strings"
	"testing"
)

// A well-formed record reads back field for field, and the round trip through Render is an identity —
// this is the shape every axiom in the constitution has.
func TestParseAxiomCleanRecord(t *testing.T) {
	const content = "---\n" +
		"id: ax_01J8Z9Q4K7T3M2\n" +
		"titel: Kein paralleler Datenpfad\n" +
		"quelle: axioms/SOURCE.md#single-source-of-truth\n" +
		"---\n" +
		"Existiert ein Zugangspunkt, ist er wiederzuverwenden.\n" +
		"\n" +
		"Zweiter Absatz.\n"

	ax := ParseAxiom(content)
	if ax.Defect != "" || ax.Err() != nil {
		t.Fatalf("a clean record must carry no defect, got %q / %v", ax.Defect, ax.Err())
	}
	if ax.ID != "ax_01J8Z9Q4K7T3M2" || ax.Titel != "Kein paralleler Datenpfad" ||
		ax.Quelle != "axioms/SOURCE.md#single-source-of-truth" {
		t.Errorf("front matter misread: %+v", ax)
	}
	if ax.Body != "Existiert ein Zugangspunkt, ist er wiederzuverwenden.\n\nZweiter Absatz." {
		t.Errorf("body = %q", ax.Body)
	}
	if again := ParseAxiom(Render(ax)); again != ax {
		t.Errorf("round trip changed the record:\n got %+v\nwant %+v", again, ax)
	}
}

// THE defect this parser must never repeat: front matter that opens with "---" and never closes. The
// closing delimiter is searched to the end of the record, so the wording loop then has nothing left and
// the axiom comes out with an EMPTY body — a wordless axiom that still travels through every run prompt
// and into every generated CLAUDE.md, where the loss can no longer be seen. The record must instead
// arrive whole and named as defective.
func TestParseAxiomUnterminatedFrontMatterKeepsTheWording(t *testing.T) {
	const content = "---\n" +
		"id: ax_1\n" +
		"titel: Keine Redundanz\n" +
		"Keine Änderung darf Redundanz schaffen.\n"

	ax := ParseAxiom(content)
	if ax.Body == "" {
		t.Fatal("an unterminated front-matter block emptied the axiom — the wording is lost")
	}
	if !strings.Contains(ax.Body, "Keine Änderung darf Redundanz schaffen.") {
		t.Errorf("the wording did not survive: body = %q", ax.Body)
	}
	if ax.Defect != defectUnterminatedFrontMatter {
		t.Errorf("defect = %q, want %q", ax.Defect, defectUnterminatedFrontMatter)
	}
	if !errors.Is(ax.Err(), ErrRecordDefect) {
		t.Errorf("Err() = %v, want an error wrapping ErrRecordDefect", ax.Err())
	}
	// The id is still read, so a defective record keeps its identity: a catalog that keys by id can
	// name it (and it can be repaired in place) instead of silently dropping out.
	if ax.ID != "ax_1" || ax.Titel != "Keine Redundanz" {
		t.Errorf("a defective record lost its identity: %+v", ax)
	}
}

// An empty record, and a record with front matter but no wording, are wordless — legal on disk, never
// silently passed on as an axiom. Both are named, so a caller can refuse them.
func TestParseAxiomWordlessRecordsAreNamed(t *testing.T) {
	for _, tc := range []struct{ name, content string }{
		{"empty file", ""},
		{"newlines only", "\n\n\n"},
		{"front matter, no body", "---\nid: ax_1\ntitel: Titel\n---\n"},
		{"front matter, blank body", "---\nid: ax_1\ntitel: Titel\n---\n   \n\n"},
	} {
		ax := ParseAxiom(tc.content)
		if ax.Defect != defectNoWording {
			t.Errorf("%s: defect = %q, want %q", tc.name, ax.Defect, defectNoWording)
		}
		if !errors.Is(ax.Err(), ErrRecordDefect) {
			t.Errorf("%s: Err() = %v, want an error wrapping ErrRecordDefect", tc.name, ax.Err())
		}
	}
}

// The minimal record: one front-matter id, one title, one line of wording. And the minimal record
// without any front matter at all — older, hand-dropped content that is all wording.
func TestParseAxiomMinimalRecords(t *testing.T) {
	ax := ParseAxiom("---\nid: ax_1\ntitel: T\n---\nOne line.\n")
	if ax.Defect != "" || ax.ID != "ax_1" || ax.Titel != "T" || ax.Quelle != "" || ax.Body != "One line." {
		t.Errorf("minimal record misread: %+v (defect %q)", ax, ax.Defect)
	}

	bare := ParseAxiom("Just wording, no front matter.\n")
	if bare.Defect != "" || bare.ID != "" || bare.Body != "Just wording, no front matter." {
		t.Errorf("bare record misread: %+v (defect %q)", bare, bare.Defect)
	}
}

// A "---" inside the wording (a markdown rule) belongs to the wording: the FIRST closing delimiter ends
// the front matter, everything after it is body, verbatim.
func TestParseAxiomRuleInsideTheWordingStays(t *testing.T) {
	ax := ParseAxiom("---\nid: ax_1\ntitel: T\n---\nBefore.\n\n---\n\nAfter.\n")
	if ax.Defect != "" {
		t.Fatalf("this record is well formed, got defect %q", ax.Defect)
	}
	if ax.Body != "Before.\n\n---\n\nAfter." {
		t.Errorf("body = %q", ax.Body)
	}
}

// A record with a line beyond the scanner's limit cannot be read to its end. It is kept whole and the
// failure is named — never quietly truncated, and never quietly empty.
func TestParseAxiomUnreadableRecordIsNamedAndKeptWhole(t *testing.T) {
	huge := strings.Repeat("x", 2<<20) // one line, no newline: past the 1 MiB scanner limit
	ax := ParseAxiom(huge)
	if !strings.HasPrefix(ax.Defect, defectUnreadable) {
		t.Errorf("defect = %q, want it to start with %q", ax.Defect, defectUnreadable)
	}
	if ax.Body != huge {
		t.Errorf("the record was not kept whole: %d bytes of %d", len(ax.Body), len(huge))
	}

	withFrontMatter := "---\nid: ax_1\ntitel: T\n---\n" + huge
	ax = ParseAxiom(withFrontMatter)
	if !strings.HasPrefix(ax.Defect, defectUnreadable) {
		t.Errorf("defect = %q, want it to start with %q", ax.Defect, defectUnreadable)
	}
	if ax.Body != withFrontMatter {
		t.Errorf("the record was not kept whole: %d bytes of %d", len(ax.Body), len(withFrontMatter))
	}
}

// The parser's invariant over every shape a record can take: an empty body is ALWAYS named, and
// wherever the front matter cannot be delimited the record survives verbatim as the body.
func TestParseAxiomInvariantOverEveryShape(t *testing.T) {
	for _, tc := range []struct {
		content string
		body    string // the expected body
	}{
		{"", ""},
		{"\n", ""},
		{"---\n", "---"},                     // opened, never closed ⇒ verbatim
		{"---\nid: ax_1\n", "---\nid: ax_1"}, // same
		{"---\nnot a field\n", "---\nnot a field"}, // same
		{"---\n---\n", ""},                         // closed, no wording
		{"---\nid: ax_1\n---\n", ""},               // closed, no wording
		{"text", "text"},                           // no front matter at all
		{"---\nid: ax_1\n---\ntext\n", "text"},     // the ordinary record
		{"--- \nid: ax_1\n --- \ntext\n", "text"},  // padded delimiters still delimit
		{"---\nid: ax_1\n---\ntext\n\n\n", "text"}, // trailing blank lines trimmed
	} {
		ax := ParseAxiom(tc.content)
		if ax.Body != tc.body {
			t.Errorf("%q: body = %q, want %q", tc.content, ax.Body, tc.body)
		}
		if strings.TrimSpace(ax.Body) == "" && ax.Defect == "" {
			t.Errorf("%q parsed to an empty body without naming why", tc.content)
		}
	}
}

// A defect describes the record that was READ; it must never be written back to disk.
func TestRenderDropsTheDefect(t *testing.T) {
	out := Render(Axiom{ID: "ax_1", Titel: "T", Body: "Wording.", Defect: defectNoWording})
	if strings.Contains(out, "defect") || strings.Contains(out, defectNoWording) {
		t.Errorf("Render leaked the parse defect into the record: %q", out)
	}
	if ParseAxiom(out).Defect != "" {
		t.Errorf("re-reading a rendered record found a defect: %q", out)
	}
}

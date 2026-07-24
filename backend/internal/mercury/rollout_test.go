package mercury

import "strings"

import "testing"

func TestSpliceMarkerPreservesOutside(t *testing.T) {
	old := "# my repo CLAUDE.md\n\nSome local notes.\n\n" + MarkerBegin + "\nOLD BLOCK\n" + MarkerEnd + "\n\nMore notes after.\n"
	out := SpliceMarker(old, "NEW BLOCK")

	if !strings.Contains(out, "Some local notes.") || !strings.Contains(out, "More notes after.") {
		t.Fatalf("surrounding content lost:\n%s", out)
	}
	if strings.Contains(out, "OLD BLOCK") || !strings.Contains(out, "NEW BLOCK") {
		t.Fatalf("block not replaced:\n%s", out)
	}
	// idempotent: splicing the same block again is a no-op
	if again := SpliceMarker(out, "NEW BLOCK"); again != out {
		t.Errorf("splice not idempotent")
	}
}

func TestSpliceMarkerAppendsWhenAbsent(t *testing.T) {
	out := SpliceMarker("# repo\n\nnotes\n", "BLOCK")
	if !strings.Contains(out, "notes") || !strings.Contains(out, MarkerBegin) || !strings.Contains(out, "BLOCK") {
		t.Fatalf("append failed:\n%s", out)
	}
	// into an empty file, no leading blank lines
	empty := SpliceMarker("", "BLOCK")
	if !strings.HasPrefix(empty, MarkerBegin) {
		t.Errorf("empty-file splice should start with the marker:\n%q", empty)
	}
}

func TestRenderBlockGroups(t *testing.T) {
	axiome := []Record{
		{Path: "axiome/architektur/ssot/atomar.md", Axiom: Axiom{Titel: "Atomare Zugriffe", Body: "Jede Abfrage ist atomar."}},
		{Path: "axiome/architektur/ssot/kein-pfad.md", Axiom: Axiom{Titel: "Kein paralleler Datenpfad", Body: "Zwingend\nwiederverwenden."}},
		{Path: "axiome/minimalismus/tooltips.md", Axiom: Axiom{Titel: "Keine Tooltips", Body: "Intuitiv by design."}},
	}
	out := RenderBlock(axiome, nil)
	if !strings.Contains(out, "### architektur / ssot") || !strings.Contains(out, "### minimalismus") {
		t.Fatalf("category headings missing:\n%s", out)
	}
	if !strings.Contains(out, "- **Atomare Zugriffe** — Jede Abfrage ist atomar.") {
		t.Errorf("axiom bullet wrong:\n%s", out)
	}
	// newlines in a body are flattened so a bullet stays one line
	if strings.Contains(out, "Zwingend\nwiederverwenden") {
		t.Errorf("body newline not flattened:\n%s", out)
	}
}

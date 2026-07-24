package mercury

import (
	"strings"
	"testing"
)

func TestDecomposeConstitution(t *testing.T) {
	atoms := Decompose()

	// The source has ~34 fine-grained points; assert a sane range rather than an exact count so the
	// test survives light edits to the constitution.
	if len(atoms) < 25 || len(atoms) > 45 {
		t.Fatalf("expected ~34 atoms, got %d", len(atoms))
	}

	// Every atom must carry a non-empty body, a title, a quelle, and a suggested category.
	seenCats := map[string]int{}
	for i, a := range atoms {
		if strings.TrimSpace(a.Body) == "" {
			t.Errorf("atom %d has empty body", i)
		}
		if a.SeedTitle == "" {
			t.Errorf("atom %d has no title (quelle %s)", i, a.Quelle)
		}
		if !strings.HasPrefix(a.Quelle, "axioms/CLAUDE.MD.md#") {
			t.Errorf("atom %d bad quelle %q", i, a.Quelle)
		}
		if a.SuggestedCategory == "" {
			t.Errorf("atom %d has no suggested category", i)
		}
		seenCats[a.SuggestedCategory]++
	}

	// The two maxims that split into multiple bullets must produce multiple atoms.
	ssot := countQuelle(atoms, "holistic_architecture_maxims/Single Source of Truth")
	if ssot != 2 {
		t.Errorf("SSOT should decompose into 2 atoms, got %d", ssot)
	}
	// Reuse before Build is a numbered procedure → exactly one atom.
	if n := countQuelle(atoms, "holistic_architecture_maxims/Reuse before Build"); n != 1 {
		t.Errorf("Reuse before Build should be 1 atom (numbered procedure), got %d", n)
	}
	// All the expected top-level categories appear.
	for _, want := range []string{"architektur", "prozess", "gesetzbuecher", "umgebung", "mobile"} {
		if seenCats[want] == 0 {
			t.Errorf("no atoms suggested for category %q", want)
		}
	}
	// The nested klaerung_im_nachtlauf block is captured.
	if countQuelle(atoms, "holistic_mobile_maxims/klaerung_im_nachtlauf") != 1 {
		t.Errorf("klaerung_im_nachtlauf atom missing")
	}
}

func countQuelle(atoms []Atom, suffix string) int {
	n := 0
	for _, a := range atoms {
		if strings.HasSuffix(a.Quelle, suffix) {
			n++
		}
	}
	return n
}

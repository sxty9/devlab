package mercury

import (
	"errors"
	"testing"
)

func TestFirstJSONObject(t *testing.T) {
	cases := []struct {
		in   string
		want string
		ok   bool
	}{
		{`{"a":1}`, `{"a":1}`, true},
		{"```json\n{\"a\":{\"b\":2}}\n```", `{"a":{"b":2}}`, true},
		{`prose before {"pfad":"axiome/x/y.md"} and after`, `{"pfad":"axiome/x/y.md"}`, true},
		{`{"s":"a } brace in a string"}`, `{"s":"a } brace in a string"}`, true},
		{`no object here`, "", false},
	}
	for _, c := range cases {
		got, ok := firstJSONObject(c.in)
		if ok != c.ok || got != c.want {
			t.Errorf("firstJSONObject(%q) = %q,%v want %q,%v", c.in, got, ok, c.want, c.ok)
		}
	}
}

func TestParsePlacement(t *testing.T) {
	known := []string{"axiome/architektur", "axiome/architektur/ssot", "axiome/minimalismus"}

	// valid, existing category
	p, err := ParsePlacement(`{"pfad":"axiome/architektur/ssot/atomar.md","beschreibung":"x","titel":"T","neue_kategorie":false,"duplikat_von":null}`, known, "axiome")
	if err != nil {
		t.Fatalf("valid placement rejected: %v", err)
	}
	if p.Pfad != "axiome/architektur/ssot/atomar.md" {
		t.Errorf("pfad = %q", p.Pfad)
	}

	// new category is allowed even though the parent is unknown
	if _, err := ParsePlacement(`{"pfad":"axiome/neu/tief/x.md","beschreibung":"x","titel":"T","neue_kategorie":true}`, known, "axiome"); err != nil {
		t.Errorf("new category rejected: %v", err)
	}

	// bad path (CamelCase segment) → invalid
	if _, err := ParsePlacement(`{"pfad":"axiome/Architektur/x.md","beschreibung":"x","titel":"T","neue_kategorie":true}`, known, "axiome"); !errors.Is(err, ErrInvalidPlacement) {
		t.Errorf("CamelCase path should be invalid, got %v", err)
	}

	// unknown category without neue_kategorie → invalid
	if _, err := ParsePlacement(`{"pfad":"axiome/erfunden/x.md","beschreibung":"x","titel":"T","neue_kategorie":false}`, known, "axiome"); !errors.Is(err, ErrInvalidPlacement) {
		t.Errorf("unknown category should be invalid, got %v", err)
	}

	// empty description → invalid
	if _, err := ParsePlacement(`{"pfad":"axiome/minimalismus/x.md","beschreibung":"  ","titel":"T","neue_kategorie":false}`, known, "axiome"); !errors.Is(err, ErrInvalidPlacement) {
		t.Errorf("empty description should be invalid, got %v", err)
	}

	// no JSON at all
	if _, err := ParsePlacement("das Modell hat nur Prosa geliefert", known, "axiome"); !errors.Is(err, ErrNoJSON) {
		t.Errorf("want ErrNoJSON, got %v", err)
	}
}

// TestParsePlacementNamespaces locks in the symmetric design: regeln and laeufe file exactly like
// axiome, including flat top-level placement (no category), and a path in the wrong namespace fails.
func TestParsePlacementNamespaces(t *testing.T) {
	// top-level record in a flat namespace, no category needed (parent == namespace root)
	if _, err := ParsePlacement(`{"pfad":"laeufe/nachtwache.md","beschreibung":"x","titel":"T","neue_kategorie":false}`, nil, "laeufe"); err != nil {
		t.Errorf("flat top-level laeufe placement rejected: %v", err)
	}
	// regeln into an existing category
	known := []string{"regeln/prozess"}
	if _, err := ParsePlacement(`{"pfad":"regeln/prozess/x.md","beschreibung":"x","titel":"T","neue_kategorie":false}`, known, "regeln"); err != nil {
		t.Errorf("regeln placement rejected: %v", err)
	}
	// namespace mismatch: an axiome path offered for the regeln namespace → invalid
	if _, err := ParsePlacement(`{"pfad":"axiome/x/y.md","beschreibung":"x","titel":"T","neue_kategorie":true}`, nil, "regeln"); !errors.Is(err, ErrInvalidPlacement) {
		t.Errorf("cross-namespace path should be invalid, got %v", err)
	}
}

func TestCategories(t *testing.T) {
	paths := []string{
		"axiome/architektur/ssot/atomar.md",
		"axiome/architektur/passive.md",
		"axiome/minimalismus/keine-tooltips.md",
		"regeln/prozess/x.md", // not axiome → excluded from the axiome view
	}
	got := Categories(paths, "axiome")
	want := map[string]bool{"axiome/architektur": true, "axiome/architektur/ssot": true, "axiome/minimalismus": true}
	if len(got) != len(want) {
		t.Fatalf("categories = %v, want %v", got, want)
	}
	for _, c := range got {
		if !want[c] {
			t.Errorf("unexpected category %q", c)
		}
	}
	// symmetric: the same function lists regeln categories
	if r := Categories(paths, "regeln"); len(r) != 1 || r[0] != "regeln/prozess" {
		t.Errorf("regeln categories = %v, want [regeln/prozess]", r)
	}
}

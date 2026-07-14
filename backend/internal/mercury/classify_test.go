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
	p, err := ParsePlacement(`{"pfad":"axiome/architektur/ssot/atomar.md","beschreibung":"x","titel":"T","neue_kategorie":false,"duplikat_von":null}`, known)
	if err != nil {
		t.Fatalf("valid placement rejected: %v", err)
	}
	if p.Pfad != "axiome/architektur/ssot/atomar.md" {
		t.Errorf("pfad = %q", p.Pfad)
	}

	// new category is allowed even though the parent is unknown
	if _, err := ParsePlacement(`{"pfad":"axiome/neu/tief/x.md","beschreibung":"x","titel":"T","neue_kategorie":true}`, known); err != nil {
		t.Errorf("new category rejected: %v", err)
	}

	// bad path (CamelCase segment) → invalid
	if _, err := ParsePlacement(`{"pfad":"axiome/Architektur/x.md","beschreibung":"x","titel":"T","neue_kategorie":true}`, known); !errors.Is(err, ErrInvalidPlacement) {
		t.Errorf("CamelCase path should be invalid, got %v", err)
	}

	// unknown category without neue_kategorie → invalid
	if _, err := ParsePlacement(`{"pfad":"axiome/erfunden/x.md","beschreibung":"x","titel":"T","neue_kategorie":false}`, known); !errors.Is(err, ErrInvalidPlacement) {
		t.Errorf("unknown category should be invalid, got %v", err)
	}

	// empty description → invalid
	if _, err := ParsePlacement(`{"pfad":"axiome/minimalismus/x.md","beschreibung":"  ","titel":"T","neue_kategorie":false}`, known); !errors.Is(err, ErrInvalidPlacement) {
		t.Errorf("empty description should be invalid, got %v", err)
	}

	// no JSON at all
	if _, err := ParsePlacement("das Modell hat nur Prosa geliefert", known); !errors.Is(err, ErrNoJSON) {
		t.Errorf("want ErrNoJSON, got %v", err)
	}
}

func TestCategories(t *testing.T) {
	paths := []string{
		"axiome/architektur/ssot/atomar.md",
		"axiome/architektur/passive.md",
		"axiome/minimalismus/keine-tooltips.md",
		"regeln/go/x.md", // not axiome → excluded
	}
	got := Categories(paths)
	want := map[string]bool{"axiome/architektur": true, "axiome/architektur/ssot": true, "axiome/minimalismus": true}
	if len(got) != len(want) {
		t.Fatalf("categories = %v, want %v", got, want)
	}
	for _, c := range got {
		if !want[c] {
			t.Errorf("unexpected category %q", c)
		}
	}
}

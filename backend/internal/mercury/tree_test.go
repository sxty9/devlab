package mercury

import "testing"

func TestBuildArbitraryDepth(t *testing.T) {
	paths := []string{
		"axiome/architektur/ssot.md",
		"axiome/architektur/passive-pools.md",
		"axiome/architektur/reuse/sdk-zuerst.md", // deeper: a subcategory under architektur
		"axiome/minimalismus/keine-tooltips.md",
		"regeln/go/fehler-wrappen.md",
		"eingang/abc123.bin", // must be ignored — outside the model
		"axiome",             // bare namespace — no record
	}
	tr := Build(paths, nil)

	if len(tr.Axiome) != 2 {
		t.Fatalf("axiome top-level categories = %d, want 2 (architektur, minimalismus)", len(tr.Axiome))
	}
	// categories sort before axioms, then by name → architektur first
	arch := tr.Axiome[0]
	if arch.Name != "architektur" || arch.IsAxiom {
		t.Fatalf("first node = %+v, want category 'architektur'", arch)
	}
	// architektur holds: category 'reuse' (first), then axioms passive-pools, ssot
	if len(arch.Children) != 3 {
		t.Fatalf("architektur children = %d, want 3", len(arch.Children))
	}
	if arch.Children[0].Name != "reuse" || arch.Children[0].IsAxiom {
		t.Errorf("category should sort first: %+v", arch.Children[0])
	}
	reuse := arch.Children[0]
	if len(reuse.Children) != 1 || reuse.Children[0].Path != "axiome/architektur/reuse/sdk-zuerst.md" {
		t.Errorf("deep leaf missing/wrong: %+v", reuse.Children)
	}
	if reuse.Children[0].Name != "sdk-zuerst" {
		t.Errorf("leaf name should trim .md: %q", reuse.Children[0].Name)
	}
	// regeln namespace populated independently
	if len(tr.Regeln) != 1 || tr.Regeln[0].Name != "go" {
		t.Errorf("regeln tree wrong: %+v", tr.Regeln)
	}
	// eingang ignored
	for _, n := range tr.Axiome {
		if n.Name == "eingang" {
			t.Error("eingang leaked into the model")
		}
	}
}

func TestBuildEmptyNamespacesAreNonNil(t *testing.T) {
	// An empty namespace must marshal to [] not null, or the client crashes reading .length.
	tr := Build([]string{"axiome/a/x.md"}, nil) // only axiome populated
	if tr.Regeln == nil || tr.Laeufe == nil {
		t.Fatalf("empty namespaces must be [] not nil: regeln=%v laeufe=%v", tr.Regeln, tr.Laeufe)
	}
	empty := Build(nil, nil)
	if empty.Axiome == nil || empty.Regeln == nil || empty.Laeufe == nil {
		t.Fatalf("all-empty tree must have [] slices, got %+v", empty)
	}
}

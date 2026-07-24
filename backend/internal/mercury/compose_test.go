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

func TestRunInputsHash(t *testing.T) {
	a := []RunAxiom{{ID: "ax_1", Body: "x"}}
	r := []RunAxiom{{ID: "r_1", Body: "y"}}
	h1 := RunInputsHash(a, r)
	if h1 != RunInputsHash(a, r) {
		t.Error("hash not stable across calls")
	}
	// a pure re-title must NOT change the hash (identity + body only)
	if RunInputsHash([]RunAxiom{{ID: "ax_1", Titel: "New Title", Body: "x"}}, r) != h1 {
		t.Error("title change changed the hash")
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

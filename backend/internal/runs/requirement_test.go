package runs

import "testing"

// The digest ignores pure presentation: case, line breaks and runs of whitespace fold away, so a
// rewrap or a capitalised word is the SAME stand — never a reason to re-run a delivered todo.
func TestRequirementDigestFoldsPresentation(t *testing.T) {
	a := RequirementDigest("Fix the edge", "Serve   the\nweb face   from the edge.")
	b := RequirementDigest("fix the edge", "Serve the web face\n\nfrom the edge.")
	if a != b {
		t.Fatalf("presentation was not folded:\n a=%q\n b=%q", a, b)
	}
}

// The heart of the fix: "already delivered?" is answered from CONTENT, not identity.
func TestRequirementDemandsNewWork(t *testing.T) {
	title := "Give sxgate one edge"
	body := "sxgate owns the whole outward connection of its environment."

	delivered := RequirementDigest(title, body)

	t.Run("a: unchanged text demands no new work — the ban stays right where it is right", func(t *testing.T) {
		grown, _ := RequirementDemandsNewWork(delivered, title, body)
		if grown {
			t.Fatal("an unchanged todo was reported as demanding new work")
		}
	})

	t.Run("b: a binding addition demands new work — the todo runs again", func(t *testing.T) {
		grownBody := body + " It must also refuse to hold any zone it does not belong to, and reconcile routes on every boot."
		grown, why := RequirementDemandsNewWork(delivered, title, grownBody)
		if !grown {
			t.Fatal("an added requirement was NOT detected as new work")
		}
		if why == "" {
			t.Fatal("a positive verdict must name the stand it rests on")
		}
	})

	t.Run("c: a typo fix and a rewrap do NOT re-trigger", func(t *testing.T) {
		// One corrected letter ("sxgate"→"sxgatee" reverted style: here a genuine typo fix) plus a
		// line break inserted — editorial, below the edit budget.
		editorial := "sxgate owns the whole\noutward conection of its environment." // "conection" typo + rewrap
		delivered2 := RequirementDigest(title, "sxgate owns the whole outward conection of its environment.")
		grown, _ := RequirementDemandsNewWork(delivered2, title, "sxgate owns the whole outward connection of its environment.")
		if grown {
			t.Fatal("a one-letter typo fix must not re-trigger the todo")
		}
		// the rewrap alone (identical after normalisation) never re-triggers either
		if g, _ := RequirementDemandsNewWork(delivered2, title, editorial); g {
			t.Fatal("a rewrap must not re-trigger the todo")
		}
	})

	t.Run("legacy: an empty delivered stand never overrides the historical verdict", func(t *testing.T) {
		grown, _ := RequirementDemandsNewWork("", title, body+" and much, much more added here beyond any budget")
		if grown {
			t.Fatal("a delivery that recorded no stand must not be treated as suddenly undelivered")
		}
	})
}

// editDistanceWithin is the editorial-vs-substantive boundary; pin its edges so the chosen budget
// stays the documented one.
func TestEditDistanceWithin(t *testing.T) {
	cases := []struct {
		a, b   string
		budget int
		within bool
	}{
		{"abcdef", "abcdef", 8, true},                                  // identical
		{"receive", "recieve", 8, true},                                // one transposition ≈ 2 edits
		{"short", "short plus a whole clause of new demand", 8, false}, // a clause added
		{"aaaa", "aaaabbbbbbbbb", 8, false},                            // length gap wider than the budget
		{"kitten", "sitting", 8, true},                                 // classic distance 3
		{"kitten", "sitting", 2, false},                                // same pair, tighter budget
	}
	for _, c := range cases {
		if got := editDistanceWithin(c.a, c.b, c.budget); got != c.within {
			t.Errorf("editDistanceWithin(%q,%q,%d) = %v, want %v", c.a, c.b, c.budget, got, c.within)
		}
	}
}

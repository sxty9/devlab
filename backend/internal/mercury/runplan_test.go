package mercury

import (
	"errors"
	"testing"
)

func TestParseRunPlan(t *testing.T) {
	known := []string{"ax_1", "ax_2"}

	ok := `{"runs":[{"name":"Architektur","axiomIds":["ax_1","ax_2"],"schedule":{"kind":"weekly","timeOfDay":"03:00","weekdays":[1,4]},"rationale":"x"}]}`
	plan, err := ParseRunPlan(ok, known)
	if err != nil {
		t.Fatalf("valid plan rejected: %v", err)
	}
	if len(plan.Runs) != 1 || plan.Runs[0].Name != "Architektur" {
		t.Fatalf("parsed wrong: %+v", plan)
	}

	bad := map[string]string{
		"unknown id":    `{"runs":[{"name":"A","axiomIds":["ax_9"],"schedule":{"kind":"daily","timeOfDay":"03:00"}}]}`,
		"empty":         `{"runs":[]}`,
		"no axioms":     `{"runs":[{"name":"A","axiomIds":[],"schedule":{"kind":"daily","timeOfDay":"03:00"}}]}`,
		"bad time":      `{"runs":[{"name":"A","axiomIds":["ax_1"],"schedule":{"kind":"daily","timeOfDay":"3pm"}}]}`,
		"weekly no wd":  `{"runs":[{"name":"A","axiomIds":["ax_1"],"schedule":{"kind":"weekly","timeOfDay":"03:00"}}]}`,
		"daily with wd": `{"runs":[{"name":"A","axiomIds":["ax_1"],"schedule":{"kind":"daily","timeOfDay":"03:00","weekdays":[1]}}]}`,
		"unknown kind":  `{"runs":[{"name":"A","axiomIds":["ax_1"],"schedule":{"kind":"monthly","timeOfDay":"03:00"}}]}`,
		"dup name":      `{"runs":[{"name":"A","axiomIds":["ax_1"],"schedule":{"kind":"daily","timeOfDay":"03:00"}},{"name":"a","axiomIds":["ax_2"],"schedule":{"kind":"daily","timeOfDay":"04:00"}}]}`,
		"blank name":    `{"runs":[{"name":"  ","axiomIds":["ax_1"],"schedule":{"kind":"daily","timeOfDay":"03:00"}}]}`,
	}
	for label, in := range bad {
		if _, err := ParseRunPlan(in, known); !errors.Is(err, ErrInvalidPlacement) {
			t.Errorf("%s: expected ErrInvalidPlacement, got %v", label, err)
		}
	}
	if _, err := ParseRunPlan("das Modell lieferte nur Prosa", known); !errors.Is(err, ErrNoJSON) {
		t.Errorf("no json: got %v", err)
	}
}

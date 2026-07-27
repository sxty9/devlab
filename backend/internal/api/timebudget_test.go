package api

import (
	"testing"
	"time"
)

func TestCompactDuration(t *testing.T) {
	cases := map[time.Duration]string{
		3 * time.Hour:                "3h",
		90 * time.Minute:             "1h30m",
		30 * time.Minute:             "30m",
		4 * time.Hour:                "4h",
		time.Hour + 5*time.Minute:    "1h5m",
		45 * time.Second:             "0m", // sub-minute budgets collapse; not a real budget
		2*time.Hour + 30*time.Second: "2h", // seconds rounded away
	}
	for d, want := range cases {
		if got := compactDuration(d); got != want {
			t.Errorf("compactDuration(%v) = %q, want %q", d, got, want)
		}
	}
}

func TestBudgetLabel(t *testing.T) {
	if got := budgetLabel(0); got != "0" {
		t.Errorf("budgetLabel(0) = %q, want \"0\" (no cap)", got)
	}
	if got := budgetLabel(3 * time.Hour); got != "3h" {
		t.Errorf("budgetLabel(3h) = %q, want \"3h\"", got)
	}
}

func TestNormalizeTimeBudget(t *testing.T) {
	ok := func(in, want string) {
		t.Helper()
		got, code, msg := normalizeTimeBudget(in)
		if code != 0 {
			t.Errorf("normalizeTimeBudget(%q) rejected: %s", in, msg)
			return
		}
		if got != want {
			t.Errorf("normalizeTimeBudget(%q) = %q, want %q", in, got, want)
		}
	}
	bad := func(in string) {
		t.Helper()
		if _, code, _ := normalizeTimeBudget(in); code == 0 {
			t.Errorf("normalizeTimeBudget(%q) should have been rejected", in)
		}
	}

	ok("", "")           // unset → follow the service default
	ok("  ", "")         // whitespace-only → unset
	ok("0", "0")         // deliberate no-cap
	ok("3h", "3h")       // canonical
	ok("180m", "3h")     // canonicalized to the compact form
	ok("1h30m", "1h30m") // mixed
	bad("-1h")           // negative
	bad("later")         // unparseable
	bad("3 hours")       // not a Go duration
}

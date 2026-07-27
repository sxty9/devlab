package report

import (
	"strings"
	"testing"

	"devlab/backend/internal/runs"
)

func TestDeliveryStageHonestlyReflectsOutcome(t *testing.T) {
	cases := []struct {
		name string
		rr   runs.RepoResult
		want string
	}{
		{"deployed", runs.RepoResult{OK: true, Deployed: true}, "deployed"},
		{"pr", runs.RepoResult{OK: true, PRUrl: "https://x/pr/1"}, "PR opened"},
		{"analyzed-only", runs.RepoResult{OK: true, Steps: []runs.Step{{Name: "analyze", OK: true}}}, "analyzed"},
		{"implemented", runs.RepoResult{OK: true, Steps: []runs.Step{{Name: "analyze", OK: true}, {Name: "implement", OK: true}}}, "implemented"},
		{"ok-no-steps", runs.RepoResult{OK: true}, "analyzed"},
		{"failed-at-push", runs.RepoResult{OK: false, Steps: []runs.Step{{Name: "implement", OK: true}, {Name: "push", OK: false}}}, "failed at pushed"},
		{"failed-no-steps", runs.RepoResult{OK: false}, "failed"},
	}
	for _, c := range cases {
		if got := deliveryStage(c.rr); got != c.want {
			t.Errorf("%s: deliveryStage = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestTypeLabel(t *testing.T) {
	if typeLabel(runs.TypeTodo) != "ToDo" {
		t.Error("todo label")
	}
	if typeLabel(runs.TypeAuto) != "Automatic run" {
		t.Error("auto label")
	}
	if typeLabel(runs.Type("")) != "Automatic run" {
		t.Error("empty type should read as auto")
	}
}

func sampleItems() []Item {
	return []Item{
		{RunName: "Nightly axioms", TypeLabel: "Automatic run", Finished: true, OK: true,
			Repos:    []RepoLine{{Repo: "devlab", Stage: "deployed"}, {Repo: "aigentic", Stage: "PR opened"}},
			InTokens: 12000, OutTokens: 3400, CostUSD: 0.42},
		{RunName: "Fix login bug", TypeLabel: "ToDo", Finished: true, OK: false,
			Repos:    []RepoLine{{Repo: "studiq", Stage: "failed at pushed"}},
			InTokens: 5000, OutTokens: 900, CostUSD: 0.10},
		{RunName: "Big migration", TypeLabel: "ToDo", Finished: false, Suspended: true,
			Repos:    []RepoLine{{Repo: "hostek", Stage: "implemented"}},
			InTokens: 2000, OutTokens: 100, CostUSD: 0.03},
	}
}

func TestComposeSectionsTotalsAndSubject(t *testing.T) {
	c := Compose("2026-07-26", sampleItems(), "https://holistic.example/devlab#/mercury")

	// Subject: count + explicit attention flag (failures are not hidden).
	if !strings.Contains(c.Subject, "2026-07-26") || !strings.Contains(c.Subject, "3 executions") {
		t.Errorf("subject = %q", c.Subject)
	}
	if !strings.Contains(c.Subject, "1 need attention") {
		t.Errorf("subject should flag attention: %q", c.Subject)
	}

	for _, body := range []string{c.Text, c.HTML} {
		// Three sections present.
		for _, sec := range []string{"Completed", "In progress", "Needs attention", "Totals"} {
			if !strings.Contains(strings.ToLower(body), strings.ToLower(sec)) {
				t.Errorf("body missing section %q", sec)
			}
		}
		// The failing ToDo and its stalled stage are named (not hidden among successes).
		if !strings.Contains(body, "Fix login bug") || !strings.Contains(body, "failed at pushed") {
			t.Errorf("failure not clearly named in body")
		}
		// The completed run and its deployed repo appear.
		if !strings.Contains(body, "Nightly axioms") || !strings.Contains(body, "deployed") {
			t.Errorf("completed run missing")
		}
		// Suspended item flagged as pending.
		if !strings.Contains(body, "usage limit") {
			t.Errorf("suspended item not flagged")
		}
		// Totals: 3 distinct repos across the day (devlab, aigentic, studiq, hostek = 4 actually).
		if !strings.Contains(body, "4 repositories") {
			t.Errorf("repo total wrong in body")
		}
		// Deep link present.
		if !strings.Contains(body, "holistic.example/devlab") {
			t.Errorf("deep link missing")
		}
	}
}

func TestComposeWithoutLinkFallsBackToText(t *testing.T) {
	c := Compose("2026-07-26", sampleItems(), "")
	if strings.Contains(c.Text, "http") {
		t.Errorf("expected no URL in text without a link: %q", c.Text)
	}
	if !strings.Contains(c.Text, "Mercury runs view") {
		t.Errorf("expected fallback pointer text")
	}
}

func TestComposeHTMLEscapesDynamicValues(t *testing.T) {
	items := []Item{{RunName: `<script>x</script>`, TypeLabel: "ToDo", Finished: true, OK: true,
		Repos: []RepoLine{{Repo: `a&b`, Stage: "deployed"}}}}
	c := Compose("2026-07-26", items, "")
	if strings.Contains(c.HTML, "<script>x</script>") {
		t.Errorf("run name not escaped in HTML")
	}
	if !strings.Contains(c.HTML, "a&amp;b") {
		t.Errorf("repo name not escaped in HTML")
	}
}

func TestCommasFormatting(t *testing.T) {
	for in, want := range map[int]string{0: "0", 12: "12", 999: "999", 1000: "1,000", 12345: "12,345", 1234567: "1,234,567"} {
		if got := commas(in); got != want {
			t.Errorf("commas(%d) = %q, want %q", in, got, want)
		}
	}
}

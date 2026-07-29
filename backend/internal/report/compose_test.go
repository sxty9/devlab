package report

import (
	"strings"
	"testing"
	"time"

	"devlab/backend/internal/model"
)

func TestDeliveryStageHonestlyReflectsOutcome(t *testing.T) {
	sv := func(stage string, state model.StepState) model.StageView {
		return model.StageView{Stage: model.Stage(stage), State: state}
	}
	cases := []struct {
		name string
		rr   model.RepoPipeline
		want string
	}{
		{"deployed", model.RepoPipeline{Stages: []model.StageView{sv("preflight", model.StepExecuted), sv("implement", model.StepExecuted), sv("deliver-dev", model.StepExecuted)}}, "deployed"},
		{"pr", model.RepoPipeline{Stages: []model.StageView{sv("implement", model.StepExecuted), sv("pull-request", model.StepExecuted)}}, "PR opened"},
		{"analyzed-only", model.RepoPipeline{Stages: []model.StageView{sv("preflight", model.StepExecuted)}}, "analyzed"},
		{"implemented", model.RepoPipeline{Stages: []model.StageView{sv("preflight", model.StepExecuted), sv("implement", model.StepExecuted)}}, "implemented"},
		{"no-stages", model.RepoPipeline{}, "not started"},
		{"failed-at-publish", model.RepoPipeline{Stages: []model.StageView{sv("implement", model.StepExecuted), sv("publish", model.StepFailed), sv("pull-request", model.StepNotExecuted)}}, "failed at published"},
		{"running", model.RepoPipeline{Stages: []model.StageView{sv("implement", model.StepRunning)}}, "working on implemented"},
		{"legacy-step-names", model.RepoPipeline{Stages: []model.StageView{sv("analyze", model.StepExecuted), sv("push", model.StepFailed)}}, "failed at published"},
	}
	for _, c := range cases {
		if got := deliveryStage(c.rr); got != c.want {
			t.Errorf("%s: deliveryStage = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestTypeLabel(t *testing.T) {
	if typeLabel(model.KindTodo) != "ToDo" {
		t.Error("todo label")
	}
	if typeLabel(model.KindAuto) != "Automatic run" {
		t.Error("auto label")
	}
	if typeLabel(model.RunKind("")) != "Automatic run" {
		t.Error("empty kind should read as auto")
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
	c := Compose("2026-07-26", sampleItems(), nil, "https://holistic.example/devlab#/mercury")

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
	c := Compose("2026-07-26", sampleItems(), nil, "")
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
	c := Compose("2026-07-26", items, nil, "")
	if strings.Contains(c.HTML, "<script>x</script>") {
		t.Errorf("run name not escaped in HTML")
	}
	if !strings.Contains(c.HTML, "a&amp;b") {
		t.Errorf("repo name not escaped in HTML")
	}
}

// REQ-042.6: the report carries its three rubrics — delivery alarms, emergency-route overrides,
// protection deviations — with the finding, its repository, the next step and, for a bundled
// notice, the count and the period. An alarm-free day states no alarms at all.
func TestComposeRendersTheThreeRubrics(t *testing.T) {
	first := time.Date(2026, 7, 26, 8, 0, 0, 0, time.UTC)
	rubrics := []Rubric{
		{Title: RubricDeliveryAlarms, Lines: []RubricLine{{
			Repo: "devlab", Text: "implemented without dev delivery",
			NextStep: "fix the delivery path and resume the execution",
			Count:    7, FirstAt: first, LastAt: first.Add(3 * time.Hour),
		}}},
		{Title: RubricOverrides, Lines: []RubricLine{{
			Text: "ada overrode the delivery-origin protection for PR #12", Count: 1, FirstAt: first, LastAt: first,
		}}},
		{Title: RubricProtection, Lines: []RubricLine{{
			Repo: "aigentic", Text: "branch protection deviated (required review off) and was restored",
			Count: 1, FirstAt: first, LastAt: first,
		}}},
	}
	c := Compose("2026-07-26", sampleItems(), rubrics, "")

	if !strings.Contains(c.Subject, "3 alarms") {
		t.Errorf("subject must state the alarms: %q", c.Subject)
	}
	for _, body := range []string{c.Text, c.HTML} {
		for _, want := range []string{
			RubricDeliveryAlarms, RubricOverrides, RubricProtection,
			"implemented without dev delivery", "fix the delivery path and resume the execution",
			"overrode the delivery-origin protection", "branch protection deviated",
			"7 times between",
		} {
			if !strings.Contains(strings.ToLower(body), strings.ToLower(want)) {
				t.Errorf("body missing %q", want)
			}
		}
		// No transcripts, no raw logs — the report summarizes and points into the UI.
		if !strings.Contains(strings.ToLower(body), "mercury runs view") {
			t.Errorf("body must point into the UI for details")
		}
	}

	// An alarm-free day: no empty headings.
	clean := Compose("2026-07-26", sampleItems(), nil, "")
	if strings.Contains(clean.Text, RubricDeliveryAlarms) || strings.Contains(clean.Subject, "alarm") {
		t.Errorf("a day without findings must state no rubrics:\n%s\n%s", clean.Subject, clean.Text)
	}
}

// The rubric renderer escapes what it renders: a finding's text is data, never markup.
func TestComposeRubricHTMLEscapes(t *testing.T) {
	c := Compose("2026-07-26", sampleItems(), []Rubric{{Title: RubricOverrides, Lines: []RubricLine{
		{Repo: `a&b`, Text: `<script>x</script>`, NextStep: `<b>no</b>`, Count: 1},
	}}}, "")
	if strings.Contains(c.HTML, "<script>x</script>") || strings.Contains(c.HTML, "<b>no</b>") {
		t.Errorf("rubric values must be escaped:\n%s", c.HTML)
	}
	if !strings.Contains(c.HTML, "a&amp;b") {
		t.Error("rubric repo not escaped")
	}
}

func TestCommasFormatting(t *testing.T) {
	for in, want := range map[int]string{0: "0", 12: "12", 999: "999", 1000: "1,000", 12345: "12,345", 1234567: "1,234,567"} {
		if got := commas(in); got != want {
			t.Errorf("commas(%d) = %q, want %q", in, got, want)
		}
	}
}

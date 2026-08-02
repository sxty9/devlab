package executor

import (
	"strings"
	"testing"

	"devlab/backend/internal/model"
	"devlab/backend/internal/preflight"
)

// B-7: the snapshot is the ONE composed part; the addenda (preamble, examined stand, finding,
// manifest) are appended verbatim and never compose constitution text of their own.
func TestAssemblePrompt(t *testing.T) {
	f := preflight.Finding{
		State:    model.TaskImplementedUndelivered,
		Evidence: []string{"open delivery dlv_1 recorded"},
		OpenPR:   &model.PRRef{Number: 3, URL: "https://example.invalid/pr/3"},
	}
	p := AssemblePrompt("SNAP-BODY with the full constitution wording", model.AutonomyAutonomous, f,
		"\n## Zuletzt geprüfter Stand (für dieses Repository)\n\n- Keine Redundanz: zuletzt geprüft bei Commit aaaa1111\n",
		"- .mercury/attachments/a.png\n")

	order := []string{
		"## Division of labor",
		"Do not shrink the task",
		"three-part report",
		"never end with a question",
		"SNAP-BODY with the full constitution wording",
		"## Zuletzt geprüfter Stand (für dieses Repository)",
		"zuletzt geprüft bei Commit aaaa1111",
		"## Preflight — observed state of this repository",
		"State: implemented-undelivered",
		"open delivery dlv_1 recorded",
		"https://example.invalid/pr/3",
		"only close the missing part of the path",
		"## Attached media",
		".mercury/attachments/a.png",
	}
	last := -1
	for _, want := range order {
		i := strings.Index(p, want)
		if i < 0 {
			t.Fatalf("prompt misses %q:\n%s", want, p)
		}
		if i < last {
			t.Fatalf("%q out of order", want)
		}
		last = i
	}
}

// The three autonomy levels shape the ask policy: only the autonomous level forbids asking; the two
// asking levels carry the machine-read question block the parser recognises.
func TestAssemblePromptAutonomy(t *testing.T) {
	f := preflight.Finding{State: model.TaskNotImplemented}
	auto := AssemblePrompt("SNAP", model.AutonomyAutonomous, f, "", "")
	if !strings.Contains(auto, "never end with a question") {
		t.Fatalf("autonomous prompt should forbid asking")
	}
	if strings.Contains(auto, questionOpen) {
		t.Fatalf("autonomous prompt should not offer the question block")
	}
	for _, lvl := range []model.AutonomyLevel{model.AutonomyBalanced, model.AutonomyCollaborative} {
		p := AssemblePrompt("SNAP", lvl, f, "", "")
		if !strings.Contains(p, questionOpen) || !strings.Contains(p, questionClose) {
			t.Fatalf("%s prompt should carry the question block", lvl)
		}
	}
	// An empty level resolves to autonomous — today's behavior for an unspecified run.
	if got := AssemblePrompt("SNAP", "", f, "", ""); !strings.Contains(got, "never end with a question") {
		t.Fatalf("empty level should resolve to autonomous")
	}
}

// parseAgentQuestion recognises a well-formed block and ignores ordinary output.
func TestParseAgentQuestion(t *testing.T) {
	text := "I got most of it done.\n\n" + questionOpen + "\n" + questionQ +
		"\nShould the store stay in package foo or move to bar?\n" + questionR +
		"\nKeep it in foo — bar would pull in a cycle.\n" + questionClose + "\ntrailing"
	q, rec, ok := parseAgentQuestion(text)
	if !ok {
		t.Fatalf("expected a parsed question")
	}
	if !strings.Contains(q, "package foo or move to bar") {
		t.Fatalf("question text wrong: %q", q)
	}
	if !strings.Contains(rec, "would pull in a cycle") {
		t.Fatalf("recommendation wrong: %q", rec)
	}
	if _, _, ok := parseAgentQuestion("just a normal report, no question here"); ok {
		t.Fatalf("ordinary output must not parse as a question")
	}
	if _, _, ok := parseAgentQuestion(questionOpen + "\n" + questionQ + "\n\n" + questionR + "\nx"); ok {
		t.Fatalf("an empty question must not parse")
	}
}

func TestAssemblePromptWithoutAttachments(t *testing.T) {
	p := AssemblePrompt("SNAP", model.AutonomyAutonomous, preflight.Finding{State: model.TaskNotImplemented}, "", "")
	if strings.Contains(p, "## Attached media") {
		t.Fatalf("empty manifest rendered a media section")
	}
	// No examined stand to name (a ToDo names no axiom) ⇒ no heading either: an empty section would
	// read as "nothing was ever examined", which is a claim nobody made.
	if strings.Contains(p, "Zuletzt geprüfter Stand") {
		t.Fatalf("empty scope rendered an examined-stand section")
	}
	if !strings.Contains(p, "State: not-implemented") {
		t.Fatalf("finding missing")
	}
}

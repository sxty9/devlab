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
	p := AssemblePrompt("SNAP-BODY with the full constitution wording", f,
		"\n## Zuletzt geprüfter Stand (für dieses Repository)\n\n- Keine Redundanz: zuletzt geprüft bei Commit aaaa1111\n",
		"- .mercury/attachments/a.png\n")

	order := []string{
		"## Division of labor",
		"never end with a question",
		"Do not shrink the task",
		"three-part report",
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

func TestAssemblePromptWithoutAttachments(t *testing.T) {
	p := AssemblePrompt("SNAP", preflight.Finding{State: model.TaskNotImplemented}, "", "")
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

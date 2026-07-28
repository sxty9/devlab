package executor

import (
	"strings"
	"testing"

	"devlab/backend/internal/model"
	"devlab/backend/internal/preflight"
)

// B-7: the snapshot is the ONE composed part; the addenda (preamble, finding, manifest) are
// appended verbatim and never compose constitution text of their own.
func TestAssemblePrompt(t *testing.T) {
	f := preflight.Finding{
		State:    model.TaskImplementedUndelivered,
		Evidence: []string{"open delivery dlv_1 recorded"},
		OpenPR:   &model.PRRef{Number: 3, URL: "https://example.invalid/pr/3"},
	}
	p := AssemblePrompt("SNAP-BODY with the full constitution wording", f, "- .mercury/attachments/a.png\n")

	order := []string{
		"## Division of labor",
		"never end with a question",
		"Do not shrink the task",
		"three-part report",
		"SNAP-BODY with the full constitution wording",
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
	p := AssemblePrompt("SNAP", preflight.Finding{State: model.TaskNotImplemented}, "")
	if strings.Contains(p, "## Attached media") {
		t.Fatalf("empty manifest rendered a media section")
	}
	if !strings.Contains(p, "State: not-implemented") {
		t.Fatalf("finding missing")
	}
}

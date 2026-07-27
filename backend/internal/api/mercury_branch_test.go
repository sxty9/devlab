package api

import (
	"strings"
	"testing"

	"devlab/backend/internal/github"
	"devlab/backend/internal/runs"
)

// TestRunPRBodyCarriesMarker pins that every Mercury PR body embeds the hidden marker, so a run can
// recognise its own still-open PRs after branches dropped the "mercury-run/" prefix. The marker is an
// HTML comment (invisible when rendered, untouched by the nightly translation pass).
func TestRunPRBodyCarriesMarker(t *testing.T) {
	body := runPRBody(runs.Run{Name: "Konsistenz-Sweep"})
	if !strings.Contains(body, mercuryPRMarker) {
		t.Fatalf("PR body is missing the Mercury marker %q:\n%s", mercuryPRMarker, body)
	}
	if !strings.HasPrefix(mercuryPRMarker, "<!--") || !strings.HasSuffix(mercuryPRMarker, "-->") {
		t.Fatalf("marker %q is not an HTML comment (would render or be translated away)", mercuryPRMarker)
	}
}

// TestIsMercuryPR pins the recognition of a Mercury PR: by the body marker (new, convention-named
// branches) OR the legacy branch prefix (in-flight PRs from before the rename), and NOT a human PR that
// merely uses a fix/ or feature/ branch.
func TestIsMercuryPR(t *testing.T) {
	marked := github.PullRequest{Body: "Some description\n" + mercuryPRMarker}
	marked.Head.Ref = "feature/dark_mode-k3f9a2"

	legacy := github.PullRequest{Body: "old body without a marker"}
	legacy.Head.Ref = "mercury-run/run_abc/2026-07-26T17-02-42.652Z"

	human := github.PullRequest{Body: "please review my fix"}
	human.Head.Ref = "fix/typo" // same kind prefix as Mercury, but not Mercury's

	if !isMercuryPR(marked) {
		t.Error("a PR with the body marker should be recognised as Mercury's")
	}
	if !isMercuryPR(legacy) {
		t.Error("a legacy mercury-run/ PR should still be recognised")
	}
	if isMercuryPR(human) {
		t.Error("a human fix/ PR without the marker must NOT be treated as Mercury's")
	}
}

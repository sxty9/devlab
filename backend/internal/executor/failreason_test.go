package executor

import (
	"strings"
	"testing"
)

// TestFailReasonKeepsCauseAtEnd is point 4(c): a long, multi-line failure output keeps its meaningful
// part. The bug was that a step-failure reason ran through firstLine, which keeps the FIRST line — and
// a wrapper prints its green log lines first and dies on the failure LAST, so the reason surfaced a
// success ("install-only deploy … done, the caller's honest gate, F10") while the real cause (the serve
// root the webserver could not read) fell off the end. failReason keeps the cause instead.
func TestFailReasonKeepsCauseAtEnd(t *testing.T) {
	// Shaped like the real deploy error: a leading wrap, then the wrapper's tail with green log lines
	// first and the actual die() reason at the very end.
	deployErr := "deploy: install of presentr failed: exit status 4: " +
		strings.Repeat("…he caller's honest gate, F10) install-only deploy of 'presentr' program done\n", 30) +
		"MERCURY-UI: failed\n" +
		"devlab-install: error: the delivered start page /opt/holistic/www/index.html is NOT readable by an unprivileged reader ('nobody') — the webserver would answer 404 over a present page (delivery incomplete)"

	got := failReason(deployErr)

	if !strings.Contains(got, "NOT readable") || !strings.Contains(got, "404 over a present page") {
		t.Errorf("failReason dropped the cause; got:\n%s", got)
	}
	// It must NOT reduce to the success log line the way firstLine did.
	if strings.Contains(got, "program done") && !strings.Contains(got, "NOT readable") {
		t.Errorf("failReason surfaced the success line instead of the cause; got:\n%s", got)
	}
	// Sanity: firstLine (the old behaviour) would have kept only the head and hidden the cause — this
	// documents exactly what regressed.
	old := firstLine(deployErr)
	if strings.Contains(old, "NOT readable") {
		t.Fatalf("test fixture is wrong: firstLine already keeps the cause, nothing to fix")
	}
}

// TestFailReasonSingleLineVerbatim: a single-line error has its cause where it is; failReason keeps it
// (up to the bound) rather than mangling it.
func TestFailReasonSingleLineVerbatim(t *testing.T) {
	msg := "delivery not yet set up for repository presentr"
	if got := failReason(msg); got != msg {
		t.Errorf("single-line reason changed: want %q, got %q", msg, got)
	}
}

// TestFailReasonBoundsToTail: an over-long single line is shortened from the FRONT (leading …), keeping
// the tail where the cause sits.
func TestFailReasonBoundsToTail(t *testing.T) {
	msg := strings.Repeat("x", 700) + " THE-ACTUAL-CAUSE-AT-THE-END"
	got := failReason(msg)
	if !strings.HasPrefix(got, "…") {
		t.Errorf("over-long reason should keep the tail with a leading …; got prefix %.10q", got)
	}
	if !strings.Contains(got, "THE-ACTUAL-CAUSE-AT-THE-END") {
		t.Errorf("over-long reason dropped its tail cause; got:\n%s", got)
	}
}

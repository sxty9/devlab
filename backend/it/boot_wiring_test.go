package it

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// K-2b / REQ-039.1 / REQ-013: the boot ORDER is a list, so it is checked as one. The integration
// suite drives the documented steps itself (harness boot()); this test proves the shipped daemon
// constructs the SAME sequence, in the SAME order — an unwired or mis-ordered daemon would make
// every downstream criterion unreachable in production while the suite stayed green.
//
// The order carries meaning step by step: the ghost exorcism and the restart completion must precede
// the listener (the first answer is ghost-free and already reflects the restart), and the drain must
// precede the HTTP shutdown (a running execution is persisted, never dropped).
func TestDaemonConstructsTheDocumentedBootOrder(t *testing.T) {
	src := readDaemonSource(t)
	body := mainBody(t, src)

	// Each entry: the call main() makes for one boot step, and what that step is for.
	steps := []struct{ call, why string }{
		{"statepath.FromEnv", "1. the env contract names the state root — no path is baked in"},
		{"paths.CheckWritable", "1. the state root is writable and its tmp leftovers are swept"},
		{"auth.New", "1. SSO is fail-closed: no readable secret, no service"},
		{"api.New", "2. the passive pools are opened by their one owner"},
		{"runs.NewSettingsStore", "2. the settings pool (env seeds the FIRST start only, REQ-013.2)"},
		{"live.NewBroker", "2. the ONE live stream's broker (REQ-034)"},
		{"SetBroker", "2. the surfaces subscribe to the one stream"},
		{"execstate.Open", "2. the execution documents ARE the concurrency state"},
		{"MarkInterruptedAtBoot", "3. ghosts are exorcised BEFORE the first answer (REQ-039.1)"},
		{"sched.New", "the scheduler over the documents, with the executor injected (B-3)"},
		{"server.PreflightGate", "the B-4 admission gate runs before any document exists"},
		{"SetExecution", "the API answers slots and active work out of the machinery"},
		{"CompleteBootRestart", "4. a present restart marker means THIS boot is the restart"},
		{"server.Handler", "5. the route table goes up"},
		{"ServeReadySocket", "5. the restart wrapper's only gate (A2-7)"},
		{"ListenAndServe", "5. the first answers are served"},
		{"SyncStartupTodos", "6. work already in a default branch is checked off (REQ-039.2)"},
		{"scheduler.Start", "7.+8. resume enqueueing, the due ticker and the PR maintenance"},
		{"StartReporter", "8. the daily report and the delivery self-check"},
		{"verifyProtectionLoop", "8. branch protection is re-checked on its cadence (REQ-033.7)"},
		{"DrainAndPersist", "SIGTERM persists running executions as interrupted (K-2)"},
		{"httpServer.Shutdown", "only THEN does HTTP stop — the drain owns the budget"},
	}

	at := -1
	for _, step := range steps {
		i := strings.Index(body, step.call)
		if i < 0 {
			t.Errorf("main() never calls %s — %s", step.call, step.why)
			continue
		}
		if i < at {
			t.Errorf("%s appears out of order (%s) — the boot sequence is not the documented one", step.call, step.why)
		}
		at = i
	}

	// Present somewhere in the daemon (they sit in the helpers main() drives, so their POSITION
	// carries no meaning — their existence does).
	present := []struct{ call, why string }{
		{"executor.Execute", "the chain motor is the execution function the scheduler drives"},
		{"NewResultRecorder", "the recorder is the only writer of the live transcript and consumption"},
		{"ExecutionSink", "the sink composes the document half and the recorder half (§3.8)"},
		{"VerifyRepoProtection", "the protection check has a caller (REQ-033.7)"},
		{"EffectiveTuning", "the time budget is resolved at the ONE resolution point (REQ-010)"},
	}
	for _, p := range present {
		if !strings.Contains(src, p.call) {
			t.Errorf("the daemon never calls %s — %s", p.call, p.why)
		}
	}

	// No inline restart anywhere in the daemon: the handover lives in the root wrapper (K-2).
	for _, forbidden := range []string{"systemd-run", "systemctl"} {
		if strings.Contains(src, forbidden) {
			t.Errorf("the daemon reaches for %q — the restart is a handover through the root wrapper (K-2)", forbidden)
		}
	}
}

// mainBody isolates the body of func main — the boot order is what main DOES, not what the file
// happens to contain further down.
func mainBody(t *testing.T, src string) string {
	t.Helper()
	start := strings.Index(src, "func main()")
	if start < 0 {
		t.Fatal("the daemon has no func main")
	}
	rest := src[start+len("func main()"):]
	if end := strings.Index(rest, "\nfunc "); end >= 0 {
		return rest[:end]
	}
	return rest
}

// readDaemonSource concatenates the daemon's own sources (cmd/devlabd) with the COMMENTS REMOVED, so
// the order check reads the shipped composition and never the prose that documents it — the boot
// order at the top of main.go names every step and would otherwise satisfy the check by itself.
func readDaemonSource(t *testing.T) string {
	t.Helper()
	const dir = "../cmd/devlabd"
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		src, err := os.ReadFile(dir + "/" + e.Name())
		if err != nil {
			t.Fatal(err)
		}
		b.WriteString(stripComments(string(src)))
	}
	if b.Len() == 0 {
		t.Fatalf("no daemon source found under %s", dir)
	}
	return b.String()
}

// stripComments removes // line comments and /* */ blocks. The daemon's string literals carry no
// "//", so a scanner-free removal is exact enough for an order check and keeps this test free of a
// parser of its own.
func stripComments(src string) string {
	src = blockCommentRe.ReplaceAllString(src, " ")
	lines := strings.Split(src, "\n")
	for i, l := range lines {
		if j := strings.Index(l, "//"); j >= 0 {
			lines[i] = l[:j]
		}
	}
	return strings.Join(lines, "\n")
}

var blockCommentRe = regexp.MustCompile(`(?s)/\*.*?\*/`)

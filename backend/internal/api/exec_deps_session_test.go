package api

import (
	"regexp"
	"testing"

	"devlab/backend/internal/executor"
	"devlab/backend/internal/runs"
)

// The conversation of one repository inside one execution has a DERIVED name: same execution and
// same repository always yield the same name (so a resume finds it again), different repositories
// never share one, and the shape is the UUID the agent accepts. The runner used to hand the raw
// execution id to --resume and the agent refused it — no conversation had ever been opened under
// that name, because nothing was named on the fresh start.
func TestTheAgentConversationHasAStableDerivedName(t *testing.T) {
	uuidShape := regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-5[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

	a := sessionUUID("exec_20260730-210608-320811518", "holistic")
	if !uuidShape.MatchString(a) {
		t.Fatalf("not the shape the agent accepts: %q", a)
	}
	if b := sessionUUID("exec_20260730-210608-320811518", "holistic"); b != a {
		t.Fatalf("not stable: %q then %q — a resume would never find its conversation", a, b)
	}
	if b := sessionUUID("exec_20260730-210608-320811518", "holisdk"); b == a {
		t.Fatal("two repositories of one execution share a conversation")
	}
	if b := sessionUUID("exec_20260730-999999-000000000", "holistic"); b == a {
		t.Fatal("two executions of one repository share a conversation")
	}
}

// The fresh call OPENS the conversation under its name; the continuation asks for that very name.
// Both halves must hold — either one alone leaves the resume with nothing to find.
func TestTheFreshCallOpensTheConversationTheResumeAsksFor(t *testing.T) {
	flag := func(args []string, name string) (string, bool) {
		for i, a := range args {
			if a == name && i+1 < len(args) {
				return args[i+1], true
			}
		}
		return "", false
	}
	sess := executor.AgentSession{Key: "exec_1"}

	fresh := chainAgentArgs("holistic", "mercury-dev", runs.ResolvedTuning{}, sess)
	opened, ok := flag(fresh, "--session-id")
	if !ok {
		t.Fatalf("the fresh call opens no named conversation: %v", fresh)
	}
	if _, bad := flag(fresh, "--resume"); bad {
		t.Fatalf("the fresh call must not resume: %v", fresh)
	}

	sess.Resume = true
	again := chainAgentArgs("holistic", "mercury-dev", runs.ResolvedTuning{}, sess)
	asked, ok := flag(again, "--resume")
	if !ok {
		t.Fatalf("the continuation resumes nothing: %v", again)
	}
	if asked != opened {
		t.Fatalf("the continuation asks for %q but %q was opened", asked, opened)
	}
	if _, bad := flag(again, "--session-id"); bad {
		t.Fatalf("a resume must not re-open the conversation: %v", again)
	}
}

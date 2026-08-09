// The implement stage's session is OPEN: it can be watched while it works and written into. These
// tests pin the two halves that make that true and safe — the message actually reaches the agent,
// and the conversation still ENDS.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"devlab/backend/internal/executor"
	"devlab/backend/internal/model"
	"devlab/backend/internal/runs"
	"devlab/backend/internal/workspace"
)

// pipeInput is a stand-in for a running agent's input: it records what was written and whether it
// was closed — the two facts every test here is about.
type pipeInput struct {
	mu     sync.Mutex
	writes []string
	closed bool
}

func (p *pipeInput) Write(b []byte) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return 0, errors.New("write on a closed input")
	}
	p.writes = append(p.writes, string(b))
	return len(b), nil
}

func (p *pipeInput) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.closed = true
	return nil
}

func (p *pipeInput) said() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.writes...)
}

func (p *pipeInput) isClosed() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.closed
}

// spoken reads the text out of one message handed to the agent, in the shape streaming input takes.
func spoken(t *testing.T, raw string) string {
	t.Helper()
	var msg struct {
		Type    string `json:"type"`
		Message struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"message"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(raw)), &msg); err != nil {
		t.Fatalf("the agent was handed something that is not a message: %q (%v)", raw, err)
	}
	if msg.Type != "user" || msg.Message.Role != "user" {
		t.Fatalf("the agent was handed a %q/%q, not a user message", msg.Type, msg.Message.Role)
	}
	if !strings.HasSuffix(raw, "\n") {
		t.Fatalf("a message must be ONE line — without the terminator the agent never sees it: %q", raw)
	}
	return msg.Message.Content
}

// A person's message reaches the RUNNING agent, is recorded in the same session as the agent's own
// output, and marks the execution as one a person stepped into. All three, or the intervention is
// invisible somewhere.
func TestAPersonsMessageReachesTheAgentAndIsRecorded(t *testing.T) {
	f := newExecFixture(t)
	doc, run := f.doc("run_speak", "alpha")
	rec := f.srv.NewResultRecorder(doc, run)
	defer rec.Finish(nil)

	in := &pipeInput{}
	desk := f.srv.openDesk(doc.ID, "alpha", in)
	if err := desk.speak("# the order"); err != nil { // the chain's own opening message
		t.Fatal(err)
	}

	at := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	if err := f.srv.SpeakIntoSession(doc.ID, "alpha", "stop and check the migration first", model.Actor{User: "ada"}, at); err != nil {
		t.Fatalf("a person could not write into a running session: %v", err)
	}

	// 1. it reached the agent
	said := in.said()
	if len(said) != 2 {
		t.Fatalf("the agent was handed %d messages, want the order and the person's words", len(said))
	}
	if got := spoken(t, said[1]); got != "stop and check the migration first" {
		t.Errorf("the agent was handed %q", got)
	}

	// 2. it stands in the SAME record as the agent's own output, marked as the person's
	portion, err := f.srv.results.ReadSession(doc.ID, runs.SessionWindow{Back: true, Max: 50})
	if err != nil {
		t.Fatal(err)
	}
	var found *model.SessionLine
	for i := range portion.Lines {
		if portion.Lines[i].From != "" {
			found = &portion.Lines[i]
		}
	}
	if found == nil {
		t.Fatal("the person's words are not in the session record — the recording is not the whole session")
	}
	if found.From != "ada" || found.Text != "stop and check the migration first" || found.Repo != "alpha" {
		t.Errorf("the recorded line is %+v", *found)
	}

	// 3. the execution says a person stepped in, and who
	res, ok, err := f.srv.results.Get(doc.ID)
	if err != nil || !ok {
		t.Fatalf("no result document: ok=%v err=%v", ok, err)
	}
	if len(res.Interventions) != 1 {
		t.Fatalf("the execution records %d interventions, want 1 — it would read as purely self-acting", len(res.Interventions))
	}
	if res.Interventions[0].By.User != "ada" || !res.Interventions[0].At.Equal(at) || res.Interventions[0].Repo != "alpha" {
		t.Errorf("the intervention is recorded as %+v", res.Interventions[0])
	}
}

// The conversation must still END. Every message handed over is released by the turn that answers
// it: once they are all answered the input is closed, which is what lets the agent finish.
func TestTheConversationEndsWhenEveryMessageHasBeenAnswered(t *testing.T) {
	f := newExecFixture(t)
	doc, run := f.doc("run_turns", "alpha")
	rec := f.srv.NewResultRecorder(doc, run)
	defer rec.Finish(nil)

	in := &pipeInput{}
	desk := f.srv.openDesk(doc.ID, "alpha", in)
	if err := desk.speak("# the order"); err != nil {
		t.Fatal(err)
	}

	// A person writes BEFORE the first turn comes back: the answer to the order must not end the
	// conversation, because a message is still outstanding.
	if err := f.srv.SpeakIntoSession(doc.ID, "alpha", "also rename the flag", model.Actor{User: "ada"}, time.Now()); err != nil {
		t.Fatal(err)
	}
	desk.answer()
	if in.isClosed() {
		t.Fatal("the input closed while a person's message was still unanswered — their words would be cut off")
	}
	desk.answer()
	if !in.isClosed() {
		t.Fatal("the input never closed — the agent waits for input for ever and the stage never ends")
	}

	// And once it is over it says so, instead of pretending to take more.
	err := f.srv.SpeakIntoSession(doc.ID, "alpha", "one more thing", model.Actor{User: "ada"}, time.Now())
	if !errors.Is(err, errSessionClosed) {
		t.Fatalf("writing into an ended session answered %v, want the named refusal", err)
	}
}

// Without an open conversation nothing is accepted and nothing is recorded: a message that did not
// arrive must never leave a trace claiming it did.
func TestAMessageThatCannotArriveIsRefusedAndNotRecorded(t *testing.T) {
	f := newExecFixture(t)
	doc, run := f.doc("run_gone", "alpha")
	rec := f.srv.NewResultRecorder(doc, run)

	// The execution has ended: its register entry is gone.
	rec.Finish(nil)

	if err := f.srv.SpeakIntoSession(doc.ID, "alpha", "hello?", model.Actor{User: "ada"}, time.Now()); !errors.Is(err, errNoSuchSession) {
		t.Fatalf("writing into a finished execution answered %v, want the named refusal", err)
	}
	res, ok, err := f.srv.results.Get(doc.ID)
	if err != nil || !ok {
		t.Fatal(err)
	}
	if len(res.Interventions) != 0 {
		t.Errorf("a refused message was recorded as an intervention: %+v", res.Interventions)
	}
}

// A blank message is not an intervention, and is refused before it can be recorded as one.
func TestABlankMessageIsNotAnIntervention(t *testing.T) {
	f := newExecFixture(t)
	doc, run := f.doc("run_blank", "alpha")
	rec := f.srv.NewResultRecorder(doc, run)
	defer rec.Finish(nil)
	f.srv.openDesk(doc.ID, "alpha", &pipeInput{})

	if err := f.srv.SpeakIntoSession(doc.ID, "alpha", "   \n ", model.Actor{User: "ada"}, time.Now()); !errors.Is(err, errEmptyMessage) {
		t.Fatalf("a blank message answered %v, want the named refusal", err)
	}
}

// While several repositories are working, a message must NAME the one it is for: guessing which
// agent a person meant is worse than asking.
func TestSeveralWorkingRepositoriesMustBeNamed(t *testing.T) {
	f := newExecFixture(t)
	doc, run := f.doc("run_two", "alpha", "beta")
	rec := f.srv.NewResultRecorder(doc, run)
	defer rec.Finish(nil)
	f.srv.openDesk(doc.ID, "alpha", &pipeInput{})

	// One open conversation: naming it is optional.
	if err := f.srv.SpeakIntoSession(doc.ID, "", "carry on", model.Actor{User: "ada"}, time.Now()); err != nil {
		t.Fatalf("with a single open conversation the repository must be optional: %v", err)
	}

	f.srv.openDesk(doc.ID, "beta", &pipeInput{})
	err := f.srv.SpeakIntoSession(doc.ID, "", "carry on", model.Actor{User: "ada"}, time.Now())
	if err == nil {
		t.Fatal("with two open conversations an unaddressed message was accepted — it reached a guessed agent")
	}
	if !strings.Contains(err.Error(), "name the one") {
		t.Errorf("the refusal does not say what to do: %v", err)
	}
	if err := f.srv.SpeakIntoSession(doc.ID, "beta", "carry on", model.Actor{User: "ada"}, time.Now()); err != nil {
		t.Fatalf("a named conversation was refused: %v", err)
	}
}

// ── the invocation itself ────────────────────────────────────────────────────────────────

// The opening message travels on the INPUT, not on the command line. It has to: with streaming
// input the CLI reads its prompt from stdin and ignores one given as an argument — an order left on
// the command line would produce an agent that does nothing at all, silently.
func TestTheOrderTravelsOnTheOpenInput(t *testing.T) {
	f := newExecFixture(t)
	doc, run := f.doc("run_args", "alpha")
	rec := f.srv.NewResultRecorder(doc, run)
	defer rec.Finish(nil)

	args := chainAgentArgs("alpha", "fix/thing", runs.ResolvedTuning{}, executor.AgentSession{Key: doc.ID})
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "--input-format stream-json") {
		t.Fatalf("the conversation is started with a closed input — nobody can write into it: %v", args)
	}
	for i, a := range args {
		if a == "-p" && i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
			t.Fatalf("the order is still on the command line (%q) — with streaming input the CLI ignores it", args[i+1])
		}
	}

	in := &pipeInput{}
	desk := f.srv.openDesk(doc.ID, "alpha", in)
	if err := desk.speak("# the order"); err != nil {
		t.Fatal(err)
	}
	said := in.said()
	if len(said) != 1 || spoken(t, said[0]) != "# the order" {
		t.Fatalf("the order did not reach the agent: %v", said)
	}
}

// The invocation hands its input over and takes it back: after it ends nothing can be written into
// it any more, however it ended.
func TestTheInputIsReleasedWhenTheInvocationEnds(t *testing.T) {
	f := newExecFixture(t)
	doc, run := f.doc("run_release", "alpha")
	rec := f.srv.NewResultRecorder(doc, run)
	defer rec.Finish(nil)

	deps := &ChainDeps{s: f.srv}
	stream := deps.startAgent(context.Background(), workspace.Executor{}, t.TempDir(), doc.ID, "alpha", "# the order",
		[]string{"--this-invocation-never-runs"})

	// Read the (empty) output to EOF the way the motor does, then wait for the outcome.
	_, _ = io.ReadAll(stream.Output())
	_ = stream.Wait()

	if err := f.srv.SpeakIntoSession(doc.ID, "alpha", "hello?", model.Actor{User: "ada"}, time.Now()); err == nil {
		t.Fatal("an ended invocation still accepts messages — they would go nowhere")
	}
}

// A turn-ending event is recognised for what it is, and nothing else is mistaken for one — that
// recognition is the whole close mechanism.
func TestOnlyATurnEndReleasesAMessage(t *testing.T) {
	cases := []struct {
		line string
		want bool
	}{
		{`{"type":"result","subtype":"success","is_error":false}`, true},
		{`{"type":"assistant","message":{"content":[{"type":"text","text":"working"}]}}`, false},
		{`{"type":"system","subtype":"init"}`, false},
		{`not json at all`, false},
		{``, false},
	}
	for _, c := range cases {
		if got := isAgentTurnEnd([]byte(c.line)); got != c.want {
			t.Errorf("isAgentTurnEnd(%q) = %v, want %v", c.line, got, c.want)
		}
	}
}

// ── the window must never become a valve ──────────────────────────────────────────────────

// Watching is a WINDOW, not a valve: with the register gone (nobody can follow or speak) the
// invocation still starts, still receives its order and still ends. Losing sight of a run must
// never cost the run.
func TestVisibilityIsAWindowNotAValve(t *testing.T) {
	f := newExecFixture(t)
	doc, run := f.doc("run_blind", "alpha")
	rec := f.srv.NewResultRecorder(doc, run)
	defer rec.Finish(nil)

	f.srv.sessions = nil // the register is gone

	in := &pipeInput{}
	desk := f.srv.openDesk(doc.ID, "alpha", in)
	if desk == nil {
		t.Fatal("no desk without a register — the invocation could not even be given its order")
	}
	if err := desk.speak("# the order"); err != nil {
		t.Fatalf("the order did not reach the agent although only the WATCHING is gone: %v", err)
	}
	if len(in.said()) != 1 {
		t.Fatalf("the agent was handed %d messages, want the order", len(in.said()))
	}
	// And the invocation still ends: releasing the desk closes the input, which is what lets the
	// agent finish.
	f.srv.dropDesk(doc.ID, "alpha", desk)
	if !in.isClosed() {
		t.Fatal("the input stayed open with no register to close it — the agent would wait for ever")
	}
	// Nobody can speak, and that is said plainly rather than pretended.
	if err := f.srv.SpeakIntoSession(doc.ID, "alpha", "hello?", model.Actor{User: "ada"}, time.Now()); !errors.Is(err, errNoSuchSession) {
		t.Errorf("speaking without a register answered %v, want the named refusal", err)
	}
}

// A journal that cannot be written costs the transcript, never the execution: the stage array is
// still recorded and the run walks on.
func TestAnUnwritableJournalDoesNotStopTheExecution(t *testing.T) {
	f := newExecFixture(t)
	doc, run := f.doc("run_nojournal", "alpha")
	rec := f.srv.NewResultRecorder(doc, run)
	defer rec.Finish(nil)

	// Point the executions directory at a FILE: every journal append now fails.
	broken := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(broken, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DEVLAB_MERCURY_EXECUTIONS", broken)
	f.srv.results = runs.NewResultStore(f.paths)

	rec.Transcript("alpha", executor.SessionLine("alpha", "", "this line cannot be stored"))

	started := time.Now().UTC()
	rec.StageUpdate("alpha", model.StageView{Stage: model.StageImplement, State: model.StepExecuted, StartedAt: &started, Session: true})
	// Nothing panicked, nothing blocked — the stage went on being recorded, which is what the
	// execution depends on.
}

// An oversized message is REFUSED, never shortened: half a person's words would reach the agent as
// something they never said, and be recorded as such.
func TestAnOversizedMessageIsRefusedNotShortened(t *testing.T) {
	f := newExecFixture(t)
	doc, run := f.doc("run_long", "alpha")
	rec := f.srv.NewResultRecorder(doc, run)
	defer rec.Finish(nil)
	in := &pipeInput{}
	f.srv.openDesk(doc.ID, "alpha", in)

	long := strings.Repeat("x", maxSpokenBytes+1)
	if err := f.srv.SpeakIntoSession(doc.ID, "alpha", long, model.Actor{User: "ada"}, time.Now()); !errors.Is(err, errMessageTooLong) {
		t.Fatalf("an oversized message answered %v, want the named refusal", err)
	}
	if len(in.said()) != 0 {
		t.Error("a shortened message reached the agent anyway")
	}
	res, _, _ := f.srv.results.Get(doc.ID)
	if len(res.Interventions) != 0 {
		t.Error("a refused message was recorded as an intervention")
	}
}

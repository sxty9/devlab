package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"devlab/backend/internal/live"
	"devlab/backend/internal/mcp"
	"devlab/backend/internal/mercury"
)

// The AI proposal accesses (ai-fill / ai-finetune) are the two calls whose duration nobody can
// predict: a plan over the whole constitution is minutes of model work, and every hop in front of
// devlabd drops a connection that has been silent for a while. The surface then showed "failed"
// for work the server had finished cleanly.
//
// These tests hold the repaired shape: the call TAKES THE WORK ON and answers at once, the result
// arrives over the one live stream, a failure carries a NAMED reason, and coming back (a reload)
// loses neither the running analysis nor the finished proposal.

// slowAigentic is an aigentic stand-in that answers only once the test releases it — the model
// call that outlives the connection its request came in on. It answers with `body` (the text a
// claude-cli run would print) unless `status` demands a refusal.
type slowAigentic struct {
	release chan struct{}
	once    sync.Once
	calls   chan string // one entry per call, carrying the prompt
	mu      sync.Mutex  // the answer may change mid-test (a model that works again)
	status  int
	body    string
}

// answersNow lets every waiting call through. It is idempotent and runs on cleanup too, so a test
// that ends early — a failing assertion above all — is never held open by a stand-in still waiting.
func (a *slowAigentic) answersNow() {
	a.once.Do(func() { close(a.release) })
}

// answersWith changes what the stand-in replies from now on.
func (a *slowAigentic) answersWith(status int, body string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.status, a.body = status, body
}

func (a *slowAigentic) reply() (int, string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.status, a.body
}

// modelPasses is what the calls so far really COST: one entry per prompt that reached the model.
// It drains the record, so each check reads the passes since the last one.
func modelPasses(a *slowAigentic) int {
	n := 0
	for {
		select {
		case <-a.calls:
			n++
		default:
			return n
		}
	}
}

func startSlowAigentic(t *testing.T, a *slowAigentic) {
	t.Helper()
	a.release = make(chan struct{})
	a.calls = make(chan string, 8)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var env struct {
			Header map[string]string `json:"header"`
			Data   json.RawMessage   `json:"data"`
		}
		_ = json.NewDecoder(r.Body).Decode(&env)
		var req struct {
			Prompt string `json:"prompt"`
		}
		_ = json.Unmarshal(env.Data, &req)
		select {
		case a.calls <- req.Prompt:
		default:
		}
		select {
		case <-a.release:
		case <-r.Context().Done():
			return // the caller gave up — answer nobody
		}
		status, body := a.reply()
		w.Header().Set("Content-Type", "application/json")
		if status != 0 {
			w.WriteHeader(status)
			_ = json.NewEncoder(w).Encode(map[string]string{"detail": "no AI right"})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"header": map[string]string{},
			"data":   map[string]any{"output": body, "engine": "claude-cli", "model": "claude-test"},
		})
	}))
	t.Cleanup(srv.Close)
	// Registered last, so it runs FIRST: no call may still be waiting when the stand-in shuts down.
	t.Cleanup(a.answersNow)
	t.Setenv("DEVLAB_AIGENTIC_URL", srv.URL)
}

// planJSON is the answer a healthy model gives: one run covering the named axioms.
func planJSON(name string, ids ...string) string {
	quoted := make([]string, 0, len(ids))
	for _, id := range ids {
		quoted = append(quoted, `"`+id+`"`)
	}
	return fmt.Sprintf(`{"runs":[{"name":%q,"axiomIds":[%s],"schedule":{"kind":"daily","timeOfDay":"03:00"},"rationale":"themed"}]}`,
		name, strings.Join(quoted, ","))
}

// aiRequest drives one AI proposal access; action "" is the plain request an MCP caller sends.
func aiRequest(t *testing.T, h http.HandlerFunc, action string) *httptest.ResponseRecorder {
	t.Helper()
	body := map[string]any{}
	if action != "" {
		body["action"] = action
	}
	return doJSON(t, h, http.MethodPost, "/api/mercury/runs/ai-fill", "planner", "", body)
}

// aiState decodes the state document every access answers with.
func aiState(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("answer is not JSON (status %d): %s", rec.Code, rec.Body)
	}
	return out
}

func aiPost(t *testing.T, h http.HandlerFunc, action string) (int, map[string]any) {
	t.Helper()
	rec := aiRequest(t, h, action)
	return rec.Code, aiState(t, rec)
}

// awaitState does exactly what the surface does: it re-reads ONLY when the live stream ticks, and
// waits for the state the outcome should have reached. Nothing here polls — a result that never
// announced itself on the stream lets this run into its deadline.
// The deadline is the OUTER bound, not just one branch of the wait: a defect that makes the state
// tick over and over must end this in ten seconds like any other, never keep it alive forever on
// its own noise.
func awaitState(t *testing.T, ticks <-chan live.Topic, h http.HandlerFunc, want string) map[string]any {
	t.Helper()
	end := time.Now().Add(10 * time.Second)
	for time.Now().Before(end) {
		select {
		case got := <-ticks:
			if got != live.TopicRuns {
				continue
			}
			if _, doc := aiPost(t, h, "read"); doc["state"] == want {
				return doc
			}
		case <-time.After(time.Until(end)):
		}
	}
	t.Fatalf("no runs tick carried the state %q within 10s — the outcome never reached the surface", want)
	return nil
}

// THE regression: the request must not wait for the model. The stand-in answers only after the
// test releases it, so a handler that waits for the answer can never return here — which is
// exactly what the user met as "failed" once the connection was dropped at ~100 s.
func TestAiFillAnswersWhileTheModelIsStillThinking(t *testing.T) {
	s, ids := newRunsTestServer(t, 3)
	s.broker = live.NewBroker()
	ticks, stop := s.broker.Subscribe(nil)
	defer stop()
	ai := &slowAigentic{body: planJSON("Architecture", ids...)}
	startSlowAigentic(t, ai)

	answered := make(chan *httptest.ResponseRecorder, 1)
	go func() { answered <- aiRequest(t, s.runsAiFill, "") }()

	var rec *httptest.ResponseRecorder
	select {
	case rec = <-answered:
	case <-time.After(10 * time.Second):
		t.Fatal("ai-fill did not answer while the model was still thinking — the call still hangs on the connection")
	}
	first := aiState(t, rec)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status %d, want 202 Accepted (work taken on, not done)", rec.Code)
	}
	if first["state"] != aiStateRunning {
		t.Fatalf("state %v, want %q — the surface must be told work is under way, never that it failed", first["state"], aiStateRunning)
	}
	if first["proposal"] != nil {
		t.Error("a running analysis must not carry a proposal — nothing may claim to be ready")
	}

	// The work really is under way: the model has been asked, and asking again reports THAT
	// analysis instead of starting a rival.
	select {
	case <-ai.calls:
	case <-time.After(10 * time.Second):
		t.Fatal("the model was never asked — the work was not taken on at all")
	}
	if _, again := aiPost(t, s.runsAiFill, ""); again["state"] != aiStateRunning || again["id"] != first["id"] {
		t.Errorf("a second request started a rival analysis: %v", again)
	}

	// The answer arrives long after the connection its request came in on is gone — and reaches the
	// surface over the one live stream.
	ai.answersNow()
	doc := awaitState(t, ticks, s.runsAiFill, aiStateCompleted)
	proposal, _ := doc["proposal"].(map[string]any)
	planned, _ := proposal["runs"].([]any)
	if len(planned) != 1 {
		t.Fatalf("the finished proposal did not reach the surface: %v", doc["proposal"])
	}
	legend, _ := doc["axioms"].(map[string]any)
	if len(legend) != len(ids) {
		t.Errorf("the axiom legend is missing from the result: %v", doc["axioms"])
	}
}

// A refused model call must reach the user as a REASON, never as a bare "failed" and never as a
// raw transport line.
func TestAiProposalFailureCarriesANamedReason(t *testing.T) {
	s, ids := newRunsTestServer(t, 2)
	s.broker = live.NewBroker()
	ticks, stop := s.broker.Subscribe(nil)
	defer stop()
	ai := &slowAigentic{status: http.StatusForbidden}
	startSlowAigentic(t, ai)
	ai.answersNow() // fail at once

	if code, doc := aiPost(t, s.runsAiFill, ""); code != http.StatusAccepted || doc["state"] != aiStateRunning {
		t.Fatalf("start = %d %v, want 202 running", code, doc)
	}
	doc := awaitState(t, ticks, s.runsAiFill, aiStateFailed)
	reason, _ := doc["reason"].(string)
	if strings.TrimSpace(reason) == "" || strings.EqualFold(reason, "failed") {
		t.Fatalf("reason %q — a failure must NAME why", reason)
	}
	if !strings.Contains(strings.ToLower(reason), "access") || !strings.Contains(reason, "aigentic") {
		t.Errorf("reason %q does not tell the user what is missing (right / key)", reason)
	}
	if strings.Contains(reason, "403 Forbidden") {
		t.Errorf("reason %q is the transport line, not a sentence the user can act on", reason)
	}
	if n := modelPasses(ai); n != 1 {
		t.Fatalf("the analysis cost %d model passes, want 1", n)
	}

	// Asking again over a failure REPORTS it — the caller learns what went wrong instead of being
	// told "running" while a second model pass begins behind its back.
	ai.answersWith(0, planJSON("Architecture", ids...))
	code, again := aiPost(t, s.runsAiFill, "")
	if code != http.StatusOK || again["state"] != aiStateFailed || again["reason"] != reason {
		t.Fatalf("a repeat over a failure = %d %v, want 200 with the SAME failure and its reason", code, again)
	}
	if n := modelPasses(ai); n != 0 {
		t.Fatalf("the repeat asked the model %d more times — a failure must not restart by itself", n)
	}

	// Starting over is a DELIBERATE act of two steps: put the failure aside, then ask again.
	if _, put := aiPost(t, s.runsAiFill, "cancel"); put["state"] != aiStateNone {
		t.Fatalf("cancel = %v, want none", put)
	}
	if code, fresh := aiPost(t, s.runsAiFill, ""); code != http.StatusAccepted || fresh["state"] != aiStateRunning {
		t.Fatalf("the deliberate restart = %d %v, want a fresh 202 running", code, fresh)
	}
}

// Reading is a READ: returning to the surface (or reloading it) shows what is running or waiting,
// and never sets an analysis off by itself.
func TestReadingNeverStartsAnAnalysisAndAReloadLosesNothing(t *testing.T) {
	s, ids := newRunsTestServer(t, 2)
	s.broker = live.NewBroker()
	ticks, stop := s.broker.Subscribe(nil)
	defer stop()
	ai := &slowAigentic{body: planJSON("Architecture", ids...)}
	startSlowAigentic(t, ai)

	// A surface that has just been opened asks first — and nothing happens.
	code, doc := aiPost(t, s.runsAiFill, "read")
	if code != http.StatusOK || doc["state"] != aiStateNone {
		t.Fatalf("read on a quiet server = %d %v, want 200 none", code, doc)
	}
	select {
	case p := <-ai.calls:
		t.Fatalf("a read asked the model: %q", p)
	case <-time.After(200 * time.Millisecond):
	}

	if _, doc := aiPost(t, s.runsAiFill, ""); doc["state"] != aiStateRunning {
		t.Fatalf("start = %v, want running", doc)
	}
	// The page is reloaded while the model is still thinking: the running analysis is still there.
	_, reloaded := aiPost(t, s.runsAiFill, "read")
	if reloaded["state"] != aiStateRunning || reloaded["startedAt"] == nil {
		t.Fatalf("after a reload the running analysis was lost: %v", reloaded)
	}

	ai.answersNow()
	if after := awaitState(t, ticks, s.runsAiFill, aiStateCompleted); after["proposal"] == nil {
		t.Fatalf("after a reload the finished proposal was lost: %v", after)
	}
}

// Cancelling abandons the analysis — and its late answer must not resurrect a proposal the user
// has put aside.
func TestCancelAbandonsTheAnalysisAndItsLateAnswer(t *testing.T) {
	s, ids := newRunsTestServer(t, 2)
	s.broker = live.NewBroker()
	ai := &slowAigentic{body: planJSON("Architecture", ids...)}
	startSlowAigentic(t, ai)

	if _, doc := aiPost(t, s.runsAiFill, ""); doc["state"] != aiStateRunning {
		t.Fatalf("start = %v, want running", doc)
	}
	code, doc := aiPost(t, s.runsAiFill, "cancel")
	if code != http.StatusOK || doc["state"] != aiStateNone {
		t.Fatalf("cancel = %d %v, want 200 none", code, doc)
	}
	ai.answersNow() // the model answers after the user gave up
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, doc := aiPost(t, s.runsAiFill, "read"); doc["state"] != aiStateNone {
			t.Fatalf("a cancelled analysis came back as %v", doc)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// An applied proposal is consumed: afterwards nothing waits for review — otherwise a reload would
// offer the very same plan again, over runs that have moved on since.
func TestApplyingAProposalConsumesIt(t *testing.T) {
	s, ids := newRunsTestServer(t, 2)
	s.broker = live.NewBroker()
	ticks, stop := s.broker.Subscribe(nil)
	defer stop()
	plan := mercury.RunPlan{Runs: []mercury.PlannedRun{{
		Name: "Architecture", AxiomIDs: ids,
		Schedule: mercury.PlanSchedule{Kind: "daily", TimeOfDay: "03:00"},
	}}}
	s.aiProposalStart(aiFill, func(context.Context) (mercury.RunPlan, error) { return plan, nil }, map[string]string{})
	awaitState(t, ticks, s.runsAiFill, aiStateCompleted)

	rec := doJSON(t, s.runsApplyProposal, http.MethodPost, "/api/mercury/runs/apply-proposal", "planner", "",
		map[string]any{"mode": "fill", "plan": plan})
	if rec.Code != http.StatusOK {
		t.Fatalf("apply = %d: %s", rec.Code, rec.Body)
	}
	if _, doc := aiPost(t, s.runsAiFill, "read"); doc["state"] != aiStateNone {
		t.Fatalf("the applied proposal is still waiting for review: %v", doc)
	}
}

// An action nobody defined is refused rather than silently treated as a request that burns tokens.
func TestUnknownProposalActionIsRefused(t *testing.T) {
	s, _ := newRunsTestServer(t, 1)
	rec := doJSON(t, s.runsAiFill, http.MethodPost, "/api/mercury/runs/ai-fill", "planner", "",
		map[string]string{"action": "restart"})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d, want 400 for an unknown action", rec.Code)
	}
}

// ── the second way in: an agent over MCP (REQ-043 parity) ────────────────────────────────────
//
// The surface is one way to the access point, the tool table the other, and both must be able to
// do the SAME three things. An agent that could only ask — the shape the table had — could never
// learn why an analysis failed and could never stop one, so every round cost a full constitution
// prompt while it was told "running" again and again. These tests drive the tool, not the route.

// toolRow is the table row an agent calls by name.
func toolRow(t *testing.T, name string) mcpTool {
	t.Helper()
	for _, row := range mcpToolRows() {
		if row.Name == name {
			return row
		}
	}
	t.Fatalf("no MCP tool named %q", name)
	return mcpTool{}
}

// callTool drives one tool exactly as an agent does: named arguments in, the tier re-checked, the
// route's OWN handler answering — so what it shows here is what an agent really gets.
func callTool(t *testing.T, s *Server, name string, args map[string]any) map[string]any {
	t.Helper()
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	out, err := s.mcpInvoke(mcpCtx(true), toolRow(t, name), withRight(), raw)
	if err != nil {
		t.Fatalf("%s(%s): %v", name, raw, err)
	}
	content, ok := out.(*mcp.Content)
	if !ok || len(content.JSON) == 0 {
		t.Fatalf("%s(%s) answered no JSON document: %#v", name, raw, out)
	}
	var doc map[string]any
	if err := json.Unmarshal(content.JSON, &doc); err != nil {
		t.Fatalf("%s: the answer is not a document: %s", name, content.JSON)
	}
	return doc
}

// awaitToolState is awaitState for the agent's way in: it re-reads ONLY when the live stream
// ticks, and reading over MCP must start nothing either.
func awaitToolState(t *testing.T, ticks <-chan live.Topic, s *Server, tool, want string) map[string]any {
	t.Helper()
	end := time.Now().Add(10 * time.Second)
	for time.Now().Before(end) {
		select {
		case got := <-ticks:
			if got != live.TopicRuns {
				continue
			}
			if doc := callTool(t, s, tool, map[string]any{"action": "read"}); doc["state"] == want {
				return doc
			}
		case <-time.After(time.Until(end)):
		}
	}
	t.Fatalf("no runs tick carried the state %q to the agent within 10s", want)
	return nil
}

// Both planning tools offer all three acts. Without them an agent has one button for a capability
// the surface has three, which is precisely the parity REQ-043 requires in BOTH directions.
func TestBothPlanningToolsOfferRequestReadAndCancel(t *testing.T) {
	for _, name := range []string{"run_ai_fill", "run_ai_finetune"} {
		var action mcpParam
		for _, p := range toolRow(t, name).Params {
			if p.Name == "action" {
				action = p
			}
		}
		if action.Name == "" {
			t.Fatalf("%s carries no action argument — an agent could only ever ask, never read or cancel", name)
		}
		if !slices.Equal(action.Enum, []string{"request", "read", "cancel"}) {
			t.Errorf("%s offers %v, want request, read and cancel", name, action.Enum)
		}
		if action.Required {
			t.Errorf("%s: the action is optional — the plain call IS the request", name)
		}
	}
}

// An agent must be able to stop what it started: `cancel` over MCP abandons the analysis — the
// same act the Stop button performs — and the late answer must not resurrect it.
func TestCancelOverMCPAbandonsTheAnalysis(t *testing.T) {
	s, ids := newRunsTestServer(t, 2)
	s.broker = live.NewBroker()
	ai := &slowAigentic{body: planJSON("Architecture", ids...)}
	startSlowAigentic(t, ai)

	if doc := callTool(t, s, "run_ai_fill", map[string]any{}); doc["state"] != aiStateRunning {
		t.Fatalf("the plain call = %v, want running (the empty arguments ARE the request)", doc)
	}
	select {
	case <-ai.calls:
	case <-time.After(10 * time.Second):
		t.Fatal("the model was never asked — the agent's call took no work on")
	}
	if doc := callTool(t, s, "run_ai_fill", map[string]any{"action": "cancel"}); doc["state"] != aiStateNone {
		t.Fatalf("cancel over MCP = %v, want none", doc)
	}

	ai.answersNow() // the model answers after the agent gave up
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if doc := callTool(t, s, "run_ai_fill", map[string]any{"action": "read"}); doc["state"] != aiStateNone {
			t.Fatalf("a cancelled analysis came back as %v", doc)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// A failure must reach the AGENT too, by its name — and reading it costs nothing.
func TestReadingAFailureOverMCPDeliversItsReasonAndAsksNoModel(t *testing.T) {
	s, _ := newRunsTestServer(t, 2)
	s.broker = live.NewBroker()
	ticks, stop := s.broker.Subscribe(nil)
	defer stop()
	ai := &slowAigentic{status: http.StatusForbidden}
	startSlowAigentic(t, ai)
	ai.answersNow() // refuse at once

	if doc := callTool(t, s, "run_ai_fill", map[string]any{}); doc["state"] != aiStateRunning {
		t.Fatalf("start = %v, want running", doc)
	}
	failed := awaitToolState(t, ticks, s, "run_ai_fill", aiStateFailed)
	reason, _ := failed["reason"].(string)
	if strings.TrimSpace(reason) == "" || strings.EqualFold(reason, aiStateFailed) {
		t.Fatalf("the agent was told %q — a failure must NAME why", reason)
	}
	if n := modelPasses(ai); n != 1 {
		t.Fatalf("the analysis cost %d model passes, want 1", n)
	}

	// Reading it three more times says the same thing and asks nobody.
	for i := 0; i < 3; i++ {
		doc := callTool(t, s, "run_ai_fill", map[string]any{"action": "read"})
		if doc["state"] != aiStateFailed || doc["reason"] != reason || doc["id"] != failed["id"] {
			t.Fatalf("read %d = %v, want the SAME failure with its reason", i+1, doc)
		}
	}
	if n := modelPasses(ai); n != 0 {
		t.Fatalf("reading asked the model %d times — a read must start nothing", n)
	}
}

// THE measured loop: five plain tool calls — the only shape the table used to allow — while
// aigentic refuses. Each call used to begin a fresh analysis: the agent was answered "running"
// every single time, never learnt why, and paid one full constitution prompt per round, without
// end. Now the first call takes the work on and the other four report ITS outcome, by name, for
// no model pass at all.
func TestFivePlainAgentCallsCostExactlyOneModelPass(t *testing.T) {
	s, _ := newRunsTestServer(t, 3)
	s.broker = live.NewBroker()
	ticks, stop := s.broker.Subscribe(nil)
	defer stop()
	ai := &slowAigentic{status: http.StatusForbidden}
	startSlowAigentic(t, ai)
	ai.answersNow() // refuse at once, as the inspected instance did

	if first := callTool(t, s, "run_ai_fill", map[string]any{}); first["state"] != aiStateRunning {
		t.Fatalf("call 1 = %v, want running", first)
	}
	failed := awaitToolState(t, ticks, s, "run_ai_fill", aiStateFailed)
	for i := 2; i <= 5; i++ {
		doc := callTool(t, s, "run_ai_fill", map[string]any{})
		if doc["state"] != aiStateFailed {
			t.Fatalf("call %d = %v, want the failure — never a fresh %q", i, doc, aiStateRunning)
		}
		if reason, _ := doc["reason"].(string); strings.TrimSpace(reason) == "" || reason != failed["reason"] {
			t.Fatalf("call %d named no reason: %v", i, doc)
		}
	}
	if n := modelPasses(ai); n != 1 {
		t.Fatalf("five calls cost %d model passes, want exactly 1", n)
	}
}

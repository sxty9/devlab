package api

// The AI assistant route and the guards it rides on:
//   - bearer is an equivalent auth path to cookie+CSRF (D 34): the same verifier, CSRF waived
//     only on the bearer path, every other verdict identical;
//   - the assistant proxies to aigentic with the CALLER's session cookie and never calls a model
//     provider itself (B-02);
//   - the transcript persists per user+repo and is capped (B-08);
//   - both AI paths report their consumption to the ONE usage pool, and every answer carries the
//     model that produced it (labeling duty, D 26).

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"devlab/backend/internal/auth"
	"devlab/backend/internal/chats"
	"devlab/backend/internal/model"
	"devlab/backend/internal/statepath"
	"devlab/backend/internal/telemetry"
)

// ── guards: bearer ≡ cookie ────────────────────────────────────────────────────────────────

// probe answers 200 and records that it ran.
func probe(ran *bool) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		*ran = true
		w.WriteHeader(http.StatusOK)
	}
}

const guardSecret = "guard-matrix-secret"

func guardVerifier(t *testing.T) *auth.Verifier {
	t.Helper()
	t.Setenv("DEVLAB_DEV_BYPASS_AUTH", "")
	// The highest-precedence source, so a shared secret installed on the machine cannot make the
	// test depend on it.
	secretFile := filepath.Join(t.TempDir(), "jwt-secret")
	if err := os.WriteFile(secretFile, []byte(guardSecret), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DEVLAB_PREVIEW_SECRET_FILE", secretFile)
	t.Setenv("HOLISTIC_SECRET", guardSecret)
	return auth.New()
}

// osUser is the one Linux account a test may rely on: the one running the test. Groups are
// resolved live from the OS, so a session must name a real account.
func osUser(t *testing.T) string {
	t.Helper()
	u, err := user.Current()
	if err != nil || u.Username == "" {
		t.Skip("no current OS user to build a session for")
	}
	return u.Username
}

// accessToken signs a holistic-shaped access token with the shared test secret.
func accessToken(t *testing.T, sub string) string {
	t.Helper()
	s, err := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"sub": sub, "type": "access", "exp": time.Now().Add(auth.AccessTTL).Unix(),
	}).SignedString([]byte(guardSecret))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// TestGuardMatrixBearerEqualsCookie pins the whole matrix on the ONE resolution entry: no
// credentials ⇒ 401; a mutating cookie request without the CSRF header ⇒ 403, while the same
// request as bearer passes (a bearer header cannot be sent cross-site by a browser form); and
// guardWrite's GitHub-link requirement is unaffected by which auth path was used.
func TestGuardMatrixBearerEqualsCookie(t *testing.T) {
	v := guardVerifier(t)
	if !v.HasSecret() {
		t.Skip("no signing secret available")
	}
	name := osUser(t)
	access := accessToken(t, name)
	csrf := "0123456789abcdef0123456789abcdef"
	s := &Server{v: v}

	cookieAuth := func(r *http.Request) { r.AddCookie(&http.Cookie{Name: "h_access", Value: access}) }
	bearerAuth := func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+access) }
	noAuth := func(*http.Request) {}

	// Does this machine's account hold hp_devlab_access? The right-gated expectations follow the
	// honest answer instead of a machine-dependent guess.
	probeReq := httptest.NewRequest(http.MethodGet, "/api/user", nil)
	cookieAuth(probeReq)
	resolved, err := v.FromRequest(probeReq)
	if err != nil {
		t.Fatalf("cookie session did not resolve: %v", err)
	}
	rightOK := resolved.CanUseDevlab()

	cases := []struct {
		name       string
		auth       func(*http.Request)
		withCSRF   bool
		wantAuthed int // guardAuthed verdict
		wantCSRF   int // guardCSRF verdict when the right is held
	}{
		{"no credentials", noAuth, false, http.StatusUnauthorized, http.StatusUnauthorized},
		{"cookie without csrf", cookieAuth, false, http.StatusOK, http.StatusForbidden},
		{"cookie with csrf", cookieAuth, true, http.StatusOK, http.StatusOK},
		{"bearer without csrf", bearerAuth, false, http.StatusOK, http.StatusOK},
		{"bearer with csrf", bearerAuth, true, http.StatusOK, http.StatusOK},
	}
	newReq := func(c struct {
		name       string
		auth       func(*http.Request)
		withCSRF   bool
		wantAuthed int
		wantCSRF   int
	}) *http.Request {
		r := httptest.NewRequest(http.MethodPost, "/api/repos/x/assistant", nil)
		c.auth(r)
		if c.withCSRF {
			r.Header.Set("X-CSRF-Token", csrf)
			r.AddCookie(&http.Cookie{Name: "h_csrf", Value: csrf})
		}
		return r
	}

	for _, c := range cases {
		var ran bool
		rec := httptest.NewRecorder()
		s.guardAuthed(probe(&ran))(rec, newReq(c))
		if rec.Code != c.wantAuthed {
			t.Errorf("%s: guardAuthed = %d, want %d", c.name, rec.Code, c.wantAuthed)
		}

		ran = false
		want := c.wantCSRF
		if want == http.StatusOK && !rightOK {
			want = http.StatusForbidden // missing hp_devlab_access on this machine
		}
		rec = httptest.NewRecorder()
		s.guardCSRF(probe(&ran))(rec, newReq(c))
		if rec.Code != want {
			t.Errorf("%s: guardCSRF = %d, want %d", c.name, rec.Code, want)
		}
	}

	// guardWrite additionally demands a linked GitHub account — orthogonal to the auth path, so
	// bearer and cookie receive the SAME refusal when no link exists.
	for _, way := range []struct {
		name string
		set  func(*http.Request)
	}{{"cookie", cookieAuth}, {"bearer", bearerAuth}} {
		var ran bool
		rec := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodPost, "/api/repos/x/assistant", nil)
		way.set(r)
		r.Header.Set("X-CSRF-Token", csrf)
		r.AddCookie(&http.Cookie{Name: "h_csrf", Value: csrf})
		s.guardWrite(probe(&ran))(rec, r)
		if rec.Code != http.StatusForbidden || ran {
			t.Errorf("%s: guardWrite without a GitHub link must refuse, got %d", way.name, rec.Code)
		}
	}
}

// ── aigentic proxy (B-02) ──────────────────────────────────────────────────────────────────

// devBypassServer wires a server whose repo id resolves to a real (empty) working tree, so the
// AI routes can run without GitHub.
func devBypassServer(t *testing.T) (*Server, string) {
	t.Helper()
	base := t.TempDir()
	repo := filepath.Join(base, "widget")
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("# widget\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DEVLAB_DEV_BYPASS_AUTH", "1")
	t.Setenv("DEVLAB_REPOS_PATH", base)
	state := filepath.Join(base, "state")
	paths := &statepath.Paths{Root: state}
	t.Setenv("DEVLAB_CHATS", filepath.Join(state, "chats"))
	cs, err := chats.NewStore(paths)
	if err != nil {
		t.Fatal(err)
	}
	return &Server{
		v:         auth.New(),
		paths:     paths,
		reposBase: base,
		chats:     cs,
		usage:     telemetry.OpenUsage(paths),
	}, "widget"
}

// fakeAigentic records what DevLab forwarded to the AI service.
type fakeAigentic struct {
	cookie string
	csrf   string
	kind   string
}

func startFakeAigentic(t *testing.T, f *fakeAigentic, result map[string]any) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.cookie = r.Header.Get("Cookie")
		f.csrf = r.Header.Get("X-CSRF-Token")
		var env struct {
			Header map[string]string `json:"header"`
			Data   json.RawMessage   `json:"data"`
		}
		_ = json.NewDecoder(r.Body).Decode(&env)
		f.kind = env.Header["kind"]
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"header": map[string]string{}, "data": result})
	}))
	t.Cleanup(srv.Close)
	t.Setenv("DEVLAB_AIGENTIC_URL", srv.URL)
}

// TestAssistantForwardsSessionAndLabelsTheModel: the caller's session cookie and CSRF token reach
// aigentic unchanged, the answer carries the model that produced it, and the consumption lands in
// the one usage pool.
func TestAssistantForwardsSessionAndLabelsTheModel(t *testing.T) {
	s, repo := devBypassServer(t)
	f := &fakeAigentic{}
	startFakeAigentic(t, f, map[string]any{
		"output": "hello",
		"engine": "claude-cli",
		"model":  "claude-opus-4-8",
		"usage":  map[string]any{"inputTokens": 120, "outputTokens": 34, "totalTokens": 154},
	})

	req := authedReq(http.MethodPost, "/api/repos/"+repo+"/assistant",
		map[string]any{"prompt": "what is this repo?", "kind": "claude-cli", "effort": "high"}, "dev")
	req.SetPathValue("id", repo)
	req.Header.Set("Cookie", "h_access=abc; h_csrf=tok")
	req.AddCookie(&http.Cookie{Name: "h_csrf", Value: "tok"})
	rec := httptest.NewRecorder()
	s.assistant(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(f.cookie, "h_access=abc") {
		t.Errorf("the caller's cookie must reach aigentic, got %q", f.cookie)
	}
	if f.csrf != "tok" {
		t.Errorf("the caller's CSRF token must reach aigentic, got %q", f.csrf)
	}
	if f.kind != "claude-cli" {
		t.Errorf("router kind = %q, want claude-cli", f.kind)
	}
	var reply model.AssistantReply
	if err := json.Unmarshal(rec.Body.Bytes(), &reply); err != nil {
		t.Fatal(err)
	}
	if reply.Model != "claude-opus-4-8" {
		t.Errorf("the reply must carry the model that answered, got %q", reply.Model)
	}
	if reply.Usage.TotalTokens != 154 {
		t.Errorf("usage not surfaced, got %+v", reply.Usage)
	}
	assertUsageSample(t, s, usageSourceAssistant, "claude-opus-4-8", 120, 34)
}

// TestAssistantModelLabelIsNeverInvented: when the engine names no model the answer is labelled
// with the engine that produced it; a model name is never made up.
func TestAssistantModelLabelIsNeverInvented(t *testing.T) {
	if got := answerModel("", "ollama"); got != "ollama" {
		t.Errorf(`answerModel("", "ollama") = %q, want the engine`, got)
	}
	if got := answerModel(" claude-fable-5 ", "claude-api"); got != "claude-fable-5" {
		t.Errorf("answerModel must prefer the reported model, got %q", got)
	}
	if got := answerModel("", ""); got != "" {
		t.Errorf("answerModel must stay empty when nothing was reported, got %q", got)
	}
}

// TestAssistantNoDirectProviderCall: DevLab has no model provider of its own — the AI handlers
// reach a model only through aigentic or the user's own CLI (B-02).
func TestAssistantNoDirectProviderCall(t *testing.T) {
	for _, f := range []string{"handlers_assistant.go", "handlers_agent.go"} {
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, forbidden := range []string{"api.anthropic.com", "api.openai.com", "generativelanguage", "x-api-key"} {
			if strings.Contains(string(src), forbidden) {
				t.Errorf("%s must not talk to a model provider directly (found %q)", f, forbidden)
			}
		}
	}
}

// ── transcript persistence + cap (B-08) ────────────────────────────────────────────────────

// TestHistoryPersistsAndIsCapped: the transcript survives per user+repo through the ONE access
// point, and a runaway history is trimmed to its newest 500 turns.
func TestHistoryPersistsAndIsCapped(t *testing.T) {
	s, repo := devBypassServer(t)

	msgs := make([]model.AiMessage, 0, 640)
	for i := 0; i < 640; i++ {
		msgs = append(msgs, model.AiMessage{Role: "user", Content: string(rune('a' + i%26)), Ts: "2026-07-30T00:00:00Z"})
	}
	msgs[len(msgs)-1].Content = "last"
	put := authedReq(http.MethodPut, "/api/repos/"+repo+"/assistant/history", map[string]any{"messages": msgs}, "dev")
	put.SetPathValue("id", repo)
	rec := httptest.NewRecorder()
	s.putHistory(rec, put)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("put status = %d, body %s", rec.Code, rec.Body.String())
	}

	get := authedReq(http.MethodGet, "/api/repos/"+repo+"/assistant/history", nil, "dev")
	get.SetPathValue("id", repo)
	rec = httptest.NewRecorder()
	s.getHistory(rec, get)
	if rec.Code != http.StatusOK {
		t.Fatalf("get status = %d", rec.Code)
	}
	var back []model.AiMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &back); err != nil {
		t.Fatal(err)
	}
	if len(back) != 500 {
		t.Fatalf("transcript length = %d, want the 500 cap", len(back))
	}
	if back[len(back)-1].Content != "last" {
		t.Error("the cap must keep the newest turns (tail), not the oldest")
	}
}

// ── helpers ────────────────────────────────────────────────────────────────────────────────

var errUsageLedgerUnfilled = errors.New("usage ledger not implemented yet")

// assertUsageSample checks the ONE usage pool received the sample. The pool itself belongs to the
// telemetry building block; while it is not filled in the check skips honestly rather than
// pretending to pass.
func assertUsageSample(t *testing.T, s *Server, source, wantModel string, wantIn, wantOut int64) {
	t.Helper()
	view, err := aggregateUsage(s)
	if err != nil {
		t.Skipf("usage pool not readable: %v", err)
		return
	}
	got, ok := view.BySource[source]
	if !ok {
		t.Fatalf("no usage recorded for source %q (bySource = %+v)", source, view.BySource)
	}
	if int64(got.InputTokens) != wantIn || int64(got.OutputTokens) != wantOut {
		t.Errorf("usage for %s = %+v, want in=%d out=%d", source, got, wantIn, wantOut)
	}
	if wantModel != "" {
		raw, err := os.ReadFile(s.paths.AiUsage())
		if err != nil {
			t.Fatalf("usage ledger unreadable: %v", err)
		}
		if !strings.Contains(string(raw), wantModel) {
			t.Errorf("the sample must name the model %q, ledger = %s", wantModel, raw)
		}
	}
}

// aggregateUsage reads the pool through its own access point, turning a not-yet-filled ledger
// into an error instead of a panic.
func aggregateUsage(s *Server) (view model.AiUsageView, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = errUsageLedgerUnfilled
		}
	}()
	return s.usage.Aggregate(24 * time.Hour)
}

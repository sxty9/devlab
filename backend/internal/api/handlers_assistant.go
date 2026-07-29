package api

import (
	"log"
	"net/http"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	"devlab/backend/internal/aigentic"
	"devlab/backend/internal/git"
	"devlab/backend/internal/model"
	"devlab/backend/internal/telemetry"
	"devlab/backend/internal/workspace"
)

const (
	ctxPerFile    = 24 << 10  // 24 KiB per context file
	ctxRepoBudget = 256 << 10 // 256 KiB repo snapshot (aigentic's claude-cli materializes these to a
	// temp workdir the CLI reads via Read/Glob/Grep; other engines trim to their own budget)
)

var (
	effortAllowed = map[string]bool{"low": true, "medium": true, "high": true, "xhigh": true, "max": true}
	kindAllowed   = map[string]bool{"choose": true, "claude-cli": true, "claude-api": true, "ollama": true}
)

// assistant proxies one AI turn to aigentic with the open repo as context. It forwards the
// caller's session cookie so aigentic bills the user's own key and enforces its own rights.
func (s *Server) assistant(w http.ResponseWriter, r *http.Request) {
	wt, ok := s.repoPath(w, r)
	if !ok {
		return
	}
	var body struct {
		Prompt       string            `json:"prompt"`
		ContextPaths []string          `json:"contextPaths"`
		History      []model.AiMessage `json:"history"`
		Kind         string            `json:"kind"`
		Model        string            `json:"model"`
		Effort       string            `json:"effort"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if !requirePrompt(w, body.Prompt) {
		return
	}
	kind := body.Kind
	if !kindAllowed[kind] {
		kind = "choose"
	}

	repo := r.PathValue("id")
	prompt := buildPrompt(repo, body.History, body.Prompt)
	inline := collectRepoContext(wt, body.ContextPaths)

	var claude *aigentic.ClaudeOpts
	if effortAllowed[body.Effort] {
		claude = &aigentic.ClaudeOpts{Effort: body.Effort}
	}

	csrf := ""
	if c, err := r.Cookie(csrfCookie); err == nil {
		csrf = c.Value
	}
	// Interactive: let the model answer with a structured multiple-choice question the user can click
	// (surfaced on result.Ask, rendered as options in the KI panel bubble).
	req := aigentic.Request{Prompt: prompt, Inline: inline, OutputFormat: "markdown", Model: body.Model, Claude: claude, Interactive: true}
	result, status, err := aigentic.Run(r.Context(), r.Header.Get("Cookie"), csrf, kind, req)
	if err != nil {
		if status == http.StatusForbidden {
			writeErr(w, http.StatusForbidden, "AI not enabled: grant hp_aigentic_run and link your Claude key/subscription in aigentic")
			return
		}
		if status == http.StatusServiceUnavailable {
			writeErr(w, http.StatusServiceUnavailable, "AI engine unavailable — link an Anthropic key or Claude subscription in aigentic")
			return
		}
		writeErr(w, http.StatusBadGateway, "AI request failed")
		return
	}
	reply := model.AssistantReply{Output: result.Output, Engine: result.Engine, Model: answerModel(result.Model, result.Engine), Ask: result.Ask}
	reply.Usage.InputTokens = result.Usage.InputTokens
	reply.Usage.OutputTokens = result.Usage.OutputTokens
	reply.Usage.TotalTokens = result.Usage.TotalTokens
	reply.Usage.Truncated = result.Usage.Truncated
	// Both AI paths report their consumption to the ONE usage pool (cross-cutting 5): no AI call
	// of this service happens past the central accounting.
	s.recordAiUsage(telemetry.UsageSample{
		Source: usageSourceAssistant,
		User:   usernameOf(r),
		Repo:   repo,
		Model:  reply.Model,
		In:     int64(result.Usage.InputTokens),
		Out:    int64(result.Usage.OutputTokens),
	})
	writeJSON(w, http.StatusOK, reply)
}

// The AI sources of this service, named once — the `source` vocabulary of the usage pool.
const (
	usageSourceAssistant = "assistant"
	usageSourceAgent     = "agent"
)

// recordAiUsage appends one sample to the ONE AI-usage pool. Reporting is fail-soft: a ledger
// that cannot be written must never cost the user their answer — telemetry reports, it neither
// judges nor gates.
func (s *Server) recordAiUsage(sample telemetry.UsageSample) {
	if s.usage == nil {
		return
	}
	if sample.At.IsZero() {
		sample.At = time.Now()
	}
	if err := s.usage.Record(sample); err != nil {
		log.Printf("devlabd: AI usage sample (%s) not recorded: %v", sample.Source, err)
	}
}

// answerModel is the label an AI answer carries (labeling duty, D 26): the model the engine
// reported, or — when it named none — the engine that answered. It never invents a model name;
// an answer nobody labelled stays unlabelled.
func answerModel(model, engine string) string {
	if m := strings.TrimSpace(model); m != "" {
		return m
	}
	return strings.TrimSpace(engine)
}

// usernameOf is the caller's username, or "" when no guard resolved one.
func usernameOf(r *http.Request) string {
	if u := userFrom(r); u != nil {
		return u.Username
	}
	return ""
}

// buildPrompt folds a repo-aware preamble + the prior transcript into aigentic's single prompt
// (aigentic has no system-prompt field and is stateless).
func buildPrompt(repo string, history []model.AiMessage, prompt string) string {
	var b strings.Builder
	b.WriteString("You are an AI assistant embedded in DevLab, an in-browser IDE, helping with the GitHub repository \"")
	b.WriteString(repo)
	b.WriteString("\". The repository's files are attached as context (REPO_FILES.txt lists every path; ")
	b.WriteString("text files are attached by content). Reason about THIS repository from the attached ")
	b.WriteString("context — do not assume access to any other filesystem. Answer concisely in ")
	b.WriteString("GitHub-flavoured Markdown.\n\n")
	for _, m := range history {
		label := "User"
		if m.Role == "assistant" {
			label = "Assistant"
		}
		b.WriteString(label)
		b.WriteString(": ")
		b.WriteString(aiMsgText(m))
		b.WriteString("\n\n")
	}
	b.WriteString("User: ")
	b.WriteString(prompt)
	b.WriteString("\n\nAssistant:")
	return b.String()
}

// aiMsgText is a history turn's text for the re-sent transcript. An assistant turn that was ONLY a
// structured question (no prose) still needs words so the model keeps context; fall back to the
// question text.
func aiMsgText(m model.AiMessage) string {
	if m.Content != "" || m.Ask == nil {
		return m.Content
	}
	qs := make([]string, 0, len(m.Ask.Questions))
	for _, q := range m.Ask.Questions {
		qs = append(qs, q.Question)
	}
	return strings.Join(qs, "\n")
}

// collectRepoContext packs the open repo as context: a REPO_FILES.txt listing every path, then the
// priority paths (the open file) followed by the rest of the repo's text files, up to the budget.
// For aigentic's claude-cli engine these are materialized to a temp workdir the CLI reads via its
// tools; for other engines aigentic trims them to its own budget. All paths are SafePath-confined.
func collectRepoContext(wt string, priority []string) []aigentic.InlineFile {
	files := git.ListFiles(wt)
	out := []aigentic.InlineFile{{Path: "REPO_FILES.txt", Content: strings.Join(files, "\n"), MediaType: "text/plain"}}
	used := 0
	seen := map[string]bool{}
	add := func(rel string) {
		if rel == "" || seen[rel] || used >= ctxRepoBudget {
			return
		}
		abs, err := workspace.SafePath(wt, rel)
		if err != nil {
			return
		}
		info, err := os.Stat(abs)
		if err != nil || info.IsDir() {
			return
		}
		b, err := os.ReadFile(abs)
		if err != nil || !utf8.Valid(b) {
			return // text context only
		}
		if len(b) > ctxPerFile {
			b = b[:ctxPerFile]
		}
		if used+len(b) > ctxRepoBudget {
			return
		}
		used += len(b)
		seen[rel] = true
		out = append(out, aigentic.InlineFile{Path: rel, Content: string(b), MediaType: "text/plain"})
	}
	for _, p := range priority {
		add(p)
	}
	for _, p := range files {
		add(p)
	}
	return out
}

// assistantModels proxies aigentic's model catalog (per engine) with the caller's session, so the
// KI tab can offer the same engine/model choices as aigentic itself.
func (s *Server) assistantModels(w http.ResponseWriter, r *http.Request) {
	raw, status, err := aigentic.Get(r.Context(), r.Header.Get("Cookie"), "models")
	if err != nil {
		if status == http.StatusForbidden {
			writeErr(w, http.StatusForbidden, "AI not enabled")
			return
		}
		writeErr(w, http.StatusBadGateway, "Could not load models")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(raw)
}

// getHistory / putHistory persist the repo-scoped transcript per user.
func (s *Server) getHistory(w http.ResponseWriter, r *http.Request) {
	if s.chats == nil {
		writeJSON(w, http.StatusOK, []model.AiMessage{})
		return
	}
	if _, ok := s.repoPath(w, r); !ok {
		return
	}
	u := userFrom(r)
	msgs, err := s.chats.Get(u.Username, r.PathValue("id"))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "Could not read history")
		return
	}
	writeJSON(w, http.StatusOK, msgs)
}

func (s *Server) putHistory(w http.ResponseWriter, r *http.Request) {
	if s.chats == nil {
		writeErr(w, http.StatusServiceUnavailable, "History is unavailable")
		return
	}
	if _, ok := s.repoPath(w, r); !ok {
		return
	}
	u := userFrom(r)
	var body struct {
		Messages []model.AiMessage `json:"messages"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if err := s.chats.Put(u.Username, r.PathValue("id"), body.Messages); err != nil {
		writeErr(w, http.StatusInternalServerError, "Could not save history")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

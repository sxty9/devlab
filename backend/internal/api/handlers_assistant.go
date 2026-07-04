package api

import (
	"net/http"
	"os"
	"strings"
	"unicode/utf8"

	"devlab/backend/internal/aigentic"
	"devlab/backend/internal/model"
	"devlab/backend/internal/workspace"
)

const (
	ctxPerFile = 16 << 10 // 16 KiB per context file
	ctxBudget  = 48 << 10 // 48 KiB total (under aigentic's ~64 KiB context budget)
)

var effortAllowed = map[string]bool{"low": true, "medium": true, "high": true, "xhigh": true, "max": true}

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
		Effort       string            `json:"effort"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if strings.TrimSpace(body.Prompt) == "" {
		writeErr(w, http.StatusBadRequest, "Empty prompt")
		return
	}

	repo := r.PathValue("id")
	prompt := buildPrompt(repo, body.History, body.Prompt)
	inline := collectContext(wt, body.ContextPaths)

	var claude *aigentic.ClaudeOpts
	if effortAllowed[body.Effort] {
		claude = &aigentic.ClaudeOpts{Effort: body.Effort}
	}

	csrf := ""
	if c, err := r.Cookie(csrfCookie); err == nil {
		csrf = c.Value
	}
	req := aigentic.Request{Prompt: prompt, Inline: inline, OutputFormat: "markdown", Claude: claude}
	result, status, err := aigentic.Run(r.Context(), r.Header.Get("Cookie"), csrf, "choose", req)
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
	reply := model.AssistantReply{Output: result.Output, Engine: result.Engine, Model: result.Model}
	reply.Usage.InputTokens = result.Usage.InputTokens
	reply.Usage.OutputTokens = result.Usage.OutputTokens
	reply.Usage.TotalTokens = result.Usage.TotalTokens
	reply.Usage.Truncated = result.Usage.Truncated
	writeJSON(w, http.StatusOK, reply)
}

// buildPrompt folds a repo-aware preamble + the prior transcript into aigentic's single prompt
// (aigentic has no system-prompt field and is stateless).
func buildPrompt(repo string, history []model.AiMessage, prompt string) string {
	var b strings.Builder
	b.WriteString("You are an AI assistant embedded in DevLab, an in-browser IDE, helping with the GitHub repository \"")
	b.WriteString(repo)
	b.WriteString("\". Answer concisely in GitHub-flavoured Markdown. Use the attached repository files as context when relevant.\n\n")
	for _, m := range history {
		label := "User"
		if m.Role == "assistant" {
			label = "Assistant"
		}
		b.WriteString(label)
		b.WriteString(": ")
		b.WriteString(m.Content)
		b.WriteString("\n\n")
	}
	b.WriteString("User: ")
	b.WriteString(prompt)
	b.WriteString("\n\nAssistant:")
	return b.String()
}

// collectContext reads the requested repo files (text only), trimmed to the aigentic budget, and
// packs them as inline context. Paths are workspace-confined and symlink-guarded via SafePath.
func collectContext(wt string, paths []string) []aigentic.InlineFile {
	var out []aigentic.InlineFile
	used := 0
	for _, rel := range paths {
		if used >= ctxBudget {
			break
		}
		abs, err := workspace.SafePath(wt, rel)
		if err != nil {
			continue
		}
		b, err := os.ReadFile(abs)
		if err != nil || !utf8.Valid(b) {
			continue // text context only
		}
		if len(b) > ctxPerFile {
			b = b[:ctxPerFile]
		}
		if used+len(b) > ctxBudget {
			b = b[:ctxBudget-used]
		}
		used += len(b)
		out = append(out, aigentic.InlineFile{Path: rel, Content: string(b), MediaType: "text/plain"})
	}
	return out
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

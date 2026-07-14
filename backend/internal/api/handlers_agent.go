package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"devlab/backend/internal/model"
	"devlab/backend/internal/workspace"
)

// agentTimeout bounds a single agentic run — claude editing a workspace can take minutes.
const agentTimeout = 8 * time.Minute

// agentModeMap maps the KI tab's mode to the claude CLI --permission-mode. Plan is read-only
// (produces a plan, no edits); Auto auto-accepts file edits; Full is autonomous incl. Bash — all
// bounded to the user's own OS rights (the run executes AS them, via Phase C's per-user isolation).
var agentModeMap = map[string]string{
	"plan": "plan",
	"auto": "acceptEdits",
	"full": "bypassPermissions",
}

// agent runs the FULL claude CLI as the user inside their repo workspace: it reads and edits the
// real files with the user's own ~/.claude config, rights and subscription. This is the agentic
// counterpart to /assistant (which proxies a read-only turn to aigentic). Because it can modify the
// tree, it returns the refreshed change set so edits show in the VCS panel immediately.
func (s *Server) agent(w http.ResponseWriter, r *http.Request) {
	wc, unlock, ok := s.mutateCtx(w, r, false)
	if !ok {
		return
	}
	defer unlock()

	var body struct {
		Prompt string `json:"prompt"`
		Model  string `json:"model"`
		Effort string `json:"effort"`
		Mode   string `json:"mode"`
		Resume string `json:"resume"` // prior session id, for multi-turn continuity
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if strings.TrimSpace(body.Prompt) == "" {
		writeErr(w, http.StatusBadRequest, "Empty prompt")
		return
	}
	mode := agentModeMap[body.Mode]
	if mode == "" {
		mode = "acceptEdits"
	}

	args := []string{"-p", body.Prompt, "--output-format", "json", "--permission-mode", mode}
	if body.Model != "" {
		args = append(args, "--model", body.Model)
	}
	if effortAllowed[body.Effort] {
		args = append(args, "--effort", body.Effort)
	}
	if body.Resume != "" {
		args = append(args, "--resume", body.Resume)
	}
	args = append(args, "--append-system-prompt", agentPreamble(r.PathValue("id")))

	ctx, cancel := context.WithTimeout(r.Context(), agentTimeout)
	defer cancel()
	out, err := wc.exec.Agent(ctx, wc.wt, args...)
	if err != nil {
		writeErr(w, http.StatusBadGateway, agentError(err))
		return
	}

	reply := parseClaudeResult(out)
	reply.Changes = workspace.Changes(wc.wt)
	writeJSON(w, http.StatusOK, reply)
}

// agentPreamble is appended to claude's system prompt so it knows it is DevLab's in-repo agent.
func agentPreamble(repo string) string {
	return "You are the AI agent inside DevLab, an in-browser IDE. You are working in the checked-out " +
		"workspace of the GitHub repository \"" + repo + "\" as the signed-in developer, with their own " +
		"tools and rights. Make focused, well-scoped changes and clearly summarise what you did."
}

// parseClaudeResult extracts the fields DevLab surfaces from the claude CLI's --output-format json.
func parseClaudeResult(out []byte) model.AgentReply {
	var raw struct {
		Result    string  `json:"result"`
		SessionID string  `json:"session_id"`
		IsError   bool    `json:"is_error"`
		TotalCost float64 `json:"total_cost_usd"`
		NumTurns  int     `json:"num_turns"`
	}
	_ = json.Unmarshal(out, &raw)
	output := strings.TrimSpace(raw.Result)
	if output == "" {
		output = "_(the agent produced no textual output)_"
	}
	return model.AgentReply{
		Output:    output,
		SessionID: raw.SessionID,
		CostUSD:   raw.TotalCost,
		NumTurns:  raw.NumTurns,
		IsError:   raw.IsError,
	}
}

// agentError turns a claude CLI failure into a clear, actionable message for the KI tab.
func agentError(err error) string {
	s := err.Error()
	low := strings.ToLower(s)
	switch {
	case strings.Contains(low, "not installed"):
		return "The Claude CLI isn't installed for your account on this server."
	case strings.Contains(low, "api key"), strings.Contains(low, "not authenticated"),
		strings.Contains(low, "unauthorized"), strings.Contains(low, "please run") && strings.Contains(low, "login"):
		return "Claude isn't signed in for your account — run `claude` once in the Terminal tab to authenticate."
	default:
		if len(s) > 300 {
			s = s[:300] + "…"
		}
		return "Agent run failed: " + s
	}
}

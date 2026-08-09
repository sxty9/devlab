// The MCP TOOL TABLE (REQ-043) — the machine-readable parity list between what the UI can do
// (src/data/source.ts, the DataSource surface) and what an agent can do.
//
// Every row names the DataSource operation it mirrors and the route from the frozen route table
// it rides. A tool does NOT re-implement its capability: it hands the arguments to the SAME
// handler the HTTP route uses, so an entity keeps exactly one access point and no second data
// path exists. The guard tier is re-checked per tool (hp_devlab_access always; CSRF on the
// cookie path; a linked GitHub account where the route requires one), which is what makes every
// exposed capability rights-covered.
//
// Naming: <subject>_<verb>, subject singular from the shared vocabulary (repo, file, change,
// commit, branch, pr, vision, comment, assistant, agent, atlas, port, axiom, category, run,
// execution, slot, delivery, notice, attachment, report, calendar, config, storage, telemetry,
// ai_usage). Verbs: list/get/read/create/update/delete/set plus the domain verbs of the surface.
//
// Two exception lists keep the parity statement complete and honest: DataSource operations that
// deliberately have no tool, and routes that deliberately have no tool — each with its reason.
package api

import (
	"encoding/json"
	"net/http"

	"devlab/backend/internal/mcp"
	"devlab/backend/internal/model"
	"devlab/backend/internal/sched"
)

// mcpTier mirrors the guard a route carries. Ordered by strictness: every tool needs the DevLab
// right (tierRead), mutating tools additionally need CSRF on the cookie path (tierCSRF), and
// those touching a user's own repository need a linked GitHub account (tierWrite).
type mcpTier int

const (
	tierRead mcpTier = iota
	tierCSRF
	tierWrite
)

func (t mcpTier) String() string {
	switch t {
	case tierCSRF:
		return "csrf"
	case tierWrite:
		return "write"
	default:
		return "read"
	}
}

// mcpLoc says where an argument travels: into the path, the query string, or the JSON body.
type mcpLoc int

const (
	inPath mcpLoc = iota
	inQuery
	inBody
)

// mcpParam is one tool argument and its place on the wire.
type mcpParam struct {
	Name     string // the argument an agent passes
	Wire     string // path placeholder / query key / body field ("" ⇒ same as Name)
	Kind     mcp.Kind
	Items    mcp.Kind
	Enum     []string
	In       mcpLoc
	Required bool
	Desc     string
}

func (p mcpParam) wire() string {
	if p.Wire != "" {
		return p.Wire
	}
	return p.Name
}

// mcpTool is one row of the table.
type mcpTool struct {
	Name string
	Desc string
	Op   string // the DataSource operation this mirrors ("" ⇒ Note says why there is none)
	Note string
	// The HTTP twin from the frozen route table (ARCHITEKTUR §7) and the handler behind it.
	Method  string
	Path    string
	Tier    mcpTier
	Handler func(*Server, http.ResponseWriter, *http.Request)
	// Destructive marks a capability that removes or overwrites state (REQ-040.6): the client
	// sees the hint before it calls.
	Destructive bool
	Params      []mcpParam
}

func (t mcpTool) readOnly() bool { return t.Method == http.MethodGet }

// ── argument constructors ─────────────────────────────────────────────────────────────────

// pathArg is a path segment: always a required string.
func pathArg(name, wire, desc string) mcpParam {
	return mcpParam{Name: name, Wire: wire, Kind: mcp.KindString, In: inPath, Required: true, Desc: desc}
}

func queryArg(name, wire string, kind mcp.Kind, required bool, desc string) mcpParam {
	return mcpParam{Name: name, Wire: wire, Kind: kind, In: inQuery, Required: required, Desc: desc}
}

func queryEnum(name, wire string, enum []string, desc string) mcpParam {
	return mcpParam{Name: name, Wire: wire, Kind: mcp.KindString, Enum: enum, In: inQuery, Desc: desc}
}

func bodyArg(name string, kind mcp.Kind, required bool, desc string) mcpParam {
	return mcpParam{Name: name, Kind: kind, In: inBody, Required: required, Desc: desc}
}

func bodyList(name string, items mcp.Kind, required bool, desc string) mcpParam {
	return mcpParam{Name: name, Kind: mcp.KindArray, Items: items, In: inBody, Required: required, Desc: desc}
}

func bodyEnum(name string, enum []string, required bool, desc string) mcpParam {
	return mcpParam{Name: name, Kind: mcp.KindString, Enum: enum, In: inBody, Required: required, Desc: desc}
}

// Argument vocabularies that must not drift from the domain constants.
var (
	runKinds        = []string{string(model.KindTodo), string(model.KindAuto)}
	placementKinds  = []string{sched.PlacementQueue, sched.PlacementDefer, sched.PlacementOverload}
	proposalModes   = []string{"fill", "replace"}
	proposalActions = []string{"request", "read", "cancel"}
	chatRoles       = []string{"user", "assistant"}
)

// proposalActionArg is the argument both AI planning tools carry. An analysis is never waited for,
// so asking for one, looking at it and putting it aside are three different acts — and a caller
// that could only ask would keep starting model passes while never learning why the last one
// failed. Optional, because the plain call IS the request.
func proposalActionArg(what string) mcpParam {
	return bodyEnum("action", proposalActions, false,
		"request starts "+what+" when none exists and otherwise reports the existing one unchanged; "+
			"read reports the state without ever starting a model pass; cancel puts a running, finished or failed analysis aside. "+
			"Starting over after a failure is cancel, then request. Default: request.")
}

const repoIDDesc = "Repository id."
const runIDDesc = "Run id (a ToDo or an automatic run)."

// mcpToolRows is THE table. Rows follow the DataSource surface in its own order so both sides
// can be read side by side.
func mcpToolRows() []mcpTool {
	return []mcpTool{
		// ── identity ──────────────────────────────────────────────────────────────────────
		{
			Name: "user_get", Desc: "Read the calling identity: display name, DevLab access and GitHub-link state.",
			Op: "getUser", Method: http.MethodGet, Path: "/api/user", Tier: tierRead,
			Handler: (*Server).user,
		},

		// ── repositories & files ──────────────────────────────────────────────────────────
		{
			Name: "repo_list", Desc: "List the repositories the caller may work on.",
			Op: "repos", Method: http.MethodGet, Path: "/api/repos", Tier: tierRead,
			Handler: (*Server).repos,
		},
		{
			Name: "repo_get", Desc: "Read one repository: file tree, changes, branches and tracking state.",
			Op: "repoData", Method: http.MethodGet, Path: "/api/repos/{id}", Tier: tierRead,
			Handler: (*Server).repoData,
			Params: []mcpParam{
				pathArg("id", "", repoIDDesc),
				queryArg("branch", "", mcp.KindString, false, "Branch to read; omitted reads the checked-out one."),
			},
		},
		{
			Name: "file_read", Desc: "Read one file from the repository workspace.",
			Op: "fileContent", Method: http.MethodGet, Path: "/api/repos/{id}/file", Tier: tierRead,
			Handler: (*Server).file,
			Params: []mcpParam{
				pathArg("id", "", repoIDDesc),
				queryArg("path", "", mcp.KindString, true, "Repository-relative file path."),
			},
		},
		{
			Name: "file_diff", Desc: "Read one file's working-tree diff as before/after text.",
			Op: "fileDiff", Method: http.MethodGet, Path: "/api/repos/{id}/diff", Tier: tierRead,
			Handler: (*Server).diff,
			Params: []mcpParam{
				pathArg("id", "", repoIDDesc),
				queryArg("path", "", mcp.KindString, true, "Repository-relative file path."),
			},
		},
		{
			Name: "github_unlink", Desc: "Remove the caller's GitHub link.",
			Op: "unlinkGitHub", Method: http.MethodPost, Path: "/api/github/unlink", Tier: tierWrite,
			Handler: (*Server).githubUnlink, Destructive: true,
		},
		{
			Name: "repo_ensure", Desc: "Prepare the caller's workspace for a repository (clone if absent). Idempotent.",
			Op: "ensureRepo", Method: http.MethodPost, Path: "/api/repos/{id}/ensure", Tier: tierWrite,
			Handler: (*Server).gitEnsure,
			Params:  []mcpParam{pathArg("id", "", repoIDDesc)},
		},
		{
			Name: "file_write", Desc: "Write file content in the workspace and answer the refreshed change set.",
			Op: "saveFile", Method: http.MethodPost, Path: "/api/repos/{id}/file", Tier: tierWrite,
			Handler: (*Server).gitWriteFile,
			Params: []mcpParam{
				pathArg("id", "", repoIDDesc),
				bodyArg("path", mcp.KindString, true, "Repository-relative file path."),
				bodyArg("content", mcp.KindString, true, "Full new file content."),
			},
		},
		{
			Name: "change_stage", Desc: "Stage one changed path for the next commit.",
			Op: "stage", Method: http.MethodPost, Path: "/api/repos/{id}/stage", Tier: tierWrite,
			Handler: (*Server).gitStage,
			Params: []mcpParam{
				pathArg("id", "", repoIDDesc),
				bodyArg("path", mcp.KindString, true, "Repository-relative path to stage."),
			},
		},
		{
			Name: "change_unstage", Desc: "Unstage one path again.",
			Op: "unstage", Method: http.MethodPost, Path: "/api/repos/{id}/unstage", Tier: tierWrite,
			Handler: (*Server).gitUnstage,
			Params: []mcpParam{
				pathArg("id", "", repoIDDesc),
				bodyArg("path", mcp.KindString, true, "Repository-relative path to unstage."),
			},
		},
		{
			Name: "commit_create", Desc: "Commit the staged changes on the current branch.",
			Op: "commit", Method: http.MethodPost, Path: "/api/repos/{id}/commit", Tier: tierWrite,
			Handler: (*Server).gitCommit,
			Params: []mcpParam{
				pathArg("id", "", repoIDDesc),
				bodyArg("message", mcp.KindString, true, "Commit message."),
			},
		},
		{
			Name: "repo_push", Desc: "Push the current branch to its remote.",
			Op: "push", Method: http.MethodPost, Path: "/api/repos/{id}/push", Tier: tierWrite,
			Handler: (*Server).gitPush,
			Params:  []mcpParam{pathArg("id", "", repoIDDesc)},
		},
		{
			Name: "repo_pull", Desc: "Pull the current branch from its remote.",
			Op: "pull", Method: http.MethodPost, Path: "/api/repos/{id}/pull", Tier: tierWrite,
			Handler: (*Server).gitPull,
			Params:  []mcpParam{pathArg("id", "", repoIDDesc)},
		},
		{
			Name: "branch_create", Desc: "Create a branch and switch to it.",
			Op: "createBranch", Method: http.MethodPost, Path: "/api/repos/{id}/branch", Tier: tierWrite,
			Handler: (*Server).gitBranch,
			Params: []mcpParam{
				pathArg("id", "", repoIDDesc),
				bodyArg("name", mcp.KindString, true, "New branch name."),
				bodyArg("from", mcp.KindString, false, "Branch to start from; omitted uses the current one."),
			},
		},
		{
			Name: "branch_checkout", Desc: "Switch the workspace to an existing branch.",
			Op: "checkout", Method: http.MethodPost, Path: "/api/repos/{id}/checkout", Tier: tierWrite,
			Handler: (*Server).gitCheckout,
			Params: []mcpParam{
				pathArg("id", "", repoIDDesc),
				bodyArg("name", mcp.KindString, true, "Branch to check out."),
			},
		},
		{
			Name: "pr_open", Desc: "Push the current branch and open (or adopt) its pull request into the default branch.",
			Op: "openPR", Method: http.MethodPost, Path: "/api/repos/{id}/pr", Tier: tierWrite,
			Handler: (*Server).openPR,
			Params: []mcpParam{
				pathArg("id", "", repoIDDesc),
				bodyArg("title", mcp.KindString, false, "Pull-request title; omitted uses the branch name."),
				bodyArg("body", mcp.KindString, false, "Pull-request description."),
			},
		},

		// ── vision catalog & comments ─────────────────────────────────────────────────────
		{
			Name: "vision_list", Desc: "List the repository's vision assets.",
			Op: "vision", Method: http.MethodGet, Path: "/api/repos/{id}/vision", Tier: tierRead,
			Handler: (*Server).visionList,
			Params:  []mcpParam{pathArg("id", "", repoIDDesc)},
		},
		{
			Name: "vision_read", Desc: "Read one vision asset's bytes (image, document or text).",
			Op: "rawUrl", Method: http.MethodGet, Path: "/api/repos/{id}/raw", Tier: tierRead,
			Handler: (*Server).visionRaw,
			Params: []mcpParam{
				pathArg("id", "", repoIDDesc),
				queryArg("path", "", mcp.KindString, true, "Repository-relative path inside the vision catalog."),
			},
		},
		{
			Name: "vision_upload", Desc: "Upload a base64 asset into the vision catalog and answer the refreshed listing.",
			Op: "uploadVision", Method: http.MethodPost, Path: "/api/repos/{id}/vision/upload", Tier: tierWrite,
			Handler: (*Server).visionUpload,
			Params: []mcpParam{
				pathArg("id", "", repoIDDesc),
				bodyArg("path", mcp.KindString, true, "Target path inside the vision catalog."),
				bodyArg("contentB64", mcp.KindString, true, "File content, base64-encoded."),
			},
		},
		{
			Name: "vision_delete", Desc: "Delete one vision asset and answer the refreshed listing.",
			Op: "deleteVision", Method: http.MethodPost, Path: "/api/repos/{id}/vision/delete", Tier: tierWrite,
			Handler: (*Server).visionDelete, Destructive: true,
			Params: []mcpParam{
				pathArg("id", "", repoIDDesc),
				bodyArg("path", mcp.KindString, true, "Path inside the vision catalog."),
			},
		},
		{
			Name: "comment_list", Desc: "List the comment threads on one file.",
			Op: "listComments", Method: http.MethodGet, Path: "/api/repos/{id}/comments", Tier: tierRead,
			Handler: (*Server).commentsList,
			Params: []mcpParam{
				pathArg("id", "", repoIDDesc),
				queryArg("path", "", mcp.KindString, true, "Repository-relative path the thread hangs on."),
			},
		},
		{
			Name: "comment_create", Desc: "Add a comment to a file, optionally as a reply.",
			Op: "addComment", Method: http.MethodPost, Path: "/api/repos/{id}/comments", Tier: tierWrite,
			Handler: (*Server).commentAdd,
			Params: []mcpParam{
				pathArg("id", "", repoIDDesc),
				bodyArg("path", mcp.KindString, true, "Repository-relative path the comment belongs to."),
				bodyArg("body", mcp.KindString, true, "Comment text."),
				bodyArg("parentId", mcp.KindString, false, "Comment to reply to; omitted starts a thread."),
			},
		},
		{
			Name: "comment_delete", Desc: "Delete one comment the caller is allowed to remove.",
			Op: "deleteComment", Method: http.MethodDelete, Path: "/api/repos/{id}/comments/{cid}", Tier: tierWrite,
			Handler: (*Server).commentDelete, Destructive: true,
			Params: []mcpParam{
				pathArg("id", "", repoIDDesc),
				pathArg("commentId", "cid", "Comment id."),
			},
		},

		// ── AI assistant & agent ──────────────────────────────────────────────────────────
		{
			Name: "assistant_ask", Desc: "Ask one read-only AI turn about the repository. The answer names the model that produced it.",
			Op: "askAssistant", Method: http.MethodPost, Path: "/api/repos/{id}/assistant", Tier: tierWrite,
			Handler: (*Server).assistant,
			Params: []mcpParam{
				pathArg("id", "", repoIDDesc),
				bodyArg("prompt", mcp.KindString, true, "The question."),
				bodyList("contextPaths", mcp.KindString, false, "Repository-relative files to put in context."),
				bodyList("history", mcp.KindObject, false, "Prior turns as {role, content} messages."),
				bodyArg("kind", mcp.KindString, false, "Engine choice; omitted lets the service choose."),
				bodyArg("model", mcp.KindString, false, "Model id from assistant_model_list."),
				bodyArg("effort", mcp.KindString, false, "Reasoning effort the engine supports."),
			},
		},
		{
			Name: "agent_ask", Desc: "Run an agentic AI turn that may edit the workspace; answers the refreshed change set.",
			Op: "askAgent", Method: http.MethodPost, Path: "/api/repos/{id}/agent", Tier: tierWrite,
			Handler: (*Server).agent,
			Params: []mcpParam{
				pathArg("id", "", repoIDDesc),
				bodyArg("prompt", mcp.KindString, true, "The task."),
				bodyArg("model", mcp.KindString, false, "Model id from assistant_model_list."),
				bodyArg("effort", mcp.KindString, false, "Reasoning effort the engine supports."),
				bodyArg("mode", mcp.KindString, false, "How far the agent may act on its own."),
				bodyArg("resume", mcp.KindString, false, "Prior session id, to continue that conversation."),
			},
		},
		{
			Name: "assistant_history_get", Desc: "Read the stored assistant conversation for a repository.",
			Op: "getAssistantHistory", Method: http.MethodGet, Path: "/api/repos/{id}/assistant/history", Tier: tierRead,
			Handler: (*Server).getHistory,
			Params:  []mcpParam{pathArg("id", "", repoIDDesc)},
		},
		{
			Name: "assistant_history_set", Desc: "Replace the stored assistant conversation for a repository.",
			Op: "saveAssistantHistory", Method: http.MethodPut, Path: "/api/repos/{id}/assistant/history", Tier: tierWrite,
			Handler: (*Server).putHistory, Destructive: true,
			Params: []mcpParam{
				pathArg("id", "", repoIDDesc),
				bodyList("messages", mcp.KindObject, true, "The full conversation as {role, content} messages."),
			},
		},
		{
			Name: "assistant_model_list", Desc: "List the AI models and engines available to the caller.",
			Op: "assistantModels", Method: http.MethodGet, Path: "/api/assistant/models", Tier: tierRead,
			Handler: (*Server).assistantModels,
		},

		// ── landscape ─────────────────────────────────────────────────────────────────────
		{
			Name: "atlas_get", Desc: "Read the service landscape graph.",
			Op: "atlas", Method: http.MethodGet, Path: "/api/atlas", Tier: tierRead,
			Handler: (*Server).atlasGraph,
		},
		{
			Name: "port_list", Desc: "Read the port ledger derived from the observed host state, including double bookings.",
			Op: "atlasPorts", Method: http.MethodGet, Path: "/api/atlas/ports", Tier: tierRead,
			Handler: (*Server).atlasPorts,
		},

		// ── constitution ──────────────────────────────────────────────────────────────────
		{
			Name: "axiom_tree", Desc: "Read the constitution tree: categories with their records.",
			Op: "mercuryTree", Method: http.MethodGet, Path: "/api/mercury/tree", Tier: tierRead,
			Handler: (*Server).mercuryTree,
		},
		{
			Name: "axiom_get", Desc: "Read one constitution record with its authorship.",
			Op: "mercuryItem", Method: http.MethodGet, Path: "/api/mercury/item", Tier: tierRead,
			Handler: (*Server).mercuryItem,
			Params:  []mcpParam{queryArg("path", "", mcp.KindString, true, "Record path in the constitution tree.")},
		},
		{
			Name: "axiom_create", Desc: "Add a constitution record. Answers a duplicate or a conformance objection instead of writing when one applies.",
			Op: "mercuryAddAxiom", Method: http.MethodPost, Path: "/api/mercury/axiom", Tier: tierCSRF,
			Handler: (*Server).addAxiom,
			Params: []mcpParam{
				bodyArg("titel", mcp.KindString, true, "Record title."),
				bodyArg("body", mcp.KindString, true, "Record text."),
				bodyArg("section", mcp.KindString, false, "Target category; omitted lets the service classify."),
				bodyArg("force", mcp.KindBoolean, false, "Write even though a similar record exists."),
			},
		},
		{
			Name: "axiom_optimize", Desc: "Answer a sharpened wording for a record draft without writing it.",
			Op: "mercuryOptimize", Method: http.MethodPost, Path: "/api/mercury/optimize", Tier: tierCSRF,
			Handler: (*Server).optimizeRecord,
			Params: []mcpParam{
				bodyArg("titel", mcp.KindString, true, "Draft title."),
				bodyArg("body", mcp.KindString, true, "Draft text."),
				bodyArg("section", mcp.KindString, false, "Category the draft is meant for."),
			},
		},
		{
			Name: "axiom_conform", Desc: "Check a record draft against the constitution's own form rules.",
			Op: "mercuryConform", Method: http.MethodPost, Path: "/api/mercury/conform", Tier: tierCSRF,
			Handler: (*Server).conformRecord,
			Params: []mcpParam{
				bodyArg("titel", mcp.KindString, true, "Draft title."),
				bodyArg("body", mcp.KindString, true, "Draft text."),
			},
		},
		{
			Name: "axiom_update", Desc: "Replace one constitution record's title and text.",
			Op: "mercuryEditAxiom", Method: http.MethodPut, Path: "/api/mercury/axiom", Tier: tierCSRF,
			Handler: (*Server).editAxiom,
			Params: []mcpParam{
				bodyArg("path", mcp.KindString, true, "Record path to edit."),
				bodyArg("titel", mcp.KindString, true, "New title."),
				bodyArg("body", mcp.KindString, true, "New text."),
			},
		},
		{
			Name: "axiom_move", Desc: "Move one constitution record to another category.",
			Op: "mercuryMoveAxiom", Method: http.MethodPost, Path: "/api/mercury/move", Tier: tierCSRF,
			Handler: (*Server).moveAxiom,
			Params: []mcpParam{
				bodyArg("from", mcp.KindString, true, "Current record path."),
				bodyArg("to", mcp.KindString, true, "Target record path."),
			},
		},
		{
			Name: "axiom_delete", Desc: "Delete one constitution record.",
			Op: "mercuryDeleteAxiom", Method: http.MethodDelete, Path: "/api/mercury/axiom", Tier: tierCSRF,
			Handler: (*Server).deleteAxiom, Destructive: true,
			Params: []mcpParam{queryArg("path", "", mcp.KindString, true, "Record path to delete.")},
		},
		{
			Name: "category_move", Desc: "Move a whole constitution category, records included.",
			Op: "mercuryMoveCategory", Method: http.MethodPost, Path: "/api/mercury/move-category", Tier: tierCSRF,
			Handler: (*Server).moveCategory,
			Params: []mcpParam{
				bodyArg("from", mcp.KindString, true, "Current category path."),
				bodyArg("to", mcp.KindString, true, "Target category path."),
			},
		},
		{
			Name: "category_reorder", Desc: "Set the display order of the records inside one category.",
			Op: "mercuryReorder", Method: http.MethodPost, Path: "/api/mercury/reorder", Tier: tierCSRF,
			Handler: (*Server).mercuryReorder,
			Params: []mcpParam{
				bodyArg("category", mcp.KindString, true, "Category path."),
				bodyList("order", mcp.KindString, true, "Record ids in the wanted order."),
			},
		},
		{
			Name: "mercury_chat", Desc: "Talk to the planning assistant about the constitution and the run plan; it answers with proposed actions.",
			Op: "mercuryChat", Method: http.MethodPost, Path: "/api/mercury/chat", Tier: tierCSRF,
			Handler: (*Server).mercuryChat,
			Params:  []mcpParam{bodyList("messages", mcp.KindObject, true, "The conversation as {role, content} messages; role is one of: "+joinEnum(chatRoles)+".")},
		},

		// ── runs (ToDos and automatic runs) ───────────────────────────────────────────────
		{
			Name: "run_list", Desc: "List all runs with the axiom titles they reference.",
			Op: "mercuryRuns", Method: http.MethodGet, Path: "/api/mercury/runs", Tier: tierRead,
			Handler: (*Server).runsList,
		},
		{
			Name: "run_get", Desc: "Read one run with the axioms it references.",
			Op: "mercuryRun", Method: http.MethodGet, Path: "/api/mercury/runs/{id}", Tier: tierRead,
			Handler: (*Server).runGet,
			Params:  []mcpParam{pathArg("id", "", runIDDesc)},
		},
		{
			Name: "run_prompt_get", Desc: "Read the prompt a run would execute with, as it stands now.",
			Op: "mercuryRunPrompt", Method: http.MethodGet, Path: "/api/mercury/runs/{id}/prompt", Tier: tierRead,
			Handler: (*Server).runPromptPreview,
			Params:  []mcpParam{pathArg("id", "", runIDDesc)},
		},
		{
			Name: "run_coverage_get", Desc: "Read which constitution records are covered by a run and which are not.",
			Op: "mercuryRunCoverage", Method: http.MethodGet, Path: "/api/mercury/runs/coverage", Tier: tierRead,
			Handler: (*Server).runsCoverage,
		},
		{
			Name: "notice_list", Desc: "List the standing notices the service raised about itself.",
			Op: "mercuryRunNotices", Method: http.MethodGet, Path: "/api/mercury/runs/notices", Tier: tierRead,
			Handler: (*Server).runsNoticesList,
		},
		{
			Name: "notice_dismiss", Desc: "Mark one notice as read.",
			Op: "mercuryDismissRunNotice", Method: http.MethodPost, Path: "/api/mercury/runs/notices/dismiss", Tier: tierCSRF,
			Handler: (*Server).runsNoticeDismiss,
			Params:  []mcpParam{bodyArg("id", mcp.KindString, true, "Notice id.")},
		},
		{
			Name: "notice_clear", Desc: "Mark every notice as read.",
			Op: "mercuryClearRunNotices", Method: http.MethodPost, Path: "/api/mercury/runs/notices/clear", Tier: tierCSRF,
			Handler: (*Server).runsNoticesClear, Destructive: true,
		},
		{
			Name: "run_create", Desc: "Create a run: a one-off ToDo or a scheduled automatic run.",
			Op: "mercuryCreateRun", Method: http.MethodPost, Path: "/api/mercury/runs", Tier: tierCSRF,
			Handler: (*Server).runCreate,
			Params:  runInputParams(true),
		},
		{
			Name: "run_update", Desc: "Replace one run's definition. Omitted fields are cleared — send the whole document.",
			Op: "mercuryUpdateRun", Method: http.MethodPut, Path: "/api/mercury/runs/{id}", Tier: tierCSRF,
			Handler: (*Server).runUpdate,
			Params:  append([]mcpParam{pathArg("id", "", runIDDesc)}, runInputParams(true)...),
		},
		{
			Name: "run_delete", Desc: "Delete one run. Its execution results stay readable.",
			Op: "mercuryDeleteRun", Method: http.MethodDelete, Path: "/api/mercury/runs/{id}", Tier: tierCSRF,
			Handler: (*Server).runDelete, Destructive: true,
			Params: []mcpParam{pathArg("id", "", runIDDesc)},
		},
		{
			Name: "run_assign", Desc: "Assign every constitution record that no run carries yet: a fitting run is extended, otherwise one is created. Answers with how many were uncovered and whether a pass started; the outcome arrives as a notice.",
			Op: "mercuryRunAssign", Method: http.MethodPost, Path: "/api/mercury/runs/assign", Tier: tierCSRF,
			Handler: (*Server).runsAssign,
		},
		{
			Name: "run_ai_fill", Desc: "Propose automatic runs for the constitution records that have none. Proposes only; nothing is written. The analysis is not waited for: every call answers with its state — running, completed with the proposal, or failed with the reason.",
			Op: "mercuryRunAiFill", Method: http.MethodPost, Path: "/api/mercury/runs/ai-fill", Tier: tierCSRF,
			Handler: (*Server).runsAiFill,
			Params:  []mcpParam{proposalActionArg("an analysis of the uncovered records")},
		},
		{
			Name: "run_ai_finetune", Desc: "Propose a reshaped run plan for the whole constitution. Proposes only; nothing is written. The analysis is not waited for: every call answers with its state — running, completed with the proposal, or failed with the reason.",
			Op: "mercuryRunAiFinetune", Method: http.MethodPost, Path: "/api/mercury/runs/ai-finetune", Tier: tierCSRF,
			Handler: (*Server).runsAiFinetune,
			Params:  []mcpParam{proposalActionArg("a regrouping analysis of the whole run plan")},
		},
		{
			Name: "run_proposal_apply", Desc: "Apply a proposed run plan: fill the gaps, or replace the automatic runs entirely.",
			Op: "mercuryApplyRunProposal", Method: http.MethodPost, Path: "/api/mercury/runs/apply-proposal", Tier: tierCSRF,
			Handler: (*Server).runsApplyProposal, Destructive: true,
			Params: []mcpParam{
				bodyEnum("mode", proposalModes, true, "fill keeps existing runs; replace discards them."),
				bodyArg("plan", mcp.KindObject, true, "The proposal as answered by run_ai_fill or run_ai_finetune."),
			},
		},
		{
			Name: "run_snapshot_list", Desc: "List the snapshots of the run plan, newest first.",
			Op: "mercuryRunHistory", Method: http.MethodGet, Path: "/api/mercury/runs/history", Tier: tierRead,
			Handler: (*Server).runsHistoryList,
		},
		{
			Name: "run_snapshot_restore", Desc: "Restore the run plan from one snapshot. The current plan is snapshotted first.",
			Op: "mercuryRestoreRunHistory", Method: http.MethodPost, Path: "/api/mercury/runs/history/restore", Tier: tierCSRF,
			Handler: (*Server).runsHistoryRestore, Destructive: true,
			Params: []mcpParam{bodyArg("ts", mcp.KindString, true, "Snapshot timestamp from run_snapshot_list.")},
		},
		{
			Name: "run_result_list", Desc: "List one run's execution result documents, newest first.",
			Op: "mercuryRunResults", Method: http.MethodGet, Path: "/api/mercury/runs/{id}/results", Tier: tierRead,
			Handler: (*Server).runResultsList,
			Params:  []mcpParam{pathArg("id", "", runIDDesc)},
		},
		{
			Name: "run_result_get", Desc: "Read one execution result document with its per-repository stage array.",
			Op: "mercuryRunResult", Method: http.MethodGet, Path: "/api/mercury/runs/{id}/results/{rid}", Tier: tierRead,
			Handler: (*Server).runResultGet,
			Params: []mcpParam{
				pathArg("id", "", runIDDesc),
				pathArg("resultId", "rid", "Result document id."),
			},
		},
		{
			Name: "calendar_get", Desc: "Read the due dates and executions on the calendar.",
			Op: "mercuryRunCalendar", Method: http.MethodGet, Path: "/api/mercury/runs/calendar", Tier: tierRead,
			Handler: (*Server).runsCalendar,
			Params: []mcpParam{
				queryArg("days", "", mcp.KindInteger, false, "How many days ahead to include."),
				queryEnum("kind", "type", runKinds, "Narrow to one kind of run."),
			},
		},
		{
			Name: "execution_list", Desc: "List stored executions (the history), newest first.",
			Op: "mercuryRunExecutions", Method: http.MethodGet, Path: "/api/mercury/runs/executions", Tier: tierRead,
			Handler: (*Server).runsExecutions,
			Params:  []mcpParam{queryEnum("kind", "type", runKinds, "Narrow to one kind of run.")},
		},
		{
			Name: "report_status_get", Desc: "Read which daily reports were sent, to whom, and what failed.",
			Op: "mercuryReportStatus", Method: http.MethodGet, Path: "/api/mercury/runs/report-status", Tier: tierRead,
			Handler: (*Server).runsReportStatus,
		},
		{
			Name: "report_resume", Desc: "Resume a blocked daily report so it is attempted again. A blocked report waits for this: it never resumes itself.",
			Op: "mercuryResumeReportDelivery", Method: http.MethodPost, Path: "/api/mercury/runs/report-status", Tier: tierCSRF,
			Handler: (*Server).runsReportStatus,
			Params: []mcpParam{
				bodyArg("day", mcp.KindString, false, "The day (YYYY-MM-DD) to resume; omit to resume every blocked day."),
			},
		},

		// ── execution & slots ─────────────────────────────────────────────────────────────
		{
			Name: "run_start", Desc: "Start or resume a run. The answer is the honest outcome: started, queued, resumed, or a named reason.",
			Op: "mercuryRunNow", Method: http.MethodPost, Path: "/api/mercury/runs/{id}/run", Tier: tierCSRF,
			Handler: (*Server).runNow,
			Params: []mcpParam{
				pathArg("id", "", runIDDesc),
				bodyArg("fresh", mcp.KindBoolean, false, "Discard a paused or blocked execution and start it over instead of resuming what is left. It does NOT touch the 'already delivered' determination: a todo whose work already merged is re-run when — and only when — its text grows to demand new work, which the determination decides on its own; fresh neither forces nor suppresses that."),
				bodyArg("placement", mcp.KindObject, false,
					"What to do when no slot is free: {kind, deferExecutionId} with kind one of: "+joinEnum(placementKinds)+"."),
			},
		},
		{
			Name: "execution_active_list", Desc: "List the executions running right now, plus the restart state.",
			Op: "mercuryRunActive", Method: http.MethodGet, Path: "/api/mercury/runs/active", Tier: tierRead,
			Handler: (*Server).runActive,
		},
		{
			Name: "slot_overview_get", Desc: "Read the execution slots: capacity, occupancy, deferred work and queued starts.",
			Op: "mercurySlots", Method: http.MethodGet, Path: "/api/mercury/runs/slots", Tier: tierRead,
			Handler: (*Server).runSlots,
		},
		{
			Name: "run_cancel", Desc: "Abort one run's live execution. Its transcript and result stay.",
			Op: "mercuryCancelRun", Method: http.MethodPost, Path: "/api/mercury/runs/{id}/cancel", Tier: tierCSRF,
			Handler: (*Server).runCancel, Destructive: true,
			Params: []mcpParam{pathArg("id", "", runIDDesc)},
		},
		{
			Name: "run_defer", Desc: "Free the slot but keep the progress: the execution pauses and can be resumed.",
			Op: "mercuryDeferRun", Method: http.MethodPost, Path: "/api/mercury/runs/{id}/defer", Tier: tierCSRF,
			Handler: (*Server).runDefer,
			Params:  []mcpParam{pathArg("id", "", runIDDesc)},
		},
		{
			Name: "run_resume", Desc: "Resume a deferred or blocked execution where it stopped.",
			Op: "mercuryResumeRun", Method: http.MethodPost, Path: "/api/mercury/runs/{id}/resume", Tier: tierCSRF,
			Handler: (*Server).runResume,
			Params:  []mcpParam{pathArg("id", "", runIDDesc)},
		},

		// ── deliveries ────────────────────────────────────────────────────────────────────
		{
			Name: "delivery_list", Desc: "List the deliveries: what was delivered where, in which state.",
			Op: "mercuryDeliveries", Method: http.MethodGet, Path: "/api/mercury/runs/deliveries", Tier: tierRead,
			Handler: (*Server).runDeliveriesList,
		},
		{
			Name: "delivery_rollback", Desc: "Reverse one delivery by counter-committing it. History is never rewritten; a conflict becomes a ToDo.",
			Op: "mercuryRollbackDelivery", Method: http.MethodPost, Path: "/api/mercury/runs/deliveries/{id}/rollback", Tier: tierCSRF,
			Handler: (*Server).runDeliveryRollback, Destructive: true,
			Params: []mcpParam{pathArg("deliveryId", "id", "Delivery id from delivery_list.")},
		},
		{
			Name: "delivery_resume", Desc: "Release the blockade of a blocked pull request so the maintenance evaluates it again. Merges nothing; safe to repeat. Without arguments it releases every blocked one.",
			Op: "mercuryResumeDelivery", Method: http.MethodPost, Path: "/api/mercury/runs/deliveries/resume", Tier: tierCSRF,
			Handler: (*Server).runPRResume,
			Params: []mcpParam{
				bodyArg("repo", mcp.KindString, false, "owner/name of ONE blocked pull request; omit to release all."),
				bodyArg("number", mcp.KindNumber, false, "Its pull request number; required together with repo."),
			},
		},
		{
			Name: "repo_reset", Desc: "Discard the development branch of one repository and start it again from the default branch.",
			Op: "mercuryRepoReset", Method: http.MethodPost, Path: "/api/mercury/runs/reset", Tier: tierCSRF,
			Handler: (*Server).runRepoReset, Destructive: true,
			Params: []mcpParam{bodyArg("repo", mcp.KindString, true, "Repository to reset.")},
		},

		// ── ToDo media ────────────────────────────────────────────────────────────────────
		{
			Name: "attachment_upload", Desc: "Attach a base64 file to a ToDo and answer the refreshed attachment list.",
			Op: "mercuryUploadAttachment", Method: http.MethodPost, Path: "/api/mercury/runs/{id}/attachments", Tier: tierCSRF,
			Handler: (*Server).runAttachmentUpload,
			Params: []mcpParam{
				pathArg("id", "", runIDDesc),
				bodyArg("filename", mcp.KindString, true, "File name to store it under."),
				bodyArg("contentB64", mcp.KindString, true, "File content, base64-encoded."),
			},
		},
		{
			Name: "attachment_delete", Desc: "Remove one attachment from a ToDo and answer the refreshed list.",
			Op: "mercuryDeleteAttachment", Method: http.MethodDelete, Path: "/api/mercury/runs/{id}/attachments/{aid}", Tier: tierCSRF,
			Handler: (*Server).runAttachmentDelete, Destructive: true,
			Params: []mcpParam{
				pathArg("id", "", runIDDesc),
				pathArg("attachmentId", "aid", "Attachment id."),
			},
		},
		{
			Name: "attachment_read", Desc: "Read one ToDo attachment's bytes.",
			Op: "mercuryAttachmentRawUrl", Method: http.MethodGet, Path: "/api/mercury/runs/{id}/attachments/{aid}/raw", Tier: tierRead,
			Handler: (*Server).runAttachmentRaw,
			Params: []mcpParam{
				pathArg("id", "", runIDDesc),
				pathArg("attachmentId", "aid", "Attachment id."),
			},
		},

		// ── service interfaces ────────────────────────────────────────────────────────────
		{
			Name: "config_get", Desc: "Read the service configuration.",
			Op: "serviceConfig", Method: http.MethodGet, Path: "/api/service/config", Tier: tierRead,
			Handler: (*Server).serviceConfigGet,
		},
		{
			Name: "config_set", Desc: "Replace the service configuration. It takes effect at once — a smaller capacity lets running executions finish, a larger one starts what waits.",
			Op: "", Note: "The UI deliberately does not write configuration: it is maintained centrally, " +
				"so this tool is the named caller of the configuration-write route.",
			Method: http.MethodPut, Path: "/api/service/config", Tier: tierCSRF,
			Handler: (*Server).serviceConfigPut, Destructive: true,
			Params: []mcpParam{
				bodyArg("maxConcurrency", mcp.KindInteger, true, "How many executions may run at once."),
				bodyArg("defaultTimeBudget", mcp.KindString, true, "Default time budget per execution, as a duration (for example 3h)."),
				bodyArg("automergeWindow", mcp.KindString, true, "How long a delivery may wait for automatic merge, as a duration."),
			},
		},
		{
			Name: "storage_get", Desc: "Read how much data the service holds, per pool.",
			Op: "serviceStorage", Method: http.MethodGet, Path: "/api/service/storage", Tier: tierRead,
			Handler: (*Server).serviceStorage,
		},
		{
			Name: "telemetry_get", Desc: "Read the service's own resource use.",
			Op: "serviceTelemetry", Method: http.MethodGet, Path: "/api/service/telemetry", Tier: tierRead,
			Handler: (*Server).serviceTelemetry,
		},
		{
			Name: "ai_usage_get", Desc: "Read the aggregated AI token and cost use.",
			Op: "serviceAiUsage", Method: http.MethodGet, Path: "/api/service/ai-usage", Tier: tierRead,
			Handler: (*Server).serviceAiUsage,
		},
	}
}

// runInputParams is the run document both run_create and run_update carry — declared once so
// the two rows cannot drift apart.
func runInputParams(titleRequired bool) []mcpParam {
	return []mcpParam{
		bodyEnum("kind", runKinds, false, "todo is a one-off task, auto a scheduled run."),
		bodyArg("title", mcp.KindString, titleRequired, "What the run is called."),
		bodyArg("task", mcp.KindString, false, "The task text (ToDos)."),
		bodyList("axiomIds", mcp.KindString, false, "Constitution records this run enforces (automatic runs)."),
		bodyArg("schedule", mcp.KindObject, false, "When it runs, as answered by run_get."),
		bodyArg("active", mcp.KindBoolean, false, "Whether the schedule is armed."),
		bodyList("targets", mcp.KindObject, false, "Target repositories as {repo} or {newRepo} entries."),
		bodyArg("dueAt", mcp.KindString, false, "Due date as an RFC 3339 timestamp."),
		bodyArg("tuning", mcp.KindObject, false, "Per-run time budget and model settings, as answered by run_get."),
	}
}

func joinEnum(values []string) string {
	out := ""
	for i, v := range values {
		if i > 0 {
			out += ", "
		}
		out += v
	}
	return out
}

// ── the machine-readable projection (REQ-043 parity list) ─────────────────────────────────

// MCPToolInfo is one row of the tool table as data: what the tool is, which DataSource
// operation it mirrors, which route it rides, and what it expects.
type MCPToolInfo struct {
	Name         string          `json:"name"`
	Description  string          `json:"description"`
	DataSourceOp string          `json:"dataSourceOp,omitempty"`
	Note         string          `json:"note,omitempty"`
	Method       string          `json:"method"`
	Route        string          `json:"route"`
	Tier         string          `json:"tier"`
	ReadOnly     bool            `json:"readOnly"`
	Destructive  bool            `json:"destructive,omitempty"`
	InputSchema  json.RawMessage `json:"inputSchema"`
}

// MCPException is one deliberately uncovered surface and the reason it stays uncovered.
type MCPException struct {
	Subject string `json:"subject"`
	Reason  string `json:"reason"`
}

// MCPParity is the whole parity statement in one document: the tools plus both exception lists.
// The frontend parity test reads the generated projection of this; the backend surface test
// reads it directly.
type MCPParity struct {
	ProtocolVersion      string         `json:"protocolVersion"`
	Tools                []MCPToolInfo  `json:"tools"`
	OmittedDataSourceOps []MCPException `json:"omittedDataSourceOps"`
	UntooledRoutes       []MCPException `json:"untooledRoutes"`
}

// MCPToolTable returns the table as data, sorted by name.
func MCPToolTable() []MCPToolInfo {
	rows := mcpToolRows()
	out := make([]MCPToolInfo, 0, len(rows))
	for _, t := range rows {
		out = append(out, MCPToolInfo{
			Name: t.Name, Description: t.Desc, DataSourceOp: t.Op, Note: t.Note,
			Method: t.Method, Route: t.Method + " " + t.Path, Tier: t.Tier.String(),
			ReadOnly: t.readOnly(), Destructive: t.Destructive, InputSchema: mcpSchema(t),
		})
	}
	sortToolInfos(out)
	return out
}

// MCPParityList returns the complete parity statement.
func MCPParityList() MCPParity {
	return MCPParity{
		ProtocolVersion:      mcp.ProtocolVersion,
		Tools:                MCPToolTable(),
		OmittedDataSourceOps: mcpOmittedOps,
		UntooledRoutes:       mcpUntooledRoutes,
	}
}

// mcpOmittedOps are the DataSource operations that deliberately have no tool. They are not
// capabilities: they build an address the browser navigates to, they open a stream, or they are
// cookie-session plumbing an agent does not have.
var mcpOmittedOps = []MCPException{
	{
		Subject: "init",
		Reason:  "Bootstrap probe of the web surface (liveness plus identity); its substance is user_get.",
	},
	{
		Subject: "refreshSession",
		Reason:  "Re-mints the browser's short-lived cookie session; an agent authenticates per request with its own bearer token, so it has no session to refresh.",
	},
	{
		Subject: "githubAuthorizeUrl",
		Reason:  "Builds the address that starts the browser's GitHub consent flow — an interactive redirect, not a request/response capability.",
	},
	{
		Subject: "terminalUrl",
		Reason:  "Builds the WebSocket address of a shell session — a bidirectional stream with no request/response shape.",
	},
	{
		Subject: "events",
		Reason:  "Opens the one live-update stream; MCP carries its own notification channel, so a second stream would be a parallel path.",
	},
	{
		Subject: "mercuryRunQuestions",
		Reason:  "The Blocked surface is a human-decision list; a run's open question already surfaces as its execution's block reason, which the execution tools show.",
	},
	{
		Subject: "mercuryAnswerRunQuestion",
		Reason:  "Answering a run's blocking question is the user's decision by definition — an agent answering its own question would defeat the whole mechanism.",
	},
	{
		Subject: "mercuryDeclineRunQuestion",
		Reason:  "Rejecting a run's blocking question is the user's decision by definition — the co-equal 'no' to answering it; an agent declining its own question would defeat the whole mechanism.",
	},
}

// mcpUntooledRoutes are the routes of the frozen route table that deliberately carry no tool.
var mcpUntooledRoutes = []MCPException{
	{Subject: "GET /api/health", Reason: "Public liveness probe, deliberately minimal; not a user capability."},
	{Subject: "POST /api/auth/refresh", Reason: "Cookie-session plumbing of the browser surface; an MCP caller authenticates per call."},
	{Subject: "GET /api/github/authorize", Reason: "Starts the browser's GitHub consent flow."},
	{Subject: "GET /api/github/callback", Reason: "Receives the browser's GitHub consent redirect."},
	{Subject: "GET /api/repos/{id}/pty", Reason: "Bidirectional terminal stream (WebSocket)."},
	{Subject: "GET /api/events", Reason: "The one live-update stream (server-sent events)."},
	{Subject: "POST /api/mcp", Reason: "The MCP endpoint itself — a tool for it would be a loop."},
	{Subject: "GET /api/mercury/runs/questions", Reason: "The Blocked surface is a human-decision list; a run's open question already surfaces as its execution's block reason, so the agent tools show it there."},
	{Subject: "POST /api/mercury/runs/questions/answer", Reason: "Answering a run's blocking question is the user's decision by definition — an agent answering its own question would defeat the whole mechanism."},
	{Subject: "/", Reason: "Delivery of the web surface."},
}

func sortToolInfos(list []MCPToolInfo) {
	for i := 1; i < len(list); i++ {
		for j := i; j > 0 && list[j].Name < list[j-1].Name; j-- {
			list[j], list[j-1] = list[j-1], list[j]
		}
	}
}

// mcpSchema renders one row's arguments as its JSON Schema — the ONE derivation, so a tool's
// schema can never disagree with what it actually accepts.
func mcpSchema(t mcpTool) json.RawMessage {
	props := make([]mcp.Property, 0, len(t.Params))
	for _, p := range t.Params {
		props = append(props, mcp.Property{
			Name: p.Name, Kind: p.Kind, Items: p.Items, Enum: p.Enum,
			Required: p.Required, Description: p.Desc,
		})
	}
	return mcp.ObjectSchema(props)
}

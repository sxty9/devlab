import type { AgentAsk, AgentReply, AiMessage, AiModelCatalog, AssistantAsk, AssistantReply, AtlasGraph, Axiom, Branch, Change, Comment, Conformance, FileContent, MercuryTree, MetaViolation, PullRequestResult, Repo, RepoData, Run, RunActive, RunAttachment, RunCalendar, RunChatMessage, RunChatReply, RunCoverage, RunExecution, RunInput, RunList, RunPlan, RunProposal, RunResult, RunResultRef, RunSnapshotMeta, RunType, User, VisionFile } from '@/types';

export interface DiffPayload {
  before: string;
  after: string;
  lang: string;
}

/** Refreshed change set returned by working-tree mutations (write/stage/unstage). */
export interface WriteResult {
  changes: Change[];
}

/** Returned by a successful commit. */
export interface CommitResult {
  hash: string;
  branch: string;
  changes: Change[];
}

/** Returned by push/pull — refreshed tracking + the raw git message. */
export interface PushResult {
  branch: string;
  ahead: number;
  behind: number;
  message: string;
  branches: Branch[];
}

/** Returned after creating/switching a branch. */
export interface BranchResult {
  branch: string;
  branches: Branch[];
}

export interface InitResult {
  /** 'api' when the Go backend is reachable; 'mock' when offline (dev fallback). */
  mode: 'api' | 'mock';
  /** true when a valid Holistic session is present on this origin (the SSO cookie). */
  signedIn: boolean;
  /** true when the signed-in user holds hp_devlab_access (or is admin). */
  canUseDevlab: boolean;
  /** true when the user has linked their GitHub account (mandatory before the workspace loads). */
  githubLinked: boolean;
}

/** The single seam between the UI and its data. mockSource serves bundled mock data;
 *  httpSource talks to the devlabd /api. The UI components are agnostic to which is active. */
export interface DataSource {
  init(): Promise<InitResult>;
  getUser(): Promise<User>;
  repos(): Promise<Repo[]>;
  repoData(id: string, branch?: string): Promise<RepoData>;
  fileContent(id: string, path: string): Promise<FileContent>;
  fileDiff(id: string, path: string): Promise<DiffPayload>;

  /** The backend path the GitHub-link button navigates to (begins the OAuth flow). */
  githubAuthorizeUrl(): string;
  /** Remove the GitHub link (POST, CSRF-guarded). */
  unlinkGitHub(): Promise<void>;

  // ── write loop (all CSRF-guarded; require GitHub push on the repo) ──────────
  /** Clone the repo into the caller's workspace if needed (idempotent). */
  ensureRepo(id: string): Promise<void>;
  saveFile(id: string, path: string, content: string): Promise<WriteResult>;
  stage(id: string, path: string): Promise<WriteResult>;
  unstage(id: string, path: string): Promise<WriteResult>;
  commit(id: string, message: string): Promise<CommitResult>;
  push(id: string): Promise<PushResult>;
  pull(id: string): Promise<PushResult>;
  createBranch(id: string, name: string, from?: string): Promise<BranchResult>;
  checkout(id: string, name: string): Promise<BranchResult>;
  /** Push the current branch and open (or focus) a GitHub PR into the default branch. */
  openPR(id: string, title?: string, body?: string): Promise<PullRequestResult>;

  // ── Vision Catalog + threaded comments ─────────────────────────────────────
  vision(id: string): Promise<VisionFile[]>;
  /** Direct URL for an <img>/<iframe> to a raw vision file (bytes, correct MIME). */
  rawUrl(id: string, path: string): string;
  uploadVision(id: string, path: string, contentB64: string): Promise<VisionFile[]>;
  deleteVision(id: string, path: string): Promise<VisionFile[]>;
  listComments(id: string, path: string): Promise<Comment[]>;
  addComment(id: string, path: string, body: string, parentId?: string): Promise<Comment>;
  deleteComment(id: string, commentId: string): Promise<void>;

  // ── AI assistant (proxied to aigentic, repo as context) ────────────────────
  askAssistant(id: string, ask: AssistantAsk): Promise<AssistantReply>;
  /** Run the FULL claude CLI agentically in the repo workspace, as the user (can edit the tree). */
  askAgent(id: string, ask: AgentAsk): Promise<AgentReply>;
  getAssistantHistory(id: string): Promise<AiMessage[]>;
  saveAssistantHistory(id: string, messages: AiMessage[]): Promise<void>;
  assistantModels(): Promise<AiModelCatalog>;

  // ── Terminal (WebSocket to the remshel-backed shell in the repo workspace) ──
  /** WebSocket URL for a shell in the repo workspace, or null when no live provider (mock/offline). */
  terminalUrl(id: string): string | null;

  // ── Atlas (the deployed Holistic landscape, derived from the host's own config) ──
  atlas(): Promise<AtlasGraph>;

  // ── Mercury (the axiom model over aigentic's scheme graveyard) ──────────────
  mercuryTree(): Promise<MercuryTree>;
  mercuryItem(path: string): Promise<Axiom>;
  /**
   * Add a record to a section (`axiome` | `regeln` | `laeufe`, default `axiome`); aigentic
   * classifies it into that namespace's tree. Returns where it landed.
   */
  /** Add a record. If the classifier finds an existing record with the same content, NOTHING is
   *  written and `duplicate` + `proposed` come back so the user can extend/adjust it or force a new
   *  one via `force`. */
  mercuryAddAxiom(
    titel: string,
    body: string,
    section?: string,
    force?: boolean,
  ): Promise<{
    path?: string;
    id?: string;
    classified?: boolean;
    duplicate?: { path: string; axiom: Axiom };
    proposed?: { titel: string; body: string; path: string };
    // An axiom that violates a meta-axiom is not written; the violations + an AI correction come back.
    nonconform?: { violations: MetaViolation[]; proposed: { titel: string; body: string; path: string } };
  }>;
  /** Polish a record (orthography, generalization, brevity) via aigentic; returns improved text, does not persist. */
  mercuryOptimize(titel: string, body: string, section?: string): Promise<{ titel: string; body: string }>;
  /** Check an axiom strictly against every meta-axiom (the binding requirements an axiom must satisfy). */
  mercuryConform(titel: string, body: string): Promise<Conformance>;
  /** Edit an axiom's title + body in place (id and quelle preserved). */
  mercuryEditAxiom(path: string, titel: string, body: string): Promise<{ path: string; axiom: Axiom }>;
  /** Move or rename an axiom (from/to are full record paths). */
  mercuryMoveAxiom(from: string, to: string): Promise<{ path: string }>;
  /** Delete an axiom. */
  mercuryDeleteAxiom(path: string): Promise<void>;
  /** Rename/re-home a whole category (moves every record under it). */
  mercuryMoveCategory(from: string, to: string): Promise<{ moved: number }>;
  /** Set the manual order of a category's immediate children (full ordered child-name list). */
  mercuryReorder(category: string, order: string[]): Promise<void>;

  // ── Mercury — Automatische Läufe (run instances) ────────────────────────────
  /** List all runs (each with a `stale` flag) plus an axiom id→title legend. */
  mercuryRuns(): Promise<RunList>;
  /** One run + the axiom id→title legend. */
  mercuryRun(id: string): Promise<{ run: Run; axioms: Record<string, string> }>;
  /** Live-compose a run's prompt from the current scheme (does not persist). */
  mercuryRunPrompt(id: string): Promise<{ prompt: string }>;
  /** Which axioms are already covered by a run (+ id→path, id→title). */
  mercuryRunCoverage(): Promise<RunCoverage>;
  mercuryCreateRun(body: RunInput): Promise<Run>;
  mercuryUpdateRun(id: string, body: RunInput): Promise<Run>;
  mercuryDeleteRun(id: string): Promise<void>;
  /** Refresh a run's prompt snapshot from the current scheme. */
  mercuryRecomposeRun(id: string): Promise<void>;
  /** Ask AI to plan the not-yet-covered axioms into runs (reviewable; writes nothing). */
  mercuryRunAiFill(): Promise<RunProposal>;
  /** Ask AI to regroup the current runs (reviewable; writes nothing). */
  mercuryRunAiFinetune(): Promise<RunProposal>;
  /** Persist a reviewed proposal. `fill` creates/extends named runs; `replace` replaces the set. */
  mercuryApplyRunProposal(mode: 'fill' | 'replace', plan: RunPlan): Promise<void>;
  /** Config-change history (each mutation is a snapshot). */
  mercuryRunHistory(): Promise<{ snapshots: RunSnapshotMeta[] }>;
  /** Restore a past run-config snapshot by its timestamp. */
  mercuryRestoreRunHistory(ts: string): Promise<void>;
  /** Execution-result history for a run (persists past deletion). */
  mercuryRunResults(id: string): Promise<{ results: RunResultRef[] }>;
  /** One execution result document. */
  mercuryRunResult(id: string, resultId: string): Promise<RunResult>;
  /** Upcoming occurrences over a window (default 30 days) — the auto-updating Laufkalender.
   *  `type` narrows to automatic runs or ToDos; omitted = both (the global calendar). */
  mercuryRunCalendar(days?: number, type?: RunType): Promise<RunCalendar>;
  /** Completed executions (execution history; includes deleted runs). `type` narrows to automatic
   *  runs or ToDos so each surface shows its own; omitted = the global log. */
  mercuryRunExecutions(type?: RunType): Promise<{ executions: RunExecution[] }>;
  /** The Mercury-WIDE assistant: knows axioms, rules, Laufregeln, runs and ToDos. May return a
   *  reviewable run-plan proposal when asked to create/change runs. */
  mercuryChat(messages: RunChatMessage[]): Promise<RunChatReply>;
  /** Trigger a run immediately (detached). 503 if the executor is unconfigured, 409 if one is running. */
  mercuryRunNow(id: string): Promise<{ started: boolean }>;
  /** EVERY run executing right now (server truth) — read on mount so running runs survive a page reload,
   *  and polled to follow them live. Empty when nothing runs; several at once when runs are concurrent. */
  mercuryRunActive(): Promise<{ active: RunActive[] }>;
  /** Abort ONE run in progress (kill-switch), named by id — other concurrent runs keep going. */
  mercuryCancelRun(id: string): Promise<void>;

  // ── ToDo media (images/documents the agent takes into account) ─────────────
  /** Attach one medium (base64) to a ToDo; returns the ToDo's refreshed attachment list. */
  mercuryUploadAttachment(id: string, filename: string, contentB64: string): Promise<RunAttachment[]>;
  /** Remove one attachment from a ToDo; returns the refreshed list. */
  mercuryDeleteAttachment(id: string, attachmentId: string): Promise<RunAttachment[]>;
  /** Direct URL for an <img>/link to an attachment's raw bytes (correct MIME). */
  mercuryAttachmentRawUrl(id: string, attachmentId: string): string;
}

/** Thrown by httpSource when the backend returns 401 (login required / expired). */
export class AuthRequiredError extends Error {
  constructor() {
    super('Authentication required');
    this.name = 'AuthRequiredError';
  }
}

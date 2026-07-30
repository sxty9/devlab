// DataSource — the ONE seam between the UI and its data (frozen after Welle 0). httpSource
// implements it completely against /api; stubSource serves defined empty states (no behavior
// clone, B-24). Everything the UI can do goes through here — the MCP tool table mirrors this
// surface (REQ-043).
import type {
  AgentAsk,
  AgentReply,
  AiMessage,
  AiModelCatalog,
  AssistantAsk,
  AssistantReply,
  AtlasGraph,
  Axiom,
  AiUsageView,
  Branch,
  Change,
  Comment,
  Conformance,
  Delivery,
  ExecutionView,
  FileContent,
  LoadView,
  MercuryTree,
  MetaViolation,
  PortAllocation,
  PullRequestResult,
  Repo,
  RepoData,
  ReportDelivery,
  RestartState,
  Run,
  RunAttachment,
  RunCalendar,
  RunChatMessage,
  RunChatReply,
  RunCoverage,
  RunInput,
  RunKind,
  RunList,
  RunNotice,
  RunPlan,
  RunProposal,
  RunResult,
  RunSnapshotMeta,
  ServiceConfig,
  SlotOverview,
  StartOutcome,
  StorageView,
  User,
  VisionFile,
} from '@/types';

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
  /** 'api' when the Go backend is reachable; 'stub' when offline (defined empty states). */
  mode: 'api' | 'stub';
  signedIn: boolean;
  canUseDevlab: boolean;
  githubLinked: boolean;
}

/** Slot placement when starting into contended slots (mirror of sched.Placement). */
export interface StartPlacement {
  kind: 'queue' | 'defer' | 'overload';
  deferExecutionId?: string;
}

export interface DataSource {
  init(): Promise<InitResult>;
  /** Re-mints the short-lived access credential, resolving to whether the session is usable again.
   *  The ONE refresh access point: every caller (including the live stream's reconnect, C F5) rides
   *  it instead of opening a second path to the same entity. */
  refreshSession(): Promise<boolean>;
  getUser(): Promise<User>;
  repos(): Promise<Repo[]>;
  repoData(id: string, branch?: string): Promise<RepoData>;
  fileContent(id: string, path: string): Promise<FileContent>;
  fileDiff(id: string, path: string): Promise<DiffPayload>;

  /** The backend path the GitHub-link button navigates to (begins the OAuth flow). */
  githubAuthorizeUrl(): string;
  unlinkGitHub(): Promise<void>;

  // ── write loop (all CSRF-guarded; require GitHub push on the repo) ──────────
  ensureRepo(id: string): Promise<void>;
  saveFile(id: string, path: string, content: string): Promise<WriteResult>;
  stage(id: string, path: string): Promise<WriteResult>;
  unstage(id: string, path: string): Promise<WriteResult>;
  commit(id: string, message: string): Promise<CommitResult>;
  push(id: string): Promise<PushResult>;
  pull(id: string): Promise<PushResult>;
  createBranch(id: string, name: string, from?: string): Promise<BranchResult>;
  checkout(id: string, name: string): Promise<BranchResult>;
  /** Push the current branch and open (or adopt) a PR — served by the ONE PR path (B-1). */
  openPR(id: string, title?: string, body?: string): Promise<PullRequestResult>;

  // ── Vision catalog + threaded comments ─────────────────────────────────────
  vision(id: string): Promise<VisionFile[]>;
  rawUrl(id: string, path: string): string;
  uploadVision(id: string, path: string, contentB64: string): Promise<VisionFile[]>;
  deleteVision(id: string, path: string): Promise<VisionFile[]>;
  listComments(id: string, path: string): Promise<Comment[]>;
  addComment(id: string, path: string, body: string, parentId?: string): Promise<Comment>;
  deleteComment(id: string, commentId: string): Promise<void>;

  // ── AI assistant (proxied to aigentic, repo as context) ────────────────────
  askAssistant(id: string, ask: AssistantAsk): Promise<AssistantReply>;
  askAgent(id: string, ask: AgentAsk): Promise<AgentReply>;
  getAssistantHistory(id: string): Promise<AiMessage[]>;
  saveAssistantHistory(id: string, messages: AiMessage[]): Promise<void>;
  assistantModels(): Promise<AiModelCatalog>;

  // ── Terminal ───────────────────────────────────────────────────────────────
  /** WebSocket URL for a shell in the repo workspace; sessionKey reattaches an existing
   *  session after a reload (S3). null when no live provider (stub). */
  terminalUrl(id: string, sessionKey?: string): string | null;

  // ── Atlas ──────────────────────────────────────────────────────────────────
  atlas(): Promise<AtlasGraph>;
  atlasPorts(): Promise<PortAllocation[]>;

  // ── Mercury: constitution ──────────────────────────────────────────────────
  mercuryTree(): Promise<MercuryTree>;
  mercuryItem(path: string): Promise<Axiom>;
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
    nonconform?: { violations: MetaViolation[]; proposed: { titel: string; body: string; path: string } };
  }>;
  mercuryOptimize(titel: string, body: string, section?: string): Promise<{ titel: string; body: string }>;
  mercuryConform(titel: string, body: string): Promise<Conformance>;
  mercuryEditAxiom(path: string, titel: string, body: string): Promise<{ path: string; axiom: Axiom }>;
  mercuryMoveAxiom(from: string, to: string): Promise<{ path: string }>;
  mercuryDeleteAxiom(path: string): Promise<void>;
  mercuryMoveCategory(from: string, to: string): Promise<{ moved: number }>;
  mercuryReorder(category: string, order: string[]): Promise<void>;

  // ── Mercury: tasks & runs (one machinery, two kinds) ───────────────────────
  mercuryRuns(): Promise<RunList>;
  mercuryRun(id: string): Promise<{ run: Run; axioms: Record<string, string> }>;
  mercuryRunPrompt(id: string): Promise<{ prompt: string }>;
  mercuryRunCoverage(): Promise<RunCoverage>;
  mercuryRunNotices(): Promise<{ notices: RunNotice[] }>;
  mercuryDismissRunNotice(id: string): Promise<void>;
  mercuryClearRunNotices(): Promise<void>;
  mercuryCreateRun(body: RunInput): Promise<Run>;
  mercuryUpdateRun(id: string, body: RunInput): Promise<Run>;
  mercuryDeleteRun(id: string): Promise<void>;
  mercuryRunAiFill(): Promise<RunProposal>;
  mercuryRunAiFinetune(): Promise<RunProposal>;
  mercuryApplyRunProposal(mode: 'fill' | 'replace', plan: RunPlan): Promise<void>;
  mercuryRunHistory(): Promise<{ snapshots: RunSnapshotMeta[] }>;
  mercuryRestoreRunHistory(ts: string): Promise<void>;
  /** Execution-result documents of one run (persist past deletion; carry the stage arrays). */
  mercuryRunResults(id: string): Promise<{ results: RunResult[] }>;
  /** One execution result document — carries THE server stage array. */
  mercuryRunResult(id: string, resultId: string): Promise<RunResult>;
  /** The one calendar access point (REQ-012); `kind` narrows to one surface. */
  mercuryRunCalendar(days?: number, kind?: RunKind): Promise<RunCalendar>;
  /** Stored executions (history), newest first; `kind` narrows per surface. */
  mercuryRunExecutions(kind?: RunKind): Promise<{ executions: RunResult[] }>;
  mercuryReportStatus(): Promise<{ records: ReportDelivery[] }>;
  mercuryChat(messages: RunChatMessage[]): Promise<RunChatReply>;

  // ── Mercury: execution & slots (S7/S13) ────────────────────────────────────
  /** Start (or resume) a run — the honest outcome, never a silent no-op. */
  mercuryRunNow(id: string, opts?: { placement?: StartPlacement; fresh?: boolean }): Promise<StartOutcome>;
  /** The ACTIVE executions as a LIST + the restart state. Read once on mount, then
   *  SSE-driven — NEVER polled (REQ-034). */
  mercuryRunActive(): Promise<{ active: ExecutionView[]; restart: RestartState }>;
  mercurySlots(): Promise<SlotOverview>;
  mercuryCancelRun(runId: string): Promise<void>;
  mercuryDeferRun(id: string): Promise<void>;
  mercuryResumeRun(id: string): Promise<void>;

  // ── Mercury: deliveries (S10) ──────────────────────────────────────────────
  mercuryDeliveries(): Promise<Delivery[]>;
  mercuryRollbackDelivery(deliveryId: string): Promise<{ outcome: string; todoId?: string }>;
  /** The DELIBERATE dev reset (REQ-022.4) — always behind a UI confirmation. */
  mercuryRepoReset(repoId: string): Promise<void>;

  // ── Todo media ─────────────────────────────────────────────────────────────
  mercuryUploadAttachment(id: string, filename: string, contentB64: string): Promise<RunAttachment[]>;
  mercuryDeleteAttachment(id: string, attachmentId: string): Promise<RunAttachment[]>;
  mercuryAttachmentRawUrl(id: string, attachmentId: string): string;

  // ── Live updates (S12) ─────────────────────────────────────────────────────
  /** The ONE stream (the provider opens exactly one); null when no live provider — the
   *  provider then falls back to a gentle poll. */
  events(): EventSource | null;

  // ── Service interfaces (cross-cutting 5) ───────────────────────────────────
  serviceConfig(): Promise<ServiceConfig>;
  serviceStorage(): Promise<StorageView>;
  serviceTelemetry(): Promise<LoadView>;
  serviceAiUsage(): Promise<AiUsageView>;
}

/** Thrown by httpSource when the backend returns 401 (login required / expired). */
export class AuthRequiredError extends Error {
  constructor() {
    super('Authentication required');
    this.name = 'AuthRequiredError';
  }
}

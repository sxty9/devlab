// The frontend wire contract — a FIELD-FOR-FIELD mirror of backend internal/model (and the
// runs-domain wire forms), pinned by the golden fixtures under contract/fixtures/ via
// types.contract.test.ts. Drift breaks both builds. Frozen after Welle 0.

/** The tools in the left icon rail. */
export type PanelId = 'vision' | 'project' | 'vcs' | 'git' | 'claude' | 'terminal';

/** The signed-in DevLab user (model.User). */
export interface User {
  username: string;
  displayName: string;
  isAdmin: boolean;
  canUseDevlab: boolean;
  /** May read the session a run works in — live and afterwards. */
  canWatchSession: boolean;
  /** May write INTO a running session. Separate from watching: the two are two rights. */
  canSpeakSession: boolean;
  githubLinked: boolean;
  githubLogin?: string;
}

/** The deliberately MINIMAL health probe (model.Health): no operational internals. */
export interface Health {
  ok: boolean;
  mode: string;
}

/** User-tunable editor settings (Settings modal → Monaco). */
export interface EditorSettings {
  fontSize: number;
  tabSize: number;
}

/** A full-screen overlay (modal) that's currently open, if any. */
export type Overlay = 'settings' | 'help' | null;

export type RepoKind = 'service' | 'repo' | 'library';
export type RepoPermission = 'pull' | 'push' | 'admin';
export type RepoIcon = 'go' | 'ts' | 'rust' | 'python' | 'shell' | 'service' | 'repo' | 'library';

/** A selectable repository/service (model.Repo). */
export interface Repo {
  id: string;
  name: string;
  fullName: string;
  kind: RepoKind;
  description: string;
  language: string;
  icon: RepoIcon;
  tint: 'accent' | 'success' | 'warning' | 'gpu' | 'net' | 'ssd' | 'ram';
  permission: RepoPermission;
}

export interface Branch {
  name: string;
  isDefault: boolean;
  ahead: number;
  behind: number;
  updated: string;
}

export type GitStatus = 'modified' | 'added' | 'deleted' | 'untracked' | 'renamed' | 'conflict';

export interface FileNode {
  id: string;
  name: string;
  kind: 'file' | 'dir';
  children?: FileNode[];
  lang?: string;
  status?: GitStatus;
}

export interface FileContent {
  path: string;
  lang: string;
  code: string;
}

export interface Change {
  path: string;
  status: GitStatus;
  additions: number;
  deletions: number;
  staged: boolean;
}

export interface ClaudeMsg {
  id: string;
  role: 'user' | 'assistant' | 'tool';
  text: string;
  tool?: string;
  ts: string;
}

export interface TermLine {
  id: string;
  kind: 'cmd' | 'stdout' | 'stderr' | 'system';
  text: string;
}

export interface Tab {
  id: string;
  title: string;
  kind: 'code' | 'structure' | 'diff' | 'vision';
  path?: string;
  lang?: string;
  dirty?: boolean;
}

export interface AiMessage {
  role: 'user' | 'assistant';
  content: string;
  ts: string;
  ask?: AiAsk;
}

export interface AiAskOption {
  label: string;
  description?: string;
}
export interface AiAskQuestion {
  header?: string;
  question: string;
  options: AiAskOption[];
  multiSelect?: boolean;
}
export interface AiAsk {
  questions: AiAskQuestion[];
}

export interface AssistantReply {
  output: string;
  engine: string;
  model: string;
  usage: { inputTokens: number; outputTokens: number; totalTokens: number; truncated: boolean };
  ask?: AiAsk;
}

export interface AssistantAsk {
  prompt: string;
  contextPaths: string[];
  history: AiMessage[];
  kind?: string;
  model?: string;
  effort?: string;
}

export interface AgentAsk {
  prompt: string;
  model?: string;
  effort?: string;
  mode?: string;
  resume?: string;
}

export interface AgentReply {
  output: string;
  sessionId: string;
  costUsd: number;
  numTurns: number;
  isError: boolean;
  changes: Change[];
}

export interface PullRequestResult {
  number: number;
  url: string;
  state: string;
  title: string;
  branch: string;
  base: string;
  existed: boolean;
}

export interface AiModelCatalog {
  claude: { id: string; label: string }[];
  ollama: string[];
}

export type VisionFileKind = 'image' | 'pdf' | 'markdown' | 'text' | 'other';

export interface VisionFile {
  path: string;
  name: string;
  kind: VisionFileKind;
  size: number;
  status?: string;
}

export interface Comment {
  id: string;
  path: string;
  parentId: string;
  author: string;
  authorName: string;
  body: string;
  createdAt: string;
  editedAt?: string;
}

export interface CommitLine {
  from: number;
  to: number;
  lane: number;
}

export interface Commit {
  hash: string;
  message: string;
  author: string;
  time: string;
  refs?: string[];
  dotLane: number;
  lines: CommitLine[];
}

export interface Worktree {
  branch: string;
  note: string;
  url?: string;
  current?: boolean;
}

export type StageState = 'done' | 'active' | 'pending';

/** A row of the honest repo overview (model.RepoStage) — only git-attested rows exist. */
export interface RepoStage {
  id: string;
  label: string;
  state: StageState;
  hint: string;
}

export type VisionKind = 'spec' | 'mindmap' | 'jet' | 'note';

export interface VisionDoc {
  id: string;
  title: string;
  kind: VisionKind;
  summary: string;
  state: StageState;
  updated: string;
}

export interface RepoData {
  branches: Branch[];
  tree: FileNode[];
  files: Record<string, FileContent>;
  diffBefore?: Record<string, string>;
  changes: Change[];
  commits: Commit[];
  worktrees: Worktree[];
  vision: VisionDoc[];
  claude: ClaudeMsg[];
  terminal: TermLine[];
  stages: RepoStage[];
  defaultTabs: Tab[];
  activeTabId: string;
  structure: StructureSection[];
}

export interface StructureSection {
  title: string;
  hint: string;
  entries: { name: string; kind: 'dir' | 'file'; note: string }[];
}

// ── Chain vocabulary (model — the frozen wire literals) ───────────────────────

/** The ONE chain (REQ-027). */
export type Stage = 'preflight' | 'implement' | 'deliver-dev' | 'publish' | 'pull-request';

/** Two transient + four terminal states; every stage ENDS in one of the four. */
export type StepState = 'pending' | 'running' | 'executed' | 'failed' | 'not-applicable' | 'not-executed';

/** The preflight observation; 'unknown' = source unreachable, never guessed. */
export type TaskState = 'not-implemented' | 'implemented-undelivered' | 'delivered' | 'unknown';

export type ExecPhase =
  | 'created'
  | 'queued'
  | 'running'
  | 'paused'
  | 'blocked'
  | 'interrupted'
  | 'completed'
  | 'failed'
  | 'discarded';

export type PauseReason = 'deferred-by-user' | 'usage-limit';

/** The two kinds sharing the run machinery. */
export type RunKind = 'auto' | 'todo';

/** Who acted (REQ-041): a label, never a barrier. Empty user = unknown, never invented. */
export interface Actor {
  user: string;
  autonomous?: boolean;
  onBehalfOf?: string;
}

export interface Authorship {
  created: Actor;
  createdAt: string;
  updated: Actor;
  updatedAt: string;
}

/** One stage's honest, server-derived state — the client ONLY renders it (B-17/B-35). */
export interface StageView {
  stage: Stage | string; // legacy archive stages carry their historical names verbatim
  state: StepState;
  reason?: string;
  evidence?: string;
  log?: string;
  /** The SERVER's mark for the stage whose record is the agent session, not a log text. The
   *  client never recognises that stage by its name — it reads this. */
  session?: boolean;
  link?: string;
  startedAt?: string;
  endedAt?: string;
}

export interface Backoff {
  reason: string;
  class: string;
  attempts: number;
  firstAt: string;
  lastAt: string;
  nextAt: string;
}

export interface RepoPipeline {
  repo: string;
  stages: StageView[];
  taskState?: TaskState;
  block?: Backoff;
  done: boolean;
  succeeded: boolean;
}

/** Consumption and its monetary equivalent — informative, NEVER a cap (REQ-017). */
export interface UsageView {
  inputTokens: number;
  outputTokens: number;
  costUsd: number;
}

export interface ContinuationView {
  repo: string;
  stage: Stage;
}

export interface PauseView {
  reason: PauseReason;
  message?: string;
  resumeAttempts: number;
  notBefore?: string;
}

export interface ExecutionView {
  id: string;
  runId: string;
  runTitle: string;
  kind: RunKind;
  phase: ExecPhase;
  reason?: string;
  pause?: PauseView;
  continuation?: ContinuationView;
  repos: RepoPipeline[];
  overload?: boolean;
  usage: UsageView;
  requested: Authorship;
  createdAt: string;
  startedAt: string;
  updatedAt: string;
  deliveredCommit?: string;
}

export interface DeferSuggestion {
  executionId: string;
  reason: string;
  score: number;
}

export interface QueuedStart {
  runId: string;
  title: string;
  by: Actor;
  at: string;
}

export interface SlotOverview {
  capacity: number;
  occupied: number;
  overloadActive: boolean;
  restartPending: boolean;
  active: ExecutionView[];
  deferred: ExecutionView[];
  queuedStarts: QueuedStart[];
}

export interface StartOutcome {
  executionId?: string;
  started: boolean;
  queued?: boolean;
  resumed?: boolean;
  fresh?: boolean;
  notStarted?: string;
  taskStates?: Record<string, TaskState>;
  // taskEvidence names, per target repo, the observation the state rests on — for a "delivered"
  // refusal it names the delivered stand, so the rejection is checkable against the todo's text.
  taskEvidence?: Record<string, string[]>;
  suggestion?: DeferSuggestion;
  restartPending?: boolean;
}

export interface RestartState {
  pending: boolean;
  requestedBy: Actor;
  requestedAt: string;
  deadline: string;
  queuedStarts?: QueuedStart[];
}

/** The ledger view of one delivery (model.Delivery, REQ-024). */
export interface Delivery {
  id: string;
  repo: string;
  branch: string;
  fromCommit: string;
  toCommit: string;
  prNumber?: number;
  prUrl?: string;
  createdAt: string;
  mergedAt?: string;
  reversalOf?: string;
  stage?: string;
  /** The execution this delivery arose from. Reading it against `stage` is how a surface knows
   *  which executions still hold an open delivery — the server's own B-8 rule — instead of guessing
   *  it from a chain stage name (B-35). Absent on a record the ledger cannot attribute. */
  executionId?: string;
  /** When the auto-merge of this delivery's pull request is due (K-5): the deadline the open list
   *  names so a todo waiting only for its merge shows that it waits, and until when. Absent when no
   *  tracked pull request carries a deadline (already merged, closed, or never opened). */
  mergeBy?: string;
  /** Whether this delivery's pull request is BLOCKED — the honest terminal state after a DURABLE
   *  obstacle (the pull request or repo is gone, the rights are missing, the request is invalid),
   *  waiting for an explicit release rather than for the clock (K-5). */
  blocked?: boolean;
  /** Why the delivery is blocked, in words a person can act on (only set when `blocked`). */
  blockedReason?: string;
  /** Whether this delivery's pull request is being RETRIED after a SELF-ENDING obstacle (a passing
   *  rate limit, a server hiccup, a dropped connection). Unlike `blocked`, this never waits for a
   *  person — the maintenance keeps trying and it clears itself once the obstacle passes. A record is
   *  either blocked OR retrying, never both. */
  retrying?: boolean;
  /** What is stuck — the retry's reason (only set when `retrying`). */
  retryReason?: string;
  /** How often the retry has been attempted so far (only set when `retrying`). */
  retryAttempts?: number;
  /** Since when the obstacle has stood — the first failed attempt (only set when `retrying`). */
  retrySince?: string;
  /** When the next attempt falls (only set when `retrying`). */
  retryNextAt?: string;
  /** Why a FAILED delivery ("Lieferung gescheitert") did not ship — set only when `stage` is
   *  'failed'. It is what lets the ledger surface say which layer at the tip is broken and on what,
   *  without anyone having to ask (WHAT-4). */
  failedReason?: string;

  // ── The production step (WHAT-1) — the PROD view of the deliveries surface reads these ──────
  /** The PRODUCTION lifecycle of a MERGED delivery, distinct from the dev/PR `stage` above.
   *  '' (not merged, production not reached), 'not-applicable' (the repo is no service), 'pending'
   *  (merged, production not yet done), 'failed' (last production send failed, retrying) or 'live'
   *  (proven running in production). The PROD view shows only merged deliveries and reads this. */
  prodStage?: string;
  /** When the service was proven running in production — the "since when" the PROD view shows. */
  prodDeployedAt?: string;
  /** Why the last production send failed — shown while `prodStage` is 'failed'. */
  prodFailedReason?: string;
  /** When the production send is next attempted (a self-ending failure retries by itself). */
  prodRetryNextAt?: string;
  /** The attested property behind a not-applicable production step (the repo is no service). */
  prodEvidence?: string;
}

export interface PRRef {
  number: number;
  url: string;
  headBranch: string;
}

export interface PortAllocation {
  port: number;
  service: string;
  routed: boolean;
  bound: boolean;
  conflict: boolean;
}

/** One persistent hint, coalesced by key (model.Notice, REQ-032.5). */
export interface ServiceNotice {
  id: string;
  kind: string;
  repo?: string;
  text: string;
  nextStep?: string;
  count: number;
  firstAt: string;
  lastAt: string;
  read: boolean;
}

/** The service configuration (durations as Go duration strings, e.g. "3h"). */
export interface ServiceConfig {
  maxConcurrency: number;
  defaultTimeBudget: string;
  automergeWindow: string;
}

export interface PoolUsage {
  name: string;
  bytes: number;
  files: number;
}

export interface StorageView {
  pools: PoolUsage[];
  totalBytes: number;
}

export interface LoadView {
  cpuPercent: number;
  rssBytes: number;
  goroutines: number;
}

export interface AiUsageView {
  windowHours: number;
  samples: number;
  totals: UsageView;
  bySource: Record<string, UsageView>;
}

/** The preflight finding (preflight.Finding): state WITH evidence (REQ-031.3). */
export interface Finding {
  state: TaskState;
  evidence: string[];
  observedAt: string;
  openPr?: PRRef;
  err?: string;
}

// ── Mercury — constitution (ported wire forms) ────────────────────────────────

export interface MercuryNode {
  name: string;
  path: string;
  isAxiom: boolean;
  children?: MercuryNode[];
}

export interface MercuryTree {
  axiome: MercuryNode[];
  regeln: MercuryNode[];
  laeufe: MercuryNode[];
  meta: MercuryNode[];
}

export interface MetaViolation {
  meta: string;
  issue: string;
}

export interface Conformance {
  conforms: boolean;
  violations: MetaViolation[];
  proposed?: { titel: string; body: string };
  metaCount: number;
  unavailable?: boolean;
}

export interface AxiomAuthor {
  createdBy?: string;
  createdAt?: string;
  updatedBy?: string;
  updatedAt?: string;
}

export interface Axiom {
  id: string;
  titel: string;
  quelle?: string;
  body: string;
  author?: AxiomAuthor;
}

// ── Mercury — tasks & runs (one machinery, two kinds) ─────────────────────────

export type RunScheduleKind = 'daily' | 'weekly';

/** A recurring schedule (runs.ScheduleSpec): time-of-day, daily or on weekdays (0=Sun..6=Sat). */
export interface RunSchedule {
  kind: RunScheduleKind;
  timeOfDay: string;
  weekdays?: number[];
}

/** One target of a todo (runs.Target): a repo, with create marking a repo to be created first. */
export interface RunTarget {
  repo: string;
  create?: boolean;
}

/** One attached medium (runs.AttachmentRef). */
export interface RunAttachment {
  id: string;
  filename: string;
  mime?: string;
  size: number;
  sha256?: string;
  uploadedAt: string;
  uploadedBy?: string;
}

/** A run's engine choice (runs.Tuning). Empty fields REFER to the service default
 *  (REQ-010.2); a present timeBudget of "0s" means "no budget". */
export interface RunTuning {
  model?: string;
  modelVersion?: string;
  effort?: string;
  timeBudget?: string;
}

/** How self-reliant a run works (model.AutonomyLevel) — WHEN it stops and asks instead of deciding
 *  for itself. Empty resolves to 'autonomous'. */
export type AutonomyLevel = 'collaborative' | 'balanced' | 'autonomous';

/** A run definition (runs.Run) — SLIM (B-20): no state flags; every execution fact is a
 *  projection over the execution documents and results. */
export interface Run {
  id: string;
  kind: RunKind;
  title: string;
  task?: string;
  axiomIds?: string[];
  schedule?: RunSchedule;
  active?: boolean;
  targets?: RunTarget[];
  dueAt?: string;
  autonomy?: AutonomyLevel;
  tuning: RunTuning;
  promptSnapshot?: string;
  promptInputHash?: string;
  attachments?: RunAttachment[];
  authorship: Authorship;
}

/** The create/update payload (runs.RunInput). */
export interface RunInput {
  kind?: RunKind;
  title: string;
  task?: string;
  axiomIds?: string[];
  schedule?: RunSchedule;
  active?: boolean;
  targets?: RunTarget[];
  dueAt?: string | null;
  autonomy?: AutonomyLevel;
  tuning?: RunTuning;
}

export interface RunList {
  runs: Run[];
  axioms: Record<string, string>;
}

export interface RunCoverage {
  covered: Record<string, string[]>;
  index: Record<string, string>;
  axioms: Record<string, string>;
  pending?: boolean;
}

/** One entry in the automatic axiom→run assignment feed. */
export interface RunNotice {
  id: string;
  at: string;
  kind: 'assigned' | 'failed';
  runId?: string;
  runName?: string;
  newRun?: boolean;
  axiomIds: string[];
  axioms: string[];
  reason?: string;
}

/** One blocking question on the Blocked surface (runs.Question): a run that stopped and asked, plus
 *  the answer once given. Three qKinds are GUARDED handles whose freeing needs an explicit approval:
 *  'wrapper-renewal' (detail carries the exact difference to the installed root scripts), 'prod-host-key'
 *  (the production host presented a new ssh key; hostKeyFingerprint pins the key the approval covers), and
 *  'prod-receiver' (a devlab delivery changed the root receiver scripts the chain cannot install on the
 *  production host; prodReceiverCommand carries the operator command and wrappers pins the checksums it
 *  must reach — the chain re-measures the host before it settles the delivery live). Everything else is a
 *  plain 'decision' answered with free text. */
export interface RunQuestion {
  id: string;
  runId: string;
  runTitle?: string;
  kind?: RunKind;
  executionId: string;
  repo: string;
  qKind: 'decision' | 'wrapper-renewal' | 'prod-host-key' | 'prod-receiver';
  autonomy?: AutonomyLevel;
  question: string;
  recommendation?: string;
  progress?: string;
  detail?: string;
  askedAt: string;
  askedBy: Actor;
  answer?: string;
  approved?: boolean;
  answeredAt?: string;
  answeredBy?: Actor;
  resolved?: boolean;
  resolvedAt?: string;
  /** The user REJECTED the question (the co-equal "no"): it is resolved, holds nothing, and its run
   *  ended as failed with the rejection as the reason. */
  declined?: boolean;
  declinedBy?: Actor;
  /** Closed because its run no longer exists — the question was gegenstandslos and blocks nothing. */
  moot?: boolean;
  /** Closed because its order FINISHED (the run still exists but its execution ended, so answering can
   *  no longer take effect) — distinct from moot (run gone) and declined (rejected). */
  ended?: boolean;
  /** Why the question closed without an effective answer (rejected, its run is gone, or its order finished). */
  closeNote?: string;
  /** For a 'wrapper-renewal' question: the exact (file, checksum) set the approval covers — the version
   *  this run delivers (its own branch, not yet merged). Approving installs only these named files with
   *  these checksums; detail renders the same set for the reader. */
  wrappers?: { name: string; sha: string }[];
  /** For a GUARDED question ('wrapper-renewal' | 'prod-host-key' | 'prod-receiver'): the exact sentence
   *  the user affirms to approve, derived by the backend from the question's own subject (which version,
   *  which files and checksums, which host key, or which receiver command). It is both the consent shown
   *  on the checkbox and — verbatim — the answer recorded in the ledger, so the two can never describe
   *  different things. Absent for a plain 'decision'. */
  approvalStatement?: string;
  /** For a 'prod-host-key' question: the production host whose key changed and the SHA256 fingerprint
   *  of the key now presented — the exact key the approval covers (the accept path re-verifies it). */
  hostKeyTarget?: string;
  hostKeyFingerprint?: string;
  /** For a 'prod-receiver' question: the production host whose root receiver scripts are older than the
   *  merged delivery ships, and the exact one-line command an operator with root runs on that host to
   *  bring them current (wrappers pins the checksums each script must reach). The chain never installs
   *  them itself — it re-measures the host afterwards and settles the delivery live only once they match. */
  prodReceiverTarget?: string;
  prodReceiverCommand?: string;
}

export interface PlannedRun {
  name: string;
  axiomIds: string[];
  schedule: RunSchedule;
  rationale?: string;
}

export interface RunPlan {
  runs: PlannedRun[];
}

/** What one may do with a proposal at its single access point: ask for it, look at it, put it
 *  aside. `request` starts an analysis when none is in flight and otherwise reports that one;
 *  `read` never starts anything (a returning or reloaded surface asks this way); `cancel` abandons
 *  work in flight as well as a finished or failed proposal. */
export type RunProposalAction = 'request' | 'read' | 'cancel';

/** The two AI planning kinds: filling the axioms no run carries, and regrouping the whole set. */
export type RunProposalKind = 'fill' | 'finetune';

/** One AI planning analysis. It is never waited for: the access takes the work on and answers at
 *  once, and the outcome arrives over the live stream (topic `runs`). The states are the contract's
 *  own words; `none` means nothing is in flight and nothing waits for review. */
export interface RunProposal {
  kind: RunProposalKind;
  state: 'none' | 'running' | 'completed' | 'failed';
  /** Identifies ONE analysis, so a surface can tell a new outcome from one it has already shown. */
  id?: string;
  startedAt?: string;
  endedAt?: string;
  /** Why it failed, in words the user can act on — never a bare "failed". */
  reason?: string;
  /** The reviewable plan — present ONLY in state `completed`. */
  proposal?: RunPlan;
  /** id → title legend for the axioms the plan names (arrives with the plan). */
  axioms?: Record<string, string>;
}

export interface RunSnapshotMeta {
  ts: string;
  action: string;
  actor: string;
  runCount: number;
}

/** One execution result document (runs.Result) — carries THE server stage array. */
export interface RunResult {
  id: string;
  runId: string;
  runTitle?: string;
  kind: RunKind;
  model?: string;
  startedAt: string;
  endedAt?: string;
  mergedAt?: string;
  repos: RepoPipeline[];
  report?: string;
  usage: UsageView;
  prompt?: string;
  requested: Authorship;
  /** Every time a PERSON wrote into this execution's running session. Present ⇒ the run did not
   *  work purely by itself. */
  interventions?: Intervention[];
  synthetic?: boolean;
  legacy?: boolean;
}

/** One line of an execution's agent session (model.SessionLine): what the agent said or did, or
 *  what a person wrote into it. `from` empty ⇒ the agent itself; otherwise the person's username. */
export interface SessionLine {
  at: string;
  repo?: string;
  from?: string;
  text: string;
}

/** That a PERSON wrote into a running execution (model.Intervention): who, when, where. */
export interface Intervention {
  by: Actor;
  at: string;
  repo?: string;
}

/** One PORTION of a session together with what a viewer must know about it (api.sessionView).
 *  `from`/`next` are the anchors for older lines and for the next follow-up read. */
export interface SessionPortion {
  lines: SessionLine[];
  from: number;
  next: number;
  /** The journal continues BEFORE `from` — there are older lines to load. */
  older: boolean;
  /** The conversation is running right now and can take a message. */
  open: boolean;
  /** Which repositories' conversations are open — what to name when several are working. */
  repos?: string[];
  interventions?: Intervention[];
}

/** One calendar entry (model.RunOccurrence): future firing (schedule) or past execution
 *  (resultId) — the calendar opens the same detail as the history (REQ-012). */
export interface RunOccurrence {
  runId: string;
  runTitle: string;
  kind: RunKind;
  at: string;
  schedule?: string;
  resultId?: string;
  succeeded?: boolean;
  paused?: boolean;
}

export interface RunCalendar {
  from: string;
  to: string;
  occurrences: RunOccurrence[];
}

export interface ReportDelivery {
  recipient: string;
  day: string;
  /** `blocked` is the honest end of the retries (K-5): the send is not attempted again until it is
   *  resumed explicitly. `backoff` then carries the class, the attempts and the times. */
  status: 'sent' | 'failed' | 'blocked';
  executions: number;
  attempts: number;
  sentAt?: string;
  lastAttempt?: string;
  lastError?: string;
  backoff?: Backoff;
}

// ── Mercury chat (reviewable single-action proposals) ─────────────────────────

export interface RunChatMessage {
  role: 'user' | 'assistant';
  content: string;
}

export interface ActionTarget {
  repo?: string;
  newRepo?: string;
}

export type MercuryAction =
  | { kind: 'create_todo'; name: string; task: string; targets: ActionTarget[]; dueAt?: string }
  | { kind: 'create_run'; name: string; axiomIds: string[]; schedule: RunSchedule }
  | { kind: 'add_record'; section: 'axiome' | 'regeln' | 'laeufe' | 'meta'; titel: string; body: string }
  | { kind: 'edit_record'; path: string; titel: string; body: string }
  | { kind: 'delete_record'; path: string }
  | { kind: 'delete_run'; runId: string }
  | { kind: 'run_now'; runId: string }
  | { kind: 'plan_runs'; mode: 'fill' | 'replace'; runs: PlannedRun[] };

export interface RunChatReply {
  reply: string;
  model?: string;
  action?: MercuryAction;
}

// ── Atlas ─────────────────────────────────────────────────────────────────────

export interface AtlasNode {
  id: string;
  port: number;
  rights: string[];
  hasManifest: boolean;
  hasRoute: boolean;
  repo: string;
}

export interface AtlasFinding {
  severity: 'warn' | 'error';
  message: string;
  nodes: string[];
}

export interface AtlasGraph {
  nodes: AtlasNode[];
  findings: AtlasFinding[];
  scannedAt: string;
}

// ── Live updates (S12) ────────────────────────────────────────────────────────

/** The CLOSED topic set of the ONE SSE stream (live.Topics on the server). Every live surface
 *  rides on this one stream — a new surface takes a topic here, never a second channel. */
export type LiveTopic =
  | 'axioms'
  | 'runs'
  | 'active'
  | 'progress'
  | 'deliveries'
  | 'notices'
  | 'slots'
  | 'restart'
  | 'questions'
  | 'session';

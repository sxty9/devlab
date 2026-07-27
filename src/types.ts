// Shared domain types for the DevLab workspace. Phase 1 is mock-data only; these shapes are
// deliberately close to what a real backend (git + sxgate + Claude) would return, so wiring
// the backend later is a swap of the data source, not a rewrite of the UI.

/** The tools in the left icon rail. */
export type PanelId = 'vision' | 'project' | 'vcs' | 'git' | 'claude' | 'terminal';

/** The signed-in DevLab user. Identity comes from the Holistic session (a Linux account);
 *  `canUseDevlab` reflects the single Holistic right (hp_devlab_access, admin implicit).
 *  `githubLinked` gates the workspace — repo visibility/authorization derive from GitHub. */
export interface User {
  username: string;
  displayName: string;
  isAdmin: boolean;
  canUseDevlab: boolean;
  githubLinked: boolean;
  /** The linked GitHub login, when linked. */
  githubLogin?: string;
}

/** User-tunable editor settings (Settings modal → Monaco). */
export interface EditorSettings {
  fontSize: number;
  tabSize: number;
}

/** A full-screen overlay (modal) that's currently open, if any. */
export type Overlay = 'settings' | 'help' | null;

export type RepoKind = 'service' | 'repo' | 'library';

/** The viewer's effective GitHub permission on a repo (the single source of truth for write). */
export type RepoPermission = 'pull' | 'push' | 'admin';

/** The repo's card glyph, derived server-side from its language and kind (discover.icon()).
 *  src/ui/repoIcon.ts maps each name to an SVG. */
export type RepoIcon = 'go' | 'ts' | 'rust' | 'python' | 'shell' | 'service' | 'repo' | 'library';

/** A selectable repository/service in the top-bar dropdown. */
export interface Repo {
  id: string;
  name: string;
  /** GitHub canonical "owner/repo". */
  fullName: string;
  kind: RepoKind;
  description: string;
  /** Primary language label, e.g. "TypeScript", "Go", "Shell". */
  language: string;
  /** The card glyph name; derived from language, falling back to kind. */
  icon: RepoIcon;
  /** A design-token color name used as the repo's accent dot (accent | success | warning | gpu | net | ssd | ram). */
  tint: 'accent' | 'success' | 'warning' | 'gpu' | 'net' | 'ssd' | 'ram';
  /** The viewer's effective right from GitHub; 'pull' repos are read-only in the UI. */
  permission: RepoPermission;
}

/** A git branch / working session for the active repo. */
export interface Branch {
  name: string;
  isDefault: boolean;
  ahead: number;
  behind: number;
  /** Human-relative last activity, e.g. "2h ago". */
  updated: string;
}

export type GitStatus = 'modified' | 'added' | 'deleted' | 'untracked' | 'renamed' | 'conflict';

/** A node in the project file tree. `id` is the repo-relative path and is globally unique. */
export interface FileNode {
  id: string;
  name: string;
  kind: 'file' | 'dir';
  children?: FileNode[];
  /** Monaco language id for files (e.g. "typescript", "go", "shell", "json", "markdown"). */
  lang?: string;
  /** Decorate the row when the file has an uncommitted change. */
  status?: GitStatus;
}

/** Editor contents for a file path. */
export interface FileContent {
  path: string;
  lang: string;
  code: string;
}

/** A row in the Version Control panel. */
export interface Change {
  path: string;
  status: GitStatus;
  additions: number;
  deletions: number;
  /** false → unstaged (working tree), true → staged for commit. */
  staged: boolean;
}

/** A message in the Claude panel transcript. */
export interface ClaudeMsg {
  id: string;
  role: 'user' | 'assistant' | 'tool';
  text: string;
  /** For role: 'tool' — the tool name shown as a chip. */
  tool?: string;
  ts: string;
}

/** A line in the Terminal panel. */
export interface TermLine {
  id: string;
  kind: 'cmd' | 'stdout' | 'stderr' | 'system';
  text: string;
}

/** An open editor tab. */
export interface Tab {
  id: string;
  title: string;
  kind: 'code' | 'structure' | 'diff' | 'vision';
  /** Present for kind: 'code' | 'diff' | 'vision'. */
  path?: string;
  lang?: string;
  dirty?: boolean;
}

/** One turn in the repo-scoped AI assistant transcript. */
export interface AiMessage {
  role: 'user' | 'assistant';
  content: string;
  ts: string;
  /** A structured question the assistant posed (interactive turns); rendered as clickable options. */
  ask?: AiAsk;
}

/** A structured multiple-choice question the AI posed, answerable by clicking options in the chat
 * bubble (à la Claude Code). Mirrors aigentic's interactive protocol, surfaced verbatim. */
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

/** What the AI assistant returns after a run (proxied from aigentic). */
export interface AssistantReply {
  output: string;
  engine: string;
  model: string;
  usage: { inputTokens: number; outputTokens: number; totalTokens: number; truncated: boolean };
  /** Set when the model replied with a structured question instead of (or alongside) prose. */
  ask?: AiAsk;
}

/** The payload for one AI turn. */
export interface AssistantAsk {
  prompt: string;
  contextPaths: string[];
  history: AiMessage[];
  /** aigentic engine: 'choose' (router) | 'claude-cli' | 'claude-api' | 'ollama'. */
  kind?: string;
  /** model id override (from the catalog); '' = the engine's default. */
  model?: string;
  effort?: string;
}

/** The payload for one agentic run — the full claude CLI, in the repo workspace, as the user. */
export interface AgentAsk {
  prompt: string;
  /** model id (from the catalog); '' = the CLI default. */
  model?: string;
  effort?: string;
  /** 'plan' (read-only) | 'auto' (accept edits) | 'full' (autonomous, incl. shell). */
  mode?: string;
  /** prior session id to continue the conversation (--resume). */
  resume?: string;
}

/** What an agentic run returns: the summary + the refreshed change set (edits it made). */
export interface AgentReply {
  output: string;
  sessionId: string;
  costUsd: number;
  numTurns: number;
  isError: boolean;
  changes: Change[];
}

/** Result of opening (or focusing) a GitHub pull request for the current branch. */
export interface PullRequestResult {
  number: number;
  url: string;
  state: string;
  title: string;
  branch: string;
  base: string;
  /** true when an already-open PR was focused rather than a new one created. */
  existed: boolean;
}

/** aigentic's model catalog (GET /api/assistant/models). */
export interface AiModelCatalog {
  claude: { id: string; label: string }[];
  ollama: string[];
}

/** How a Vision-Catalog file is rendered. */
export type VisionFileKind = 'image' | 'pdf' | 'markdown' | 'text' | 'other';

/** One artifact in a repo's /vision folder (the Vision Catalog). */
export interface VisionFile {
  path: string;
  name: string;
  kind: VisionFileKind;
  size: number;
  /** git status decoration, if any. */
  status?: string;
}

/** One message in a per-file threaded discussion (nested via parentId). */
export interface Comment {
  id: string;
  path: string;
  /** '' for a top-level comment, else the parent comment's id. */
  parentId: string;
  author: string;
  authorName: string;
  body: string;
  createdAt: string;
  editedAt?: string;
}

/** One drawn segment in a commit-graph row (a lane line from the row's top to its bottom). */
export interface CommitLine {
  from: number;
  to: number;
  /** Lane index that owns this segment's colour. */
  lane: number;
}

/** A commit in the Git log graph. */
export interface Commit {
  hash: string;
  message: string;
  author: string;
  time: string;
  /** Branch/tag labels on this commit (e.g. ['main', 'HEAD']). */
  refs?: string[];
  /** Lane the commit node sits on. */
  dotLane: number;
  /** Lines drawn through this row. */
  lines: CommitLine[];
}

/** A git worktree (IntelliJ-style management). */
export interface Worktree {
  branch: string;
  note: string;
  /** A live preview URL if this worktree is deployed. */
  url?: string;
  current?: boolean;
}

export type StageState = 'done' | 'active' | 'pending';

/** A stage in the bottom delivery-pipeline bar (Vision → … → main merge). */
export interface Stage {
  id: string;
  label: string;
  state: StageState;
  hint: string;
}

export type VisionKind = 'spec' | 'mindmap' | 'jet' | 'note';

/** A "Vision Deposit" artifact — the front of the pipeline (specs, mindmaps, jets, notes). */
export interface VisionDoc {
  id: string;
  title: string;
  kind: VisionKind;
  summary: string;
  /** Pipeline state this idea has reached. */
  state: StageState;
  updated: string;
}

/** Everything the workspace knows about one repo (mock in phase 1). */
export interface RepoData {
  branches: Branch[];
  tree: FileNode[];
  files: Record<string, FileContent>;
  changes: Change[];
  /** Optional explicit "before" content for changed files; otherwise synthesized for the diff. */
  diffBefore?: Record<string, string>;
  commits: Commit[];
  worktrees: Worktree[];
  vision: VisionDoc[];
  claude: ClaudeMsg[];
  terminal: TermLine[];
  stages: Stage[];
  /** Tabs opened by default when this repo becomes active. */
  defaultTabs: Tab[];
  /** Which default tab is focused. */
  activeTabId: string;
  /** A short overview shown in StructureView / the repo skeleton. */
  structure: StructureSection[];
}

// ── Mercury — the axiom-management model (scheme-backed, via aigentic) ────────

/** One node of a namespace tree: a category (folder) or an axiom (leaf). Categories nest to any
 *  depth; `path` is the node's stable scheme address. */
export interface MercuryNode {
  name: string;
  path: string;
  isAxiom: boolean;
  children?: MercuryNode[];
}

/** The whole model, grouped by namespace. */
export interface MercuryTree {
  axiome: MercuryNode[];
  regeln: MercuryNode[];
  laeufe: MercuryNode[];
  meta: MercuryNode[];
}

/** One unmet meta-axiom: which requirement, and the concrete way the axiom fails it. */
export interface MetaViolation {
  meta: string;
  issue: string;
}

/** The verdict of checking an axiom against every meta-axiom (with a corrected proposal when it fails). */
export interface Conformance {
  conforms: boolean;
  violations: MetaViolation[];
  proposed?: { titel: string; body: string };
  metaCount: number;
  unavailable?: boolean; // the checker (aigentic) was unreachable — treated as conforming
}

/** A parsed axiom record: front-matter fields + the body markdown. */
export interface Axiom {
  id: string;
  titel: string;
  quelle?: string;
  body: string;
}

// ── Mercury — Automatische Läufe (scheduled autonomous run instances) ─────────

export type RunScheduleKind = 'daily' | 'weekly';

/** A recurring schedule: a time-of-day, either every day or on selected weekdays (0=Sun..6=Sat). */
export interface RunSchedule {
  kind: RunScheduleKind;
  timeOfDay: string; // "HH:MM", 24h
  weekdays?: number[];
}

/** The two things that share the run machinery: `auto` = a recurring, axiom-driven run over all
 *  Holistic repos (Automatische Läufe); `todo` = a one-time concrete task against ONE repo
 *  (Konkrete ToDos) — an ad-hoc fix or a newly planned service. */
export type RunType = 'auto' | 'todo';

/** One destination of a ToDo: an existing Holistic repo (`repo` — its id) or a repo to be created
 *  first (`newRepo` — a newly planned service). Exactly one of the two is set. */
export interface RunTarget {
  repo?: string;
  newRepo?: string;
}

/** One medium (image, document) attached to a ToDo. The bytes are served raw at the attachment endpoint;
 *  this is the metadata the list and previews render. The agent takes the media into account at run time. */
export interface RunAttachment {
  id: string;
  filename: string;
  mime?: string;
  size: number;
  sha256?: string;
  uploadedAt: string;
  uploadedBy?: string;
}

/** A run (`auto`) or a concrete one-time task (`todo`). An auto run's prompt is composed from its
 *  axioms + all Laufregeln and kept current by every scheme write (so it never drifts); a todo's prompt
 *  is just its task — axioms and rules reach the agent through the repo's CLAUDE.md. */
export interface Run {
  id: string;
  name: string;
  type?: RunType; // absent = auto (records predating ToDos)
  enabled: boolean;
  model?: string; // Claude model id/alias the executor drives; absent = runner default (opus)
  effort?: string; // low|medium|high|xhigh|max|ultracode; absent = runner default (max)
  schedule: RunSchedule;
  axiomIds: string[];
  // todo only
  task?: string;
  targets?: RunTarget[]; // one or more destinations (existing and/or newly-created repos)
  attachments?: RunAttachment[]; // media (images, documents) the agent takes into account
  repo?: string; // deprecated single target — read only for records predating `targets`
  newRepo?: string; // deprecated single new-repo target — read only for legacy records
  dueAt?: string; // optional one-time due date; absent = run it manually
  done?: boolean; // set after a successful execution
  prompt: string;
  promptAt?: string;
  promptHash?: string;
  createdAt: string;
  updatedAt: string;
  nextFireAt?: string;
  lastFiredAt?: string;
  lastResult?: RunResultRef;
  // Set when an execution paused on the Claude usage limit and will auto-resume once the window resets.
  suspended?: { resumeAt: string; resultId: string; attempts: number; reason?: string };
}

/** The editable fields of a run or todo (create/update payload). */
export interface RunInput {
  name: string;
  type?: RunType; // default 'auto'
  enabled: boolean;
  model?: string; // Claude model id/alias; '' or absent = runner default (opus)
  effort?: string; // low|medium|high|xhigh|max|ultracode; '' or absent = runner default (max)
  schedule?: RunSchedule; // auto only
  axiomIds?: string[]; // auto only
  task?: string; // todo only
  targets?: RunTarget[]; // todo only — one or more destinations (existing and/or new repos)
  dueAt?: string | null; // todo only — optional one-time due date
}

export interface RunList {
  runs: Run[];
  axioms: Record<string, string>; // axiom id → title, for display
}

/** One repo the last automatic rollout could not update, with a short reason (not a raw log). */
export interface RolloutSkip {
  repo: string;
  reason: string;
}

/** The portioned outcome of the last automatic CLAUDE.md rollout: when it ran, which repos received a
 *  new commit, how many were already current, and which were skipped. `error` is a whole-rollout
 *  failure (e.g. no runner token). Absent (`last: null`) until the first rollout since startup. */
export interface RolloutReport {
  at: string;
  repos: number;
  changed: string[];
  unchanged: number;
  skipped?: RolloutSkip[];
  error?: string;
}

/** Which axioms are already backed by a run (badges), plus id→path and id→title lookups. */
export interface RunCoverage {
  covered: Record<string, string[]>; // axiom id → run ids
  index: Record<string, string>; // axiom id → scheme path
  axioms: Record<string, string>; // axiom id → title
  // An automatic assignment is scheduled or running, so a currently-uncovered axiom is only TEMPORARILY
  // uncovered. Coverage itself stays honest; this merely lets the UI show the state as transient.
  pending?: boolean;
}

/** One entry in the automatic axiom→run assignment feed: either an assignment happened (`assigned`) or
 *  it could not be carried out (`failed`). Portioned, and free of any raw log. */
export interface RunNotice {
  id: string;
  at: string;
  kind: 'assigned' | 'failed';
  runId?: string; // assigned only — the run the axioms landed in (click-through to adjust it)
  runName?: string; // assigned only
  newRun?: boolean; // assigned only — a fresh run was created (vs. an existing one extended)
  axiomIds: string[];
  axioms: string[]; // title snapshot, so the feed reads without a live lookup
  reason?: string; // failed only — a short human reason
}

/** One proposed run from an AI-planning button (reviewable before it is applied). */
export interface PlannedRun {
  name: string;
  axiomIds: string[];
  schedule: RunSchedule;
  rationale?: string;
}

export interface RunPlan {
  runs: PlannedRun[];
}

export interface RunProposal {
  proposal: RunPlan;
  axioms: Record<string, string>; // axiom id → title, so the review can name the axioms
}

/** One entry in the run-config history (each mutation snapshots the full config). */
export interface RunSnapshotMeta {
  ts: string;
  action: string;
  actor: string;
  runCount: number;
}

export interface RunResultRef {
  resultId: string;
  at: string;
  ok: boolean;
  repoCount: number;
  inputTokens?: number;
  outputTokens?: number;
  costUsd?: number;
  /** How far delivery got. A finished execution is not "done" — it sits on a rung: implemented →
   *  dev-deployed → PR offen → gemergt → prod-live. The server attests each rung (the executor the
   *  first two, the PR maintenance the last two); `runStage` turns them into the label. */
  deployed?: boolean;
  prUrl?: string;
  merged?: boolean;
  prodDeployed?: boolean;
}

/** The delivery ladder, mirroring runs.Stage on the server: the furthest rung actually reached.
 *  Deliberately NOT a green "Erledigt" — a run whose PR is still open has delivered nothing yet. */
export type RunStage = 'failed' | 'suspended' | 'implemented' | 'dev-deployed' | 'pr-open' | 'merged' | 'prod-deployed';

/** Derives the stage from a result reference (most-advanced rung first). */
export function runStage(ref: RunResultRef | null | undefined): RunStage | null {
  if (!ref) return null;
  if (ref.prodDeployed) return 'prod-deployed';
  if (ref.merged) return 'merged';
  if (!ref.ok) return 'failed';
  if (ref.prUrl) return 'pr-open';
  if (ref.deployed) return 'dev-deployed';
  return 'implemented';
}

/** The German label + tint for each rung, so every surface names a stage identically. */
export const RUN_STAGE_LABEL: Record<RunStage, { label: string; tint: 'success' | 'accent' | 'warning' | 'danger' }> = {
  'prod-deployed': { label: 'prod-live', tint: 'success' },
  merged: { label: 'main-merged', tint: 'success' },
  'pr-open': { label: 'PR offen', tint: 'accent' },
  'dev-deployed': { label: 'dev-deployed', tint: 'accent' },
  implemented: { label: 'implementiert', tint: 'warning' },
  suspended: { label: 'pausiert', tint: 'warning' },
  failed: { label: 'fehlgeschlagen', tint: 'danger' },
};

/** A pipeline step's honest outcome: executed and succeeded (ok), executed and failed, structurally
 *  not-applicable to this repo, or never executed because an earlier step failed. Only `ok` is a success
 *  — a not-applicable step is never rendered green, and a not-executed one marks where the chain stopped. */
export type RunStepStatus = 'ok' | 'failed' | 'not-applicable' | 'not-executed';

export interface RunStep {
  name: string; // analyze | implement | dev-deploy | push | pr
  running?: boolean; // in flight — while true, `log` carries the agent's streaming transcript
  status: RunStepStatus;
  /** Legacy pre-status flag; the server now always sends `status`, but a mock/older cache may carry ok. */
  ok?: boolean;
  // for analyze/implement `log` is the agent's full report (what was done / blocked); for a
  // not-applicable or not-executed step it is a short reason in the user's language (what was not done, why)
  log?: string;
  at: string;
}

export interface RepoResult {
  repo: string;
  running?: boolean; // the repo currently being worked (only ever set on RunResult.live)
  ok: boolean;
  deployed: boolean;
  prUrl?: string;
  /** The delivered dev state: the persistent integration branch this run grew (mercury-dev) and the
   *  exact commit dev serves. Always set once a run reaches the dev branch — even when it added nothing. */
  devBranch?: string;
  devCommit?: string;
  /** This delivery's stacked-PR base: the previous still-open delivery's branch, else the default branch. */
  prBase?: string;
  /** The recorded delivery id (rollback target), when this run produced a delivery for the repo. */
  deliveryId?: string;
  steps: RunStep[] | null; // null when the repo failed before any step ran (Go marshals the empty slice as null)
  error?: string;
  inputTokens?: number;
  outputTokens?: number;
  costUsd?: number;
  numTurns?: number;
}

/** One recorded delivery — a run's addressable unit of work at a repo (commit range + stacked PR). */
export interface Delivery {
  id: string;
  runId: string;
  resultId: string;
  runName?: string;
  repo: string;
  branch: string;
  devBranch: string;
  baseBranch: string;
  fromCommit: string;
  toCommit: string;
  prNumber?: number;
  prUrl?: string;
  createdAt: string;
  status: 'open' | 'merged' | 'closed' | 'reverted';
  revertedAt?: string;
  revertedBy?: string;
  revertOf?: string;
}

export interface RunResult {
  runId: string;
  resultId: string;
  runName?: string;
  startedAt: string;
  finishedAt?: string;
  promptHash?: string;
  /** The run's Promptstellung for THIS execution — the exact prompt the agent was driven by,
   *  snapshotted at run time. Absent on executions recorded before it was captured. */
  prompt?: string;
  /** Which Claude engine drove THIS execution: the resolved model, and the selected effort tier
   *  (absent = the runner default max, 'ultracode' = the maximal tier). Absent on older executions. */
  model?: string;
  effort?: string;
  ok: boolean;
  repos: RepoResult[] | null; // null when the execution failed before any repo completed (Go marshals the empty slice as null) — every reader must guard
  // The repo in flight while the run executes — kept apart from `repos` (which holds only completed
  // repos) so the live view can show it with its running steps and the agent's streaming output.
  live?: RepoResult;
  inputTokens: number;
  outputTokens: number;
  costUsd: number;
  numTurns: number;
}

/** The run executing right now, as the server sees it: its id, the live result id, and when it started
 *  — or null when nothing runs. Read on mount (so a running run survives a page reload) and polled to
 *  follow a live run. Reflects an actually-running process, so it is empty again after a server restart. */
export interface RunActive {
  runId: string;
  resultId: string;
  startedAt: string;
}

/** A prod-delivery the scheduler has BLOCKED after repeated permanent failures (typically: the service
 *  is not set up on the target). Surfaced in the UI so a stuck delivery is seen without reading the
 *  system log, and can be explicitly resumed. */
export interface BlockedDeploy {
  repo: string; // owner/name
  number: number; // the merged PR number the delivery belongs to
  url: string;
  runId: string;
  reason: string; // human cause, naming the service and the target
  attempts: number; // permanent-failure attempts before it blocked
  blockedAt: string;
}

/** What a trigger decided to do, reported back so the difference between continuing an interrupted
 *  execution and starting a new one is visible. `action` is the honest answer; `reason` explains why.
 *  On a resume it names the continued execution (`resultId`) and how far it got (`reposDone`); on an
 *  explicit fresh start it names the `discarded` execution (and any open PR it left) instead of leaving
 *  it unnoticed. Omitted by sources without an executor (the mock), so consumers guard for undefined. */
export interface RunResumePlan {
  action: 'resume' | 'fresh';
  resultId?: string;
  reposDone: number;
  reason: string;
  discarded?: string;
  discardedPrUrl?: string;
}

/** One run the system is currently working, for the transparent "Aktive Läufe" overview: either
 *  EXECUTING right now or SUSPENDED on the usage limit mid-execution (paused, waiting to resume). Enough
 *  to render a list row — which run, on which repo/step, how far, how much spent — without a per-run
 *  fetch. Server-assembled from the live Activity + the run's result document; purely observational. */
export interface RunInFlight {
  runId: string;
  runName: string;
  type: RunType; // auto | todo
  state: 'executing' | 'suspended' | 'deferred'; // executing | paused on the usage limit | stood down to free a slot
  exclusive?: boolean; // executing: holds the whole floor (an auto sweep)
  overload?: boolean; // executing: in a temporary extra slot beyond the cap
  resultId?: string;
  startedAt?: string; // execution start (executing)
  resumeAt?: string; // when a suspended/deferred run resumes
  attempts?: number; // suspended: resume attempts so far
  currentRepo?: string; // the repo in flight (executing)
  currentStep?: string; // the step running right now (executing)
  reposDone: number; // repos already completed this execution
  reposTotal?: number; // known only for ToDos (exact target count); omitted for automatic runs
  inputTokens: number;
  outputTokens: number;
  costUsd: number;
  numTurns: number;
}

/** The full slot picture the /active endpoint returns (task point 8): the standing capacity and how much
 *  of it is used/free, how many runs overload beyond the cap right now, the live runs (each with its
 *  exclusive/overload flags), and the enriched inflight list — executing plus suspended and deferred runs,
 *  the deferred ones carrying their resume point. One read the UI mounts on and polls to follow live. */
export interface RunSlotOverview {
  capacity: number;
  used: number;
  free: number;
  overload: number;
  active: RunActive[];
  inflight: RunInFlight[];
}

/** The system's own pick of which run to stand down to free a slot — the one whose interruption loses the
 *  least and is easiest to resume — with a plain-language justification (task point 6). */
export interface RunDeferSuggestion {
  runId: string;
  runName?: string;
  reason: string;
}

/** Returned when a run cannot start because the slots are full: WHY it is blocked, the ways forward
 *  (task point 5), the automatic suggestion (task point 6), and the current slot state so the dialog needs
 *  no second request. `options` is a subset of queue | defer | overload. */
export interface RunStartDecision {
  blocked: 'running' | 'exclusive' | 'repo-busy' | 'cap';
  options: Array<'queue' | 'defer' | 'overload'>;
  suggestion?: RunDeferSuggestion;
  slots: RunSlotOverview;
}

/** One entry in the calendar — the union of past and upcoming runs. `type` separates automatic runs
 *  from ToDos (colour). A FUTURE firing carries its `schedule`; a completed (PAST) execution carries
 *  `resultId` and status (`ok`/`suspended`) instead. The presence of `resultId` marks an occurrence as
 *  past — the calendar shows its outcome and opens the full report on click. */
export interface RunOccurrence {
  runId: string;
  runName: string;
  type: RunType;
  at: string;
  /** Future firing: how the run recurs. Absent on a past execution. */
  schedule?: string;
  /** Past execution: its result id — opens the full status/report via mercuryRunResult. */
  resultId?: string;
  /** Past execution: pass/fail outcome. */
  ok?: boolean;
  /** Past execution: paused on the Claude usage limit. */
  suspended?: boolean;
}

export interface RunCalendar {
  from: string;
  to: string;
  occurrences: RunOccurrence[];
}

/** One completed execution in the execution history (with token/cost; survives run deletion). */
export interface RunExecution {
  runId: string;
  runName: string;
  type?: RunType; // auto|todo — the run's kind, so each surface shows only its own executions
  resultId: string;
  at: string;
  finishedAt?: string;
  ok: boolean;
  repoCount: number;
  inputTokens: number;
  outputTokens: number;
  costUsd: number;
  numTurns: number;
}

/** One turn of the free-form run-planning chat. */
export interface RunChatMessage {
  role: 'user' | 'assistant';
  content: string;
}

/** One destination of a proposed ToDo action: exactly an existing repo id OR a new repo name. */
export interface ActionTarget {
  repo?: string;
  newRepo?: string;
}

/** A single mutating operation the Mercury-wide assistant proposes for the user to review and apply.
 *  Each kind mirrors one operation of the Mercury UI and is applied through the SAME data-source method
 *  the UI uses — the chat opens no parallel path. Discriminated on `kind`. */
export type MercuryAction =
  | { kind: 'create_todo'; name: string; task: string; targets: ActionTarget[]; dueAt?: string }
  | { kind: 'create_run'; name: string; axiomIds: string[]; schedule: RunSchedule }
  | { kind: 'add_record'; section: 'axiome' | 'regeln' | 'laeufe' | 'meta'; titel: string; body: string }
  | { kind: 'edit_record'; path: string; titel: string; body: string }
  | { kind: 'delete_record'; path: string }
  | { kind: 'delete_run'; runId: string }
  | { kind: 'run_now'; runId: string }
  | { kind: 'plan_runs'; mode: 'fill' | 'replace'; runs: PlannedRun[] };

/** A chat reply, optionally carrying ONE reviewable action the model proposed. */
export interface RunChatReply {
  reply: string;
  action?: MercuryAction;
}

// ── Atlas — the deployed Holistic landscape ──────────────────────────────────

/** A deployed Holistic service, derived from its rights manifest and its Caddy route. */
export interface AtlasNode {
  id: string;
  /** The port it answers on; 0 when it has no route. */
  port: number;
  /** The hp_* groups it declares. */
  rights: string[];
  hasManifest: boolean;
  hasRoute: boolean;
  /** The repo it is built from, when the viewer can see one — '' otherwise. */
  repo: string;
}

/** An inconsistency between what is deployed and what is declared. */
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

/** A section of the repo "skeleton" overview rendered by StructureView. */
export interface StructureSection {
  title: string;
  hint: string;
  entries: { name: string; kind: 'dir' | 'file'; note: string }[];
}

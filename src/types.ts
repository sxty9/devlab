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
}

/** What the AI assistant returns after a run (proxied from aigentic). */
export interface AssistantReply {
  output: string;
  engine: string;
  model: string;
  usage: { inputTokens: number; outputTokens: number; totalTokens: number; truncated: boolean };
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
 *  axioms + all Laufregeln (`stale` = that snapshot drifted from the store); a todo's prompt is just
 *  its task — axioms and rules reach the agent through the repo's CLAUDE.md. */
export interface Run {
  id: string;
  name: string;
  type?: RunType; // absent = auto (records predating ToDos)
  enabled: boolean;
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
  stale?: boolean;
  // The run's still-open pull requests awaiting their merge to main. While non-empty a ToDo is not yet
  // "erledigt" — it stays in the active list as "wartet auf Merge" until the last PR lands (then Done).
  pendingPrs?: { repo: string; number: number; url: string }[];
  // Set when an execution paused on the Claude usage limit and will auto-resume once the window resets.
  suspended?: { resumeAt: string; resultId: string; attempts: number; reason?: string };
}

/** The editable fields of a run or todo (create/update payload). */
export interface RunInput {
  name: string;
  type?: RunType; // default 'auto'
  enabled: boolean;
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
}

export interface RunStep {
  name: string; // analyze | implement | push | pr | deploy
  running?: boolean; // in flight — while true, `log` carries the agent's streaming transcript
  ok: boolean;
  log?: string; // for analyze/implement this is the agent's full report (what was done / blocked)
  at: string;
}

export interface RepoResult {
  repo: string;
  running?: boolean; // the repo currently being worked (only ever set on RunResult.live)
  ok: boolean;
  deployed: boolean;
  prUrl?: string;
  steps: RunStep[] | null; // null when the repo failed before any step ran (Go marshals the empty slice as null)
  error?: string;
  inputTokens?: number;
  outputTokens?: number;
  costUsd?: number;
  numTurns?: number;
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

/** A chat reply, optionally carrying a reviewable run-plan the model proposed. */
export interface RunChatReply {
  reply: string;
  proposal?: RunPlan;
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

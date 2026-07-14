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

// ── Atlas — the deployed Holistic landscape ──────────────────────────────────

/** A deployed Holistic service, derived from its rights manifest and its Caddy route. */
export interface AtlasNode {
  id: string;
  /** The port it answers on; 0 when it has no route. */
  port: number;
  /** The hp_* groups it declares. */
  rights: string[] | null;
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

// Shared domain types for the DevLab workspace. Phase 1 is mock-data only; these shapes are
// deliberately close to what a real backend (git + sxgate + Claude) would return, so wiring
// the backend later is a swap of the data source, not a rewrite of the UI.

/** The four tools in the left icon rail. */
export type PanelId = 'project' | 'vcs' | 'claude' | 'terminal';

export type RepoKind = 'service' | 'repo' | 'library';

/** A selectable repository/service in the top-bar dropdown. */
export interface Repo {
  id: string;
  name: string;
  kind: RepoKind;
  description: string;
  /** Primary language label, e.g. "TypeScript", "Go", "Shell". */
  language: string;
  /** A design-token color name used as the repo's accent dot (accent | success | warning | gpu | net | ssd | ram). */
  tint: 'accent' | 'success' | 'warning' | 'gpu' | 'net' | 'ssd' | 'ram';
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
  kind: 'code' | 'structure';
  /** Present for kind: 'code'. */
  path?: string;
  lang?: string;
  dirty?: boolean;
}

export type StageState = 'done' | 'active' | 'pending';

/** A stage in the bottom delivery-pipeline bar (Vision → … → main merge). */
export interface Stage {
  id: string;
  label: string;
  state: StageState;
  hint: string;
}

/** Everything the workspace knows about one repo (mock in phase 1). */
export interface RepoData {
  branches: Branch[];
  tree: FileNode[];
  files: Record<string, FileContent>;
  changes: Change[];
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

/** A section of the repo "skeleton" overview rendered by StructureView. */
export interface StructureSection {
  title: string;
  hint: string;
  entries: { name: string; kind: 'dir' | 'file'; note: string }[];
}

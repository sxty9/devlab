import type { AiMessage, AssistantAsk, AssistantReply, Branch, Change, Comment, FileContent, Repo, RepoData, User, VisionFile } from '@/types';

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

  // ── Vision Catalog + threaded comments ─────────────────────────────────────
  vision(id: string): Promise<VisionFile[]>;
  /** Direct URL for an <img>/<iframe> to a raw vision file (bytes, correct MIME). */
  rawUrl(id: string, path: string): string;
  uploadVision(id: string, path: string, contentB64: string): Promise<VisionFile[]>;
  listComments(id: string, path: string): Promise<Comment[]>;
  addComment(id: string, path: string, body: string, parentId?: string): Promise<Comment>;
  deleteComment(id: string, commentId: string): Promise<void>;

  // ── AI assistant (proxied to aigentic, repo as context) ────────────────────
  askAssistant(id: string, ask: AssistantAsk): Promise<AssistantReply>;
  getAssistantHistory(id: string): Promise<AiMessage[]>;
  saveAssistantHistory(id: string, messages: AiMessage[]): Promise<void>;
}

/** Thrown by httpSource when the backend returns 401 (login required / expired). */
export class AuthRequiredError extends Error {
  constructor() {
    super('Authentication required');
    this.name = 'AuthRequiredError';
  }
}

import type { Change, FileContent, RepoData } from '@/types';
import { REPOS, REPO_DATA, DEFAULT_REPO_ID } from '@/mock/workspace';
import { guessLang } from '@/lib/lang';
import type { BranchResult, CommitResult, DataSource, DiffPayload, PushResult, WriteResult } from './source';

function stub(path: string): FileContent {
  return { path, lang: guessLang(path), code: `// ${path}\n// (mock) file contents arrive with the backend.\n` };
}

/** Fabricate a believable "before" for a modified file with no explicit diff content. */
function synthBefore(after: string): string {
  const lines = after.split('\n');
  if (lines.length <= 5) return lines.slice(0, Math.max(1, lines.length - 1)).join('\n');
  const out = lines.slice();
  const mid = Math.floor(out.length / 2);
  out.splice(mid, 2);
  const tweak = Math.min(3, out.length - 1);
  out[tweak] = `${out[tweak]}  // (previous)`;
  return out.join('\n');
}

const data = (id: string): RepoData => REPO_DATA[id] ?? REPO_DATA[DEFAULT_REPO_ID];

/** The bundled mock data source — the permanent offline/dev fallback. */
export const mockSource: DataSource = {
  async init() {
    return { mode: 'mock' as const, signedIn: true, canUseDevlab: true, githubLinked: true };
  },
  async getUser() {
    return { username: 'dev', displayName: 'Developer', isAdmin: true, canUseDevlab: true, githubLinked: true, githubLogin: 'dev' };
  },
  async repos() {
    return REPOS;
  },
  async repoData(id) {
    return data(id);
  },
  async fileContent(id, path) {
    return data(id).files[path] ?? stub(path);
  },
  async fileDiff(id, path): Promise<DiffPayload> {
    const d = data(id);
    const after = d.files[path]?.code ?? stub(path).code;
    const change = d.changes.find((c) => c.path === path);
    const lang = guessLang(path);
    if (change?.status === 'added' || change?.status === 'untracked') return { before: '', after, lang };
    if (change?.status === 'deleted') return { before: after, after: '', lang };
    return { before: d.diffBefore?.[path] ?? synthBefore(after), after, lang };
  },
  githubAuthorizeUrl() {
    return '#';
  },
  async unlinkGitHub() {
    /* mock: no-op */
  },

  // ── write loop: mutate the in-memory REPO_DATA so the offline loop is exercisable ──
  async ensureRepo() {
    /* mock: already "cloned" */
  },
  async saveFile(id, path, content): Promise<WriteResult> {
    const d = data(id);
    d.files[path] = { path, lang: guessLang(path), code: content };
    upsertChange(d, path, 'modified', false);
    return { changes: d.changes };
  },
  async stage(id, path): Promise<WriteResult> {
    setStaged(data(id), path, true);
    return { changes: data(id).changes };
  },
  async unstage(id, path): Promise<WriteResult> {
    setStaged(data(id), path, false);
    return { changes: data(id).changes };
  },
  async commit(id, message): Promise<CommitResult> {
    const d = data(id);
    d.changes = d.changes.filter((c) => !c.staged);
    void message;
    const branch = d.branches.find((b) => b.isDefault)?.name ?? d.branches[0]?.name ?? 'main';
    const b = d.branches.find((x) => x.name === branch);
    if (b) b.ahead += 1;
    return { hash: mockHash(), branch, changes: d.changes };
  },
  async push(id): Promise<PushResult> {
    const d = data(id);
    const branch = d.branches.find((b) => b.isDefault)?.name ?? d.branches[0]?.name ?? 'main';
    const b = d.branches.find((x) => x.name === branch);
    if (b) b.ahead = 0;
    return { branch, ahead: 0, behind: 0, message: 'Everything up-to-date (mock)', branches: d.branches };
  },
  async pull(id): Promise<PushResult> {
    const d = data(id);
    const branch = d.branches.find((b) => b.isDefault)?.name ?? d.branches[0]?.name ?? 'main';
    return { branch, ahead: 0, behind: 0, message: 'Already up to date (mock)', branches: d.branches };
  },
  async createBranch(id, name, from): Promise<BranchResult> {
    const d = data(id);
    void from;
    if (!d.branches.some((b) => b.name === name)) {
      d.branches.push({ name, isDefault: false, ahead: 0, behind: 0, updated: 'just now' });
    }
    return { branch: name, branches: d.branches };
  },
  async checkout(id, name): Promise<BranchResult> {
    return { branch: name, branches: data(id).branches };
  },
};

function upsertChange(d: RepoData, path: string, status: Change['status'], staged: boolean) {
  if (!d.changes.some((c) => c.path === path)) {
    d.changes = [...d.changes, { path, status, additions: 1, deletions: 0, staged }];
  }
}

function setStaged(d: RepoData, path: string, staged: boolean) {
  d.changes = d.changes.map((c) => (c.path === path ? { ...c, staged } : c));
}

function mockHash(): string {
  return Array.from({ length: 7 }, () => '0123456789abcdef'[Math.floor(Math.random() * 16)]).join('');
}

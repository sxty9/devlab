import type { Change, Comment, FileContent, RepoData, VisionFile } from '@/types';
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

  // ── Vision Catalog + comments (in-memory so the offline loop is exercisable) ──
  async vision(id): Promise<VisionFile[]> {
    return mockVision(id);
  },
  rawUrl(_id, path): string {
    const label = path.split('/').pop() ?? 'file';
    const svg = `<svg xmlns="http://www.w3.org/2000/svg" width="480" height="300"><rect width="100%" height="100%" fill="#1e2230"/><text x="50%" y="50%" fill="#8b93a7" font-family="sans-serif" font-size="15" text-anchor="middle">${label} (mock)</text></svg>`;
    return `data:image/svg+xml;utf8,${encodeURIComponent(svg)}`;
  },
  async uploadVision(id, path, _contentB64): Promise<VisionFile[]> {
    const name = path.split('/').pop() ?? path;
    const list = mockVision(id);
    if (!list.some((f) => f.path === path)) list.push({ path, name, kind: mockKind(name), size: 1024, status: 'untracked' });
    return list;
  },
  async listComments(id, path): Promise<Comment[]> {
    return (commentStore[id] ?? []).filter((c) => c.path === path);
  },
  async addComment(id, path, body, parentId): Promise<Comment> {
    const c: Comment = {
      id: mockHash(),
      path,
      parentId: parentId ?? '',
      author: 'dev',
      authorName: 'Developer',
      body,
      createdAt: new Date().toISOString(),
    };
    (commentStore[id] ??= []).push(c);
    return c;
  },
  async deleteComment(id, commentId): Promise<void> {
    const list = commentStore[id] ?? [];
    const remove = new Set([commentId]);
    for (let changed = true; changed; ) {
      changed = false;
      for (const c of list) if (!remove.has(c.id) && c.parentId && remove.has(c.parentId)) { remove.add(c.id); changed = true; }
    }
    commentStore[id] = list.filter((c) => !remove.has(c.id));
  },
};

const visionStore: Record<string, VisionFile[]> = {};
const commentStore: Record<string, Comment[]> = {};

function mockKind(name: string): VisionFile['kind'] {
  const ext = name.includes('.') ? name.split('.').pop()!.toLowerCase() : '';
  if (['png', 'jpg', 'jpeg', 'gif', 'webp', 'svg'].includes(ext)) return 'image';
  if (ext === 'pdf') return 'pdf';
  if (ext === 'md' || ext === 'markdown') return 'markdown';
  if (['txt', 'json', 'yaml', 'yml'].includes(ext)) return 'text';
  return 'other';
}

function mockVision(id: string): VisionFile[] {
  if (!visionStore[id]) {
    visionStore[id] = [
      { path: 'vision/overview.md', name: 'overview.md', kind: 'markdown', size: 240 },
      { path: 'vision/sketch.png', name: 'sketch.png', kind: 'image', size: 10240 },
    ];
  }
  return visionStore[id];
}

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

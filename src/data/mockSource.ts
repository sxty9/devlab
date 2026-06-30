import type { FileContent, RepoData } from '@/types';
import { REPOS, REPO_DATA, DEFAULT_REPO_ID } from '@/mock/workspace';
import { guessLang } from '@/lib/lang';
import type { DataSource, DiffPayload } from './source';

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
};

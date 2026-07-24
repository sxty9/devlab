import type { AgentReply, AiMessage, AssistantReply, Change, Comment, FileContent, RepoData, VisionFile } from '@/types';
import { REPOS, REPO_DATA, DEFAULT_REPO_ID } from '@/mock/workspace';
import { basename, guessLang, visionKind } from '@/lib/lang';
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
  async openPR(id) {
    const branch = data(id).branches.find((b) => !b.isDefault)?.name ?? 'feature';
    return { number: 42, url: 'https://github.com/example/repo/pull/42', state: 'open', title: branch, branch, base: 'main', existed: false };
  },

  // ── Vision Catalog + comments (in-memory so the offline loop is exercisable) ──
  async vision(id): Promise<VisionFile[]> {
    return mockVision(id);
  },
  rawUrl(_id, path): string {
    const label = basename(path) || 'file';
    const svg = `<svg xmlns="http://www.w3.org/2000/svg" width="480" height="300"><rect width="100%" height="100%" fill="#1e2230"/><text x="50%" y="50%" fill="#8b93a7" font-family="sans-serif" font-size="15" text-anchor="middle">${label} (mock)</text></svg>`;
    return `data:image/svg+xml;utf8,${encodeURIComponent(svg)}`;
  },
  async uploadVision(id, path, _contentB64): Promise<VisionFile[]> {
    const name = basename(path);
    const list = mockVision(id);
    if (!list.some((f) => f.path === path)) list.push({ path, name, kind: visionKind(name), size: 1024, status: 'untracked' });
    return list;
  },
  async deleteVision(id, path): Promise<VisionFile[]> {
    visionStore[id] = mockVision(id).filter((f) => f.path !== path);
    return visionStore[id];
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
  async askAssistant(_id, ask): Promise<AssistantReply> {
    const last = ask.prompt.slice(0, 80);
    return {
      output: `**(mock AI)** I'd help with “${last}” using this repo as context — the aigentic backend answers here in production.\n\n\`\`\`\n// example\n\`\`\``,
      engine: 'mock',
      model: 'mock',
      usage: { inputTokens: 0, outputTokens: 0, totalTokens: 0, truncated: false },
    };
  },
  async askAgent(_id, ask): Promise<AgentReply> {
    return {
      output: `**(mock agent)** In production I'd run the full Claude CLI in this workspace (mode: ${ask.mode ?? 'auto'}) and edit files directly.`,
      sessionId: 'mock-session',
      costUsd: 0,
      numTurns: 0,
      isError: false,
      changes: [],
    };
  },
  async getAssistantHistory(id): Promise<AiMessage[]> {
    return aiStore[id] ?? [];
  },
  async saveAssistantHistory(id, messages): Promise<void> {
    aiStore[id] = messages;
  },
  async assistantModels() {
    return {
      claude: [
        { id: 'claude-opus-4-8', label: 'Opus' },
        { id: 'claude-sonnet-4-6', label: 'Sonnet' },
        { id: 'claude-haiku-4-5-20251001', label: 'Haiku' },
      ],
      ollama: ['llama3.1', 'qwen2.5-coder'],
    };
  },
  terminalUrl() {
    return null; // no live shell offline — the panel shows a mock notice
  },

  async atlas() {
    // A stand-in for the host's own config, so `vite dev` renders Atlas offline.
    return {
      nodes: [
        { id: 'aigentic', port: 8780, rights: ['hp_aigentic_api', 'hp_aigentic_run'], hasManifest: true, hasRoute: true, repo: '' },
        { id: 'contax', port: 8777, rights: [], hasManifest: true, hasRoute: true, repo: '' },
        { id: 'devlab', port: 0, rights: ['hp_devlab_access'], hasManifest: true, hasRoute: false, repo: 'devlab' },
        { id: 'hostek', port: 8771, rights: ['hp_hostek_proc', 'hp_hostek_disks'], hasManifest: true, hasRoute: true, repo: 'hostek' },
        { id: 'icaly', port: 8776, rights: ['hp_icaly_view', 'hp_icaly_edit'], hasManifest: true, hasRoute: true, repo: '' },
        { id: 'mail', port: 8775, rights: ['hp_mail_read', 'hp_mail_send'], hasManifest: true, hasRoute: true, repo: '' },
        { id: 'notify', port: 8778, rights: ['hp_notify_broadcast'], hasManifest: true, hasRoute: true, repo: '' },
        { id: 'privleg', port: 8772, rights: ['hp_privleg_admin'], hasManifest: true, hasRoute: true, repo: '' },
        { id: 'remshel', port: 8774, rights: [], hasManifest: true, hasRoute: true, repo: 'sxgate' },
      ],
      findings: [
        { severity: 'warn', message: 'devlab liefert ein Rechte-Manifest, ist aber nicht geroutet.', nodes: ['devlab'] },
        { severity: 'warn', message: 'aigentic ist deployed, liegt aber außerhalb des DevLab-Repo-Katalogs.', nodes: ['aigentic'] },
        { severity: 'warn', message: 'mail ist deployed, liegt aber außerhalb des DevLab-Repo-Katalogs.', nodes: ['mail'] },
      ],
      scannedAt: '2026-07-14T09:00:00Z',
    };
  },

  async mercuryTree() {
    const leaf = (name: string, path: string) => ({ name, path, isAxiom: true });
    return {
      axiome: [
        {
          name: 'architektur',
          path: 'axiome/architektur',
          isAxiom: false,
          children: [
            {
              name: 'ssot',
              path: 'axiome/architektur/ssot',
              isAxiom: false,
              children: [
                leaf('atomare-zugriffe', 'axiome/architektur/ssot/atomare-zugriffe.md'),
                leaf('kein-paralleler-datenpfad', 'axiome/architektur/ssot/kein-paralleler-datenpfad.md'),
              ],
            },
            leaf('passive-pools', 'axiome/architektur/passive-pools.md'),
          ],
        },
        {
          name: 'minimalismus',
          path: 'axiome/minimalismus',
          isAxiom: false,
          children: [leaf('keine-tooltips', 'axiome/minimalismus/keine-tooltips.md')],
        },
      ],
      regeln: [
        { name: 'go', path: 'regeln/go', isAxiom: false, children: [leaf('fehler-wrappen', 'regeln/go/fehler-wrappen.md')] },
      ],
      laeufe: [],
      meta: [leaf('implementation-standard', 'meta/implementation-standard.md')],
    };
  },
  async mercuryItem(path: string) {
    return {
      id: 'ax_mock01',
      titel: path.split('/').pop()?.replace('.md', '') ?? 'Axiom',
      quelle: 'axioms/CLAUDE.MD.md#holistic_architecture_maxims/Single Source of Truth',
      body: 'Existiert für die Entität bereits ein Zugangspunkt? Zwingend wiederverwenden. Baue niemals parallele Datenpfade.',
    };
  },
  async mercuryAddAxiom(titel: string, _body: string, section?: string, _force?: boolean) {
    const ns = section || 'axiome';
    return { path: `${ns}/unsortiert/${titel.toLowerCase().replace(/\s+/g, '-')}.md`, id: 'ax_mocknew', classified: false };
  },
  async mercuryOptimize(titel: string, body: string, _section?: string) {
    return { titel, body };
  },
  async mercuryConform(_titel: string, _body: string) {
    return { conforms: true, violations: [], metaCount: 0 };
  },
  async mercuryEditAxiom(path: string, titel: string, body: string) {
    return { path, axiom: { id: 'ax_mock01', titel, body } };
  },
  async mercuryMoveAxiom(_from: string, to: string) {
    return { path: to };
  },
  async mercuryDeleteAxiom(_path: string) {
    /* mock: no-op */
  },
  async mercuryMoveCategory(_from: string, _to: string) {
    return { moved: 0 };
  },
  async mercuryReorder(_category: string, _order: string[]) {
    /* mock: no-op */
  },
  async mercuryRuns() {
    return { runs: [], axioms: {} };
  },
  async mercuryRun(id: string) {
    return {
      run: {
        id,
        name: 'Mock-Lauf',
        enabled: true,
        schedule: { kind: 'daily' as const, timeOfDay: '03:00' },
        axiomIds: [],
        prompt: '',
        createdAt: new Date().toISOString(),
        updatedAt: new Date().toISOString(),
      },
      axioms: {},
    };
  },
  async mercuryRunPrompt(_id: string) {
    return { prompt: '# Mock-Prompt' };
  },
  async mercuryRunCoverage() {
    return { covered: {}, index: {}, axioms: {} };
  },
  async mercuryCreateRun(body: import('@/types').RunInput) {
    return { id: 'run_mock', ...mockRun(body) };
  },
  async mercuryUpdateRun(id: string, body: import('@/types').RunInput) {
    return { id, ...mockRun(body) };
  },
  async mercuryDeleteRun(_id: string) {
    /* mock: no-op */
  },
  async mercuryRecomposeRun(_id: string) {
    /* mock: no-op */
  },
  async mercuryRunAiFill() {
    return { proposal: { runs: [] }, axioms: {} };
  },
  async mercuryRunAiFinetune() {
    return { proposal: { runs: [] }, axioms: {} };
  },
  async mercuryApplyRunProposal(_mode: 'fill' | 'replace', _plan: import('@/types').RunPlan) {
    /* mock: no-op */
  },
  async mercuryRunHistory() {
    return { snapshots: [] };
  },
  async mercuryRestoreRunHistory(_ts: string) {
    /* mock: no-op */
  },
  async mercuryRunResults(_id: string) {
    return { results: [] };
  },
  async mercuryRunResult(id: string, resultId: string) {
    return { runId: id, resultId, startedAt: new Date().toISOString(), ok: true, repos: [], inputTokens: 0, outputTokens: 0, costUsd: 0, numTurns: 0 };
  },
  async mercuryRunCalendar(_days?: number, _type?: import('@/types').RunType) {
    return { from: new Date().toISOString(), to: new Date().toISOString(), occurrences: [] };
  },
  async mercuryRunExecutions(_type?: import('@/types').RunType) {
    return { executions: [] };
  },
  async mercuryChat(_messages: import('@/types').RunChatMessage[]) {
    return { reply: 'Mock-Antwort' };
  },
  async mercuryRunNow(_id: string) {
    return { started: true };
  },
  async mercuryCancelRun() {
    /* mock: no-op */
  },
  async mercuryUploadAttachment(_id: string, _filename: string, _contentB64: string): Promise<import('@/types').RunAttachment[]> {
    return [];
  },
  async mercuryDeleteAttachment(_id: string, _attachmentId: string): Promise<import('@/types').RunAttachment[]> {
    return [];
  },
  mercuryAttachmentRawUrl(id: string, attachmentId: string) {
    return `/api/mercury/runs/${id}/attachments/${attachmentId}/raw`;
  },
};

/** A RunInput carries only what its type needs (a ToDo has no schedule/axioms), while a Run always
 *  has both — fill the gaps so the mock returns a well-formed Run. */
function mockRun(body: import('@/types').RunInput) {
  const { dueAt, ...rest } = body; // dueAt is nullable on the way IN (null clears it), never on a Run
  return {
    ...rest,
    ...(dueAt ? { dueAt } : {}),
    schedule: body.schedule ?? { kind: 'daily' as const, timeOfDay: '03:00' },
    axiomIds: body.axiomIds ?? [],
    prompt: '',
    createdAt: new Date().toISOString(),
    updatedAt: new Date().toISOString(),
  };
}

const visionStore: Record<string, VisionFile[]> = {};
const commentStore: Record<string, Comment[]> = {};
const aiStore: Record<string, AiMessage[]> = {};

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

import type { AgentReply, AiMessage, AssistantReply, Change, Comment, FileContent, MercuryNode, MercuryTree, RepoData, Run, RunInput, RunNotice, VisionFile } from '@/types';
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
        { id: 'claude-fable-5', label: 'Fable' },
        { id: 'claude-opus-4-8', label: 'Opus' },
        { id: 'claude-fable-5', label: 'Fable' },
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
    // A fresh deep copy each read: setTree must see a NEW reference or React skips the re-render — the
    // very reason a freshly added axiom would not appear. Adds/removes mutate axiomTree (see below).
    return cloneTree();
  },
  async mercuryItem(path: string) {
    return {
      id: 'ax_mock01',
      titel: path.split('/').pop()?.replace('.md', '') ?? 'Axiom',
      quelle: 'axioms/CLAUDE.MD.md#holistic_architecture_maxims/Single Source of Truth',
      body: 'Existiert für die Entität bereits ein Zugangspunkt? Zwingend wiederverwenden. Baue niemals parallele Datenpfade.',
      author: { createdBy: MOCK_ACTOR, createdAt: new Date().toISOString(), updatedBy: MOCK_ACTOR, updatedAt: new Date().toISOString() },
    };
  },
  async mercuryAddAxiom(titel: string, _body: string, section?: string, _force?: boolean) {
    const ns = section || 'axiome';
    const path = `${ns}/unsortiert/${titel.toLowerCase().replace(/\s+/g, '-')}.md`;
    treeInsert(path); // the new record shows up in the tree at once, as it does against the backend
    return { path, id: mockId('ax'), classified: false };
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
  async mercuryDeleteAxiom(path: string) {
    treeRemove(path);
  },
  async mercuryMoveCategory(_from: string, _to: string) {
    return { moved: 0 };
  },
  async mercuryReorder(_category: string, _order: string[]) {
    /* mock: no-op */
  },
  async mercuryRolloutStatus() {
    return { last: null };
  },
  async mercuryRuns() {
    // Fresh copies so setList always sees a new reference (and can't mutate the store by ref).
    return { runs: runStore.map((r) => ({ ...r })), axioms: {} };
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
    return { covered: {}, index: {}, axioms: {}, pending: false };
  },
  async mercuryRunNotices() {
    return { notices: noticeStore.map((n) => ({ ...n })) };
  },
  async mercuryDismissRunNotice(id: string) {
    const idx = noticeStore.findIndex((n) => n.id === id);
    if (idx >= 0) noticeStore.splice(idx, 1);
  },
  async mercuryClearRunNotices() {
    noticeStore.length = 0;
  },
  async mercuryCreateRun(body: RunInput) {
    const run: Run = { id: mockId('run'), ...mockRun(body) };
    runStore.push(run); // appended → the next mercuryRuns() reload lists it, so it appears at once
    return run;
  },
  async mercuryUpdateRun(id: string, body: RunInput) {
    const idx = runStore.findIndex((r) => r.id === id);
    const run: Run = {
      ...mockRun(body),
      id,
      createdAt: idx >= 0 ? runStore[idx].createdAt : new Date().toISOString(),
      updatedAt: new Date().toISOString(),
      // Preserve the original creator across an edit; only the last editor changes.
      createdBy: idx >= 0 ? runStore[idx].createdBy : MOCK_ACTOR,
      updatedBy: MOCK_ACTOR,
    };
    if (idx >= 0) runStore[idx] = run;
    else runStore.push(run);
    return run;
  },
  async mercuryDeleteRun(id: string) {
    const idx = runStore.findIndex((r) => r.id === id);
    if (idx >= 0) runStore.splice(idx, 1);
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
    // A believable sample so the offline surface shows the authorship history: a recent change by the
    // signed-in user, and an older one with no recorded actor ("?") — surfaced as unknown, not guessed.
    return {
      snapshots: [
        { ts: new Date().toISOString(), action: 'update', actor: MOCK_ACTOR, runCount: 2 },
        { ts: new Date(Date.now() - 864e5).toISOString(), action: 'create', actor: '?', runCount: 1 },
      ],
    };
  },
  async mercuryRestoreRunHistory(_ts: string) {
    /* mock: no-op */
  },
  async mercuryRunResults(_id: string) {
    return { results: [] };
  },
  async mercuryRunResult(id: string, resultId: string) {
    const at = new Date().toISOString();
    return {
      runId: id,
      resultId,
      startedAt: new Date().toISOString(),
      ok: !resultId.includes('fail'), // keep the preview coherent with the calendar's status pill
      prompt: '# Konkretes ToDo: Beispiel\n\n## Aufgabe\n\nBeispielhafte Promptstellung dieser Ausführung.',
      // One repo showing the growing dev state: the delivered state is named (mercury-dev@<sha>) and the
      // delivery's stacked PR sits on the previous open delivery's branch.
      repos: [
        {
          repo: 'holistic/example',
          ok: true,
          deployed: true,
          prUrl: 'https://example.invalid/pull/42',
          devBranch: 'mercury-dev',
          devCommit: '1a2b3c4d5e6f7a8b',
          prBase: 'mercury-run/run_example/dlv_prev',
          deliveryId: 'dlv_example',
          steps: [
            { name: 'fold', ok: true, log: 'Standard-Branch main eingefaltet', at },
            { name: 'implement', ok: true, log: 'Feature umgesetzt und committet.', at },
            { name: 'dev-deploy', ok: true, log: 'Ausgelieferter Stand: mercury-dev@1a2b3c4d', at },
            { name: 'push', ok: true, log: 'mercury-dev, mercury-run/run_example/dlv_example', at },
            { name: 'pr', ok: true, log: 'https://example.invalid/pull/42 (Basis: mercury-run/run_example/dlv_prev)', at },
          ],
          inputTokens: 0,
          outputTokens: 0,
          costUsd: 0,
          numTurns: 0,
        },
      ],
      inputTokens: 0,
      outputTokens: 0,
      costUsd: 0,
      numTurns: 0,
      // An autonomous run that still names the person it acted for (the "name both" case).
      trigger: { auto: true },
      requestedBy: MOCK_ACTOR,
    };
  },
  async mercuryRunCalendar(days?: number, type?: import('@/types').RunType) {
    const dayMs = 86400000;
    const iso = (offsetDays: number) => new Date(Date.now() + offsetDays * dayMs).toISOString();
    // A representative union so the offline preview shows past executions (each with a status pill and
    // openable via mercuryRunResult) beside upcoming firings. resultId present ⇒ past; absent ⇒ upcoming.
    const all: import('@/types').RunOccurrence[] = [
      { runId: 'run_auto_demo', runName: 'Nightly Axiom-Rollout', type: 'auto', at: iso(-3), resultId: 'res_demo_1', ok: true },
      { runId: 'run_auto_demo', runName: 'Nightly Axiom-Rollout', type: 'auto', at: iso(-1), resultId: 'res_demo_fail', ok: false },
      { runId: 'run_auto_demo', runName: 'Nightly Axiom-Rollout', type: 'auto', at: iso(1), schedule: 'täglich 03:00' },
      { runId: 'run_todo_demo', runName: 'ToDo: Kalender-Union', type: 'todo', at: iso(-2), resultId: 'res_demo_3', ok: true },
      { runId: 'run_todo_demo', runName: 'ToDo: Kalender-Union', type: 'todo', at: iso(2), schedule: 'einmalig' },
    ];
    const occurrences = type ? all.filter((o) => o.type === type) : all;
    return { from: iso(-3), to: iso(days ?? 30), occurrences };
  },
  async mercuryRunExecutions(_type?: import('@/types').RunType) {
    return { executions: [] };
  },
  async mercuryChat(messages: import('@/types').RunChatMessage[]) {
    // Offline stand-in: infer a plausible action from the last message so the review-and-apply flow is
    // demonstrable without the backend. The real assistant derives the action from the model.
    const last = messages[messages.length - 1]?.content.toLowerCase() ?? '';
    let action: import('@/types').MercuryAction | undefined;
    if (/todo|aufgabe|bug/.test(last)) {
      action = { kind: 'create_todo', name: 'Neues ToDo', task: last.slice(0, 200) || 'Beschreibung', targets: [{ repo: DEFAULT_REPO_ID }] };
    } else if (/axiom|regel/.test(last)) {
      action = { kind: 'add_record', section: 'axiome', titel: 'Neues Axiom', body: last.slice(0, 200) || 'Ein Satz.' };
    } else if (/lauf|läufe|plan/.test(last)) {
      action = { kind: 'create_run', name: 'Neuer Lauf', axiomIds: ['ax_mock'], schedule: { kind: 'daily', timeOfDay: '03:00' } };
    }
    return action ? { reply: 'Mock-Antwort mit Vorschlag.', action } : { reply: 'Mock-Antwort' };
  },
  async mercuryReportStatus() {
    return { records: [] as import('@/types').ReportDelivery[] };
  },
  async mercuryRunNow(_id: string) {
    return { started: true };
  },
  async mercuryRunActive() {
    // The mock has no executor, so nothing is ever genuinely in flight — an empty list is the honest
    // answer (the "Aktive Läufe" overview simply stays quiet in offline/preview mode).
    return { active: [], inflight: [] };
  },
  async mercuryCancelRun(_runId: string) {
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
    createdBy: MOCK_ACTOR,
    updatedBy: MOCK_ACTOR,
  };
}

/** The mock's signed-in user (mirrors getUser) — the author stamped on runs/ToDos created offline. */
const MOCK_ACTOR = 'dev';

const visionStore: Record<string, VisionFile[]> = {};
const commentStore: Record<string, Comment[]> = {};
const aiStore: Record<string, AiMessage[]> = {};

// --- Mercury is stateful in the mock, exactly like commentStore/visionStore above: a newly added
//     ToDo, Lauf or axiom must appear in its list at once — the same way it does against the real
//     backend. (Before this these were dead stubs, so an added element never showed until you gave up
//     and it was simply lost — the "neu hinzugefügte Elemente aktualisieren sich nicht" bug.) These
//     are passive in-memory pools; every read returns a FRESH copy so setList/setTree re-render. ---
let mockSeq = 0;
const mockId = (prefix: string) => `${prefix}_mock${(mockSeq += 1)}`;

/** ToDos + Läufe, discriminated by `type` — the one store the backend keeps in runs.json. Starts empty,
 *  like a fresh instance; create/update/delete below keep it in sync so the list reflects every change. */
const runStore: Run[] = [];

/** The automatic axiom→run assignment feed — seeded with one believable entry so the panel is visible in
 *  preview; dismiss/clear mutate it. The real feed is written by the backend's background assigner. */
const noticeStore: RunNotice[] = [
  {
    id: 'ntc_mock1',
    at: new Date(Date.now() - 90_000).toISOString(),
    kind: 'assigned',
    runId: 'run_mock_seed',
    runName: 'Architektur & Minimalismus',
    newRun: true,
    axiomIds: ['ax_mock_seed'],
    axioms: ['Portionierte Daten'],
  },
];

/** The axiom / Implementierungsregeln / Läufe / Meta tree the sidebar renders — seeded with a believable
 *  sample, then mutated by add/delete so the sidebar reflects changes live. Mirrors the backend, which
 *  derives the tree from the record paths. */
const axiomTree: MercuryTree = {
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
            { name: 'atomare-zugriffe', path: 'axiome/architektur/ssot/atomare-zugriffe.md', isAxiom: true },
            { name: 'kein-paralleler-datenpfad', path: 'axiome/architektur/ssot/kein-paralleler-datenpfad.md', isAxiom: true },
          ],
        },
        { name: 'passive-pools', path: 'axiome/architektur/passive-pools.md', isAxiom: true },
      ],
    },
    {
      name: 'minimalismus',
      path: 'axiome/minimalismus',
      isAxiom: false,
      children: [{ name: 'keine-tooltips', path: 'axiome/minimalismus/keine-tooltips.md', isAxiom: true }],
    },
  ],
  regeln: [
    { name: 'go', path: 'regeln/go', isAxiom: false, children: [{ name: 'fehler-wrappen', path: 'regeln/go/fehler-wrappen.md', isAxiom: true }] },
  ],
  laeufe: [],
  meta: [{ name: 'implementation-standard', path: 'meta/implementation-standard.md', isAxiom: true }],
};

function cloneNode(n: MercuryNode): MercuryNode {
  return n.children
    ? { name: n.name, path: n.path, isAxiom: n.isAxiom, children: n.children.map(cloneNode) }
    : { name: n.name, path: n.path, isAxiom: n.isAxiom };
}
/** A fresh copy each read — a new object graph so setTree always sees a changed reference. */
function cloneTree(): MercuryTree {
  return {
    axiome: axiomTree.axiome.map(cloneNode),
    regeln: axiomTree.regeln.map(cloneNode),
    laeufe: axiomTree.laeufe.map(cloneNode),
    meta: axiomTree.meta.map(cloneNode),
  };
}

/** Insert a leaf at `path`, creating category nodes along the way — the freshly added record then
 *  appears in the sidebar, the same tree the backend derives from its record paths. */
function treeInsert(path: string): void {
  const segs = path.split('/').filter(Boolean);
  if (segs.length < 2) return;
  const ns = segs[0] as keyof MercuryTree;
  let level = axiomTree[ns];
  if (!level) return;
  let prefix = ns;
  for (let i = 1; i < segs.length; i += 1) {
    prefix += `/${segs[i]}`;
    const isLeaf = i === segs.length - 1;
    const name = isLeaf ? segs[i].replace(/\.md$/, '') : segs[i];
    let node = level.find((n) => n.name === name && n.isAxiom === isLeaf);
    if (!node) {
      node = isLeaf ? { name, path: prefix, isAxiom: true } : { name, path: prefix, isAxiom: false, children: [] };
      level.push(node);
    }
    if (!isLeaf) {
      if (!node.children) node.children = [];
      level = node.children;
    }
  }
}

/** Remove the node at `path` (a leaf, or a category with its subtree) from the tree. */
function treeRemove(path: string): void {
  const segs = path.split('/').filter(Boolean);
  if (segs.length < 2) return;
  const ns = segs[0] as keyof MercuryTree;
  let level = axiomTree[ns];
  if (!level) return;
  for (let i = 1; i < segs.length - 1; i += 1) {
    const cat = level.find((n) => !n.isAxiom && n.name === segs[i]);
    if (!cat?.children) return;
    level = cat.children;
  }
  const leaf = segs[segs.length - 1].replace(/\.md$/, '');
  const idx = level.findIndex((n) => n.name === leaf);
  if (idx >= 0) level.splice(idx, 1);
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

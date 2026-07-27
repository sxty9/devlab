import {
  AuthRequiredError,
  type BranchResult,
  type CommitResult,
  type DataSource,
  type DiffPayload,
  type InitResult,
  type PushResult,
  type WriteResult,
} from './source';
import type { AgentReply, AiMessage, AiModelCatalog, AssistantReply, Comment, PullRequestResult, User, VisionFile } from '@/types';
import { openLiveStream, type LiveEventSource } from '@/lib/live';

const opts: RequestInit = { credentials: 'include', cache: 'no-store' };

function csrfToken(): string {
  const m = document.cookie.match(/(?:^|;\s*)h_csrf=([^;]+)/);
  return m ? decodeURIComponent(m[1]) : '';
}

// The access token (h_access) is short-lived (15 min); the shared h_refresh cookie (path
// /api/auth) is exchanged for a fresh access token at POST /api/auth/refresh. Single-flight the
// refresh so a burst of concurrent 401s re-mints only once. devlabd's refresh is mint-only (it
// does not rotate h_refresh), so racing calls are harmless, but coalescing avoids the stampede.
let refreshing: Promise<boolean> | null = null;

function refreshSession(): Promise<boolean> {
  if (!refreshing) {
    refreshing = fetch('/api/auth/refresh', {
      ...opts,
      method: 'POST',
      headers: { 'X-CSRF-Token': csrfToken() },
    })
      .then((r) => r.ok)
      .catch(() => false)
      .finally(() => {
        refreshing = null;
      });
  }
  return refreshing;
}

// fetch with transparent single-retry after a refresh. The refresh call itself goes direct (not
// through here), so there is no recursion to guard against.
async function request(input: string, init: RequestInit = {}, retry = true): Promise<Response> {
  const res = await fetch(input, { ...opts, ...init });
  if (res.status === 401 && retry && (await refreshSession())) {
    // Re-read of the rotated CSRF cookie happens inside the caller's withCsrf(), if any.
    return request(input, withFreshCsrf(init), false);
  }
  return res;
}

// If the original init carried an X-CSRF-Token (a mutating call), refresh it from the cookie the
// refresh just re-minted; otherwise leave init untouched.
function withFreshCsrf(init: RequestInit): RequestInit {
  const h = init.headers as Record<string, string> | undefined;
  if (h && 'X-CSRF-Token' in h) {
    return { ...init, headers: { ...h, 'X-CSRF-Token': csrfToken() } };
  }
  return init;
}

async function json<T>(res: Response): Promise<T> {
  if (res.status === 401) throw new AuthRequiredError();
  if (!res.ok) {
    let detail = res.statusText;
    try {
      detail = (await res.json()).detail ?? detail;
    } catch {
      /* ignore */
    }
    throw new Error(detail);
  }
  if (res.status === 204) return undefined as T;
  return res.json() as Promise<T>;
}

const enc = encodeURIComponent;

/** Talks to the devlabd backend over /api (same-origin, Holistic SSO cookie auth). */
export const httpSource: DataSource = {
  async init(): Promise<InitResult> {
    try {
      const h = await fetch('/api/health', opts);
      if (!h.ok) return { mode: 'mock', signedIn: true, canUseDevlab: true, githubLinked: true };
      // /api/user requires only a session (not the right), so we can tell "signed in but no
      // access" from "not signed in". request() refreshes once on 401 so an expired access
      // token (with a still-valid refresh) does not bounce the user to the login gate.
      const u = await request('/api/user');
      if (u.status === 401) return { mode: 'api', signedIn: false, canUseDevlab: false, githubLinked: false };
      if (!u.ok) return { mode: 'api', signedIn: true, canUseDevlab: false, githubLinked: false };
      const body = (await u.json()) as { canUseDevlab?: boolean; githubLinked?: boolean };
      return { mode: 'api', signedIn: true, canUseDevlab: !!body.canUseDevlab, githubLinked: !!body.githubLinked };
    } catch {
      // Backend unreachable (offline dev) → caller falls back to mock.
      return { mode: 'mock', signedIn: true, canUseDevlab: true, githubLinked: true };
    }
  },

  async getUser(): Promise<User> {
    return json(await request('/api/user'));
  },

  async repos() {
    return json(await request('/api/repos'));
  },

  async repoData(id, branch) {
    const q = branch ? `?branch=${enc(branch)}` : '';
    return json(await request(`/api/repos/${enc(id)}${q}`));
  },

  async fileContent(id, path) {
    return json(await request(`/api/repos/${enc(id)}/file?path=${enc(path)}`));
  },

  async fileDiff(id, path): Promise<DiffPayload> {
    return json(await request(`/api/repos/${enc(id)}/diff?path=${enc(path)}`));
  },

  githubAuthorizeUrl() {
    return '/api/github/authorize';
  },

  async unlinkGitHub() {
    await json<void>(await request('/api/github/unlink', withCsrf({ method: 'POST' })));
  },

  async ensureRepo(id) {
    await json<void>(await post(`/api/repos/${enc(id)}/ensure`));
  },
  async saveFile(id, path, content): Promise<WriteResult> {
    return json(await post(`/api/repos/${enc(id)}/file`, { path, content }));
  },
  async stage(id, path): Promise<WriteResult> {
    return json(await post(`/api/repos/${enc(id)}/stage`, { path }));
  },
  async unstage(id, path): Promise<WriteResult> {
    return json(await post(`/api/repos/${enc(id)}/unstage`, { path }));
  },
  async commit(id, message): Promise<CommitResult> {
    return json(await post(`/api/repos/${enc(id)}/commit`, { message }));
  },
  async push(id): Promise<PushResult> {
    return json(await post(`/api/repos/${enc(id)}/push`));
  },
  async pull(id): Promise<PushResult> {
    return json(await post(`/api/repos/${enc(id)}/pull`));
  },
  async createBranch(id, name, from): Promise<BranchResult> {
    return json(await post(`/api/repos/${enc(id)}/branch`, { name, from }));
  },
  async checkout(id, name): Promise<BranchResult> {
    return json(await post(`/api/repos/${enc(id)}/checkout`, { name }));
  },
  async openPR(id, title, body): Promise<PullRequestResult> {
    return json(await post(`/api/repos/${enc(id)}/pr`, { title: title ?? '', body: body ?? '' }));
  },

  async vision(id): Promise<VisionFile[]> {
    return json(await request(`/api/repos/${enc(id)}/vision`));
  },
  rawUrl(id, path) {
    return `/api/repos/${enc(id)}/raw?path=${enc(path)}`;
  },
  async uploadVision(id, path, contentB64): Promise<VisionFile[]> {
    return json(await post(`/api/repos/${enc(id)}/vision/upload`, { path, contentB64 }));
  },
  async deleteVision(id, path): Promise<VisionFile[]> {
    return json(await post(`/api/repos/${enc(id)}/vision/delete`, { path }));
  },
  async listComments(id, path): Promise<Comment[]> {
    return json(await request(`/api/repos/${enc(id)}/comments?path=${enc(path)}`));
  },
  async addComment(id, path, body, parentId): Promise<Comment> {
    return json(await post(`/api/repos/${enc(id)}/comments`, { path, body, parentId: parentId ?? '' }));
  },
  async deleteComment(id, commentId): Promise<void> {
    await json<void>(await request(`/api/repos/${enc(id)}/comments/${enc(commentId)}`, withCsrf({ method: 'DELETE' })));
  },

  async askAssistant(id, ask): Promise<AssistantReply> {
    return json(await post(`/api/repos/${enc(id)}/assistant`, ask));
  },
  async askAgent(id, ask): Promise<AgentReply> {
    return json(await post(`/api/repos/${enc(id)}/agent`, ask));
  },
  async getAssistantHistory(id): Promise<AiMessage[]> {
    return json(await request(`/api/repos/${enc(id)}/assistant/history`));
  },
  async saveAssistantHistory(id, messages): Promise<void> {
    await json<void>(await post(`/api/repos/${enc(id)}/assistant/history`, { messages }, 'PUT'));
  },
  async assistantModels(): Promise<AiModelCatalog> {
    return json(await request('/api/assistant/models'));
  },

  terminalUrl(id) {
    const proto = location.protocol === 'https:' ? 'wss' : 'ws';
    return `${proto}://${location.host}/api/repos/${enc(id)}/pty`;
  },

  async atlas() {
    return json(await request('/api/atlas'));
  },

  async mercuryTree() {
    return json(await request('/api/mercury/tree'));
  },
  async mercuryItem(path) {
    const r = await request(`/api/mercury/item?path=${encodeURIComponent(path)}`);
    return (await json<{ axiom: import('@/types').Axiom }>(r)).axiom;
  },
  async mercuryAddAxiom(titel, body, section, force) {
    const r = await post('/api/mercury/axiom', { titel, body, section, force });
    return json(r);
  },
  async mercuryOptimize(titel, body, section) {
    return json(await post('/api/mercury/optimize', { titel, body, section }));
  },
  async mercuryConform(titel, body) {
    return json(await post('/api/mercury/conform', { titel, body }));
  },
  async mercuryEditAxiom(path, titel, body) {
    return json(await post('/api/mercury/axiom', { path, titel, body }, 'PUT'));
  },
  async mercuryMoveAxiom(from, to) {
    return json(await post('/api/mercury/move', { from, to }));
  },
  async mercuryDeleteAxiom(path) {
    await json<void>(await request(`/api/mercury/axiom?path=${enc(path)}`, withCsrf({ method: 'DELETE' })));
  },
  async mercuryMoveCategory(from, to) {
    return json(await post('/api/mercury/move-category', { from, to }));
  },
  async mercuryReorder(category, order) {
    await json<void>(await post('/api/mercury/reorder', { category, order }));
  },
  async mercuryRolloutStatus() {
    return json(await request('/api/mercury/rollout'));
  },

  async mercuryRuns() {
    return json(await request('/api/mercury/runs'));
  },
  async mercuryRun(id) {
    return json(await request(`/api/mercury/runs/${enc(id)}`));
  },
  async mercuryRunPrompt(id) {
    return json(await request(`/api/mercury/runs/${enc(id)}/prompt`));
  },
  async mercuryRunCoverage() {
    return json(await request('/api/mercury/runs/coverage'));
  },
  async mercuryRunNotices() {
    return json(await request('/api/mercury/runs/notices'));
  },
  async mercuryDismissRunNotice(id) {
    await json<void>(await post('/api/mercury/runs/notices/dismiss', { id }));
  },
  async mercuryClearRunNotices() {
    await json<void>(await post('/api/mercury/runs/notices/clear', {}));
  },
  async mercuryCreateRun(body) {
    return json(await post('/api/mercury/runs', body));
  },
  async mercuryUpdateRun(id, body) {
    return json(await post(`/api/mercury/runs/${enc(id)}`, body, 'PUT'));
  },
  async mercuryDeleteRun(id) {
    await json<void>(await request(`/api/mercury/runs/${enc(id)}`, withCsrf({ method: 'DELETE' })));
  },
  async mercuryRunAiFill() {
    return json(await post('/api/mercury/runs/ai-fill', {}));
  },
  async mercuryRunAiFinetune() {
    return json(await post('/api/mercury/runs/ai-finetune', {}));
  },
  async mercuryApplyRunProposal(mode, plan) {
    await json<void>(await post('/api/mercury/runs/apply-proposal', { mode, plan }));
  },
  async mercuryRunHistory() {
    return json(await request('/api/mercury/runs/history'));
  },
  async mercuryRestoreRunHistory(ts) {
    await json<void>(await post('/api/mercury/runs/history/restore', { ts }));
  },
  async mercuryRunResults(id) {
    return json(await request(`/api/mercury/runs/${enc(id)}/results`));
  },
  async mercuryRunResult(id, resultId) {
    return json(await request(`/api/mercury/runs/${enc(id)}/results/${enc(resultId)}`));
  },
  async mercuryRunCalendar(days, type) {
    const p = new URLSearchParams();
    if (days) p.set('days', String(days));
    if (type) p.set('type', type);
    const q = p.toString();
    return json(await request(`/api/mercury/runs/calendar${q ? `?${q}` : ''}`));
  },
  async mercuryRunExecutions(type) {
    return json(await request(`/api/mercury/runs/executions${type ? `?type=${encodeURIComponent(type)}` : ''}`));
  },
  async mercuryChat(messages) {
    return json(await post('/api/mercury/chat', { messages }));
  },
  async mercuryRunNow(id, opts) {
    // Strategy/deferRunId ride in the body; fresh also stays supported as a query flag for the primary trigger.
    return json(
      await post(`/api/mercury/runs/${enc(id)}/run`, {
        fresh: opts?.fresh ?? false,
        strategy: opts?.strategy,
        deferRunId: opts?.deferRunId,
      }),
    );
  },
  async mercuryRunActive() {
    return json(await request('/api/mercury/runs/active'));
  },
  mercuryEvents(onTopic) {
    // EventSource is same-origin and cookie-authenticated (a GET → no CSRF). openLiveStream owns the
    // reconnect loop so an expired access token is refreshed before retrying — a raw EventSource
    // cannot do that itself. Absent EventSource (non-browser) → null so the caller polls instead.
    if (typeof EventSource === 'undefined') return null;
    const stream = openLiveStream(
      '/api/mercury/events',
      { onTopic },
      {
        create: (url) => new EventSource(url, { withCredentials: true }) as unknown as LiveEventSource,
        refresh: refreshSession,
      },
    );
    return () => stream.close();
  },
  async mercuryCancelRun(id) {
    await json<void>(await post(`/api/mercury/runs/${enc(id)}/cancel`, {}));
  },
  async mercuryDeferRun(id) {
    await json<void>(await post(`/api/mercury/runs/${enc(id)}/defer`, {}));
  },
  async mercuryBlockedDeploys() {
    return json(await request('/api/mercury/runs/deploys'));
  },
  async mercuryResumeDeploy(repo, number) {
    return json(await post('/api/mercury/runs/deploys/resume', { repo, number }));
  },
  async mercuryUploadAttachment(id, filename, contentB64) {
    return json<{ attachments: import('@/types').RunAttachment[] }>(
      await post(`/api/mercury/runs/${enc(id)}/attachments`, { filename, contentB64 }),
    ).then((r) => r.attachments);
  },
  async mercuryDeleteAttachment(id, attachmentId) {
    return json<{ attachments: import('@/types').RunAttachment[] }>(
      await request(`/api/mercury/runs/${enc(id)}/attachments/${enc(attachmentId)}`, withCsrf({ method: 'DELETE' })),
    ).then((r) => r.attachments);
  },
  mercuryAttachmentRawUrl(id, attachmentId) {
    return `/api/mercury/runs/${enc(id)}/attachments/${enc(attachmentId)}/raw`;
  },
};

/** Mutating-request helper: JSON body (optional), CSRF header, refresh-aware. Defaults to POST. */
function post(path: string, body?: unknown, method = 'POST'): Promise<Response> {
  const init: RequestInit = withCsrf({ method });
  if (body !== undefined) {
    init.headers = { ...(init.headers as Record<string, string>), 'Content-Type': 'application/json' };
    init.body = JSON.stringify(body);
  }
  return request(path, init);
}

/** Builds a power-op request init with the CSRF header (for mutating calls). */
export function withCsrf(init: RequestInit = {}): RequestInit {
  return { ...init, headers: { ...(init.headers ?? {}), 'X-CSRF-Token': csrfToken() } };
}

/** Exposed so Slice 2/3 mutating sources can reuse the refresh-aware fetch. */
export { request as apiFetch, json as apiJson };

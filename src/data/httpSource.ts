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
import type { AiMessage, AiModelCatalog, AssistantReply, Comment, User, VisionFile } from '@/types';

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

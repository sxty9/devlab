import { AuthRequiredError, type DataSource, type DiffPayload, type InitResult } from './source';

const opts: RequestInit = { credentials: 'include', cache: 'no-store' };

function csrfToken(): string {
  const m = document.cookie.match(/(?:^|;\s*)dl_csrf=([^;]+)/);
  return m ? decodeURIComponent(m[1]) : '';
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
  return res.json() as Promise<T>;
}

const enc = encodeURIComponent;

/** Talks to the devlabd backend over /api (same-origin, cookie auth). */
export const httpSource: DataSource = {
  async init(): Promise<InitResult> {
    try {
      const h = await fetch('/api/health', opts);
      if (!h.ok) return { mode: 'mock', gated: false, authed: true };
      const hd = (await h.json()) as { previewGated?: boolean };
      // A cheap authorization probe.
      const probe = await fetch('/api/repos', opts);
      return { mode: 'api', gated: !!hd.previewGated, authed: probe.status !== 401 };
    } catch {
      // Backend unreachable (offline dev) → caller falls back to mock.
      return { mode: 'mock', gated: false, authed: true };
    }
  },

  async login(password) {
    const res = await fetch('/api/session', {
      ...opts,
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ password }),
    });
    return res.ok;
  },

  async repos() {
    return json(await fetch('/api/repos', opts));
  },

  async repoData(id, branch) {
    const q = branch ? `?branch=${enc(branch)}` : '';
    return json(await fetch(`/api/repos/${enc(id)}${q}`, opts));
  },

  async fileContent(id, path) {
    return json(await fetch(`/api/repos/${enc(id)}/file?path=${enc(path)}`, opts));
  },

  async fileDiff(id, path): Promise<DiffPayload> {
    return json(await fetch(`/api/repos/${enc(id)}/diff?path=${enc(path)}`, opts));
  },
};

/** Builds a power-op request init with the CSRF header (for future mutating calls). */
export function withCsrf(init: RequestInit = {}): RequestInit {
  return { ...opts, ...init, headers: { ...(init.headers ?? {}), 'X-CSRF-Token': csrfToken() } };
}

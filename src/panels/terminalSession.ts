// Terminal session bookkeeping (S3, D 19) — the part of the terminal that is decision, not
// rendering, so it can be tested without a DOM.
//
// The terminal itself belongs to remshel; DevLab keeps no sessions. What the panel keeps is the
// KEY of the session it was last attached to, per repo, so a browser reload can ask for that same
// session again. Whether it is still there is remshel's answer, delivered as one control frame:
// the panel never assumes a reattach it was not told about, and a session that is gone is shown as
// a new one instead of silently replacing the old screen.

/** The control frame DevLab sends once per connection (mirrors api.sessionNotice). */
export interface SessionNotice {
  type: 'session';
  /** 'attached' = the requested session was picked up · 'new' = a fresh shell was started. */
  state: 'attached' | 'new';
  /** The key the shell is reachable under, when the provider named one. */
  session?: string;
  /** Why a requested session could not be resumed. */
  reason?: string;
}

/** A message frame from the provider (e.g. an error it wants shown in the terminal). */
export interface MessageFrame {
  message: string;
}

/** What the terminal shows about its connection — each value is an observed fact, never a guess. */
export type TerminalStatus =
  | 'connecting'
  | 'reattaching'
  | 'attached'
  | 'new'
  | 'closed'
  | 'unavailable';

/** The status line text. Short, factual, no explanation. */
export const statusText: Record<TerminalStatus, string> = {
  connecting: 'connecting…',
  reattaching: 'resuming session…',
  attached: 'session resumed',
  new: 'new session',
  closed: 'disconnected',
  unavailable: 'no terminal provider',
};

/** The tone each status is shown in. */
export const statusTone: Record<TerminalStatus, string> = {
  connecting: 'text-warning',
  reattaching: 'text-warning',
  attached: 'text-success',
  new: 'text-success',
  closed: 'text-text-tertiary',
  unavailable: 'text-text-tertiary',
};

/** The subset of Storage this module needs, so a test can pass a plain object. */
export interface KeyStore {
  getItem(key: string): string | null;
  setItem(key: string, value: string): void;
  removeItem(key: string): void;
}

const PREFIX = 'dl.term.';

/** The storage key of a repo's terminal session (one key per repo, defined once). */
export function sessionKeyName(repo: string): string {
  return `${PREFIX}${repo}`;
}

/** The session key to ask for when (re)opening a repo's terminal, or '' when none is remembered. */
export function recallSession(repo: string, store: KeyStore | null | undefined): string {
  if (!store || !repo) return '';
  try {
    return store.getItem(sessionKeyName(repo)) ?? '';
  } catch {
    return ''; // storage unavailable (private mode, quota) — simply nothing to resume
  }
}

/** Remember the session a repo's terminal is attached to; an empty key forgets it. */
export function rememberSession(repo: string, key: string, store: KeyStore | null | undefined): void {
  if (!store || !repo) return;
  try {
    if (key) store.setItem(sessionKeyName(repo), key);
    else store.removeItem(sessionKeyName(repo));
  } catch {
    /* storage unavailable — the terminal still works, it just cannot resume after a reload */
  }
}

/** Parse a text frame into the session notice or a provider message; null when it is neither. */
export function parseControlFrame(text: string): SessionNotice | MessageFrame | null {
  let raw: unknown;
  try {
    raw = JSON.parse(text);
  } catch {
    return null;
  }
  if (!raw || typeof raw !== 'object') return null;
  const o = raw as Record<string, unknown>;
  if (o.type === 'session' && (o.state === 'attached' || o.state === 'new')) {
    return {
      type: 'session',
      state: o.state,
      session: typeof o.session === 'string' ? o.session : undefined,
      reason: typeof o.reason === 'string' ? o.reason : undefined,
    };
  }
  if (typeof o.message === 'string' && o.message) return { message: o.message };
  return null;
}

/** True for the session notice (the other case is a provider message). */
export function isSessionNotice(f: SessionNotice | MessageFrame | null): f is SessionNotice {
  return !!f && (f as SessionNotice).type === 'session';
}

/** What the notice means for the panel: the status to show, the key to keep, and — only when a
 *  requested session was lost — the line to print into the terminal so the user sees it happened. */
export function applyNotice(
  notice: SessionNotice,
  requested: string,
): { status: TerminalStatus; keep: string; announce: string } {
  if (notice.state === 'attached') {
    return { status: 'attached', keep: notice.session || requested, announce: '' };
  }
  const keep = notice.session ?? '';
  if (requested) {
    const why = notice.reason ? ` — ${notice.reason}` : '';
    return { status: 'new', keep, announce: `[new session${why}]` };
  }
  return { status: 'new', keep, announce: '' };
}

// Mercury's client-side live-update primitives, kept free of React and the DOM so they are pure,
// injectable, and unit-testable under `node --test`. The React glue (the single provider, the
// per-view subscriptions) lives in src/state/mercuryLive.tsx; the transport (a real EventSource) is
// created in src/data/httpSource.ts. Both hand their platform objects in here through the deps seam.

/** The closed vocabulary of change kinds, shared verbatim with the backend (internal/live). A stream
 *  event carries exactly one of these; the client refetches the affected views in response. */
export const MERCURY_TOPICS = ['axioms', 'runs', 'active', 'progress'] as const;
export type MercuryTopic = (typeof MERCURY_TOPICS)[number];

/** True for a string the backend is allowed to send as a topic. Guards against a stray line. */
export function isMercuryTopic(s: string): s is MercuryTopic {
  return (MERCURY_TOPICS as readonly string[]).includes(s);
}

/** The slice of the browser EventSource this module uses. A fake implementing it drives the tests. */
export interface LiveEventSource {
  onopen: (() => void) | null;
  onmessage: ((ev: { data: string }) => void) | null;
  onerror: (() => void) | null;
  close(): void;
}

export interface LiveStreamHandlers {
  /** A change of this kind happened server-side — refetch the affected views. */
  onTopic: (topic: MercuryTopic) => void;
  /** Connection came up (true) or dropped (false) — for a subtle "live/reconnecting" indicator. */
  onStatus?: (connected: boolean) => void;
}

export interface LiveStreamDeps {
  /** Build the underlying stream for a URL (a real `new EventSource(url, {withCredentials:true})`). */
  create: (url: string) => LiveEventSource;
  /** Best-effort session refresh before a reconnect — recovers from an expired access token, which a
   *  raw EventSource cannot do on its own. Awaited (success or failure) before reconnecting. */
  refresh?: () => Promise<unknown>;
  setTimer?: (fn: () => void, ms: number) => unknown;
  clearTimer?: (handle: unknown) => void;
  /** First reconnect delay; doubles per consecutive failure up to maxBackoffMs. */
  minBackoffMs?: number;
  maxBackoffMs?: number;
}

export interface LiveStream {
  close(): void;
}

/** Open a self-healing event stream. It owns the reconnect loop itself rather than leaning on the
 *  browser's built-in EventSource retry, because only we can refresh an expired auth cookie before
 *  retrying and apply a capped exponential backoff. An interrupted connection therefore finds its way
 *  back on its own (task requirement 5), and close() stops it for good (no load from a closed view). */
export function openLiveStream(url: string, handlers: LiveStreamHandlers, deps: LiveStreamDeps): LiveStream {
  const minBackoff = deps.minBackoffMs ?? 1000;
  const maxBackoff = deps.maxBackoffMs ?? 30000;
  const setTimer = deps.setTimer ?? ((fn, ms) => setTimeout(fn, ms) as unknown);
  const clearTimer = deps.clearTimer ?? ((h) => clearTimeout(h as ReturnType<typeof setTimeout>));

  let es: LiveEventSource | null = null;
  let timer: unknown = null;
  let backoff = minBackoff;
  let closed = false;

  const scheduleReconnect = () => {
    if (closed || timer != null) return;
    const wait = backoff;
    backoff = Math.min(backoff * 2, maxBackoff); // grow for the NEXT consecutive failure
    timer = setTimer(() => {
      timer = null;
      if (closed) return;
      if (deps.refresh) {
        // Refresh first (an expired token is the common non-transient cause), then reconnect either way.
        void Promise.resolve(deps.refresh()).catch(() => {}).then(connect);
      } else {
        connect();
      }
    }, wait);
  };

  const onError = (src: LiveEventSource) => {
    if (es !== src) return; // a stale handler from a superseded connection
    es = null;
    try {
      src.close();
    } catch {
      /* already dead */
    }
    handlers.onStatus?.(false);
    scheduleReconnect();
  };

  function connect(): void {
    if (closed) return;
    let src: LiveEventSource;
    try {
      src = deps.create(url);
    } catch {
      handlers.onStatus?.(false);
      scheduleReconnect(); // even the constructor failing must not end the loop
      return;
    }
    es = src;
    src.onopen = () => {
      if (closed) return;
      backoff = minBackoff; // a healthy connection resets the backoff
      handlers.onStatus?.(true);
    };
    src.onmessage = (ev) => {
      if (closed || !ev) return;
      const data = String(ev.data ?? '').trim();
      if (isMercuryTopic(data)) handlers.onTopic(data);
    };
    src.onerror = () => onError(src);
  }

  connect();

  return {
    close() {
      closed = true;
      if (timer != null) {
        clearTimer(timer);
        timer = null;
      }
      if (es) {
        try {
          es.close();
        } catch {
          /* already dead */
        }
        es = null;
      }
      handlers.onStatus?.(false);
    },
  };
}

/** How a foreign change relates to the item a user is currently editing. */
export type ExternalChange = 'none' | 'updated' | 'deleted';

/** Classify a foreign change against an open editor so the UI can NAME the conflict instead of
 *  silently overwriting (task requirement 3). The editor keeps its draft regardless; this only
 *  decides whether to surface a banner.
 *
 *  - baseline undefined  → not tracking a stored item (e.g. composing a brand-new one) → never a conflict.
 *  - latest null/absent  → the item was deleted elsewhere → 'deleted'.
 *  - fingerprints differ → the item was changed elsewhere → 'updated'.
 *  The fingerprint is any value that moves on every change (here an `updatedAt` timestamp). */
export function classifyExternalChange(
  baselineUpdatedAt: string | undefined,
  latest: { updatedAt?: string } | null | undefined,
): ExternalChange {
  if (baselineUpdatedAt === undefined) return 'none';
  if (latest == null) return 'deleted';
  if (latest.updatedAt !== undefined && latest.updatedAt !== baselineUpdatedAt) return 'updated';
  return 'none';
}

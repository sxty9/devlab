/** Pure transport for the ONE Mercury change-stream (no React, no DOM globals — unit-testable under
 *  node --test). It owns its OWN reconnect loop so it can refresh an expired session before retrying,
 *  which a raw EventSource cannot. The payload is only a topic name; the consumer refetches through the
 *  normal read endpoints, so a dropped tick is safe. */

export const MERCURY_TOPICS = ['axioms', 'runs', 'active', 'progress', 'deliveries'] as const;
export type MercuryTopic = (typeof MERCURY_TOPICS)[number];

export function isMercuryTopic(s: string): s is MercuryTopic {
  return (MERCURY_TOPICS as readonly string[]).includes(s);
}

/** The slice of EventSource this module uses — injected so tests need no DOM. */
export interface LiveEventSource {
  onopen: (() => void) | null;
  onmessage: ((ev: { data: string }) => void) | null;
  onerror: (() => void) | null;
  close(): void;
}

export interface LiveStreamHandlers {
  onTopic: (topic: MercuryTopic) => void;
  onStatus?: (connected: boolean) => void;
}

export interface LiveStreamDeps {
  create: (url: string) => LiveEventSource;
  refresh?: () => Promise<unknown>; // best-effort session refresh before a reconnect
  setTimer?: (fn: () => void, ms: number) => unknown;
  clearTimer?: (h: unknown) => void;
  minBackoffMs?: number;
  maxBackoffMs?: number;
}

export interface LiveStream {
  close(): void;
}

/** Open a self-healing live stream. Returns a handle whose close() stops the loop and the connection. */
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
        void Promise.resolve(deps.refresh())
          .catch(() => {})
          .then(connect); // refresh first, reconnect either way
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
      scheduleReconnect(); // even a constructor failure keeps the loop alive
      return;
    }
    es = src;
    src.onopen = () => {
      if (closed) return;
      backoff = minBackoff; // a healthy open resets the backoff
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
          /* ignore */
        }
        es = null;
      }
      handlers.onStatus?.(false);
    },
  };
}

/** Whether a foreign change touched the entry being edited — none | updated | deleted — used to NAME a
 *  conflict instead of silently discarding a draft. `baselineUpdatedAt === undefined` means "composing
 *  new", which is never a conflict. */
export type ExternalChange = 'none' | 'updated' | 'deleted';

export function classifyExternalChange(
  baselineUpdatedAt: string | undefined,
  latest: { updatedAt?: string } | null | undefined,
): ExternalChange {
  if (baselineUpdatedAt === undefined) return 'none';
  if (latest == null) return 'deleted';
  if (latest.updatedAt !== undefined && latest.updatedAt !== baselineUpdatedAt) return 'updated';
  return 'none';
}

// lib/live.ts — the pure SSE transport (S12, C F5): an EventSource factory with
// self-healing reconnect that refreshes the SESSION BEFORE retrying (a raw EventSource
// cannot — its automatic reconnect never re-mints an expired access cookie). No React, no
// DOM globals required (timers are injectable), so everything here runs under `node --test`.
// The payload is ONLY a topic name; the consumer refetches through the normal read
// endpoints, so a dropped tick is always safe.
import type { LiveTopic } from '@/types';

/** The EXACTLY EIGHT topics — runtime mirror of the LiveTopic union (and of the Go
 *  constants in backend/internal/live). `satisfies` rejects junk entries; the completeness
 *  assertion below rejects a union member missing from this list. */
export const LIVE_TOPICS = [
  'axioms',
  'runs',
  'active',
  'progress',
  'deliveries',
  'notices',
  'slots',
  'restart',
] as const satisfies readonly LiveTopic[];

type MissingTopic = Exclude<LiveTopic, (typeof LIVE_TOPICS)[number]>;
type AssertTopicsComplete = [MissingTopic] extends [never]
  ? true
  : ['LIVE_TOPICS is missing a LiveTopic member', MissingTopic];
const _liveTopicsComplete: AssertTopicsComplete = true;
void _liveTopicsComplete;

/** Guards the wire vocabulary: only bare names from the closed set pass. */
export function isLiveTopic(s: string): s is LiveTopic {
  return (LIVE_TOPICS as readonly string[]).includes(s);
}

/** The slice of EventSource this module touches — assignment target for handlers, so tests
 *  can pass a plain fake (cast to EventSource at the boundary). */
interface LiveEventSource {
  onopen: ((ev?: unknown) => void) | null;
  onmessage: ((ev: { data: string }) => void) | null;
  onerror: ((ev?: unknown) => void) | null;
  close(): void;
}

/** Injectable timing so tests drive reconnects and the poll fallback deterministically.
 *  Production uses the global timers and the default backoff window. */
export interface LiveTiming {
  setTimer?: (fn: () => void, ms: number) => unknown;
  clearTimer?: (h: unknown) => void;
  setRepeat?: (fn: () => void, ms: number) => unknown;
  clearRepeat?: (h: unknown) => void;
  minBackoffMs?: number;
  maxBackoffMs?: number;
}

export interface LiveStream {
  /** Subscribe to topic ticks; returns the unsubscribe. Ticks carry ONLY topic names. */
  subscribe(onTopic: (t: LiveTopic) => void): () => void;
  /** Close the stream permanently (the provider calls this on unmount). */
  close(): void;
}

/** Opens the one self-healing stream over source.events(); null source ⇒ null stream (the
 *  provider then falls back to a gentle poll). Reconnect policy (C F5): capped exponential
 *  backoff, reset by a healthy open, and a best-effort session refresh BEFORE every retry. */
export function openLiveStream(
  open: () => EventSource | null,
  refreshSession: () => Promise<boolean>,
  timing: LiveTiming = {},
): LiveStream | null {
  const minBackoff = timing.minBackoffMs ?? 1000;
  const maxBackoff = timing.maxBackoffMs ?? 30000;
  const setTimer = timing.setTimer ?? ((fn: () => void, ms: number) => setTimeout(fn, ms) as unknown);
  const clearTimer = timing.clearTimer ?? ((h: unknown) => clearTimeout(h as ReturnType<typeof setTimeout>));

  const subscribers = new Set<(t: LiveTopic) => void>();
  let es: LiveEventSource | null = null;
  let timer: unknown = null;
  let backoff = minBackoff;
  let closed = false;

  const emit = (t: LiveTopic) => {
    // Snapshot before dispatch; one throwing subscriber must not silence the others.
    for (const fn of [...subscribers]) {
      try {
        fn(t);
      } catch {
        /* subscriber errors are the subscriber's problem */
      }
    }
  };

  const scheduleReconnect = () => {
    if (closed || timer != null) return;
    const wait = backoff;
    backoff = Math.min(backoff * 2, maxBackoff); // grow for the NEXT consecutive failure
    timer = setTimer(() => {
      timer = null;
      if (closed) return;
      // Refresh the session first — reconnect either way (refresh is best-effort).
      void Promise.resolve()
        .then(() => refreshSession())
        .catch(() => false)
        .then(() => {
          if (!closed) connect();
        });
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
    scheduleReconnect();
  };

  const attach = (raw: EventSource) => {
    const src = raw as unknown as LiveEventSource;
    es = src;
    src.onopen = () => {
      if (closed) return;
      backoff = minBackoff; // a healthy open resets the backoff
    };
    src.onmessage = (ev) => {
      if (closed || !ev) return;
      const data = String(ev.data ?? '').trim();
      if (isLiveTopic(data)) emit(data);
    };
    src.onerror = () => onError(src);
  };

  function connect(): void {
    if (closed) return;
    let raw: EventSource | null;
    try {
      raw = open();
    } catch {
      scheduleReconnect(); // even a factory failure keeps the loop alive
      return;
    }
    if (raw == null) {
      // The source cannot push right now — keep trying gently rather than going dead.
      scheduleReconnect();
      return;
    }
    attach(raw);
  }

  // A source that cannot push AT ALL (stub/offline) yields no stream — the caller polls.
  const first = open();
  if (first == null) return null;
  attach(first);

  return {
    subscribe(onTopic) {
      subscribers.add(onTopic);
      return () => {
        subscribers.delete(onTopic);
      };
    },
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
      subscribers.clear();
    },
  };
}

// ── Topic fan-out registry (the provider's core, extracted for node tests) ───────────────

/** How often the offline/stub fallback re-emits everything. Deliberately gentle (seconds,
 *  not the sub-second rhythm push replaces) and only used when there is no server to push —
 *  and even then a RESTING view (no subscribers) causes zero refetches. */
export const FALLBACK_POLL_MS = 4000;

export interface TopicRegistry {
  /** Register a refetch for one topic; returns the unsubscribe. */
  subscribe(topic: LiveTopic, handler: () => void): () => void;
  /** Deliver one tick to that topic's current subscribers (snapshot; throws isolated). */
  emit(topic: LiveTopic): void;
  /** Deliver a tick for every topic that HAS subscribers (the poll fallback's beat). */
  emitAll(): void;
  /** How many handlers are registered (for one topic, or overall). */
  count(topic?: LiveTopic): number;
}

export function createTopicRegistry(): TopicRegistry {
  const subs = new Map<LiveTopic, Set<() => void>>();
  const emit = (topic: LiveTopic) => {
    const hs = subs.get(topic);
    if (!hs || hs.size === 0) return;
    for (const h of [...hs]) {
      try {
        h();
      } catch {
        /* one view's refetch throwing must not stop the others */
      }
    }
  };
  return {
    subscribe(topic, handler) {
      let set = subs.get(topic);
      if (!set) {
        set = new Set();
        subs.set(topic, set);
      }
      set.add(handler);
      return () => {
        subs.get(topic)?.delete(handler);
      };
    },
    emit,
    emitAll() {
      for (const t of LIVE_TOPICS) emit(t);
    },
    count(topic) {
      if (topic) return subs.get(topic)?.size ?? 0;
      let n = 0;
      for (const set of subs.values()) n += set.size;
      return n;
    },
  };
}

/** Wires the transport to a registry — exactly what the LiveProvider's effect runs. With a
 *  push stream, ticks flow from the server and NOTHING is scheduled client-side (a resting
 *  surface causes zero requests). With a null stream (stub/offline) it falls back to a
 *  gentle poll whose beat reaches only topics somebody subscribed to. Returns the cleanup. */
export function connectLive(
  open: () => EventSource | null,
  registry: TopicRegistry,
  refreshSession: () => Promise<boolean>,
  timing: LiveTiming = {},
): () => void {
  const stream = openLiveStream(open, refreshSession, timing);
  if (stream) {
    const unsub = stream.subscribe((t) => registry.emit(t));
    return () => {
      unsub();
      stream.close();
    };
  }
  const setRepeat = timing.setRepeat ?? ((fn: () => void, ms: number) => setInterval(fn, ms) as unknown);
  const clearRepeat = timing.clearRepeat ?? ((h: unknown) => clearInterval(h as ReturnType<typeof setInterval>));
  const iv = setRepeat(() => registry.emitAll(), FALLBACK_POLL_MS);
  return () => clearRepeat(iv);
}

// ── Edit-conflict naming (C F5, 4e931c8) ─────────────────────────────────────────────────

/** Whether a foreign change touched the entry being edited — none | updated | deleted —
 *  used to NAME a conflict instead of silently discarding a draft. On a topic tick the form
 *  compares the freshly fetched server version against its edit-time baseline.
 *  `baselineUpdatedAt === undefined` means "composing new", which is never a conflict. */
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

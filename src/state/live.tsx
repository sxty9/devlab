// state/live.tsx — the ONE EventSource provider per surface (S12). Every view subscribes via
// useLiveTopic (including both calendars, W5); a lost tick is safe (topic names only — the
// subscriber refetches through the normal read path). With a null stream the provider falls
// back to a gentle poll; a RESTING view causes zero requests. Mounted inside the surface it
// serves, so leaving the surface closes the stream (a closed surface causes no load).
import { createContext, useContext, useEffect, useRef, type ReactNode } from 'react';
import type { LiveTopic } from '@/types';
import { getDataSource } from '@/data';
import { apiFetch, withCsrf } from '@/data/httpSource';
import { connectLive, createTopicRegistry, type ExternalChange, type TopicRegistry } from '@/lib/live';

interface LiveContextValue {
  /** Registers refetch for topic ticks; returns the unsubscribe. */
  subscribe(topic: LiveTopic, refetch: () => void): () => void;
}

/** Outside a provider the subscription is inert (static views degrade gracefully). */
const LiveContext = createContext<LiveContextValue>({ subscribe: () => () => undefined });

/** Best-effort session refresh before an SSE retry (C F5) — rides the ONE refresh-aware
 *  fetch of httpSource instead of opening a second refresh path. A raw EventSource cannot
 *  re-mint an expired access cookie on its own; this can. */
async function refreshSessionForStream(): Promise<boolean> {
  try {
    const res = await apiFetch('/api/auth/refresh', withCsrf({ method: 'POST' }));
    return res.ok;
  } catch {
    return false;
  }
}

/** Mounts once per surface; owns the single stream. */
export function LiveProvider({ children }: { children: ReactNode }) {
  // The registry lives in a lazily-initialized ref so children's effects can subscribe
  // BEFORE this provider's own effect opens the transport (React runs child effects first).
  // (Un)subscribing never re-renders the provider or its subtree.
  const registryRef = useRef<TopicRegistry | null>(null);
  registryRef.current ??= createTopicRegistry();
  const registry = registryRef.current;

  const valueRef = useRef<LiveContextValue | null>(null);
  valueRef.current ??= { subscribe: (topic, refetch) => registry.subscribe(topic, refetch) };

  useEffect(() => {
    // Exactly ONE stream per surface: the transport pushes bare topic names into the
    // registry; with a null stream (stub/offline) connectLive polls gently instead —
    // and a resting surface (no subscribers) still causes zero requests.
    return connectLive(() => getDataSource().events(), registry, refreshSessionForStream);
  }, [registry]);

  return <LiveContext.Provider value={valueRef.current}>{children}</LiveContext.Provider>;
}

/** Subscribe the calling view to one topic: refetch runs on every tick. The latest closure
 *  is always used (no memoization needed at call sites). */
export function useLiveTopic(topic: LiveTopic, refetch: () => void): void {
  const { subscribe } = useContext(LiveContext);
  const ref = useRef(refetch);
  ref.current = refetch;
  useEffect(() => subscribe(topic, () => ref.current()), [subscribe, topic]);
}

/** Names a foreign change to the entry currently being edited (C F5, 4e931c8): on a topic
 *  tick the form compares the freshly fetched server version against its edit-time baseline
 *  (classifyExternalChange in lib/live.ts) and states the conflict — the draft is kept,
 *  never silently overwritten. */
export function ExternalChangeBanner({ change }: { change: ExternalChange }) {
  if (change === 'none') return null;
  const text =
    change === 'deleted'
      ? 'This entry was deleted elsewhere while you were editing. Your changes are kept here.'
      : 'This entry was changed elsewhere while you were editing. Your changes are kept — saving will overwrite that change.';
  return (
    <div className="rounded-md border border-warning/30 bg-warning/[0.06] px-3 py-2 text-caption text-warning">
      {text}
    </div>
  );
}

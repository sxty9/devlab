// state/live.tsx — the ONE EventSource provider per surface (S12). Every view subscribes via
// useLiveTopic (including both calendars, W5); a lost tick is safe (topic names only — the
// subscriber refetches through the normal read path). With a null stream the provider falls
// back to a gentle poll; a RESTING view causes zero requests. B7 fills the implementation;
// this no-op provider is the Welle-0 contract so views can already wire their subscriptions.
import { createContext, useContext, type ReactNode } from 'react';
import type { LiveTopic } from '@/types';

interface LiveContextValue {
  /** Registers refetch for topic ticks; returns the unsubscribe. No-op until B7 wires SSE. */
  subscribe(topic: LiveTopic, refetch: () => void): () => void;
}

const LiveContext = createContext<LiveContextValue>({ subscribe: () => () => undefined });

/** Mounts once per surface; owns the single stream (B7). */
export function LiveProvider({ children }: { children: ReactNode }) {
  // TODO(B7): open exactly ONE stream (dataSource.events() via lib/live.ts), fan ticks out
  // to subscribers, and fall back to a gentle poll when the stream is null.
  return <LiveContext.Provider value={{ subscribe: () => () => undefined }}>{children}</LiveContext.Provider>;
}

/** Subscribe the calling view to one topic: refetch runs on every tick. */
export function useLiveTopic(_topic: LiveTopic, _refetch: () => void): void {
  useContext(LiveContext);
  // TODO(B7): register with the provider (subscribe on mount, unsubscribe on unmount).
}

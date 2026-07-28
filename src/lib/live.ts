// lib/live.ts — the pure SSE transport (S12, C F5): an EventSource factory with
// self-healing reconnect that refreshes the SESSION BEFORE retrying (a raw EventSource
// cannot — its automatic reconnect never re-mints an expired access cookie). B7 fills the
// implementation; the surface below is the Welle-0 contract.
import type { LiveTopic } from '@/types';

export interface LiveStream {
  /** Subscribe to topic ticks; returns the unsubscribe. Ticks carry ONLY topic names. */
  subscribe(onTopic: (t: LiveTopic) => void): () => void;
  /** Close the stream permanently (the provider calls this on unmount). */
  close(): void;
}

/** Opens the one self-healing stream over source.events(); null source ⇒ null stream (the
 *  provider then falls back to a gentle poll). */
export function openLiveStream(_open: () => EventSource | null, _refreshSession: () => Promise<boolean>): LiveStream | null {
  // TODO(B7): EventSource lifecycle + reconnect with session refresh before retry (F5).
  return null;
}

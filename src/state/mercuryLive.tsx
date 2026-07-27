import { createContext, useCallback, useContext, useEffect, useMemo, useRef, type ReactNode } from 'react';
import { getDataSource } from '@/data';
import { MERCURY_TOPICS, type ExternalChange, type MercuryTopic } from '@/lib/live';

// The SINGLE live-update mechanism for the whole Mercury surface. Exactly one connection is opened
// (by this provider); every view subscribes to it by topic through useMercuryTopic, rather than each
// opening its own stream or running its own poll. This is what makes "one way, not several" true:
// the axiom tree, the run/ToDo lists, the calendars, the execution history, the live-run pointer and
// the live-follow view all ride this one stream.
//
// When the data source has no push stream (mock/offline, mercuryEvents → null), the provider falls
// back to a gentle all-topics poll so those environments still update — there is no server to burden
// there. In the real deployment nothing polls: the server pushes, and openLiveStream reconnects on
// its own after a drop.

type Handler = () => void;

interface MercuryLive {
  /** Register handler for one or more topics; returns an unsubscribe. Safe to call from an effect. */
  subscribe(topics: readonly MercuryTopic[], handler: Handler): () => void;
}

const NOOP: MercuryLive = { subscribe: () => () => {} };
const Ctx = createContext<MercuryLive>(NOOP);

/** How often the offline/mock fallback refetches everything. Deliberately gentle (seconds, not the
 *  sub-second rhythm the push mechanism replaces) and only used when there is no server to push. */
const FALLBACK_POLL_MS = 4000;

export function MercuryLiveProvider({ children }: { children: ReactNode }) {
  const source = useMemo(() => getDataSource(), []);
  // topic -> handlers. A ref, so (un)subscribing never re-renders the provider or its subtree.
  const subs = useRef<Map<MercuryTopic, Set<Handler>>>(new Map());

  const emit = useCallback((topic: MercuryTopic) => {
    const hs = subs.current.get(topic);
    if (!hs || hs.size === 0) return;
    // Snapshot before dispatch: a handler may subscribe/unsubscribe while reacting.
    for (const h of [...hs]) {
      try {
        h();
      } catch {
        /* one view's refetch throwing must not stop the others */
      }
    }
  }, []);

  const api = useMemo<MercuryLive>(
    () => ({
      subscribe(topics, handler) {
        for (const t of topics) {
          let set = subs.current.get(t);
          if (!set) {
            set = new Set();
            subs.current.set(t, set);
          }
          set.add(handler);
        }
        return () => {
          for (const t of topics) subs.current.get(t)?.delete(handler);
        };
      },
    }),
    [],
  );

  useEffect(() => {
    // Prefer the server push stream. mercuryEvents returns an unsubscribe, or null when this source
    // cannot push (mock/offline) — then fall back to a periodic all-topics refresh.
    const stop = source.mercuryEvents((topic) => emit(topic));
    if (stop) return stop;
    const iv = window.setInterval(() => {
      for (const t of MERCURY_TOPICS) emit(t);
    }, FALLBACK_POLL_MS);
    return () => window.clearInterval(iv);
  }, [source, emit]);

  return <Ctx.Provider value={api}>{children}</Ctx.Provider>;
}

/** Names a foreign change to the record a user is editing, so the update is honest instead of silent
 *  (task requirement 3). The editor keeps the user's draft regardless; this only surfaces the conflict.
 *  New UI strings are authored in English (the nightly run translates); see the language convention. */
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

/** Refetch when the server signals one of `topics` changed. The handler is always the latest closure
 *  (no need to memoize it at the call site), and the subscription is torn down on unmount — a closed
 *  view causes no ongoing load. Pass an empty list to subscribe to nothing (e.g. only-while-live). */
export function useMercuryTopic(topics: readonly MercuryTopic[], handler: Handler) {
  const { subscribe } = useContext(Ctx);
  const handlerRef = useRef(handler);
  handlerRef.current = handler;
  // Stable dependency: the set of topics, not the array identity (which changes each render).
  const key = topics.join('|');
  useEffect(() => {
    if (!key) return; // no topics → nothing to subscribe to
    const list = key.split('|') as MercuryTopic[];
    return subscribe(list, () => handlerRef.current());
  }, [subscribe, key]);
}

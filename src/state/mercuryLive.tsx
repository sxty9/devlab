import { createContext, useCallback, useContext, useEffect, useMemo, useRef, type ReactNode } from 'react';
import { getDataSource } from '@/data';
import { MERCURY_TOPICS, type ExternalChange, type MercuryTopic } from '@/lib/live';

/** ONE live stream for the whole Mercury surface. Views subscribe by topic through a ref-based registry
 *  (subscribe/unsubscribe never re-render), so a change — from this session, another window, an automatic
 *  run, or a second instance — reaches every open view without polling. When the source cannot push
 *  (mock/offline) it falls back to a gentle all-topics poll. Mounted INSIDE the Mercury view so it opens on
 *  entering Mercury and closes on leaving (a closed surface causes no load). */

type Handler = () => void;
interface MercuryLive {
  subscribe(topics: readonly MercuryTopic[], handler: Handler): () => void;
}
const NOOP: MercuryLive = { subscribe: () => () => {} };
const Ctx = createContext<MercuryLive>(NOOP);
const FALLBACK_POLL_MS = 4000;

export function MercuryLiveProvider({ children }: { children: ReactNode }) {
  const source = useMemo(() => getDataSource(), []);
  const subs = useRef<Map<MercuryTopic, Set<Handler>>>(new Map());

  const emit = useCallback((topic: MercuryTopic) => {
    const hs = subs.current.get(topic);
    if (!hs || hs.size === 0) return;
    for (const h of [...hs]) {
      try {
        h();
      } catch {
        /* one refetch throwing must not stop the others */
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
    const stop = source.mercuryEvents((topic) => emit(topic)); // exactly one stream
    if (stop) return stop;
    // No push transport → a gentle all-topics poll so the surface still self-refreshes offline/in preview.
    const iv = window.setInterval(() => {
      for (const t of MERCURY_TOPICS) emit(t);
    }, FALLBACK_POLL_MS);
    return () => window.clearInterval(iv);
  }, [source, emit]);

  return <Ctx.Provider value={api}>{children}</Ctx.Provider>;
}

/** Subscribe to one or more topics for as long as the component is mounted. `handler` is always the latest
 *  closure (no memoization needed at call sites); an empty topic list subscribes to nothing (used to
 *  subscribe only while a run is live). */
export function useMercuryTopic(topics: readonly MercuryTopic[], handler: Handler) {
  const { subscribe } = useContext(Ctx);
  const handlerRef = useRef(handler);
  handlerRef.current = handler;
  const key = topics.join('|'); // stable dep = the topic SET, not array identity
  useEffect(() => {
    if (!key) return;
    const list = key.split('|') as MercuryTopic[];
    return subscribe(list, () => handlerRef.current());
  }, [subscribe, key]);
}

/** Names a foreign change to the entry currently being edited, so a draft is never silently discarded. */
export function ExternalChangeBanner({ change }: { change: ExternalChange }) {
  if (change === 'none') return null;
  const text =
    change === 'deleted'
      ? 'This entry was deleted elsewhere while you were editing. Your changes are kept here.'
      : 'This entry was changed elsewhere while you were editing. Your changes are kept — saving will overwrite that change.';
  return <div className="rounded-md border border-warning/30 bg-warning/[0.06] px-3 py-2 text-caption text-warning">{text}</div>;
}

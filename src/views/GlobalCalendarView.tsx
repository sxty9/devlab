import { useEffect, useMemo, useRef, useState } from 'react';
import { useLiveTopic } from '@/state/live';
import { getDataSource } from '@/data';
import { MercuryCalendar } from './mercury/calendar/MercuryCalendar';
import type { RunCalendar } from '@/types';

/** Uniform error-to-string, mirroring the rest of the Mercury surface. */
const msg = (e: unknown) => String((e as Error)?.message ?? e);

/** GlobalCalendarView — the global calendar: one auto-updating surface uniting the automatic
 *  runs and the todos, separated only by colour. It fetches once on mount and refreshes on the
 *  live stream's `runs`/`active` ticks (W5) — the old 60-second poll is gone (REQ-034). */
export default function GlobalCalendarView() {
  const source = useMemo(() => getDataSource(), []);
  const [cal, setCal] = useState<RunCalendar | null>(null);
  const [failed, setFailed] = useState<string | null>(null);
  const gotDataRef = useRef(false);
  const loadRef = useRef<(() => Promise<void>) | null>(null);

  useEffect(() => {
    let cancelled = false;
    const load = async () => {
      try {
        // No type filter → both automatic runs and ToDos.
        const c = await source.mercuryRunCalendar(30);
        if (!cancelled) {
          setCal(c);
          gotDataRef.current = true;
          setFailed(null);
        }
      } catch (e) {
        // Once a calendar is on screen a transient poll error must not blank it out.
        if (!cancelled && !gotDataRef.current) setFailed(msg(e));
      }
    };
    void load();
    loadRef.current = load;
    return () => {
      cancelled = true;
    };
  }, [source]);
  useLiveTopic('runs', () => void loadRef.current?.());
  useLiveTopic('active', () => void loadRef.current?.());

  if (failed) {
    return (
      <div className="flex h-full min-h-0 w-full items-center justify-center bg-bg-base px-6">
        <p className="max-w-md text-center text-footnote text-text-secondary">{failed}</p>
      </div>
    );
  }
  if (!cal) {
    return (
      <div className="flex h-full min-h-0 w-full items-center justify-center bg-bg-base">
        <p className="text-footnote text-text-tertiary">Loading…</p>
      </div>
    );
  }

  return <MercuryCalendar occurrences={cal.occurrences} showTypes heading="Calendar — everything" />;
}

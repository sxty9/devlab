import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { getDataSource } from '@/data';
import { useMercuryTopic } from '@/state/mercuryLive';
import { MercuryCalendar } from './MercuryCalendar';
import type { RunCalendar } from '@/types';

/** Uniform error-to-string, mirroring the rest of the Mercury surface. */
const msg = (e: unknown) => String((e as Error)?.message ?? e);

/** GlobalCalendarView — Mercury's "Kalender — alles": one auto-updating surface that unites the
 *  Automatische Läufe and the Konkrete ToDos, separated only by colour. It owns the data and stays
 *  current via the live-event stream (refetches on any run/ToDo change) instead of a resting poll;
 *  the shared MercuryCalendar renders it. Self-contained; the parent only gives it a height box. */
export default function GlobalCalendarView() {
  const source = useMemo(() => getDataSource(), []);
  const [cal, setCal] = useState<RunCalendar | null>(null);
  const [failed, setFailed] = useState<string | null>(null);
  const gotDataRef = useRef(false);
  const cancelledRef = useRef(false);

  const load = useCallback(async () => {
    try {
      // No type filter → both automatic runs and ToDos.
      const c = await source.mercuryRunCalendar(30);
      if (!cancelledRef.current) {
        setCal(c);
        gotDataRef.current = true;
        setFailed(null);
      }
    } catch (e) {
      // Once a calendar is on screen a transient error must not blank it out.
      if (!cancelledRef.current && !gotDataRef.current) setFailed(msg(e));
    }
  }, [source]);

  useEffect(() => {
    cancelledRef.current = false;
    void load();
    return () => {
      cancelledRef.current = true;
    };
  }, [load]);

  // Live: a run/ToDo change (created, moved, fired, done) reshapes the agenda — refetch on it.
  useMercuryTopic(['runs'], () => void load());

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
        <p className="text-footnote text-text-tertiary">Lädt…</p>
      </div>
    );
  }

  return <MercuryCalendar occurrences={cal.occurrences} showTypes heading="Kalender — alles" />;
}

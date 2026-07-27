import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { getDataSource } from '@/data';
import { useMercuryTopic } from '@/state/mercuryLive';
import { MercuryCalendar } from './MercuryCalendar';
import type { RunCalendar } from '@/types';

/** Uniform error-to-string, mirroring the rest of the Mercury surface. */
const msg = (e: unknown) => String((e as Error)?.message ?? e);

/** GlobalCalendarView — Mercury's "Kalender — alles": one auto-updating surface that unites the
 *  Automatische Läufe and the Konkrete ToDos, separated only by colour. It owns the data and polls
 *  every 60s; the shared MercuryCalendar renders it. Self-contained; the parent only gives it a
 *  height box. */
export default function GlobalCalendarView() {
  const source = useMemo(() => getDataSource(), []);
  const [cal, setCal] = useState<RunCalendar | null>(null);
  const [failed, setFailed] = useState<string | null>(null);
  const gotDataRef = useRef(false);

  const load = useCallback(async () => {
    try {
      // No type filter → both automatic runs and ToDos.
      const c = await source.mercuryRunCalendar(30);
      setCal(c);
      gotDataRef.current = true;
      setFailed(null);
    } catch (e) {
      // Once a calendar is on screen a transient error must not blank it out.
      if (!gotDataRef.current) setFailed(msg(e));
    }
  }, [source]);

  useEffect(() => {
    void load();
  }, [load]);
  // Live: the calendar reflects the run/ToDo schedules and their past executions — a 'runs' change
  // (created, edited, scheduled, done) refreshes it on its own, replacing the old 60s poll (req 9, 12).
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

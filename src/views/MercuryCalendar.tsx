import { useMemo, useState } from 'react';
import { cn } from '@/lib/cn';
import { Button, IconButton } from '@/ui/Button';
import { ChevronRightIcon } from '@/ui/icons';
import type { RunOccurrence, RunType } from '@/types';

/** The two kinds an occurrence can carry, spelled out so Tailwind sees whole class names. `auto`
 *  (Automatische Läufe) takes the accent, `todo` (Konkrete ToDos) the warning token — matching the
 *  global calendar's colour split. `NEUTRAL` is used when the caller does not colour-separate. */
interface TypeStyle {
  label: string;
  dot: string;
  text: string;
  borderLeft: string;
  tint: string;
}
const TYPE_STYLE: Record<RunType, TypeStyle> = {
  auto: { label: 'Automatischer Lauf', dot: 'bg-accent', text: 'text-accent', borderLeft: 'border-l-accent', tint: 'bg-accent/10' },
  todo: { label: 'Konkretes ToDo', dot: 'bg-warning', text: 'text-warning', borderLeft: 'border-l-warning', tint: 'bg-warning/10' },
};
const NEUTRAL_STYLE: TypeStyle = { label: 'Lauf', dot: 'bg-accent', text: 'text-accent', borderLeft: 'border-l-accent', tint: 'bg-accent/10' };

type CalView = 'tag' | 'woche' | 'agenda';
const VIEWS: { id: CalView; label: string }[] = [
  { id: 'tag', label: 'Tag' },
  { id: 'woche', label: 'Woche' },
  { id: 'agenda', label: 'Agenda' },
];

const WEEKDAY_LABELS = ['Mo', 'Di', 'Mi', 'Do', 'Fr', 'Sa', 'So'];

// ── date helpers (all Monday-first, viewer timezone) ──────────────────────────
const startOfDay = (d: Date) => new Date(d.getFullYear(), d.getMonth(), d.getDate());
const addDays = (d: Date, n: number) => {
  const r = startOfDay(d);
  r.setDate(r.getDate() + n);
  return r;
};
/** Shift by whole months, clamping the day into the target month (31 Jan + 1 → 28/29 Feb). */
const addMonths = (d: Date, n: number) => {
  const t = new Date(d.getFullYear(), d.getMonth() + n, 1);
  const dim = new Date(t.getFullYear(), t.getMonth() + 1, 0).getDate();
  t.setDate(Math.min(d.getDate(), dim));
  return t;
};
const startOfWeek = (d: Date) => addDays(d, -((d.getDay() + 6) % 7)); // back to Monday
const sameDay = (a: Date, b: Date) => a.getFullYear() === b.getFullYear() && a.getMonth() === b.getMonth() && a.getDate() === b.getDate();
const dayKey = (d: Date) => `${d.getFullYear()}-${d.getMonth()}-${d.getDate()}`;
const fmtTime = (iso: string) => new Date(iso).toLocaleTimeString('de-DE', { hour: '2-digit', minute: '2-digit' });

/** MercuryCalendar — the one shared calendar behind every Mercury surface (Laufkalender, ToDo-Kalender
 *  and „Kalender — alles"). Pure presentation: the parent fetches the occurrences (and auto-refreshes),
 *  this renders them. Modelled on a real calendar app: a month mini-grid with clickable days beside a
 *  Tag / Woche / Agenda view, navigable by day or week. `showTypes` colour-separates automatic runs
 *  from ToDos (accent vs warning) and shows a legend; without it a single accent style is used. */
export function MercuryCalendar({
  occurrences,
  showTypes = false,
  heading = 'Kalender',
}: {
  occurrences: RunOccurrence[];
  showTypes?: boolean;
  heading?: string;
}) {
  const [view, setView] = useState<CalView>('agenda');
  const [focusDate, setFocusDate] = useState<Date>(() => startOfDay(new Date()));
  const today = useMemo(() => startOfDay(new Date()), []);

  const styleOf = (o: RunOccurrence): TypeStyle => (showTypes ? TYPE_STYLE[o.type ?? 'auto'] : NEUTRAL_STYLE);

  // Occurrences bucketed by calendar day, each bucket sorted by time.
  const byDay = useMemo(() => {
    const map = new Map<string, RunOccurrence[]>();
    const sorted = [...occurrences].sort((a, b) => (a.at < b.at ? -1 : a.at > b.at ? 1 : 0));
    for (const o of sorted) {
      const k = dayKey(new Date(o.at));
      const arr = map.get(k) ?? [];
      arr.push(o);
      map.set(k, arr);
    }
    return map;
  }, [occurrences]);

  // The whole window, chronological, grouped into days (the Agenda view).
  const agendaGroups = useMemo(() => {
    const out: { key: string; date: Date; occ: RunOccurrence[] }[] = [];
    for (const [key, occ] of byDay) out.push({ key, date: startOfDay(new Date(occ[0].at)), occ });
    return out.sort((a, b) => a.date.getTime() - b.date.getTime());
  }, [byDay]);

  const shift = (delta: number) => setFocusDate((d) => addDays(d, delta * (view === 'woche' ? 7 : 1)));
  const pickDay = (d: Date) => {
    setFocusDate(startOfDay(d));
    setView('tag');
  };

  const weekStart = startOfWeek(focusDate);
  const weekDays = Array.from({ length: 7 }, (_, i) => addDays(weekStart, i));
  const weekEnd = weekDays[6];

  const navLabel =
    view === 'woche'
      ? `${weekStart.toLocaleDateString('de-DE', { day: 'numeric', month: 'short' })} – ${weekEnd.toLocaleDateString('de-DE', { day: 'numeric', month: 'short' })}`
      : focusDate.toLocaleDateString('de-DE', { weekday: 'long', day: 'numeric', month: 'long' });

  return (
    <div className="dl-scroll min-h-0 w-full flex-1 overflow-y-auto bg-bg-base">
      <div className="px-6 py-6">
        <h1 className="text-title3 font-semibold tracking-tight text-text-primary">{heading}</h1>
        {showTypes && <Legend />}

        <div className={cn('flex flex-col gap-6 xl:flex-row xl:items-start', showTypes ? 'mt-4' : 'mt-4')}>
          {/* LEFT — the month mini-grid; clicking a day opens it in the Tag view. */}
          <div className="xl:w-[340px] xl:shrink-0">
            <MiniMonth
              anchor={focusDate}
              today={today}
              byDay={byDay}
              showTypes={showTypes}
              onPick={pickDay}
              onShiftMonth={(delta) => setFocusDate((d) => addMonths(d, delta))}
            />
          </div>

          {/* RIGHT — view switcher, navigator, and the active view. */}
          <div className="min-w-0 flex-1">
            <div className="mb-4 flex flex-wrap items-center justify-between gap-3">
              <div className="inline-flex items-center gap-0.5 rounded-md bg-fill/10 p-0.5">
                {VIEWS.map((v) => (
                  <button
                    key={v.id}
                    type="button"
                    onClick={() => setView(v.id)}
                    className={cn(
                      'rounded px-3 py-1 text-caption font-medium transition duration-fast',
                      view === v.id ? 'bg-surface-raised text-text-primary shadow-elev-1' : 'text-text-secondary hover:text-text-primary',
                    )}
                  >
                    {v.label}
                  </button>
                ))}
              </div>

              {view !== 'agenda' && (
                <div className="flex items-center gap-1.5">
                  <IconButton label="Zurück" onClick={() => shift(-1)}>
                    <ChevronRightIcon className="h-4 w-4 rotate-180" />
                  </IconButton>
                  <span className="min-w-[11rem] text-center text-footnote font-medium text-text-primary">{navLabel}</span>
                  <IconButton label="Weiter" onClick={() => shift(1)}>
                    <ChevronRightIcon className="h-4 w-4" />
                  </IconButton>
                  <Button variant="ghost" size="sm" onClick={() => setFocusDate(startOfDay(new Date()))}>
                    Heute
                  </Button>
                </div>
              )}
            </div>

            {view === 'tag' && <TagView occ={byDay.get(dayKey(focusDate)) ?? []} styleOf={styleOf} showTypes={showTypes} />}
            {view === 'woche' && (
              <WocheView days={weekDays} today={today} byDay={byDay} styleOf={styleOf} onPickDay={pickDay} />
            )}
            {view === 'agenda' && <AgendaView groups={agendaGroups} styleOf={styleOf} showTypes={showTypes} />}
          </div>
        </div>
      </div>
    </div>
  );
}

/** What the two colours mean (only shown when the caller colour-separates the kinds). */
function Legend() {
  return (
    <div className="mt-4 flex flex-wrap items-center gap-4 text-caption text-text-secondary">
      {(['auto', 'todo'] as const).map((t) => (
        <span key={t} className="flex items-center gap-1.5">
          <span className={cn('h-2 w-2 rounded-full', TYPE_STYLE[t].dot)} />
          {t === 'auto' ? 'Automatische Läufe' : 'Konkrete ToDos'}
        </span>
      ))}
    </div>
  );
}

/** One agenda/day row: the time in its type's colour, a matching left border, the run name and a
 *  sub-line (the type spelled out, when separated, plus the schedule). */
function OccurrenceRow({ occ, style, showType }: { occ: RunOccurrence; style: TypeStyle; showType: boolean }) {
  return (
    <div className={cn('flex items-center gap-3 rounded-md border border-separator border-l-2 bg-surface px-3 py-2', style.borderLeft)}>
      <span className={cn('w-14 shrink-0 font-mono text-footnote tabular-nums', style.text)}>{fmtTime(occ.at)}</span>
      <div className="min-w-0 flex-1">
        <p className="truncate text-footnote text-text-primary">{occ.runName}</p>
        <p className="truncate text-caption text-text-tertiary">{showType ? `${style.label} · ${occ.schedule}` : occ.schedule}</p>
      </div>
      {showType && <span className={cn('h-2 w-2 shrink-0 rounded-full', style.dot)} />}
    </div>
  );
}

/** Tag — the focused day as a vertical time-of-day list. */
function TagView({
  occ,
  styleOf,
  showTypes,
}: {
  occ: RunOccurrence[];
  styleOf: (o: RunOccurrence) => TypeStyle;
  showTypes: boolean;
}) {
  if (occ.length === 0) {
    return (
      <div className="rounded-card border border-separator bg-surface px-4 py-8 text-center">
        <p className="text-footnote text-text-tertiary">Für diesen Tag sind keine Läufe geplant.</p>
      </div>
    );
  }
  return (
    <div className="flex flex-col gap-1.5">
      {occ.map((o, i) => (
        <OccurrenceRow key={`${o.runId}:${o.at}:${i}`} occ={o} style={styleOf(o)} showType={showTypes} />
      ))}
    </div>
  );
}

/** Woche — the focused week as a 7-column Mo→So grid; each column stacks that day's occurrences as
 *  small chips and scrolls if it grows tall. The column header opens the day in the Tag view. */
function WocheView({
  days,
  today,
  byDay,
  styleOf,
  onPickDay,
}: {
  days: Date[];
  today: Date;
  byDay: Map<string, RunOccurrence[]>;
  styleOf: (o: RunOccurrence) => TypeStyle;
  onPickDay: (d: Date) => void;
}) {
  return (
    <div className="grid grid-cols-1 gap-2 sm:grid-cols-7">
      {days.map((day) => {
        const occ = byDay.get(dayKey(day)) ?? [];
        const isToday = sameDay(day, today);
        return (
          <div key={dayKey(day)} className="flex min-w-0 flex-col overflow-hidden rounded-card border border-separator bg-surface">
            <button
              type="button"
              onClick={() => onPickDay(day)}
              className={cn(
                'border-b border-separator px-2 py-1.5 text-center transition duration-fast hover:bg-fill/10',
                isToday && 'bg-accent/10',
              )}
            >
              <p className="text-caption text-text-tertiary">{day.toLocaleDateString('de-DE', { weekday: 'short' })}</p>
              <p className={cn('text-footnote font-semibold', isToday ? 'text-accent' : 'text-text-primary')}>{day.getDate()}</p>
            </button>
            <div className="dl-scroll flex max-h-[60vh] min-h-[3rem] flex-1 flex-col gap-1 overflow-y-auto p-1.5">
              {occ.length === 0 ? (
                <p className="px-1 py-1 text-center text-caption text-text-tertiary">—</p>
              ) : (
                occ.map((o, i) => {
                  const s = styleOf(o);
                  return (
                    <div
                      key={`${o.runId}:${o.at}:${i}`}
                      className={cn('flex flex-col gap-0.5 rounded border border-l-2 border-separator bg-bg-base px-1.5 py-1', s.borderLeft)}
                    >
                      <span className={cn('font-mono text-caption tabular-nums', s.text)}>{fmtTime(o.at)}</span>
                      <span className="truncate text-caption text-text-primary">{o.runName}</span>
                    </div>
                  );
                })
              )}
            </div>
          </div>
        );
      })}
    </div>
  );
}

/** Agenda — the whole window grouped by day, flowing into as many columns as the pane affords. */
function AgendaView({
  groups,
  styleOf,
  showTypes,
}: {
  groups: { key: string; date: Date; occ: RunOccurrence[] }[];
  styleOf: (o: RunOccurrence) => TypeStyle;
  showTypes: boolean;
}) {
  if (groups.length === 0) {
    return <p className="text-footnote text-text-tertiary">Keine anstehenden Einträge im Zeitraum.</p>;
  }
  return (
    <div className="grid gap-5 sm:grid-cols-2 2xl:grid-cols-3">
      {groups.map((g) => (
        <div key={g.key}>
          <p className="mb-2 text-footnote font-semibold text-text-primary">
            {g.date.toLocaleDateString('de-DE', { weekday: 'long', day: 'numeric', month: 'long' })}
          </p>
          <div className="flex flex-col gap-1.5">
            {g.occ.map((o, i) => (
              <OccurrenceRow key={`${o.runId}:${o.at}:${i}`} occ={o} style={styleOf(o)} showType={showTypes} />
            ))}
          </div>
        </div>
      ))}
    </div>
  );
}

/** The month mini-grid (Mo-first): days that hold occurrences are tinted and carry a marker (dots per
 *  kind when separated, else a count); today is ringed and the focused day is filled. Clicking a day
 *  lifts it to the parent (which opens the Tag view); the header browses months. */
function MiniMonth({
  anchor,
  today,
  byDay,
  showTypes,
  onPick,
  onShiftMonth,
}: {
  anchor: Date;
  today: Date;
  byDay: Map<string, RunOccurrence[]>;
  showTypes: boolean;
  onPick: (d: Date) => void;
  onShiftMonth: (delta: number) => void;
}) {
  const year = anchor.getFullYear();
  const month = anchor.getMonth();
  const first = new Date(year, month, 1);
  const offset = (first.getDay() + 6) % 7; // Monday-first lead-in
  const daysInMonth = new Date(year, month + 1, 0).getDate();

  const cells: (number | null)[] = [];
  for (let i = 0; i < offset; i += 1) cells.push(null);
  for (let day = 1; day <= daysInMonth; day += 1) cells.push(day);
  while (cells.length % 7 !== 0) cells.push(null);

  return (
    <div className="rounded-card border border-separator bg-surface p-3">
      <div className="mb-2 flex items-center justify-between gap-2">
        <p className="text-footnote font-medium text-text-primary">
          {first.toLocaleDateString('de-DE', { month: 'long', year: 'numeric' })}
        </p>
        <div className="flex items-center gap-0.5">
          <IconButton label="Vorheriger Monat" onClick={() => onShiftMonth(-1)}>
            <ChevronRightIcon className="h-4 w-4 rotate-180" />
          </IconButton>
          <IconButton label="Nächster Monat" onClick={() => onShiftMonth(1)}>
            <ChevronRightIcon className="h-4 w-4" />
          </IconButton>
        </div>
      </div>
      <div className="grid grid-cols-7 gap-1 text-center">
        {WEEKDAY_LABELS.map((d) => (
          <div key={d} className="pb-1 text-caption text-text-tertiary">
            {d}
          </div>
        ))}
        {cells.map((c, i) => {
          if (c === null) return <div key={`b${i}`} />;
          const date = new Date(year, month, c);
          const occ = byDay.get(dayKey(date)) ?? [];
          let auto = 0;
          let todo = 0;
          for (const o of occ) {
            if (showTypes && (o.type ?? 'auto') === 'todo') todo += 1;
            else auto += 1;
          }
          const has = occ.length > 0;
          const isSelected = sameDay(date, anchor);
          const isToday = sameDay(date, today);
          return (
            <button
              key={`d${c}`}
              type="button"
              onClick={() => onPick(date)}
              title={has ? `${occ.length} ${occ.length === 1 ? 'Eintrag' : 'Einträge'}` : undefined}
              className={cn(
                'flex h-10 flex-col items-center justify-center gap-0.5 rounded-md text-caption transition duration-fast',
                isSelected
                  ? 'bg-accent font-semibold text-accent-fg'
                  : cn(
                      showTypes && auto > 0 && todo > 0
                        ? 'bg-fill/15 text-text-primary'
                        : todo > 0
                          ? 'bg-warning/10 text-text-primary'
                          : auto > 0
                            ? 'bg-accent/10 text-text-primary'
                            : 'text-text-secondary hover:bg-fill/10',
                      isToday && 'ring-1 ring-accent',
                    ),
              )}
            >
              <span>{c}</span>
              {!isSelected && has && (
                showTypes ? (
                  <span className="flex items-center gap-0.5">
                    {auto > 0 && <span className="h-1.5 w-1.5 rounded-full bg-accent" />}
                    {todo > 0 && <span className="h-1.5 w-1.5 rounded-full bg-warning" />}
                  </span>
                ) : (
                  <span className="text-[10px] font-medium text-accent">{occ.length}</span>
                )
              )}
            </button>
          );
        })}
      </div>
    </div>
  );
}

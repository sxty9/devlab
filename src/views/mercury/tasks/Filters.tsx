import { Dropdown, DropdownItem, DropdownLabel } from '@/ui/Dropdown';
import { cn } from '@/lib/cn';
import type { Run } from '@/types';

const MS_HOUR = 3_600_000;
const MS_DAY = 86_400_000;

/** "Not run since" — keep runs whose last execution is at least this long ago (never-run counts). */
export type IdleSince = 'any' | '1d' | '7d' | '30d';
/** "Due within" — keep runs whose next firing (auto) or due date (todo) lands within this window. */
export type DueWithin = 'any' | '1h' | '24h' | '7d';

/** The two windows a run/todo list can be narrowed by. `any` on a field disables that filter. */
export interface RunFilter {
  idleSince: IdleSince;
  dueWithin: DueWithin;
}

export const NO_RUN_FILTER: RunFilter = { idleSince: 'any', dueWithin: 'any' };

const IDLE_MS: Record<Exclude<IdleSince, 'any'>, number> = {
  '1d': MS_DAY,
  '7d': 7 * MS_DAY,
  '30d': 30 * MS_DAY,
};
const DUE_MS: Record<Exclude<DueWithin, 'any'>, number> = {
  '1h': MS_HOUR,
  '24h': 24 * MS_HOUR,
  '7d': 7 * MS_DAY,
};

const parseTime = (iso?: string | null): number | null => {
  if (!iso) return null;
  const t = Date.parse(iso);
  return Number.isNaN(t) ? null : t;
};

/** The moment a run is next due. The SLIM definition carries no fire pointer (B-20) — a
 *  todo is due on its dueAt; an auto run's next fire is derived server-side, so the filter
 *  gets it injected (B8 wires lastStarted/nextDue from the execution documents). */
function dueTime(run: Run, nextDue?: Record<string, string>): number | null {
  if (run.kind === 'todo') return parseTime(run.dueAt);
  return parseTime(nextDue?.[run.id]);
}

/** Narrow a run/todo list by the "not run since" and "due within" windows. Pure: `now` is injected so
 *  the same inputs always yield the same output. Reused by the runs and todos lists (symmetry). */
export function applyRunFilter(
  runs: Run[],
  f: RunFilter,
  now: number = Date.now(),
  derived?: { lastStarted?: Record<string, string>; nextDue?: Record<string, string> },
): Run[] {
  return runs.filter((run) => {
    if (f.idleSince !== 'any') {
      const last = parseTime(derived?.lastStarted?.[run.id]);
      // "not run since X" — a never-run entry qualifies (it has not run within the window either).
      if (last !== null && now - last < IDLE_MS[f.idleSince]) return false;
    }
    if (f.dueWithin !== 'any') {
      const due = dueTime(run, derived?.nextDue);
      if (due === null || due > now + DUE_MS[f.dueWithin]) return false;
    }
    return true;
  });
}

interface Option<T extends string> {
  key: T;
  label: string;
  chip: string;
}

const IDLE_OPTIONS: Option<IdleSince>[] = [
  { key: 'any', label: 'Any time', chip: 'any' },
  { key: '1d', label: '1+ days', chip: '1d+' },
  { key: '7d', label: '7+ days', chip: '7d+' },
  { key: '30d', label: '30+ days', chip: '30d+' },
];
const DUE_OPTIONS: Option<DueWithin>[] = [
  { key: 'any', label: 'Any time', chip: 'any' },
  { key: '1h', label: 'Within 1 hour', chip: '1h' },
  { key: '24h', label: 'Within 24 hours', chip: '24h' },
  { key: '7d', label: 'Within 7 days', chip: '7d' },
];

function FilterChip<T extends string>({
  prefix,
  label,
  options,
  value,
  onPick,
}: {
  prefix: string;
  label: string;
  options: Option<T>[];
  value: T;
  onPick: (v: T) => void;
}) {
  const active = value !== 'any';
  const current = options.find((o) => o.key === value) ?? options[0];
  return (
    <Dropdown
      ariaLabel={label}
      triggerClassName={cn(
        'h-7 min-w-0 flex-1 justify-between border',
        active ? 'border-accent/40 bg-accent/10 text-accent' : 'border-separator text-text-secondary',
      )}
      trigger={
        <span className="min-w-0 flex-1 truncate text-left text-caption font-medium">
          {prefix} · {current.chip}
        </span>
      }
    >
      {(close) => (
        <>
          <DropdownLabel>{label}</DropdownLabel>
          {options.map((o) => (
            <DropdownItem
              key={o.key}
              title={o.label}
              selected={o.key === value}
              onClick={() => {
                onPick(o.key);
                close();
              }}
            />
          ))}
        </>
      )}
    </Dropdown>
  );
}

/** The filter toolbar above a run/todo list: a "not run since" chip and a "due within" chip. `showIdle`
 *  is false for the ToDos list, where every entry is never-run so that window is structurally moot. */
export function RunFilterBar({
  filter,
  onChange,
  showIdle = true,
}: {
  filter: RunFilter;
  onChange: (f: RunFilter) => void;
  showIdle?: boolean;
}) {
  return (
    <div className="flex items-center gap-1.5">
      {showIdle && (
        <FilterChip
          prefix="Not run"
          label="Not run in"
          options={IDLE_OPTIONS}
          value={filter.idleSince}
          onPick={(v) => onChange({ ...filter, idleSince: v })}
        />
      )}
      <FilterChip
        prefix="Due"
        label="Due within"
        options={DUE_OPTIONS}
        value={filter.dueWithin}
        onPick={(v) => onChange({ ...filter, dueWithin: v })}
      />
    </div>
  );
}

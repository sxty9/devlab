// TaskList — the SHARED list building block of both kinds (REQ-005; with TaskForm it dissolves
// the old RunsView/TodosView structural twin, B-32/B-33). Presentational: it renders the rows
// it is given (the caller applies the filter and the open/history split), shows each entry's
// kind-specific facts plus its EXPLICIT tuning (a reference to the defaults shows nothing,
// REQ-010.2), and supports keyboard navigation over the rows (arrow keys move the selection).
import { useCallback } from 'react';
import { cn } from '@/lib/cn';
import { fmtDateTime } from '@/lib/format';
import { openTodoStateNote, openTodoStatePill, scheduleSummary, targetLabel, tuningChips } from './logic';
import { TonePill } from '../exec/PipelineStages';
import type { OpenTodoState } from './select';
import type { Repo, Run } from '@/types';

export interface TaskListProps {
  /** The rows to show — for todos the OPEN list (the caller split history off, REQ-011.2). */
  runs: Run[];
  /** Repo display names for todo target lines. */
  repos?: Repo[];
  selectedId: string | null;
  onSelect: (id: string) => void;
  /** Live executions by run id — a running entry is marked. */
  liveRunIds?: ReadonlySet<string>;
  /** WHY each open todo is still open, by run id — the derived state shown as a pill so no open row
   *  stands mute (REQ-011.2). Absent for the auto surface, whose rows carry no such state. */
  stateByRun?: Record<string, OpenTodoState>;
  emptyText: string;
  /** Shown when `runs` is empty but the unfiltered set was not. */
  noMatchText?: string;
  filtered?: boolean;
}

export function TaskList({ runs, repos = [], selectedId, onSelect, liveRunIds, stateByRun, emptyText, noMatchText, filtered }: TaskListProps) {
  // Arrow keys move the selection through the list (keyboard navigation over list-like UI).
  const onKeyDown = useCallback(
    (e: React.KeyboardEvent) => {
      if (e.key !== 'ArrowDown' && e.key !== 'ArrowUp') return;
      if (runs.length === 0) return;
      e.preventDefault();
      const idx = runs.findIndex((r) => r.id === selectedId);
      const next =
        e.key === 'ArrowDown' ? Math.min(idx + 1, runs.length - 1) : Math.max(idx < 0 ? runs.length - 1 : idx - 1, 0);
      onSelect(runs[next].id);
    },
    [runs, selectedId, onSelect],
  );

  if (runs.length === 0) {
    return <p className="px-2.5 py-3 text-caption text-text-tertiary">{filtered ? (noMatchText ?? emptyText) : emptyText}</p>;
  }
  return (
    <div className="flex flex-col gap-0.5" role="listbox" aria-label="Tasks" onKeyDown={onKeyDown}>
      {runs.map((run) => (
        <TaskRow
          key={run.id}
          run={run}
          repos={repos}
          selected={selectedId === run.id}
          live={liveRunIds?.has(run.id) ?? false}
          state={stateByRun?.[run.id]}
          onSelect={() => onSelect(run.id)}
        />
      ))}
    </div>
  );
}

/** One row: title, the kind's plan line (schedule / targets + due), explicit tuning chips and
 *  state pills. The list carries no completed todos (they live in the history, REQ-011.2), so no
 *  "done" state exists here. For a todo the header pill is the DERIVED open state (why it is still
 *  open); for an auto run it is the live/inactive marker as before. */
function TaskRow({
  run,
  repos,
  selected,
  live,
  state,
  onSelect,
}: {
  run: Run;
  repos: Repo[];
  selected: boolean;
  live: boolean;
  state?: OpenTodoState;
  onSelect: () => void;
}) {
  const isTodo = run.kind === 'todo';
  const chips = tuningChips(run.tuning);
  // A todo names WHY it is open; the pill's derivation already folds "running" in (it reads the
  // same live set), so there is one pill, not a live badge beside a state badge.
  const pill = isTodo && state ? openTodoStatePill(state) : null;
  const note = isTodo && state ? openTodoStateNote(state, fmtDateTime) : '';
  return (
    <button
      type="button"
      role="option"
      aria-selected={selected}
      onClick={onSelect}
      className={cn(
        'flex w-full flex-col gap-1 rounded-md px-2.5 py-2 text-left transition duration-fast',
        selected ? 'bg-accent/15' : 'hover:bg-fill/10',
      )}
    >
      <div className="flex items-center gap-1.5">
        <span className={cn('min-w-0 flex-1 truncate text-footnote font-medium', selected ? 'text-text-primary' : 'text-text-secondary')}>
          {run.title}
        </span>
        {pill && <TonePill label={pill.label} tone={pill.tone} pulse={pill.pulse} />}
        {!isTodo && live && (
          <span className="flex shrink-0 items-center gap-1 rounded bg-warning/15 px-1.5 py-0.5 text-caption font-medium text-warning">
            <span className="h-1.5 w-1.5 animate-pulse rounded-full bg-warning" /> running
          </span>
        )}
        {!isTodo && !run.active && (
          <span className="shrink-0 rounded bg-fill/15 px-1.5 py-0.5 text-caption font-medium text-text-tertiary">inactive</span>
        )}
      </div>
      <span className="truncate text-caption text-text-tertiary">
        {isTodo ? targetLabel(run, repos) : scheduleSummary(run.schedule)}
      </span>
      {isTodo && (
        <span className="text-caption text-text-tertiary">Due: {run.dueAt ? fmtDateTime(run.dueAt) : 'on demand'}</span>
      )}
      {note && <span className="truncate text-caption text-text-tertiary">{note}</span>}
      {chips.length > 0 && (
        <span className="flex flex-wrap gap-1">
          {chips.map((c) => (
            <span key={c} className="rounded bg-fill/10 px-1.5 py-0.5 text-caption font-medium text-text-secondary">
              {c}
            </span>
          ))}
        </span>
      )}
    </button>
  );
}

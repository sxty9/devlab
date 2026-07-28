// Surface — the ONE list/detail/editor scaffold both task surfaces instantiate (REQ-005: two
// kinds, one machinery; with TaskForm/TaskList this dissolves the old structural twin). It
// owns the data of its kind: the run definitions, the kind's executions (for the open/history
// split, REQ-011.2, and the "not run since" filter), the delivery ledger (open deliveries keep
// an execution out of the history, B-8) and what is live right now. Every dataset refreshes
// over the one live stream — no polling (REQ-034).
import { useCallback, useEffect, useMemo, useState, type ReactNode } from 'react';
import { getDataSource } from '@/data';
import { useToast } from '@/ui/Toast';
import { Button } from '@/ui/Button';
import { ErrorBoundary } from '@/ui/ErrorBoundary';
import { useLiveTopic } from '@/state/live';
import { cn } from '@/lib/cn';
import { PlusIcon } from '@/ui/icons';
import { NO_RUN_FILTER, RunFilterBar, applyRunFilter, type RunFilter } from './Filters';
import { TaskForm } from './TaskForm';
import { TaskList } from './TaskList';
import { TaskDetail } from './TaskDetail';
import { errMsg } from './logic';
import { openDeliveryExecutionIds, splitOpenHistory } from './select';
import type { Repo, RunCoverage, RunKind, RunList, RunResult } from '@/types';

export interface SurfaceProps {
  kind: RunKind;
  newLabel: string;
  emptyText: string;
  /** Extra toolbar actions of this surface (the auto surface adds its AI planning). */
  toolbar?: (ctx: { reload: () => Promise<void>; coverage: RunCoverage | null }) => ReactNode;
}

export function TaskSurface({ kind, newLabel, emptyText, toolbar }: SurfaceProps) {
  const source = useMemo(() => getDataSource(), []);
  const { toast } = useToast();
  const isTodo = kind === 'todo';

  const [list, setList] = useState<RunList | null>(null);
  const [coverage, setCoverage] = useState<RunCoverage | null>(null);
  const [repos, setRepos] = useState<Repo[]>([]);
  const [executions, setExecutions] = useState<RunResult[]>([]);
  const [liveRunIds, setLiveRunIds] = useState<ReadonlySet<string>>(() => new Set());
  const [nextDue, setNextDue] = useState<Record<string, string>>({});
  const [failed, setFailed] = useState<string | null>(null);

  const [selectedId, setSelectedId] = useState<string | null>(null);
  const [mode, setMode] = useState<'view' | 'create' | 'edit'>('view');
  const [filter, setFilter] = useState<RunFilter>(NO_RUN_FILTER);

  // ── Reads (each refetched by its live topic; the first failure owns the pane) ──
  const reload = useCallback(async () => {
    try {
      const l = await source.mercuryRuns();
      setList(l);
      setFailed(null);
      if (!isTodo) {
        setCoverage(await source.mercuryRunCoverage());
        // The auto filter's "due within" needs each run's next firing — served by the same
        // calendar access the calendar view reads (REQ-012: no second data path).
        const cal = await source.mercuryRunCalendar(30, kind);
        const due: Record<string, string> = {};
        for (const occ of cal.occurrences) {
          if (!occ.resultId && (!due[occ.runId] || occ.at < due[occ.runId])) due[occ.runId] = occ.at;
        }
        setNextDue(due);
      }
    } catch (e) {
      setFailed((prev) => prev ?? errMsg(e));
    }
  }, [source, isTodo, kind]);

  const reloadExecutions = useCallback(async () => {
    try {
      setExecutions((await source.mercuryRunExecutions(kind)).executions);
    } catch {
      /* keep the last good list */
    }
  }, [source, kind]);

  const reloadActive = useCallback(async () => {
    try {
      const { active } = await source.mercuryRunActive();
      setLiveRunIds(new Set(active.map((a) => a.runId)));
    } catch {
      /* keep the last good set */
    }
  }, [source]);

  useEffect(() => {
    void reload();
    void reloadExecutions();
    void reloadActive();
    if (isTodo) {
      source
        .repos()
        .then(setRepos)
        .catch((e) => toast({ title: 'Could not load the repositories', description: errMsg(e), variant: 'danger' }));
    }
  }, [source, isTodo, reload, reloadExecutions, reloadActive, toast]);

  // The one live mechanism keeps every dataset fresh — foreign sessions, autonomous runs and
  // merges appear on their own (REQ-034); a resting view causes no periodic requests. A merge
  // stamps results (B-8), so the deliveries topic refreshes the executions too.
  useLiveTopic('runs', reload);
  useLiveTopic('progress', reloadExecutions);
  useLiveTopic('deliveries', reloadExecutions);
  useLiveTopic('active', reloadActive);

  const handleSaved = useCallback(
    async (id: string) => {
      setSelectedId(id);
      setMode('view');
      await Promise.all([reload(), reloadActive()]);
    },
    [reload, reloadActive],
  );

  const handleDeleted = useCallback(async () => {
    setSelectedId(null);
    setMode('view');
    await reload();
  }, [reload]);

  if (failed) {
    return (
      <div className="flex h-full min-h-0 w-full items-center justify-center bg-bg-base px-6">
        <p className="max-w-md text-center text-footnote text-text-secondary">{failed}</p>
      </div>
    );
  }
  if (!list) {
    return (
      <div className="flex h-full min-h-0 w-full items-center justify-center bg-bg-base">
        <p className="text-footnote text-text-tertiary">Loading…</p>
      </div>
    );
  }

  const ofKind = list.runs.filter((r) => r.kind === kind);
  // Open ∩ history = ∅ (REQ-011.2): a done todo leaves this list; its executions live in the
  // History section. An open delivery keeps its execution — and so its todo — open (B-8).
  const { open } = splitOpenHistory(ofKind, executions, openDeliveryExecutionIds(executions));
  const shownBase = isTodo ? open : ofKind;

  const lastStarted: Record<string, string> = {};
  for (const res of executions) {
    if (!lastStarted[res.runId] || res.startedAt > lastStarted[res.runId]) lastStarted[res.runId] = res.startedAt;
  }
  const shown = applyRunFilter(shownBase, filter, Date.now(), { lastStarted, nextDue });
  const selected = selectedId ? (ofKind.find((r) => r.id === selectedId) ?? null) : null;

  let rightPane: ReactNode;
  if (mode === 'create') {
    rightPane = (
      <TaskForm kind={kind} base={null} repos={repos} coverage={coverage} onCancel={() => setMode('view')} onSaved={handleSaved} onRunStarted={reloadActive} />
    );
  } else if (mode === 'edit' && selected) {
    rightPane = (
      <TaskForm
        key={selected.id}
        kind={kind}
        base={selected}
        repos={repos}
        coverage={coverage}
        onCancel={() => setMode('view')}
        onSaved={handleSaved}
        onRunStarted={reloadActive}
      />
    );
  } else if (selected) {
    rightPane = (
      <TaskDetail
        key={`${selected.id}:${selected.authorship.updatedAt}`}
        run={selected}
        repos={repos}
        axioms={list.axioms}
        live={liveRunIds.has(selected.id)}
        results={executions.filter((res) => res.runId === selected.id)}
        onEdit={() => setMode('edit')}
        onDeleted={handleDeleted}
        onRunStarted={reloadActive}
      />
    );
  } else {
    rightPane = (
      <div className="flex h-full min-h-0 items-center justify-center p-8">
        <p className="max-w-md text-center text-footnote text-text-tertiary">
          {isTodo ? 'Pick a todo on the left or create a new one.' : 'Pick a run on the left or create a new one.'}
        </p>
      </div>
    );
  }

  return (
    // w-full: the hosting pane is a flex ROW — without it this surface shrinks to content width.
    <div className="flex h-full min-h-0 w-full">
      {/* LEFT — toolbar + list */}
      <div className="flex w-80 shrink-0 flex-col border-r border-separator bg-surface">
        <div className="flex flex-col gap-2 border-b border-separator p-2">
          <Button
            variant="primary"
            size="sm"
            onClick={() => {
              setSelectedId(null);
              setMode('create');
            }}
          >
            <PlusIcon className="h-4 w-4" /> {newLabel}
          </Button>
          {toolbar?.({ reload, coverage })}
          {shownBase.length > 0 && <RunFilterBar filter={filter} onChange={setFilter} showIdle={!isTodo} />}
          {liveRunIds.size > 0 && (
            <span className="flex items-center gap-1.5 text-caption text-text-secondary">
              <span className="h-2 w-2 animate-pulse rounded-full bg-warning" />
              {liveRunIds.size === 1 ? '1 execution live' : `${liveRunIds.size} executions live`}
            </span>
          )}
        </div>
        <div className={cn('dl-scroll flex-1 overflow-y-auto p-1.5')}>
          <TaskList
            runs={shown}
            repos={repos}
            selectedId={mode === 'create' ? null : selectedId}
            onSelect={(id) => {
              setSelectedId(id);
              setMode('view');
            }}
            liveRunIds={liveRunIds}
            emptyText={emptyText}
            noMatchText="Nothing matches the current filter."
            filtered={shownBase.length > 0 && shown.length === 0}
          />
        </div>
      </div>

      {/* RIGHT — detail, editor, or placeholder. Boundary-wrapped so a single dead/partial
          entry only fails its own pane while the list stays live. */}
      <div className="dl-scroll min-h-0 flex-1 overflow-y-auto bg-bg-base">
        <ErrorBoundary resetKeys={[selectedId, mode]}>{rightPane}</ErrorBoundary>
      </div>
    </div>
  );
}

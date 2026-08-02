// BlockedTasks — the failed / stuck TODOS of the Blocked surface (WHAT-4). The Blocked tab shows
// "a failed layer OR an open question": the questions are BlockedPanel's job; this lists the todos
// whose lifecycle state is a failed one — a run that failed or left a target uncovered, or a delivery
// stuck at the tip. It reads the SAME partition every lifecycle tab reads (tasks/select: openTodoState
// → taskBucket), so a task shown here appears in no other tab, and every failed task appears here.
//
// It is deliberately read-only and compact: it names WHAT is stuck and WHY (the "no silent absence"
// axiom at the list level), and points at the repository. Acting on it — re-running the task — is the
// existing run flow on the todo itself; this surface exists so a failed layer is never invisible.
import { useCallback, useEffect, useMemo, useState } from 'react';
import { getDataSource } from '@/data';
import { useLiveTopic } from '@/state/live';
import { fmtDateTime } from '@/lib/format';
import type { Delivery, Repo, Run, RunResult } from '@/types';
import { openTodoState, splitOpenHistory, taskBucket } from './tasks/select';
import { openTodoStateNote } from './tasks/logic';
import { openDeliveryExecutionIds, openDeliveryFactsByExecution } from './deliveries/deliveries';

interface BlockedTask {
  run: Run;
  reason: string;
  repos: string;
}

/** The todos whose lifecycle state falls in the 'blocked' bucket (failed / stuck), each with the
 *  reason its state carries, so no row stands mute. Pure over the passive pools — same derivation as
 *  every other lifecycle tab. */
export function blockedTasks(
  runs: Run[],
  executions: RunResult[],
  deliveries: Delivery[],
  liveRunIds: ReadonlySet<string>,
  repos: Repo[],
  at: (iso: string) => string,
): BlockedTask[] {
  const openIds = openDeliveryExecutionIds(deliveries);
  const facts = openDeliveryFactsByExecution(deliveries);
  const { open } = splitOpenHistory(runs, executions, openIds);
  const out: BlockedTask[] = [];
  for (const run of open) {
    const state = openTodoState(run, executions, openIds, facts, liveRunIds);
    if (taskBucket(state) !== 'blocked') continue;
    const note = openTodoStateNote(state, at);
    const reason = note || (state.kind === 'failed' ? 'The last run failed or left a target uncovered.' : 'Blocked.');
    const names = (run.targets ?? [])
      .map((t) => (t.create ? `new: ${t.repo}` : (repos.find((r) => r.id === t.repo)?.name ?? t.repo)))
      .join(', ');
    out.push({ run, reason, repos: names || '—' });
  }
  return out;
}

export function BlockedTasks() {
  const source = useMemo(() => getDataSource(), []);
  const [runs, setRuns] = useState<Run[]>([]);
  const [executions, setExecutions] = useState<RunResult[]>([]);
  const [deliveries, setDeliveries] = useState<Delivery[]>([]);
  const [liveRunIds, setLiveRunIds] = useState<ReadonlySet<string>>(() => new Set());
  const [repos, setRepos] = useState<Repo[]>([]);

  const reload = useCallback(async () => {
    try {
      const [l, ex, dv, ac, rp] = await Promise.all([
        source.mercuryRuns(),
        source.mercuryRunExecutions('todo'),
        source.mercuryDeliveries(),
        source.mercuryRunActive(),
        source.repos(),
      ]);
      setRuns(l.runs.filter((r) => r.kind === 'todo'));
      setExecutions(ex.executions);
      setDeliveries(dv);
      setLiveRunIds(new Set(ac.active.map((a) => a.runId)));
      setRepos(rp);
    } catch {
      /* keep the last good data — a transient read never clears a real blockade */
    }
  }, [source]);

  useEffect(() => {
    void reload();
  }, [reload]);
  useLiveTopic('runs', () => void reload());
  useLiveTopic('progress', () => void reload());
  useLiveTopic('deliveries', () => void reload());
  useLiveTopic('active', () => void reload());

  const list = blockedTasks(runs, executions, deliveries, liveRunIds, repos, fmtDateTime);
  if (list.length === 0) return null;

  return (
    <section className="mb-4">
      <h3 className="mb-2 text-caption font-medium uppercase tracking-wide text-text-tertiary">Failed &amp; stuck tasks</h3>
      <ul className="flex flex-col gap-2">
        {list.map((b) => (
          <li key={b.run.id} className="rounded-md border border-danger/40 bg-danger/[0.05] px-3 py-2.5">
            <div className="flex flex-wrap items-center gap-1.5">
              <span className="rounded bg-danger/[0.12] px-1.5 py-0.5 text-caption text-danger">Failed layer</span>
              <span className="text-caption font-medium text-text-primary">{b.run.title}</span>
              <span className="text-caption text-text-secondary">{b.repos}</span>
            </div>
            <p className="mt-1.5 whitespace-pre-wrap break-words text-footnote text-text-secondary">{b.reason}</p>
          </li>
        ))}
      </ul>
    </section>
  );
}

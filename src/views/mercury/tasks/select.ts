// Open/history selectors — the client mirror of the backend derivation (runs/derive.go): pure
// functions over the passive pools, nothing stored (REQ-011.2, REQ-037.1, B-8). Consumed by
// the task lists here and by the execution history surface.
import type { Run, RunResult } from '../../../types';

/** Whether an execution is history-ready: it ENDED and none of its deliveries is still open
 *  (open = neither merged nor closed with a reason). `openDeliveryExecutionIds` is the set of
 *  execution ids that still hold an open delivery, derived from the delivery ledger by the
 *  caller — an execution the ledger never names historizes on ending (a failed run delivered
 *  nothing). */
export function executionCompleted(res: RunResult, openDeliveryExecutionIds?: ReadonlySet<string>): boolean {
  if (!res.endedAt) return false;
  return !openDeliveryExecutionIds?.has(res.id);
}

/** Whether one completed execution successfully covered EVERY target of the run — each target
 *  repo appears in the server stage array as done and succeeded (the server derives those; the
 *  client only reads them, B-35). A target-less legacy record counts as covered. */
function executionCoversTargets(res: RunResult, run: Run): boolean {
  const targets = run.targets ?? [];
  if (targets.length === 0) return true;
  const byRepo = new Map(res.repos.map((rp) => [rp.repo, rp]));
  return targets.every((t) => {
    const rp = byRepo.get(t.repo);
    return !!rp && rp.done && rp.succeeded;
  });
}

/** Whether a run counts as done: only a todo can be (an auto run recurs forever), and only
 *  through at least one completed execution covering all its targets. */
export function runCompleted(run: Run, results: RunResult[], openDeliveryExecutionIds?: ReadonlySet<string>): boolean {
  if (run.kind !== 'todo') return false;
  return results.some(
    (res) => res.runId === run.id && executionCompleted(res, openDeliveryExecutionIds) && executionCoversTargets(res, run),
  );
}

/** Derive which executions still hold an open delivery FROM the server's own stamps: an ended
 *  execution that delivered (a server-stated executed pull-request stage) but is not yet
 *  stamped merged (`mergedAt` is the B-8 completion stamp — set once every delivery is merged,
 *  rolled back, or closed with a reason). Legacy archive entries predate the ledger and are
 *  never open. No stage is DERIVED here (B-35) — only server-provided state is read. */
export function openDeliveryExecutionIds(results: RunResult[]): ReadonlySet<string> {
  const out = new Set<string>();
  for (const res of results) {
    if (res.legacy || !res.endedAt || res.mergedAt) continue;
    const delivered = res.repos.some((rp) => rp.stages.some((s) => s.stage === 'pull-request' && s.state === 'executed'));
    if (delivered) out.add(res.id);
  }
  return out;
}

/** Split runs and executions into the open list and the history so that open ∩ history = ∅
 *  (REQ-011.2): a done todo leaves the list the moment its completing execution historizes; an
 *  unfinished execution appears nowhere in the history. */
export function splitOpenHistory(
  runs: Run[],
  results: RunResult[],
  openDeliveryExecutionIds?: ReadonlySet<string>,
): { open: Run[]; history: RunResult[] } {
  return {
    open: runs.filter((r) => !runCompleted(r, results, openDeliveryExecutionIds)),
    history: results.filter((res) => executionCompleted(res, openDeliveryExecutionIds)),
  };
}

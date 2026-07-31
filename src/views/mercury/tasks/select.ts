// Open/history selectors — the client mirror of the backend derivation (runs/derive.go): pure
// functions over the passive pools, nothing stored (REQ-011.2, REQ-037.1, B-8). Consumed by
// the task lists here and by the execution history surface.
//
// The one thing this module never does is decide a CHAIN stage. Which executions still hold an open
// delivery is read off the delivery ledger, where the server states it (deliveries.ts:
// openDeliveryExecutionIds) — never guessed from a stage name (B-35).
import type { Run, RunResult } from '../../../types';

/** Whether an execution is history-ready: it ENDED and none of its deliveries is still open
 *  (open = neither merged nor closed with a reason). `openDeliveryExecutionIds` is the set of
 *  execution ids that still hold an open delivery, read from the delivery ledger by the
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

/** What the history does NOT show, split by the REASON that keeps each record out — the exact
 *  complement of `splitOpenHistory`'s history, derived from the same predicate over the same pool.
 *
 *  A remainder computed as "everything minus the history" is a number without a subject: it counts
 *  records the list hides without saying what they are, so a count can stand beside an empty list
 *  and nobody can point at what it counts. The two reasons are the only two there are, and each has
 *  a place the record IS shown:
 *
 *   - `inFlight` — no end stamp: the execution is running, and the Active surface shows it.
 *   - `awaitingDelivery` — ended, but the ledger still holds a delivery of it open (B-8): the
 *     delivery ledger shows it, and its todo stays in the open list. */
export function outsideHistory(
  results: RunResult[],
  openDeliveryExecutionIds?: ReadonlySet<string>,
): { inFlight: RunResult[]; awaitingDelivery: RunResult[] } {
  const out: { inFlight: RunResult[]; awaitingDelivery: RunResult[] } = { inFlight: [], awaitingDelivery: [] };
  for (const res of results) {
    if (executionCompleted(res, openDeliveryExecutionIds)) continue;
    if (!res.endedAt) out.inFlight.push(res);
    else out.awaitingDelivery.push(res);
  }
  return out;
}

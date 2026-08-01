// Open/history selectors — the client mirror of the backend derivation (runs/derive.go): pure
// functions over the passive pools, nothing stored (REQ-011.2, REQ-037.1, B-8). Consumed by
// the task lists here and by the execution history surface.
//
// The one thing this module never does is decide a CHAIN stage. Which executions still hold an open
// delivery is read off the delivery ledger, where the server states it (deliveries.ts:
// openDeliveryExecutionIds) — never guessed from a stage name (B-35).
import type { Run, RunResult } from '../../../types';
import type { OpenDeliveryFacts } from '../deliveries/deliveries';

/** Whether an execution is history-ready: it ENDED and none of its deliveries is still open
 *  (open = neither merged nor closed with a reason). `openDeliveryExecutionIds` is the set of
 *  execution ids that still hold an open delivery, read from the delivery ledger by the
 *  caller — an execution the ledger never names historizes on ending (a failed run delivered
 *  nothing). */
export function executionCompleted(res: RunResult, openDeliveryExecutionIds?: ReadonlySet<string>): boolean {
  if (!res.endedAt) return false;
  return !openDeliveryExecutionIds?.has(res.id);
}

/** Whether ONE execution reports this target repo as done and succeeded — the server derives those
 *  flags; the client only reads them (B-35). */
function repoSucceeded(res: RunResult, repo: string): boolean {
  const rp = res.repos.find((p) => p.repo === repo);
  return !!rp && rp.done && rp.succeeded;
}

/** Whether a run counts as done: only a todo can be (an auto run recurs forever), and only once
 *  EVERY target repo has been carried through by SOME completed execution — the coverage is summed
 *  across the run's completed executions, not demanded of a single one.
 *
 *  A todo may finish its repositories in more than one execution — repository A in one, repository B
 *  in the next. Requiring a single execution to hold ALL targets would leave such a todo open for
 *  ever, even though every repository's work is delivered and settled. This is the same question the
 *  startup reconciliation answers (preflight.SyncStartupTodos: every target has a merged delivery of
 *  this run), so the two must not disagree: both sum coverage per repository over the run's
 *  executions. A target-less legacy run is done through any one completed execution. */
export function runCompleted(run: Run, results: RunResult[], openDeliveryExecutionIds?: ReadonlySet<string>): boolean {
  if (run.kind !== 'todo') return false;
  const completed = results.filter((res) => res.runId === run.id && executionCompleted(res, openDeliveryExecutionIds));
  if (completed.length === 0) return false;
  const targets = run.targets ?? [];
  if (targets.length === 0) return true;
  return targets.every((t) => completed.some((res) => repoSucceeded(res, t.repo)));
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

/** WHY a todo is still open — a derived state, never stored, that covers the open list completely:
 *  every open todo carries exactly one of these, so no row stands there mute (the "no silent
 *  absence" axiom, at the level of the list itself).
 *
 *   - `not-run`        — no execution exists yet; nobody has built it.
 *   - `running`        — an execution is in flight (the Active surface shows it move).
 *   - `awaiting-merge` — the chain ran through and shipped; its pull request is open and waits out
 *                        the auto-merge window. `mergeBy` names that deadline so the wait is visible.
 *   - `blocked`        — the delivery's merge retries are spent and it waits for an explicit release
 *                        (K-5); `reason` says why. The release is the ONE that already exists in the
 *                        notices surface — this only names the state, it does not add a second exit.
 *   - `failed`         — every execution ended, nothing is in flight and nothing awaits its merge,
 *                        yet the work did not carry through: the last run failed or left a target
 *                        uncovered, so the todo needs attention before it can finish.
 *
 *  `not-run` and `failed` stay SEPARATE on purpose: a never-built todo is merely pending, a failed
 *  one demands a person look — folding them would hide that a build was attempted and broke.
 *
 *  The running/awaiting split is READ from `outsideHistory`, the one partition the history is the
 *  complement of, rather than re-derived here — two derivations of the same fact are two truths that
 *  can drift (B-35). The blockade and the deadline of the open delivery are INJECTED by the caller
 *  (`openDeliveryExecutionIds` and `deliveryFacts`, both read from the delivery ledger where the
 *  server states them), the same way this module already takes the open-delivery set — the client
 *  reads the ledger's own stamps, it never derives them from a chain stage (B-35). */
export type OpenTodoState =
  | { kind: 'not-run' }
  | { kind: 'running' }
  | { kind: 'awaiting-merge'; mergeBy?: string }
  | { kind: 'blocked'; reason?: string }
  | { kind: 'failed' };

export function openTodoState(
  run: Run,
  results: RunResult[],
  openDeliveryExecutionIds: ReadonlySet<string>,
  deliveryFacts: ReadonlyMap<string, OpenDeliveryFacts>,
  liveRunIds?: ReadonlySet<string>,
): OpenTodoState {
  const execs = results.filter((res) => res.runId === run.id);
  if (execs.length === 0) return { kind: 'not-run' };
  if (liveRunIds?.has(run.id)) return { kind: 'running' };

  const outside = outsideHistory(execs, openDeliveryExecutionIds);
  if (outside.inFlight.length > 0) return { kind: 'running' };

  if (outside.awaitingDelivery.length > 0) {
    // Read the blockade and the deadline off exactly the executions that await a delivery — the
    // facts the caller joined from the ledger. A blocked delivery outranks the timed wait: it does
    // not move on its own.
    let reason: string | undefined;
    let blocked = false;
    let mergeBy: string | undefined;
    for (const res of outside.awaitingDelivery) {
      const f = deliveryFacts.get(res.id);
      if (!f) continue;
      if (f.blocked) {
        blocked = true;
        reason ??= f.reason;
      }
      if (f.mergeBy && (!mergeBy || f.mergeBy < mergeBy)) mergeBy = f.mergeBy;
    }
    if (blocked) return { kind: 'blocked', reason };
    return { kind: 'awaiting-merge', mergeBy };
  }

  return { kind: 'failed' };
}

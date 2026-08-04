// Open/history selectors — the client mirror of the backend derivation (runs/derive.go): pure
// functions over the passive pools, nothing stored (REQ-011.2, REQ-037.1, B-8). Consumed by
// the task lists here and by the execution history surface.
//
// The one thing this module never does is decide a CHAIN stage. Which executions still hold an open
// delivery is read off the delivery ledger, where the server states it (deliveries.ts:
// openDeliveryExecutionIds) — never guessed from a stage name (B-35).
import type { Run, RunResult } from '../../../types';
import type { OpenDeliveryFacts } from '../deliveries/deliveries';

/** Whether an execution is history-ready. HISTORISED IS STRICT (WHAT-4): the whole chain ran
 *  through — up to and including the production delivery — with no intervention and no approval
 *  needed. Anything short of that is not a history entry but a CURRENT state, and belongs in the
 *  tab that holds that state.
 *
 *  The old rule asked only "did it end, and does a delivery still hang on it". That let an execution
 *  which blocked or failed on an EARLY stage count as history: it produced no delivery, so nothing
 *  hung on it, so it slipped in — even though its chain never ran through. The positive proof the
 *  chain DID run through is the server's `mergedAt` stamp: the server sets it only once EVERY
 *  delivery of the execution is settled through production (runs.SettleExecution, B-8 + WHAT-1), or,
 *  for a startup-reconciliation check-off, once the work verifiably arrived in the default branch. A
 *  blocked or failed execution never earns that stamp.
 *
 *  So an execution historises iff it ENDED and either it is a frozen archive record (legacy — a
 *  closed past, display-only, that has no live tab to move to), or it carries the `mergedAt` stamp
 *  AND the live ledger agrees no delivery of it is still open (`openDeliveryExecutionIds`, the set of
 *  execution ids the ledger still holds open — read by the caller, never derived from a chain stage,
 *  B-35). */
export function executionCompleted(res: RunResult, openDeliveryExecutionIds?: ReadonlySet<string>): boolean {
  if (!res.endedAt) return false;
  // A frozen archive record is a closed past — history by construction, with no live state to move to.
  if (res.legacy) return true;
  // No production-settled stamp ⇒ the chain did not run through: a current state, not history.
  if (!res.mergedAt) return false;
  // The stamp says every delivery settled; the live ledger must agree it holds nothing open.
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
 *  and nobody can point at what it counts. The three reasons are the only three there are, and each
 *  has a place the record IS shown:
 *
 *   - `inFlight` — no end stamp: the execution is running, and the Active surface shows it.
 *   - `awaitingDelivery` — ended, and the ledger still holds a delivery of it open (B-8): a live
 *     automatic step is under way (an open pull request, or a merged delivery still owing its
 *     production step). The delivery ledger shows it, and its todo waits in the open list.
 *   - `failed` — ended, no live delivery, and no production-settled stamp: the chain did not run
 *     through and nothing automatic is in progress. It is not a wait — it needs attention, so it
 *     stands in Blocked as a failed task. This is the case the old two-way split wrongly hid in the
 *     history (a run that blocked early delivered nothing, so nothing "hung" on it). */
export function outsideHistory(
  results: RunResult[],
  openDeliveryExecutionIds?: ReadonlySet<string>,
): { inFlight: RunResult[]; awaitingDelivery: RunResult[]; failed: RunResult[] } {
  const out: { inFlight: RunResult[]; awaitingDelivery: RunResult[]; failed: RunResult[] } = {
    inFlight: [],
    awaitingDelivery: [],
    failed: [],
  };
  for (const res of results) {
    if (executionCompleted(res, openDeliveryExecutionIds)) continue;
    if (!res.endedAt) out.inFlight.push(res);
    else if (openDeliveryExecutionIds?.has(res.id)) out.awaitingDelivery.push(res);
    else out.failed.push(res);
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
 *                        the auto-merge window. `mergeBy` names that deadline when the maintenance
 *                        has stamped one; a freshly opened pull request is a wait even without it.
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
  | { kind: 'awaiting-prod'; retrying?: boolean; reason?: string }
  | { kind: 'blocked'; reason?: string }
  | { kind: 'failed' };

/** The FIVE lifecycle tabs a task lives in — the real "Lebenslauf" of a task (WHAT-4). Every open
 *  task sits in exactly ONE of these, derived from its `OpenTodoState`; a done task historizes and
 *  is shown in History instead. This is the ONE partition every tab reads, so no task can be in two
 *  tabs or in none:
 *
 *   - `todo`    — created and NEVER executed yet.
 *   - `active`  — being implemented OR a delivery is running RIGHT NOW (not: a delivery is pending).
 *   - `blocked` — a failed layer, or a run stopped to ask a question.
 *   - `pending` — the work is done and delivered; it only waits for the automated tail: the pull
 *                 request's merge window, or the production step after the merge. */
export type TaskBucket = 'todo' | 'active' | 'blocked' | 'pending';

/** Which lifecycle tab an open task's state belongs to — the pure partition WHAT-4 asks for. A
 *  production wait ('awaiting-prod') is a Pending state, never Blocked: the work merged, the stack is
 *  valid, and it only waits for (or retries) the last automated step — the user is not asked anything. */
export function taskBucket(state: OpenTodoState): TaskBucket {
  switch (state.kind) {
    case 'not-run':
      return 'todo';
    case 'running':
      return 'active';
    case 'awaiting-merge':
    case 'awaiting-prod':
      return 'pending';
    case 'blocked':
    case 'failed':
      return 'blocked';
  }
}

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
    // Read the blockade, the open pull request, the merge deadline and the production wait off exactly
    // the executions that hold a delivery open — the facts the caller joined from the ledger. The
    // order of precedence is the order of the lifecycle: a blocked delivery (a person is the only way
    // out) outranks everything; an open pull request (with or without a stamped deadline) outranks a
    // production wait, because a delivery only reaches production AFTER it merges; a production wait is
    // the last thing left.
    let reason: string | undefined;
    let blocked = false;
    let awaitingMerge = false;
    let mergeBy: string | undefined;
    let prodPending = false;
    let prodFailed = false;
    let prodReason: string | undefined;
    for (const res of outside.awaitingDelivery) {
      const f = deliveryFacts.get(res.id);
      if (!f) continue;
      if (f.blocked) {
        blocked = true;
        reason ??= f.reason;
      }
      if (f.awaitingMerge) awaitingMerge = true;
      if (f.mergeBy && (!mergeBy || f.mergeBy < mergeBy)) mergeBy = f.mergeBy;
      if (f.prodPending) prodPending = true;
      if (f.prodFailed) {
        prodFailed = true;
        prodReason ??= f.prodReason;
      }
    }
    if (blocked) return { kind: 'blocked', reason };
    // An open pull request is a live automatic step, so it is a wait — even before a deadline is
    // stamped (`mergeBy` may be undefined; the row then says "merges once its window elapses").
    if (awaitingMerge || mergeBy) return { kind: 'awaiting-merge', mergeBy };
    if (prodPending) return { kind: 'awaiting-prod', retrying: prodFailed, reason: prodReason };
    // No blockade, no open pull request, no production wait: there is no live automatic step, so this
    // is NOT a wait. Never invent an 'awaiting-merge' with no deadline and nothing running behind it —
    // that is exactly how a failed delivery landed in Pending. It needs attention: it is failed.
    // (Unreachable in practice — every ledger-held delivery sets one of the flags above — but the
    // default states the intent: absent a live step, the state is failed, never a fabricated wait.)
    return { kind: 'failed' };
  }

  // An ended execution with no live delivery and no production-settled stamp (outside.failed): the
  // chain did not run through and nothing automatic is pending. It needs a person — it is failed.
  return { kind: 'failed' };
}

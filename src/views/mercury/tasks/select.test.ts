import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { executionCompleted, openTodoState, outsideHistory, runCompleted, splitOpenHistory, taskBucket } from './select.ts';
// Which executions still hold an open delivery is READ off the ledger, where the server states it —
// these selectors never derive it, so the set and the delivery facts come from the delivery module
// (B-35).
import { openDeliveryExecutionIds, openDeliveryFactsByExecution } from '../deliveries/deliveries.ts';
import type { Delivery, Run, RunResult } from '../../../types';

const AUTH = { created: { user: 'alice' }, createdAt: '2026-07-26T09:00:00Z', updated: { user: 'alice' }, updatedAt: '2026-07-26T09:00:00Z' };

const okRepo = (repo: string) => ({
  repo,
  stages: [{ stage: 'done-something', state: 'executed' as const }],
  done: true,
  succeeded: true,
});
const failedRepo = (repo: string) => ({
  repo,
  stages: [{ stage: 'did-not-work', state: 'failed' as const, reason: 'boom' }],
  done: true,
  succeeded: false,
});

/** One ledger row, as the server states it (stage + the execution it arose from). */
const dlv = (id: string, executionId: string, stage: string): Delivery => ({
  id,
  repo: 'o/svc',
  branch: `mercury-delivery/${id}`,
  fromCommit: 'c0',
  toCommit: 'c1',
  createdAt: '2026-07-26T11:30:00Z',
  stage,
  executionId,
});

/** A generic ENDED execution — no MergedAt stamp. On its own this is a CURRENT state (its chain has
 *  not run through to production), never history: exactly the case the old rule wrongly hid in the
 *  history because "nothing hung on it". */
const result = (id: string, runId: string, overrides: Partial<RunResult> = {}): RunResult => ({
  id,
  runId,
  kind: 'todo',
  startedAt: '2026-07-26T10:00:00Z',
  endedAt: '2026-07-26T11:00:00Z',
  repos: [okRepo('svc')],
  usage: { inputTokens: 0, outputTokens: 0, costUSD: 0 },
  requested: AUTH,
  ...overrides,
});

/** An execution whose WHOLE chain ran through to production: the server stamps `mergedAt` only once
 *  every delivery of it is settled through production (SettleExecution, B-8 + WHAT-1). Only such an
 *  execution is history-ready under the strict rule. */
const merged = (id: string, runId: string, overrides: Partial<RunResult> = {}): RunResult =>
  result(id, runId, { mergedAt: '2026-07-26T12:00:00Z', ...overrides });

const todo = (id: string, targets: string[]): Run => ({
  id,
  kind: 'todo',
  title: id,
  targets: targets.map((repo) => ({ repo })),
  tuning: {},
  authorship: AUTH,
});

// ── BEFUND 1: historised is STRICT — the whole chain ran through, no intervention ────────────────

test('an execution historises ONLY once the whole chain ran through to production (BEFUND 1)', () => {
  // Not ended → never history.
  assert.equal(executionCompleted(result('exec_1', 'run_a', { endedAt: undefined })), false);

  // THE HOLE THIS CLOSES: an ended execution that blocked/failed on an early stage delivered nothing,
  // so nothing hangs on it — yet its chain never ran through. It is NOT history.
  assert.equal(executionCompleted(result('exec_early', 'run_a', { repos: [failedRepo('svc')] })), false);
  assert.equal(
    executionCompleted(result('exec_nostamp', 'run_a')),
    false,
    'ended but never settled through production (no mergedAt) ⇒ not history',
  );

  // The positive proof is the mergedAt stamp — the chain ran through.
  assert.equal(executionCompleted(merged('exec_1', 'run_a')), true);

  // A merged, production-settled execution held open by nothing historises; the ledger must agree.
  assert.equal(executionCompleted(merged('exec_1', 'run_a'), new Set(['exec_1'])), false, 'the ledger still holds it open');
  assert.equal(executionCompleted(merged('exec_1', 'run_a'), new Set(['exec_other'])), true, 'another execution held open does not matter');

  // A frozen archive record is a closed past — history by construction, stamp or not.
  assert.equal(executionCompleted(result('exec_legacy', 'run_a', { legacy: true, mergedAt: undefined })), true);
});

test('a todo is done only when one PRODUCTION-SETTLED execution covered ALL targets', () => {
  const t = todo('run_t', ['a', 'b']);
  const partial = merged('exec_1', 'run_t', { repos: [okRepo('a')] });
  const failed = merged('exec_2', 'run_t', { repos: [okRepo('a'), failedRepo('b')] });
  const full = merged('exec_3', 'run_t', { repos: [okRepo('a'), okRepo('b')] });

  assert.equal(runCompleted(t, [partial]), false);
  assert.equal(runCompleted(t, [failed]), false);
  assert.equal(runCompleted(t, [full]), true);
  // Covering every target is not enough: an ended-but-unshipped execution (no mergedAt) leaves it open.
  assert.equal(runCompleted(t, [result('exec_3', 'run_t', { repos: [okRepo('a'), okRepo('b')] })]), false, 'covered but never shipped — still open');
  assert.equal(runCompleted(t, [full], new Set(['exec_3'])), false, 'undelivered execution must not complete the todo');

  const auto: Run = { id: 'run_auto', kind: 'auto', title: 'A', tuning: {}, authorship: AUTH };
  assert.equal(runCompleted(auto, [merged('exec_4', 'run_auto')]), false, 'an auto run recurs — never done');
});

test('a todo is done when its targets are covered ACROSS executions, matching the startup reconciliation', () => {
  // Repository A finished in one execution, repository B in the next — the union covers both. A
  // single-execution rule would keep this todo open for ever though every repository is delivered
  // and settled; preflight.SyncStartupTodos already counts it done (each target has a merged
  // delivery of the run), so the surface must agree.
  const t = todo('run_t', ['a', 'b']);
  const first = merged('exec_1', 'run_t', { repos: [okRepo('a')] });
  const second = merged('exec_2', 'run_t', { repos: [okRepo('b')] });

  assert.equal(runCompleted(t, [first]), false, 'only A covered — still open');
  assert.equal(runCompleted(t, [first, second]), true, 'A and B covered across two executions — done');

  // But a covering execution whose delivery is still open does NOT count yet (B-8): the todo stays
  // open until that delivery settles.
  assert.equal(
    runCompleted(t, [first, second], new Set(['exec_2'])),
    false,
    "B's execution still holds an open delivery — not done",
  );
  // A failed second execution never completes what the first left open.
  const secondFailed = merged('exec_2', 'run_t', { repos: [failedRepo('b')] });
  assert.equal(runCompleted(t, [first, secondFailed]), false, 'B failed — still open');
});

// ── BEFUND 1/2/4: every open todo carries exactly one derived state, each in exactly one tab ─────

test('every open-todo state is derived and lands in exactly one tab (BEFUND 1, 2, 4)', () => {
  const notRun = todo('run_notrun', ['svc']);
  const running = todo('run_running', ['svc']);
  const inflight = todo('run_inflight', ['svc']);
  const awaiting = todo('run_awaiting', ['svc']);
  const awaitingNoDeadline = todo('run_await_nd', ['svc']);
  const prod = todo('run_prod', ['svc']);
  const blocked = todo('run_blocked', ['svc']);
  const failedDelivery = todo('run_faildelivery', ['svc']);
  const failedEarly = todo('run_failearly', ['svc']);

  const liveExec = merged('exec_live', 'run_running');
  const stillRunning = result('exec_run', 'run_inflight', { endedAt: undefined });
  const awaitingExec = result('exec_awaiting', 'run_awaiting');
  const awaitingNdExec = result('exec_await_nd', 'run_await_nd');
  const prodExec = result('exec_prod', 'run_prod');
  const blockedExec = result('exec_blocked', 'run_blocked');
  // BEFUND 2: a FAILED delivery (stage 'failed') holds its execution open — it must read BLOCKED.
  const failDelExec = result('exec_faildelivery', 'run_faildelivery');
  // BEFUND 1: an execution that ended without ever shipping — no delivery at all, no mergedAt.
  const failEarlyExec = result('exec_failearly', 'run_failearly', { repos: [failedRepo('svc')] });
  const all = [liveExec, stillRunning, awaitingExec, awaitingNdExec, prodExec, blockedExec, failDelExec, failEarlyExec];

  const ledger: Delivery[] = [
    { ...dlv('dlv_await', 'exec_awaiting', 'open'), mergeBy: '2026-08-08T00:00:00Z' },
    // An open pull request with NO stamped deadline yet — still a live automatic step, still a wait.
    dlv('dlv_await_nd', 'exec_await_nd', 'open'),
    { ...dlv('dlv_prod', 'exec_prod', 'merged'), prodStage: 'pending' },
    { ...dlv('dlv_block', 'exec_blocked', 'open'), blocked: true, blockedReason: 'rate limit' },
    { ...dlv('dlv_fail', 'exec_faildelivery', 'failed'), failedReason: 'push rejected' },
  ];
  const openIds = openDeliveryExecutionIds(ledger);
  const facts = openDeliveryFactsByExecution(ledger);
  const live = new Set(['run_running']);
  const state = (r: Run) => openTodoState(r, all, openIds, facts, live);

  assert.deepEqual(state(notRun), { kind: 'not-run' }, 'no execution at all');
  assert.deepEqual(state(running), { kind: 'running' }, 'a live run reads from the active set');
  assert.deepEqual(state(inflight), { kind: 'running' }, 'an execution without an end stamp is running');
  assert.deepEqual(state(awaiting), { kind: 'awaiting-merge', mergeBy: '2026-08-08T00:00:00Z' }, 'the deadline is named');
  assert.deepEqual(state(awaitingNoDeadline), { kind: 'awaiting-merge', mergeBy: undefined }, 'an open PR is a wait even before a deadline is stamped');
  assert.deepEqual(state(prod), { kind: 'awaiting-prod', retrying: false, reason: undefined }, 'merged, owing production');
  assert.deepEqual(state(blocked), { kind: 'blocked', reason: 'rate limit' }, 'the blockade outranks the timed wait');
  assert.deepEqual(state(failedDelivery), { kind: 'blocked', reason: 'push rejected' }, 'BEFUND 2: a failed delivery reads blocked, never pending');
  assert.deepEqual(state(failedEarly), { kind: 'failed' }, 'BEFUND 1: ended, never shipped — failed, not history');

  // Every derived state lands in exactly one of the four live tabs, and the intended one.
  const tabByRun: Record<string, string> = {
    run_notrun: 'todo',
    run_running: 'active',
    run_inflight: 'active',
    run_awaiting: 'pending',
    run_await_nd: 'pending',
    run_prod: 'pending',
    run_blocked: 'blocked',
    run_faildelivery: 'blocked',
    run_failearly: 'blocked',
  };
  for (const r of [notRun, running, inflight, awaiting, awaitingNoDeadline, prod, blocked, failedDelivery, failedEarly]) {
    assert.equal(taskBucket(state(r)), tabByRun[r.id], `${r.id} must land in the ${tabByRun[r.id]} tab`);
  }

  // And none of these open todos is history: a strict history admits only fully-run-through chains.
  for (const res of all) {
    if (!res.endedAt) continue;
    if (res.id === 'exec_live') continue; // the only merged one; but its run is live, so still open
    assert.equal(executionCompleted(res, openIds), false, `${res.id} must not be in the history`);
  }
});

test('open deliveries come from the LEDGER, never from a stage name (B-35)', () => {
  const delivered = merged('exec_open', 'run_a');
  const settled = merged('exec_merged', 'run_a');
  const undelivered = result('exec_failed', 'run_a', { repos: [failedRepo('svc')] });
  const legacy = result('exec_legacy', 'run_a', { legacy: true, mergedAt: undefined });

  // The ledger is the answer: one row still open, one settled, and nothing at all for the two
  // executions that never delivered.
  const open = openDeliveryExecutionIds([dlv('dlv_1', 'exec_open', 'open'), dlv('dlv_2', 'exec_merged', 'merged')]);
  assert.deepEqual([...open], ['exec_open'], 'only the execution with an unsettled delivery is open');

  // The open one stays in the list; the settled one historises (its chain ran through).
  assert.equal(executionCompleted(delivered, open), false);
  assert.equal(executionCompleted(settled, open), true);
  // BEFUND 1: nothing delivered ⇒ NOT history any more — it is a current (failed) state.
  assert.equal(executionCompleted(undelivered, open), false, 'nothing delivered ⇒ not history, a current state');
  assert.equal(executionCompleted(legacy, open), true, 'a frozen archive record is history by construction');

  // These selectors read stage names nowhere — the module carries no chain-stage literal at all.
  const src = readFileSync(new URL('./select.ts', import.meta.url), 'utf8');
  for (const s of ['preflight', 'implement', 'deliver-dev', 'publish', 'pull-request']) {
    assert.ok(!src.includes(`'${s}'`), `select.ts must not carry the stage literal ${s}`);
  }
  assert.ok(!/\.stage\b/.test(src), 'select.ts must not read a chain stage at all');
});

test('what the history hides is counted BY ITS REASON in THREE buckets, every record in exactly one (BEFUND 4)', () => {
  // The three states a record can be in outside the history: still running, ended with a live
  // delivery (a pull request or a production wait), or ended having never run through (failed).
  const settled = merged('exec_settled', 'run_a');
  const archived = result('exec_archived', 'run_a', { legacy: true, mergedAt: undefined, endedAt: '2026-07-26T10:30:00Z' });
  const live = result('exec_live', 'run_a', { endedAt: undefined });
  const awaiting = merged('exec_await', 'run_a', { mergedAt: undefined }); // held open by its PR
  const failedEarly = result('exec_failed', 'run_a', { repos: [failedRepo('svc')] });
  const all = [settled, archived, live, awaiting, failedEarly];

  const open = openDeliveryExecutionIds([dlv('dlv_1', 'exec_await', 'open')]);
  const history = all.filter((res) => executionCompleted(res, open));
  const outside = outsideHistory(all, open);

  assert.deepEqual(history.map((r) => r.id), ['exec_settled', 'exec_archived']);
  assert.deepEqual(outside.inFlight.map((r) => r.id), ['exec_live'], 'only an execution without an end stamp is running');
  assert.deepEqual(outside.awaitingDelivery.map((r) => r.id), ['exec_await'], 'the ledger, not a stage, holds this one back');
  assert.deepEqual(outside.failed.map((r) => r.id), ['exec_failed'], 'ended, never shipped, nothing automatic ⇒ failed');

  // The invariant the header depends on: the list and its counters PARTITION the pool, so no number
  // can stand beside a list that does not contain what it counts.
  const placed = [...history, ...outside.inFlight, ...outside.awaitingDelivery, ...outside.failed].map((r) => r.id);
  assert.equal(placed.length, all.length, 'a record is counted twice or not at all');
  assert.deepEqual([...new Set(placed)].sort(), all.map((r) => r.id).sort());

  // An empty pool claims nothing at all.
  const none = outsideHistory([], open);
  assert.deepEqual([none.inFlight.length, none.awaitingDelivery.length, none.failed.length], [0, 0, 0]);
});

test('open ∩ history = ∅ (REQ-011.2)', () => {
  const done = todo('run_done', ['svc']);
  const open = todo('run_open', ['other']);
  const completed = merged('exec_done', 'run_done');
  const running = result('exec_live', 'run_open', { endedAt: undefined, repos: [] });

  const split = splitOpenHistory([done, open], [completed, running]);
  assert.deepEqual(split.open.map((r) => r.id), ['run_open']);
  assert.deepEqual(split.history.map((r) => r.id), ['exec_done']);

  // Disjointness proper: nothing that completed a run sits in history while the run stays open.
  for (const r of split.open) {
    for (const res of split.history) {
      assert.ok(!(res.runId === r.id && runCompleted(r, [res])), `${r.id} is open AND completed by ${res.id}`);
    }
  }
});

// ── The lifecycle tabs (WHAT-4): each open task in exactly one of the five states, one tab each ──

test('a merged delivery still owing production reads awaiting-prod, and lands in the Pending tab (WHAT-1)', () => {
  const t = todo('run_prod', ['svc']);
  const done = result('exec_prod', 'run_prod'); // no mergedAt yet — production still owed
  // The ledger states a MERGED delivery whose production step is still pending.
  const ledger: Delivery[] = [{ ...dlv('dlv_prod', 'exec_prod', 'merged'), prodStage: 'pending' }];
  const openIds = openDeliveryExecutionIds(ledger);
  const facts = openDeliveryFactsByExecution(ledger);
  const state = openTodoState(t, [done], openIds, facts, new Set());
  assert.deepEqual(state, { kind: 'awaiting-prod', retrying: false, reason: undefined });
  assert.equal(taskBucket(state), 'pending');

  // A merged delivery whose production step is pending keeps its execution OUT of the history.
  assert.equal(openIds.has('exec_prod'), true, 'a merged-but-not-in-production delivery holds its execution open');
  assert.equal(executionCompleted(done, openIds), false);

  // A FAILED production send reads as a retrying production wait — still Pending, never Blocked: the
  // stack is untouched and the user is asked nothing.
  const failing: Delivery[] = [{ ...dlv('dlv_prod', 'exec_prod', 'merged'), prodStage: 'failed', prodFailedReason: 'receiver down' }];
  const fFacts = openDeliveryFactsByExecution(failing);
  const fState = openTodoState(t, [done], openDeliveryExecutionIds(failing), fFacts, new Set());
  assert.deepEqual(fState, { kind: 'awaiting-prod', retrying: true, reason: 'receiver down' });
  assert.equal(taskBucket(fState), 'pending');
});

test('taskBucket partitions the five lifecycle states across exactly four live tabs (WHAT-4 test c)', () => {
  // ONE case per state, and each lands in exactly one bucket — never two, never none.
  assert.equal(taskBucket({ kind: 'not-run' }), 'todo');
  assert.equal(taskBucket({ kind: 'running' }), 'active');
  assert.equal(taskBucket({ kind: 'awaiting-merge', mergeBy: '2026-08-08T00:00:00Z' }), 'pending');
  assert.equal(taskBucket({ kind: 'awaiting-merge' }), 'pending');
  assert.equal(taskBucket({ kind: 'awaiting-prod', retrying: false }), 'pending');
  assert.equal(taskBucket({ kind: 'blocked', reason: 'x' }), 'blocked');
  assert.equal(taskBucket({ kind: 'failed' }), 'blocked');

  // Every bucket a task can be in is one of the four live tabs (History is the fifth, done state).
  const buckets = new Set(['todo', 'active', 'blocked', 'pending']);
  for (const s of [
    { kind: 'not-run' as const },
    { kind: 'running' as const },
    { kind: 'awaiting-merge' as const },
    { kind: 'awaiting-prod' as const },
    { kind: 'blocked' as const },
    { kind: 'failed' as const },
  ]) {
    assert.ok(buckets.has(taskBucket(s)), `${s.kind} maps to an unknown tab`);
  }
});

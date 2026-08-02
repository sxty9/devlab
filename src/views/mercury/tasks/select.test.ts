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

const todo = (id: string, targets: string[]): Run => ({
  id,
  kind: 'todo',
  title: id,
  targets: targets.map((repo) => ({ repo })),
  tuning: {},
  authorship: AUTH,
});

test('an execution historizes only once ended AND fully delivered (REQ-037.1, B-8)', () => {
  assert.equal(executionCompleted(result('exec_1', 'run_a', { endedAt: undefined })), false);
  assert.equal(executionCompleted(result('exec_1', 'run_a')), true);
  // An open delivery (PR not merged) keeps it in the list…
  assert.equal(executionCompleted(result('exec_1', 'run_a'), new Set(['exec_1'])), false);
  // …and another execution's open delivery does not.
  assert.equal(executionCompleted(result('exec_1', 'run_a'), new Set(['exec_other'])), true);
});

test('a todo is done only when one completed execution covered ALL targets', () => {
  const t = todo('run_t', ['a', 'b']);
  const partial = result('exec_1', 'run_t', { repos: [okRepo('a')] });
  const failed = result('exec_2', 'run_t', { repos: [okRepo('a'), failedRepo('b')] });
  const full = result('exec_3', 'run_t', { repos: [okRepo('a'), okRepo('b')] });

  assert.equal(runCompleted(t, [partial]), false);
  assert.equal(runCompleted(t, [failed]), false);
  assert.equal(runCompleted(t, [full]), true);
  assert.equal(runCompleted(t, [full], new Set(['exec_3'])), false, 'undelivered execution must not complete the todo');

  const auto: Run = { id: 'run_auto', kind: 'auto', title: 'A', tuning: {}, authorship: AUTH };
  assert.equal(runCompleted(auto, [result('exec_4', 'run_auto')]), false, 'an auto run recurs — never done');
});

test('a todo is done when its targets are covered ACROSS executions, matching the startup reconciliation', () => {
  // Repository A finished in one execution, repository B in the next — the union covers both. A
  // single-execution rule would keep this todo open for ever though every repository is delivered
  // and settled; preflight.SyncStartupTodos already counts it done (each target has a merged
  // delivery of the run), so the surface must agree.
  const t = todo('run_t', ['a', 'b']);
  const first = result('exec_1', 'run_t', { repos: [okRepo('a')] });
  const second = result('exec_2', 'run_t', { repos: [okRepo('b')] });

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
  const secondFailed = result('exec_2', 'run_t', { repos: [failedRepo('b')] });
  assert.equal(runCompleted(t, [first, secondFailed]), false, 'B failed — still open');
});

test('every open todo carries exactly one derived state, and each state reads its own signal', () => {
  const notRun = todo('run_notrun', ['svc']);
  const running = todo('run_running', ['svc']);
  const inflight = todo('run_inflight', ['svc']);
  const awaiting = todo('run_awaiting', ['svc']);
  const blocked = todo('run_blocked', ['svc']);
  const failed = todo('run_failed', ['svc']);

  const liveExec = result('exec_live', 'run_running');
  const stillRunning = result('exec_run', 'run_inflight', { endedAt: undefined });
  const awaitingExec = result('exec_awaiting', 'run_awaiting');
  const blockedExec = result('exec_blocked', 'run_blocked');
  const failedExec = result('exec_failed', 'run_failed', { repos: [failedRepo('svc')] });
  const all = [liveExec, stillRunning, awaitingExec, blockedExec, failedExec];

  // The ledger states two open deliveries: one merely waiting out its window, one blocked (K-5).
  const ledger: Delivery[] = [
    { ...dlv('dlv_await', 'exec_awaiting', 'open'), mergeBy: '2026-08-08T00:00:00Z' },
    { ...dlv('dlv_block', 'exec_blocked', 'open'), blocked: true, blockedReason: 'rate limit' },
  ];
  const openIds = openDeliveryExecutionIds(ledger);
  const facts = openDeliveryFactsByExecution(ledger);
  const live = new Set(['run_running']);
  const state = (r: Run) => openTodoState(r, all, openIds, facts, live);

  assert.deepEqual(state(notRun), { kind: 'not-run' }, 'no execution at all');
  assert.deepEqual(state(running), { kind: 'running' }, 'a live run reads from the active set');
  assert.deepEqual(state(inflight), { kind: 'running' }, 'an execution without an end stamp is running');
  assert.deepEqual(state(awaiting), { kind: 'awaiting-merge', mergeBy: '2026-08-08T00:00:00Z' }, 'the deadline is named');
  assert.deepEqual(state(blocked), { kind: 'blocked', reason: 'rate limit' }, 'the blockade outranks the timed wait');
  assert.deepEqual(state(failed), { kind: 'failed' }, 'ended, settled, uncovered — needs attention');

  // Completeness: every open todo lands on exactly one of the five kinds, no row left mute.
  const kinds = new Set(['not-run', 'running', 'awaiting-merge', 'blocked', 'failed']);
  for (const r of [notRun, running, inflight, awaiting, blocked, failed]) {
    assert.ok(kinds.has(state(r).kind), `${r.id} has an unknown state ${state(r).kind}`);
  }
});

test('open deliveries come from the LEDGER, never from a stage name (B-35)', () => {
  const delivered = result('exec_open', 'run_a');
  const merged = result('exec_merged', 'run_a');
  const undelivered = result('exec_failed', 'run_a', { repos: [failedRepo('svc')] });
  const legacy = result('exec_legacy', 'run_a', { legacy: true });

  // The ledger is the answer: one row still open, one settled, and nothing at all for the two
  // executions that never delivered.
  const open = openDeliveryExecutionIds([dlv('dlv_1', 'exec_open', 'open'), dlv('dlv_2', 'exec_merged', 'merged')]);
  assert.deepEqual([...open], ['exec_open'], 'only the execution with an unsettled delivery is open');

  // The open one stays in the list; the settled one historizes (REQ-037.1: history after merge).
  assert.equal(executionCompleted(delivered, open), false);
  assert.equal(executionCompleted(merged, open), true);
  assert.equal(executionCompleted(undelivered, open), true, 'nothing delivered ⇒ history-ready on end');
  assert.equal(executionCompleted(legacy, open), true, 'archive entries the ledger never names are never held open');

  // These selectors read stage names nowhere — the module carries no chain-stage literal at all.
  const src = readFileSync(new URL('./select.ts', import.meta.url), 'utf8');
  for (const s of ['preflight', 'implement', 'deliver-dev', 'publish', 'pull-request']) {
    assert.ok(!src.includes(`'${s}'`), `select.ts must not carry the stage literal ${s}`);
  }
  assert.ok(!/\.stage\b/.test(src), 'select.ts must not read a chain stage at all');
});

test('what the history hides is counted BY ITS REASON, and every record lands in exactly one place', () => {
  // The three states a record can be in, and nothing else: settled, still running, ended with an
  // open delivery. The archive record is the case that broke the surface — mapped without an end
  // stamp it was hidden from the list AND counted as open while Active stood empty — so it appears
  // here as the mapping now delivers it: ended, hence history.
  const settled = result('exec_settled', 'run_a');
  const archived = result('exec_archived', 'run_a', { legacy: true, endedAt: '2026-07-26T10:30:00Z' });
  const live = result('exec_live', 'run_a', { endedAt: undefined });
  const undelivered = result('exec_open_pr', 'run_a');
  const all = [settled, archived, live, undelivered];

  const open = openDeliveryExecutionIds([dlv('dlv_1', 'exec_open_pr', 'open')]);
  const history = all.filter((res) => executionCompleted(res, open));
  const outside = outsideHistory(all, open);

  assert.deepEqual(history.map((r) => r.id), ['exec_settled', 'exec_archived']);
  assert.deepEqual(outside.inFlight.map((r) => r.id), ['exec_live'], 'only an execution without an end stamp is running');
  assert.deepEqual(outside.awaitingDelivery.map((r) => r.id), ['exec_open_pr'], 'the ledger, not a stage, holds this one back');

  // The invariant the header depends on: the list and its counters PARTITION the pool, so no number
  // can stand beside a list that does not contain what it counts.
  const placed = [...history, ...outside.inFlight, ...outside.awaitingDelivery].map((r) => r.id);
  assert.equal(placed.length, all.length, 'a record is counted twice or not at all');
  assert.deepEqual([...new Set(placed)].sort(), all.map((r) => r.id).sort());

  // An empty pool claims nothing at all.
  const none = outsideHistory([], open);
  assert.deepEqual([none.inFlight.length, none.awaitingDelivery.length], [0, 0]);
});

test('open ∩ history = ∅ (REQ-011.2)', () => {
  const done = todo('run_done', ['svc']);
  const open = todo('run_open', ['other']);
  const completed = result('exec_done', 'run_done');
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
  const merged = result('exec_prod', 'run_prod');
  // The ledger states a MERGED delivery whose production step is still pending.
  const ledger: Delivery[] = [{ ...dlv('dlv_prod', 'exec_prod', 'merged'), prodStage: 'pending' }];
  const openIds = openDeliveryExecutionIds(ledger);
  const facts = openDeliveryFactsByExecution(ledger);
  const state = openTodoState(t, [merged], openIds, facts, new Set());
  assert.deepEqual(state, { kind: 'awaiting-prod', retrying: false, reason: undefined });
  assert.equal(taskBucket(state), 'pending');

  // A merged delivery whose production step is pending keeps its execution OUT of the history.
  assert.equal(openIds.has('exec_prod'), true, 'a merged-but-not-in-production delivery holds its execution open');
  assert.equal(executionCompleted(merged, openIds), false);

  // A FAILED production send reads as a retrying production wait — still Pending, never Blocked: the
  // stack is untouched and the user is asked nothing.
  const failing: Delivery[] = [{ ...dlv('dlv_prod', 'exec_prod', 'merged'), prodStage: 'failed', prodFailedReason: 'receiver down' }];
  const fFacts = openDeliveryFactsByExecution(failing);
  const fState = openTodoState(t, [merged], openDeliveryExecutionIds(failing), fFacts, new Set());
  assert.deepEqual(fState, { kind: 'awaiting-prod', retrying: true, reason: 'receiver down' });
  assert.equal(taskBucket(fState), 'pending');
});

test('taskBucket partitions the five lifecycle states across exactly four live tabs (WHAT-4 test c)', () => {
  // ONE case per state, and each lands in exactly one bucket — never two, never none.
  assert.equal(taskBucket({ kind: 'not-run' }), 'todo');
  assert.equal(taskBucket({ kind: 'running' }), 'active');
  assert.equal(taskBucket({ kind: 'awaiting-merge', mergeBy: '2026-08-08T00:00:00Z' }), 'pending');
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

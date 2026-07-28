import { test } from 'node:test';
import assert from 'node:assert/strict';
import { executionCompleted, openDeliveryExecutionIds, runCompleted, splitOpenHistory } from './select.ts';
import type { Run, RunResult } from '../../../types';

const AUTH = { created: { user: 'alice' }, createdAt: '2026-07-26T09:00:00Z', updated: { user: 'alice' }, updatedAt: '2026-07-26T09:00:00Z' };

const okRepo = (repo: string) => ({
  repo,
  stages: [{ stage: 'implement', state: 'executed' as const }],
  done: true,
  succeeded: true,
});
const failedRepo = (repo: string) => ({
  repo,
  stages: [{ stage: 'implement', state: 'failed' as const, reason: 'boom' }],
  done: true,
  succeeded: false,
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

test('open deliveries are read off server stamps, never derived stages (B-35)', () => {
  const prStage = { stage: 'pull-request', state: 'executed' as const };
  const delivered = result('exec_open', 'run_a', { repos: [{ repo: 'svc', stages: [prStage], done: true, succeeded: true }] });
  const merged = result('exec_merged', 'run_a', {
    repos: [{ repo: 'svc', stages: [prStage], done: true, succeeded: true }],
    mergedAt: '2026-07-26T12:00:00Z',
  });
  const undelivered = result('exec_failed', 'run_a', { repos: [failedRepo('svc')] });
  const legacy = result('exec_legacy', 'run_a', {
    legacy: true,
    repos: [{ repo: 'svc', stages: [{ stage: 'pr', state: 'executed' as const }], done: true, succeeded: true }],
  });

  const open = openDeliveryExecutionIds([delivered, merged, undelivered, legacy]);
  assert.deepEqual([...open], ['exec_open'], 'only the delivered-but-unmerged execution is open');

  // The open one stays in the list; the merged one historizes (REQ-037.1: history after merge).
  assert.equal(executionCompleted(delivered, open), false);
  assert.equal(executionCompleted(merged, open), true);
  assert.equal(executionCompleted(undelivered, open), true, 'nothing delivered ⇒ history-ready on end');
  assert.equal(executionCompleted(legacy, open), true, 'legacy archive entries are never held open');
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

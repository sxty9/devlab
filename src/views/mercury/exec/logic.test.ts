// Tests of the execution surface's presentation logic (S13) plus the source-level guarantees the
// surface must keep: the server's stage array is the only truth, reports are sanitized, the
// calendar reads through the same access points as the history, and nothing polls.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync, readdirSync } from 'node:fs';
import { join } from 'node:path';
import {
  STEP_BADGE,
  TASK_STATE,
  TERMINAL_STATES,
  blockSummary,
  classifyStartOutcome,
  executionAt,
  executionOutcome,
  fmtDuration,
  fmtSince,
  fmtUntil,
  needsDelivery,
  pauseSummary,
  phaseBadge,
  placementOptions,
  provenanceChips,
  repoOutcome,
  repoProgress,
  retriable,
  runningStage,
  sortExecutions,
  stageBadge,
  stageHasDetail,
  stageLabel,
  usageParts,
  withoutStageDetail,
} from './logic.ts';
import { executionCompleted } from '../tasks/select.ts';
import { openDeliveryExecutionIds } from '../deliveries/deliveries.ts';
import type { Delivery, ExecutionView, RepoPipeline, RunResult, SlotOverview, StageView, StepState } from '../../../types';

const AUTH = { created: { user: 'alice' }, createdAt: '2026-07-26T09:00:00Z', updated: { user: 'alice' }, updatedAt: '2026-07-26T09:00:00Z' };
const NO_USAGE = { inputTokens: 0, outputTokens: 0, costUsd: 0 };

const stage = (over: Partial<StageView> & Pick<StageView, 'stage' | 'state'>): StageView => ({ ...over });

const pipeline = (over: Partial<RepoPipeline> & Pick<RepoPipeline, 'repo'>): RepoPipeline => ({
  stages: [],
  done: false,
  succeeded: false,
  ...over,
});

const result = (over: Partial<RunResult> & Pick<RunResult, 'id' | 'runId'>): RunResult => ({
  kind: 'auto',
  startedAt: '2026-07-26T10:00:00Z',
  repos: [],
  usage: NO_USAGE,
  requested: AUTH,
  ...over,
});

const view = (over: Partial<ExecutionView> & Pick<ExecutionView, 'id' | 'runId' | 'phase'>): ExecutionView => ({
  runTitle: 'A run',
  kind: 'auto',
  repos: [],
  usage: NO_USAGE,
  requested: AUTH,
  createdAt: '2026-07-26T10:00:00Z',
  startedAt: '2026-07-26T10:00:00Z',
  updatedAt: '2026-07-26T10:00:00Z',
  ...over,
});

// ── Step states (REQ-030.5) ───────────────────────────────────────────────────

test('the four terminal states are distinguishable WITHOUT reading a label (REQ-030.5)', () => {
  const marks = TERMINAL_STATES.map((s) => STEP_BADGE[s].dot);
  assert.equal(new Set(marks).size, 4, 'each terminal state needs its own visual mark');
  // executed/failed are filled colour marks; the two skips are a hollow ring and a dim fill — so
  // "not applicable" can never read as a success.
  assert.match(STEP_BADGE.executed.dot, /bg-success/);
  assert.match(STEP_BADGE.failed.dot, /bg-danger/);
  assert.match(STEP_BADGE['not-applicable'].dot, /ring/);
  assert.doesNotMatch(STEP_BADGE['not-applicable'].dot, /bg-success/);
  // not-executed is a dim neutral fill — nothing happened, and it says so without colour.
  assert.match(STEP_BADGE['not-executed'].dot, /bg-fill/);
  // Every wire state — the two transient ones included — has a defined, non-empty badge.
  const all: StepState[] = ['pending', 'running', 'executed', 'failed', 'not-applicable', 'not-executed'];
  for (const s of all) {
    assert.ok(STEP_BADGE[s].label.length > 0, `label for ${s}`);
    assert.ok(STEP_BADGE[s].dot.length > 0, `mark for ${s}`);
  }
  assert.equal(STEP_BADGE.running.pulse, true, 'the live state is the only pulsing one');
});

test('every mark actually renders: no /opacity on a token that cannot carry it', () => {
  // Only channel-based theme colours (rgb(var(--x) / <alpha-value>)) survive Tailwind's opacity
  // modifier; on a var()-valued token (text-*, surface, separator, bg-base) the whole declaration
  // is dropped — the mark would silently vanish and two step states would look alike.
  const NO_ALPHA = /(?:bg|text|border|ring|stroke|fill|divide|outline|shadow)-(?:text-(?:primary|secondary|tertiary)|surface(?:-raised|-sidebar)?|separator|bg-base|accent-(?:hover|fg)|material-[a-z]+)\/\[?[0-9.]/;
  const classes = [
    ...Object.values(STEP_BADGE).flatMap((b) => [b.dot, b.pill]),
    stageBadge({ state: 'unknown-state' as StepState }).dot,
    stageBadge({ state: 'unknown-state' as StepState }).pill,
  ];
  for (const c of classes) assert.doesNotMatch(c, NO_ALPHA, `unrenderable utility in "${c}"`);
  // And the four terminal marks stay pairwise distinct AFTER that constraint.
  assert.equal(new Set(TERMINAL_STATES.map((s) => STEP_BADGE[s].dot)).size, 4);
});

test('an unknown stage state still renders a DEFINED badge — no black screen (REQ-037.5)', () => {
  const b = stageBadge({ state: 'from-the-future' as StepState });
  assert.ok(b.label.length > 0);
  assert.ok(b.dot.length > 0);
  assert.ok(b.pill.length > 0);
});

test('a stage is labelled by its OWN server name — archive stages keep theirs (B-35)', () => {
  assert.equal(stageLabel('deliver-dev'), 'deliver dev');
  assert.equal(stageLabel('analyze'), 'analyze'); // a historical archive stage name
  assert.equal(stageLabel('dev_deploy'), 'dev deploy');
  assert.equal(stageLabel(''), 'stage'); // never an empty chip
});

test('a stage is inspectable exactly when it carries something to read', () => {
  assert.equal(stageHasDetail(stage({ stage: 'x', state: 'executed' })), false);
  assert.equal(stageHasDetail(stage({ stage: 'x', state: 'executed', log: 'report' })), true);
  assert.equal(stageHasDetail(stage({ stage: 'x', state: 'not-applicable', reason: 'nothing to do' })), true);
  assert.equal(stageHasDetail(stage({ stage: 'x', state: 'executed', evidence: 'sha' })), true);
  assert.equal(stageHasDetail(stage({ stage: 'x', state: 'executed', link: 'https://example.invalid/pr/1' })), true);
});

// ── Task state & the one-grip delivery (REQ-020.4) ───────────────────────────

test('"implemented, not delivered" is named in its own right and earns the one grip (REQ-020.4)', () => {
  assert.match(TASK_STATE['implemented-undelivered'].label, /not delivered/);
  assert.equal(needsDelivery('implemented-undelivered'), true);
  for (const t of ['not-implemented', 'delivered', 'unknown'] as const) {
    assert.ok(TASK_STATE[t].label.length > 0, `label for ${t}`);
    assert.equal(needsDelivery(t), false);
  }
  assert.equal(needsDelivery(undefined), false);
});

// ── Repo rows ─────────────────────────────────────────────────────────────────

test('a repo row reads the SERVER flags: blocked, running, succeeded, failed', () => {
  assert.equal(repoOutcome(pipeline({ repo: 'a', block: { reason: 'boom', class: 'transient', attempts: 3, firstAt: '', lastAt: '', nextAt: '' } })).label, 'blocked');
  assert.equal(repoOutcome(pipeline({ repo: 'a' })).label, 'running');
  assert.equal(repoOutcome(pipeline({ repo: 'a', done: true, succeeded: true })).label, 'succeeded');
  assert.equal(repoOutcome(pipeline({ repo: 'a', done: true, succeeded: false })).label, 'failed');
});

test('the running stage of a repo is the server-stated one', () => {
  const rp = pipeline({
    repo: 'a',
    stages: [stage({ stage: 's1', state: 'executed' }), stage({ stage: 's2', state: 'running' }), stage({ stage: 's3', state: 'pending' })],
  });
  assert.equal(runningStage(rp)?.stage, 's2');
  assert.equal(runningStage(pipeline({ repo: 'a' })), null);
});

test('a block states reason and attempts, so it is resumable with knowledge (REQ-032.4)', () => {
  const s = blockSummary({ reason: 'prod deploy keeps failing', class: 'transient', attempts: 4, firstAt: '', lastAt: '', nextAt: '2026-07-26T12:00:00Z' });
  assert.match(s, /prod deploy keeps failing/);
  assert.match(s, /4 attempts/);
  // A block without a reason still says something.
  assert.match(blockSummary({ reason: '', class: 'permanent', attempts: 1, firstAt: '', lastAt: '', nextAt: '' }), /permanent/);
  assert.match(blockSummary({ reason: '', class: '', attempts: 1, firstAt: '', lastAt: '', nextAt: '' }), /1 attempt\b/);
});

// ── Pauses (REQ-016.2) ────────────────────────────────────────────────────────

test('a paused execution shows its REASON and its resume attempts, never a bare "paused" (REQ-016.2)', () => {
  const limit = pauseSummary({ reason: 'usage-limit', resumeAttempts: 3 });
  assert.match(limit, /usage limit/i);
  assert.match(limit, /pause together/);
  assert.match(limit, /3 resume attempts/);
  const deferred = pauseSummary({ reason: 'deferred-by-user', resumeAttempts: 0 });
  assert.match(deferred, /free the slot/);
  assert.doesNotMatch(deferred, /attempt/);
  // The server's own message wins over the generic wording.
  assert.match(pauseSummary({ reason: 'usage-limit', message: 'resets at 07:00', resumeAttempts: 1 }), /resets at 07:00 · 1 resume attempt/);
});

// ── Phases (REQ-039.1) ────────────────────────────────────────────────────────

test('every execution phase has a defined badge; interrupted differs from running (REQ-039.1)', () => {
  const phases: ExecutionView['phase'][] = ['created', 'queued', 'running', 'paused', 'blocked', 'interrupted', 'completed', 'failed', 'discarded'];
  for (const p of phases) assert.ok(phaseBadge(p).label.length > 0, `badge for ${p}`);
  assert.notEqual(phaseBadge('interrupted').label, phaseBadge('running').label);
  assert.equal(phaseBadge('running').pulse, true);
  assert.ok(!phaseBadge('interrupted').pulse, 'an interrupted execution must not look alive');
  assert.ok(phaseBadge('nonsense' as ExecutionView['phase']).label.length > 0);
});

// ── Execution outcomes over the whole fixture zoo ────────────────────────────

/** Every shape the history can hold — including the ones that used to black out the old view. */
const HISTORY_FIXTURE: RunResult[] = [
  result({ id: 'e-ok', runId: 'r1', endedAt: '2026-07-26T11:00:00Z', repos: [pipeline({ repo: 'a', stages: [stage({ stage: 'implement', state: 'executed' })], done: true, succeeded: true })] }),
  result({ id: 'e-fail', runId: 'r1', endedAt: '2026-07-26T12:00:00Z', repos: [pipeline({ repo: 'a', stages: [stage({ stage: 'implement', state: 'failed', reason: 'boom' })], done: true })] }),
  result({
    id: 'e-block',
    runId: 'r2',
    endedAt: '2026-07-26T13:00:00Z',
    repos: [pipeline({ repo: 'a', done: true, block: { reason: 'deploy fails', class: 'transient', attempts: 5, firstAt: '', lastAt: '', nextAt: '' } })],
  }),
  result({ id: 'e-empty', runId: 'r2', endedAt: '2026-07-26T14:00:00Z', repos: [] }),
  result({ id: 'e-dead', runId: 'r3', repos: [pipeline({ repo: 'a', stages: [stage({ stage: 'implement', state: 'running' })] })] }), // killed: no end stamp
  result({
    id: 'e-legacy',
    runId: 'r4',
    legacy: true,
    endedAt: '2026-05-01T10:00:00Z',
    repos: [pipeline({ repo: 'a', stages: [stage({ stage: 'analyze', state: 'executed' }), stage({ stage: 'pr', state: 'not-executed', reason: 'skipped by setting' })], done: true })],
  }),
  result({ id: 'e-synth', runId: 'r5', synthetic: true, endedAt: '2026-07-01T10:00:00Z', repos: [pipeline({ repo: 'a', stages: [stage({ stage: 'x', state: 'executed' })], done: true, succeeded: true })] }),
  result({ id: 'e-weird', runId: 'r6', endedAt: '2026-07-02T10:00:00Z', repos: [pipeline({ repo: 'a', stages: [stage({ stage: '', state: 'from-elsewhere' as StepState })], done: true })] }),
];

test('EVERY history entry renders a defined state — dead, failed, archive, empty alike (REQ-037.5)', () => {
  for (const res of HISTORY_FIXTURE) {
    const outcome = executionOutcome(res);
    assert.ok(outcome.label.length > 0, `outcome label for ${res.id}`);
    assert.ok(['success', 'accent', 'warning', 'danger', 'neutral'].includes(outcome.tone), `tone for ${res.id}`);
    for (const rp of res.repos) {
      assert.ok(repoOutcome(rp).label.length > 0, `repo outcome for ${res.id}`);
      for (const sv of rp.stages) {
        const b = stageBadge(sv);
        assert.ok(b.label.length > 0 && b.dot.length > 0, `stage badge for ${res.id}`);
        assert.ok(stageLabel(String(sv.stage)).length > 0, `stage label for ${res.id}`);
      }
    }
  }
});

test('an outcome is read off server stamps only: running, succeeded, failed, blocked, empty', () => {
  const by = (id: string) => executionOutcome(HISTORY_FIXTURE.find((r) => r.id === id)!);
  assert.deepEqual(by('e-ok'), { label: 'succeeded', tone: 'success', pulse: false });
  assert.deepEqual(by('e-fail'), { label: 'failed', tone: 'danger', pulse: false });
  assert.deepEqual(by('e-block'), { label: 'blocked', tone: 'danger', pulse: false });
  assert.deepEqual(by('e-empty'), { label: 'no repositories', tone: 'neutral', pulse: false });
  assert.deepEqual(by('e-dead'), { label: 'running', tone: 'warning', pulse: true });
  // An execution that ended with a repo still not done is incomplete — never a silent success. The
  // record must carry step detail for that verdict: "incomplete" is a statement about steps.
  const half = result({
    id: 'h',
    runId: 'r',
    endedAt: '2026-07-26T15:00:00Z',
    repos: [pipeline({ repo: 'a', stages: [stage({ stage: 'x', state: 'executed' })] })],
  });
  assert.equal(executionOutcome(half).label, 'incomplete');
});

test('a record without ANY step detail gets its own honest state, never a red "incomplete"', () => {
  // Exactly the shape an imported entry has whose source knew one outcome for the whole run: it
  // names repositories and carries no stages at all. Calling that "incomplete" claims the work
  // stopped half-way — a statement about steps nobody ever recorded.
  const imported = result({ id: 'i', runId: 'gone', legacy: true, endedAt: '2026-05-01T10:00:00Z', repos: [pipeline({ repo: 'a' })] });
  assert.equal(withoutStageDetail(imported), true);
  const outcome = executionOutcome(imported);
  assert.notEqual(outcome.label, 'incomplete');
  assert.equal(outcome.tone, 'neutral', 'an unrecorded pipeline is not an alarm');
  assert.ok(outcome.label.length > 0);
  // As soon as the server states a finished pipeline, its own verdict wins again.
  const stated = result({
    id: 'j',
    runId: 'r',
    endedAt: '2026-05-01T10:00:00Z',
    repos: [pipeline({ repo: 'a', stages: [stage({ stage: 'archived-outcome', state: 'executed' })], done: true, succeeded: true })],
  });
  assert.equal(withoutStageDetail(stated), false);
  assert.equal(executionOutcome(stated).label, 'succeeded');
  // A record with no repositories at all keeps its own older answer.
  assert.equal(withoutStageDetail(result({ id: 'k', runId: 'r', endedAt: '2026-05-01T10:00:00Z' })), false);
});

test('archive and reconciliation are stated, not disguised', () => {
  assert.deepEqual(provenanceChips(HISTORY_FIXTURE.find((r) => r.id === 'e-legacy')!), ['archive']);
  assert.deepEqual(provenanceChips(HISTORY_FIXTURE.find((r) => r.id === 'e-synth')!), ['reconciled']);
  assert.deepEqual(provenanceChips(HISTORY_FIXTURE.find((r) => r.id === 'e-ok')!), []);
});

test('anything that ended without full success is triggerable again — no corpses (REQ-037.2)', () => {
  // Every run of the fixture still exists in this pool.
  const runs = new Set(HISTORY_FIXTURE.map((r) => r.runId));
  const by = (id: string) => retriable(HISTORY_FIXTURE.find((r) => r.id === id)!, runs);
  assert.equal(by('e-fail'), true);
  assert.equal(by('e-block'), true);
  assert.equal(by('e-empty'), true);
  assert.equal(by('e-ok'), false); // nothing to redo
  assert.equal(by('e-dead'), false); // still alive — the Active surface owns it
});

test('a record whose RUN is gone is never offered again — no control into the void (REQ-040.3)', () => {
  // Results outlive their run by contract: a deleted run, and an imported entry that never had a
  // definition, both leave a result there is nothing left to start.
  const failed = HISTORY_FIXTURE.find((r) => r.id === 'e-fail')!;
  assert.equal(retriable(failed, new Set([failed.runId])), true);
  assert.equal(retriable(failed, new Set(['some-other-run'])), false, 'a deleted run offers no restart');
  assert.equal(retriable(failed, new Set()), false, 'an unread run pool offers nothing');
  const imported = result({ id: 'i', runId: 'foreign-task', legacy: true, endedAt: '2026-05-01T10:00:00Z', repos: [pipeline({ repo: 'a' })] });
  assert.equal(retriable(imported, new Set()), false);
  assert.equal(retriable(imported, new Set(['foreign-task'])), true, 'once a run exists again, the record may run');
});

// ── Ordering (REQ-037.3) ──────────────────────────────────────────────────────

test('the history is chronological, newest first, and falls back to the start time (REQ-037.3)', () => {
  const noEnd = result({ id: 'no-end', runId: 'r', startedAt: '2026-07-26T13:30:00Z' });
  const ordered = sortExecutions([
    result({ id: 'old', runId: 'r', startedAt: '2026-07-01T10:00:00Z', endedAt: '2026-07-01T11:00:00Z' }),
    noEnd,
    result({ id: 'new', runId: 'r', startedAt: '2026-07-26T09:00:00Z', endedAt: '2026-07-26T14:00:00Z' }),
  ]);
  assert.deepEqual(ordered.map((r) => r.id), ['new', 'no-end', 'old']);
});

test('a record with no usable time sorts last and NEVER disappears', () => {
  const broken = result({ id: 'broken', runId: 'r', startedAt: 'not-a-date' });
  const also = result({ id: 'also-broken', runId: 'r', startedAt: '' });
  const ordered = sortExecutions([broken, result({ id: 'good', runId: 'r', endedAt: '2026-07-26T10:00:00Z' }), also]);
  assert.deepEqual(ordered.map((r) => r.id), ['good', 'broken', 'also-broken']);
  assert.equal(executionAt(broken), 0);
  assert.equal(ordered.length, 3);
});

test('sorting is stable for equal instants (the server order survives)', () => {
  const a = result({ id: 'a', runId: 'r', endedAt: '2026-07-26T10:00:00Z' });
  const b = result({ id: 'b', runId: 'r', endedAt: '2026-07-26T10:00:00Z' });
  assert.deepEqual(sortExecutions([a, b]).map((r) => r.id), ['a', 'b']);
  assert.deepEqual(sortExecutions([b, a]).map((r) => r.id), ['b', 'a']);
});

// ── History membership: the SAME selector the task surfaces use (B-8) ────────

test('the history admits an execution only once every delivery is settled (REQ-037.1, selector)', () => {
  const shipped = (id: string, over: Partial<RunResult> = {}) =>
    result({
      id,
      runId: 'r',
      endedAt: '2026-07-26T11:00:00Z',
      repos: [pipeline({ repo: 'a', stages: [stage({ stage: 'x', state: 'executed' })], done: true, succeeded: true })],
      ...over,
    });
  const all = [
    shipped('unmerged'),
    shipped('merged'),
    result({ id: 'running', runId: 'r' }), // no end stamp → not history
    result({ id: 'nodelivery', runId: 'r', endedAt: '2026-07-26T09:00:00Z', repos: [pipeline({ repo: 'a', stages: [stage({ stage: 'y', state: 'failed' })], done: true })] }),
  ];
  // Openness comes from the LEDGER, where the server states it — the client never reconstructs it
  // from a stage name (B-35). This is exactly the composition ExecutionsView performs; no second
  // membership rule exists.
  const ledger: Delivery[] = [
    { id: 'dlv_1', repo: 'o/a', branch: 'b1', fromCommit: 'c0', toCommit: 'c1', createdAt: '2026-07-26T11:30:00Z', stage: 'open', executionId: 'unmerged' },
    { id: 'dlv_2', repo: 'o/a', branch: 'b2', fromCommit: 'c1', toCommit: 'c2', createdAt: '2026-07-26T11:30:00Z', stage: 'merged', executionId: 'merged' },
  ];
  const open = openDeliveryExecutionIds(ledger);
  const history = sortExecutions(all.filter((res) => executionCompleted(res, open)));
  assert.deepEqual(history.map((r) => r.id), ['merged', 'nodelivery']);
  assert.ok(open.has('unmerged'));
});

// ── Start outcomes (REQ-015/018/020) ─────────────────────────────────────────

test('every start outcome is classified honestly', () => {
  assert.equal(classifyStartOutcome({ started: true }).kind, 'started');
  assert.match(classifyStartOutcome({ started: true, fresh: true }).title, /Fresh/);
  assert.equal(classifyStartOutcome({ started: true, resumed: true }).kind, 'resumed');
  assert.equal(classifyStartOutcome({ started: false, resumed: true, queued: true }).kind, 'resumed');
  // The restart gate answers with queued AND a reason — a queued start is a start, not a refusal.
  const restart = classifyStartOutcome({ started: false, queued: true, restartPending: true, notStarted: 'a restart is pending — the start is queued' });
  assert.equal(restart.kind, 'queued');
  assert.match(restart.detail!, /restart is pending/);
  // Plain queue behind a busy floor.
  assert.equal(classifyStartOutcome({ started: false, queued: true, executionId: 'x' }).kind, 'queued');
  // A refusal a slot decision can still resolve → the dialog.
  const contended = classifyStartOutcome({ started: false, notStarted: 'all 3 slots occupied', suggestion: { executionId: 'e1', reason: 'is between repositories', score: 0 } });
  assert.equal(contended.kind, 'contended');
  assert.match(contended.detail!, /3 slots/);
  // The K-3 start ban: fully delivered work does not start, and says why.
  const banned = classifyStartOutcome({ started: false, notStarted: 'already delivered — every target repository carries this work in its default branch' });
  assert.equal(banned.kind, 'refused');
  assert.match(banned.detail!, /already delivered/);
  // Never a silent no-op.
  assert.ok(classifyStartOutcome({ started: false }).detail!.length > 0);
});

test('the slot decision offers exactly what the server would grant (REQ-015)', () => {
  const overview = (over: Partial<SlotOverview>): SlotOverview => ({
    capacity: 2,
    occupied: 2,
    overloadActive: false,
    restartPending: false,
    active: [view({ id: 'e1', runId: 'r1', phase: 'running' }), view({ id: 'e2', runId: 'r2', phase: 'running' })],
    deferred: [],
    queuedStarts: [],
    ...over,
  });
  const full = placementOptions(overview({}));
  assert.deepEqual(full, { queue: true, defer: true, overload: true });
  // An overload never sums: one temporary extra place at most.
  assert.equal(placementOptions(overview({ overloadActive: true })).overload, false);
  // A free place means the block is not the cap (a repository conflict) — no overload offered.
  assert.equal(placementOptions(overview({ occupied: 1 })).overload, false);
  // Nothing running ⇒ nothing to set aside.
  assert.equal(placementOptions(overview({ active: [], occupied: 0 })).defer, false);
  // Without a projection, queueing is still offered — never a dead dialog.
  assert.deepEqual(placementOptions(null), { queue: true, defer: false, overload: false });
});

// ── Live readouts ─────────────────────────────────────────────────────────────

test('repo progress counts the server-stated done flags', () => {
  assert.equal(repoProgress(view({ id: 'e', runId: 'r', phase: 'running', repos: [pipeline({ repo: 'a', done: true }), pipeline({ repo: 'b' })] })), '1/2 repos');
  assert.equal(repoProgress(view({ id: 'e', runId: 'r', phase: 'queued' })), 'no repositories yet');
});

test('durations read coarsely and tolerate missing instants', () => {
  assert.equal(fmtDuration(8_000), '8 s');
  assert.equal(fmtDuration(180_000), '3 min');
  assert.equal(fmtDuration(4_320_000), '1 h 12 min');
  assert.equal(fmtDuration(-5), '0 s');
  const now = Date.parse('2026-07-26T12:00:00Z');
  assert.equal(fmtSince('2026-07-26T11:00:00Z', now), '1 h 0 min');
  assert.equal(fmtSince(undefined, now), '—');
  assert.equal(fmtUntil('2026-07-26T12:30:00Z', now), 'in 30 min');
  assert.equal(fmtUntil('2026-07-26T11:30:00Z', now), 'now');
  assert.equal(fmtUntil('nonsense', now), '—');
});

test('consumption is input, output and its equivalent — and NOTHING about a cap (REQ-017)', () => {
  assert.deepEqual(usageParts({ inputTokens: 10, outputTokens: 2, costUsd: 0.5 }), { input: 10, output: 2, costUsd: 0.5, any: true });
  assert.equal(usageParts(undefined).any, false);
  assert.equal(usageParts(NO_USAGE).any, false);
  assert.deepEqual(Object.keys(usageParts(NO_USAGE)).sort(), ['any', 'costUsd', 'input', 'output']);
});

// ── Source-level guarantees of this surface ──────────────────────────────────

const HERE = new URL('.', import.meta.url).pathname;
const SURFACE_DIRS = [HERE, join(HERE, '..', 'deliveries'), join(HERE, '..', 'calendar')];

function surfaceFiles(): { path: string; name: string; src: string }[] {
  const out: { path: string; name: string; src: string }[] = [];
  for (const dir of SURFACE_DIRS) {
    for (const name of readdirSync(dir)) {
      if (!/\.(ts|tsx)$/.test(name) || name.endsWith('.test.ts') || name.endsWith('.test.tsx')) continue;
      out.push({ path: join(dir, name), name, src: readFileSync(join(dir, name), 'utf8') });
    }
  }
  out.push({ path: join(HERE, '..', '..', 'GlobalCalendarView.tsx'), name: 'GlobalCalendarView.tsx', src: readFileSync(join(HERE, '..', '..', 'GlobalCalendarView.tsx'), 'utf8') });
  return out;
}

/** Code with comments and JSX text stripped — the greps below must judge CODE, not prose. */
function codeOnly(src: string): string {
  return src.replace(/\/\*[\s\S]*?\*\//g, '').replace(/^[ \t]*\/\/.*$/gm, '');
}

/** The five chain stages. They are decided on the server; a client-side literal is the first step
 *  back towards the deleted step-name heuristic (B-35). */
const CHAIN_STAGES = ['preflight', 'implement', 'deliver-dev', 'publish', 'pull-request'];

test('no chain stage is derived, named or ordered in the client (B-35)', () => {
  for (const { name, src } of surfaceFiles()) {
    const code = codeOnly(src);
    for (const s of CHAIN_STAGES) {
      assert.ok(!code.includes(`'${s}'`) && !code.includes(`"${s}"`), `${name} must not carry the stage literal ${s}`);
    }
    assert.ok(!/PIPELINE_STAGES|REPORT_STAGES|deriveJobs|isReportExecution/.test(code), `${name} must not resurrect the client-side stage derivation`);
  }
});

test('and no chain stage literal exists ANYWHERE in the client — the whole tree (B-35)', () => {
  // The per-surface guard above only reads the surfaces it lists. That is how a stage-name test in a
  // sibling directory survived once; the rule is tree-wide, so the guard is too. The single
  // exception is types.ts, which MIRRORS the frozen wire vocabulary (it declares the names, it
  // decides nothing).
  const root = join(HERE, '..', '..', '..');
  const allowed = join(root, 'types.ts');
  for (const path of walkClient(root)) {
    if (path === allowed) continue;
    const code = codeOnly(readFileSync(path, 'utf8'));
    for (const s of CHAIN_STAGES) {
      assert.ok(
        !code.includes(`'${s}'`) && !code.includes(`"${s}"`),
        `${path.slice(root.length + 1)} must not carry the chain stage literal ${s}`,
      );
    }
  }
});

/** Every implementation file of the client (tests excluded — a test names a construct to prove its
 *  absence, and a guard that read the guards would report the proof as the offence). */
function walkClient(dir: string): string[] {
  const out: string[] = [];
  for (const entry of readdirSync(dir, { withFileTypes: true })) {
    const path = join(dir, entry.name);
    if (entry.isDirectory()) {
      out.push(...walkClient(path));
    } else if (/\.(ts|tsx)$/.test(entry.name) && !/\.test\.tsx?$/.test(entry.name)) {
      out.push(path);
    }
  }
  return out;
}

test('every rendered report goes through the sanitizing markdown renderer (REQ-038)', () => {
  for (const { name, src } of surfaceFiles()) {
    const hits = src.match(/dangerouslySetInnerHTML=\{\{\s*__html:[^}]*\}\}/g) ?? [];
    for (const h of hits) {
      assert.match(h, /renderMarkdown\(/, `${name} must sanitize: ${h}`);
    }
    if (hits.length > 0) assert.match(src, /from '@\/lib\/markdown'/, `${name} must import the one renderer`);
  }
});

test('nothing on this surface polls — the one live stream drives it (REQ-034)', () => {
  for (const { name, src } of surfaceFiles()) {
    const code = codeOnly(src);
    assert.ok(!/setInterval|setTimeout/.test(code), `${name} must not schedule its own refresh`);
    // A view that HOLDS live data (reads it on mount) keeps it fresh by subscription — never by
    // re-asking. A read inside a user action (the start flow) is not a refresh and needs none.
    const readsLive = /mercuryRunActive\(|mercurySlots\(|mercuryDeliveries\(|mercuryRunExecutions\(|mercuryRunResult\(|mercuryRunCalendar\(/.test(code);
    if (readsLive && /useEffect\(/.test(code)) {
      assert.match(code, /useLiveTopic\(/, `${name} holds live data and must subscribe to the stream`);
    }
  }
});

test('the calendar reads through the SAME access points as the history (REQ-012)', () => {
  const files = surfaceFiles();
  const byName = (n: string) => files.find((f) => f.name === n)!.src;
  // No surface file talks to the API directly — everything goes through the one DataSource seam.
  for (const { name, src } of files) {
    const code = codeOnly(src);
    assert.ok(!/fetch\(|EventSource\(|'\/api\//.test(code), `${name} must go through the DataSource`);
  }
  // The calendar's window comes from the one calendar access point…
  assert.match(codeOnly(byName('GlobalCalendarView.tsx')), /source\.mercuryRunCalendar\(/);
  // …and a past occurrence opens the very component the history opens, which reads the result
  // document through the one result access point.
  assert.match(byName('MercuryCalendar.tsx'), /import \{ ExecutionDetail \} from '\.\.\/exec\/ExecutionDetail'/);
  assert.match(codeOnly(byName('MercuryCalendar.tsx')), /<ExecutionDetail\b/);
  assert.ok(!/source\.mercury/.test(codeOnly(byName('MercuryCalendar.tsx'))), 'the calendar owns no data path of its own');
  assert.match(codeOnly(byName('ExecutionDetail.tsx')), /source\.mercuryRunResult\(/);
  assert.match(codeOnly(byName('ExecutionsView.tsx')), /source\.mercuryRunExecutions\(/);
});

test('the history states no membership rule of its own — it consumes the shared selector (B-8)', () => {
  const src = surfaceFiles().find((f) => f.name === 'ExecutionsView.tsx')!.src;
  assert.match(src, /import \{ executionCompleted \} from '\.\.\/tasks\/select'/);
  // Openness is READ from the ledger through the one delivery access point, not derived here.
  assert.match(src, /import \{ openDeliveryExecutionIds \} from '\.\.\/deliveries\/deliveries'/);
  const code = codeOnly(src);
  assert.match(code, /source\.mercuryDeliveries\(/, 'the ledger the membership rule needs must be read');
  assert.ok(!/mergedAt/.test(code), 'the completion stamp is the selector\'s business, not the view\'s');
});

test('the history offers a restart only while the RUN exists — and states it otherwise (REQ-040.3)', () => {
  const src = surfaceFiles().find((f) => f.name === 'ExecutionsView.tsx')!.src;
  const code = codeOnly(src);
  // The set of existing runs is READ (results outlive their run), and every run-bound control of the
  // pane — restart, the one-grip delivery and the resume — is gated on it.
  assert.match(code, /source\.mercuryRuns\(/);
  assert.match(code, /retriable\(selected, knownRunIds\)/);
  assert.match(code, /onDeliver=\{runGone === false \?/);
  assert.match(code, /onResume=\{runGone === false \?/);
  // And the pane says WHY nothing is offered instead of leaving the user guessing.
  assert.match(src, /no longer exists/);
});

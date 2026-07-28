import { test } from 'node:test';
import assert from 'node:assert/strict';
import {
  BUDGET_CHOICES,
  MAX_ATTACHMENTS,
  MAX_ATTACHMENT_BYTES,
  budgetLabel,
  defaultDueLocalInput,
  initialExecutionMode,
  initialTargetRows,
  isoToLocalInput,
  localInputToIso,
  nowLocalInput,
  parseGoDuration,
  planAttachmentIngest,
  repoOptions,
  scheduleInvalid,
  scheduleSummary,
  storedBudgetLabel,
  targetRowValid,
  toRunTargets,
  tuningChips,
} from './logic.ts';
import type { Repo } from '../../../types';

// A fixed clock (REQ-008 tests must not depend on when they run).
const FIXED = new Date(2026, 6, 26, 14, 23, 0); // local time: 26 Jul 2026, 14:23

test('a fresh todo preselects "now"; editing reflects the stored plan', () => {
  assert.equal(initialExecutionMode(null), 'now'); // REQ-008.1 — "sofort" is the default
  assert.equal(initialExecutionMode({ dueAt: '2026-08-01T10:00:00Z' }), 'scheduled');
  assert.equal(initialExecutionMode({}), 'ondemand');
});

test('the default due moment lies in the future of the fixed clock, on a full hour', () => {
  const def = defaultDueLocalInput(FIXED);
  const min = nowLocalInput(FIXED);
  assert.ok(def > min, `default ${def} must be after now ${min}`);
  assert.ok(def.endsWith(':00'), `default ${def} must be a full hour`);
});

test('past moments are not schedulable; the boundary and the future pass', () => {
  const min = nowLocalInput(FIXED);
  const past = isoToLocalInput(new Date(FIXED.getTime() - 60_000).toISOString());
  const future = defaultDueLocalInput(FIXED);
  assert.equal(scheduleInvalid('scheduled', past, min), true); // REQ-008.2
  assert.equal(scheduleInvalid('scheduled', '', min), true);
  assert.equal(scheduleInvalid('scheduled', min, min), false);
  assert.equal(scheduleInvalid('scheduled', future, min), false);
  // Only "scheduled" needs a moment at all.
  assert.equal(scheduleInvalid('now', past, min), false);
  assert.equal(scheduleInvalid('ondemand', '', min), false);
});

test('datetime-local round-trips through ISO', () => {
  const local = isoToLocalInput(FIXED.toISOString());
  const iso = localInputToIso(local);
  assert.ok(iso);
  assert.equal(new Date(iso).getTime(), FIXED.getTime());
  assert.equal(localInputToIso(''), null);
});

test('target rows map both directions, including repos to be created', () => {
  const rows = initialTargetRows({ targets: [{ repo: 'devlab' }, { repo: 'new-svc', create: true }] });
  assert.deepEqual(rows, [
    { kind: 'existing', repo: 'devlab', newRepo: '' },
    { kind: 'new', repo: '', newRepo: 'new-svc' },
  ]);
  assert.deepEqual(toRunTargets(rows), [{ repo: 'devlab' }, { repo: 'new-svc', create: true }]);
  // A fresh editor starts from one empty existing-repo row.
  assert.deepEqual(initialTargetRows(null), [{ kind: 'existing', repo: '', newRepo: '' }]);
});

test('row validation: an existing row needs a pick, a new row a bounded name', () => {
  assert.equal(targetRowValid({ kind: 'existing', repo: 'devlab', newRepo: '' }), true);
  assert.equal(targetRowValid({ kind: 'existing', repo: '  ', newRepo: '' }), false);
  assert.equal(targetRowValid({ kind: 'new', repo: '', newRepo: 'my-svc' }), true);
  assert.equal(targetRowValid({ kind: 'new', repo: '', newRepo: '../evil' }), false);
  assert.equal(targetRowValid({ kind: 'new', repo: '', newRepo: '' }), false);
});

test('the repo picker offers every repo of the instance — none is dropped (REQ-006.3)', () => {
  const repos = [
    { id: 'z', name: 'zeta', fullName: 'o/zeta' },
    { id: 'a', name: 'alpha', fullName: 'o/alpha' },
    { id: 'm', name: 'midway', fullName: 'o/midway' },
    { id: 'a2', name: 'alpha', fullName: 'other/alpha' }, // same display name — must survive
  ] as Repo[];
  const opts = repoOptions(repos);
  assert.equal(opts.length, repos.length);
  const ids = new Set(opts.map((r) => r.id));
  for (const r of repos) assert.ok(ids.has(r.id), `repo ${r.id} missing from the picker`);
  // Sorted for scanning, but sorting must never deduplicate.
  assert.deepEqual(
    opts.map((r) => r.name),
    ['alpha', 'alpha', 'midway', 'zeta'],
  );
});

test('attachment ingest is one pipeline: caps and name dedup apply to dialog and paste alike', () => {
  const taken = ['exists.png'];
  const plan = planAttachmentIngest(taken, [
    { name: 'fresh.png', size: 100 },
    { name: 'EXISTS.png', size: 100 }, // case-insensitive duplicate
    { name: 'huge.bin', size: MAX_ATTACHMENT_BYTES + 1 },
    { name: 'fresh.png', size: 200 }, // duplicate within the same batch
  ]);
  assert.deepEqual(plan.accepted.map((f) => f.name), ['fresh.png']);
  assert.deepEqual(
    plan.rejected.map((r) => `${r.name}:${r.reason}`),
    ['EXISTS.png:duplicate-name', 'huge.bin:too-large', 'fresh.png:duplicate-name'],
  );
});

test('attachment ingest enforces the per-todo count cap', () => {
  const taken = Array.from({ length: MAX_ATTACHMENTS - 1 }, (_, i) => `f${i}.txt`);
  const plan = planAttachmentIngest(taken, [
    { name: 'fits.txt', size: 1 },
    { name: 'overflow.txt', size: 1 },
  ]);
  assert.deepEqual(plan.accepted.map((f) => f.name), ['fits.txt']);
  assert.deepEqual(plan.rejected, [{ name: 'overflow.txt', reason: 'too-many' }]);
});

test('Go durations parse and label compactly', () => {
  assert.equal(parseGoDuration('3h'), 3 * 3_600_000);
  assert.equal(parseGoDuration('3h0m0s'), 3 * 3_600_000);
  assert.equal(parseGoDuration('1h30m'), 90 * 60_000);
  assert.equal(parseGoDuration('90m'), 90 * 60_000);
  assert.equal(parseGoDuration('0s'), 0);
  assert.equal(parseGoDuration('0'), 0);
  assert.equal(parseGoDuration('250ms'), 250);
  assert.equal(parseGoDuration('-2h'), -2 * 3_600_000);
  assert.equal(parseGoDuration('soon'), null);
  assert.equal(parseGoDuration(''), null);
  assert.equal(parseGoDuration('2h --evil'), null);

  assert.equal(budgetLabel(3 * 3_600_000), '3h');
  assert.equal(budgetLabel(90 * 60_000), '1h 30m');
  assert.equal(budgetLabel(45 * 60_000), '45m');
  assert.equal(budgetLabel(0), 'no budget');
});

test('a stored budget labels honestly: reference names the governing default (REQ-010)', () => {
  assert.equal(storedBudgetLabel(undefined, '3h0m0s'), 'Default (3h)');
  assert.equal(storedBudgetLabel('', '3h0m0s'), 'Default (3h)');
  assert.equal(storedBudgetLabel(undefined, undefined), 'Default');
  assert.equal(storedBudgetLabel('2h'), '2h');
  assert.equal(storedBudgetLabel('0s'), 'no budget'); // REQ-010.3
});

test('the budget ladder carries "no budget" and only wire-true Go durations', () => {
  const noBudget = BUDGET_CHOICES.find((c) => c.value === '0s');
  assert.ok(noBudget, 'the ladder must offer "no budget" (REQ-010.3)');
  for (const c of BUDGET_CHOICES) {
    assert.notEqual(parseGoDuration(c.value), null, `${c.value} must be a Go duration`);
  }
});

test('tuning chips show exactly the explicit choices — a reference shows nothing (REQ-010.2)', () => {
  assert.deepEqual(tuningChips(undefined), []);
  assert.deepEqual(tuningChips({}), []);
  assert.deepEqual(tuningChips({ model: 'claude-fable-5', modelVersion: '20260115' }), ['claude-fable-5 · 20260115']);
  assert.deepEqual(tuningChips({ effort: 'ultracode', timeBudget: '2h' }), ['ultracode', '2h']);
  assert.deepEqual(tuningChips({ timeBudget: '0s' }), ['no budget']);
});

test('schedule summaries render the stored plan', () => {
  assert.equal(scheduleSummary({ kind: 'daily', timeOfDay: '03:00' }), 'daily 03:00');
  assert.equal(scheduleSummary({ kind: 'weekly', timeOfDay: '03:00', weekdays: [1, 4] }), 'weekly Mon, Thu · 03:00');
  assert.equal(scheduleSummary(undefined), '—');
});

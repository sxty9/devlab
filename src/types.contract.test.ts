// The TS side of the golden-fixture contract (W-K): canonical values typed AGAINST types.ts
// (`satisfies` — drift breaks `tsc`, which force-includes this file via tsconfig "files")
// and deep-compared AGAINST contract/fixtures/*.json (drift breaks `node --test`). The Go
// side (backend/internal/model/contract_test.go) pins the same files — one contract, two
// enforcing builds.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { dirname, join } from 'node:path';
import type {
  Delivery,
  ExecutionView,
  Finding,
  Health,
  PortAllocation,
  Repo,
  RepoData,
  RestartState,
  Run,
  RunCalendar,
  RunCoverage,
  RunInput,
  ServiceConfig,
  ServiceNotice,
  SlotOverview,
  SessionLine,
  StageView,
  StartOutcome,
  UsageView,
  User,
} from './types';

const fixturesDir = join(dirname(fileURLToPath(import.meta.url)), '..', 'contract', 'fixtures');
const fixture = (name: string): unknown => JSON.parse(readFileSync(join(fixturesDir, `${name}.json`), 'utf8'));

/** Assert the canonical TS value equals the shared fixture byte-for-byte (after JSON round-trip). */
function pin(name: string, value: unknown) {
  test(`contract fixture: ${name}`, () => {
    assert.deepStrictEqual(JSON.parse(JSON.stringify(value)), fixture(name));
  });
}

pin('health', { ok: true, mode: 'devlab/0.2.0' } satisfies Health);

// The canonical user WATCHES sessions but may not speak into them — the split is pinned in the
// contract itself, so a build that collapses the two rights into one fails on both sides.
pin('user', {
  username: 'alice',
  displayName: 'Alice',
  isAdmin: false,
  canUseDevlab: true,
  canWatchSession: true,
  canSpeakSession: false,
  githubLinked: true,
  githubLogin: 'alice-gh',
} satisfies User);

pin('session_line', {
  at: '2026-07-28T03:00:00Z',
  repo: 'svc-a',
  from: 'alice',
  text: 'stop and check the migration first',
} satisfies SessionLine);

pin('repo', {
  id: 'svc-a',
  name: 'svc-a',
  fullName: 'org/svc-a',
  kind: 'service',
  description: 'A service.',
  language: 'Go',
  icon: 'go',
  tint: 'accent',
  permission: 'push',
} satisfies Repo);

pin('repo_data', {
  branches: [{ name: 'main', isDefault: true, ahead: 0, behind: 0, updated: '2h ago' }],
  tree: [{ id: 'cmd', name: 'cmd', kind: 'dir' }],
  files: {},
  changes: [{ path: 'a.go', status: 'modified', additions: 1, deletions: 2, staged: false }],
  commits: [{ hash: 'abc1234', message: 'init', author: 'alice', time: '1d', dotLane: 0, lines: [] }],
  worktrees: [],
  vision: [],
  claude: [],
  terminal: [],
  stages: [{ id: 'code', label: 'Code', state: 'active', hint: 'Uncommitted changes' }],
  defaultTabs: [{ id: 'structure:svc-a', title: 'svc-a — structure', kind: 'structure' }],
  activeTabId: 'structure:svc-a',
  structure: [],
} satisfies RepoData);

pin('run', {
  id: 'run_x',
  kind: 'todo',
  title: 'Ship it',
  task: 'Do the thing.',
  targets: [{ repo: 'svc-a' }, { repo: 'new-svc', create: true }],
  dueAt: '2026-07-28T04:00:00Z',
  tuning: { model: 'opus', effort: 'max', timeBudget: '3h0m0s' },
  promptSnapshot: '# Task\n\nDo the thing.',
  attachments: [
    {
      id: 'att_1',
      filename: 'sketch.png',
      mime: 'image/png',
      size: 123,
      sha256: 'ab12',
      uploadedAt: '2026-07-28T03:00:00Z',
      uploadedBy: 'alice',
    },
  ],
  authorship: {
    created: { user: 'alice' },
    createdAt: '2026-07-28T03:00:00Z',
    updated: { user: '', autonomous: true, onBehalfOf: 'alice' },
    updatedAt: '2026-07-28T03:10:00Z',
  },
} satisfies Run);

pin('run_input', {
  kind: 'auto',
  title: 'Nightly sweep',
  axiomIds: ['ax_1'],
  schedule: { kind: 'daily', timeOfDay: '03:00' },
  active: true,
  tuning: { effort: 'max' },
} satisfies RunInput);

pin('stage_view', {
  stage: 'deliver-dev',
  state: 'not-applicable',
  reason: 'library — nothing to deploy',
  evidence: 'no service CLI, no cmd/<id>d',
  startedAt: '2026-07-28T03:00:00Z',
  endedAt: '2026-07-28T03:10:00Z',
} satisfies StageView);

const executionView = {
  id: 'exec_1',
  runId: 'run_x',
  runTitle: 'Ship it',
  kind: 'todo',
  phase: 'running',
  continuation: { repo: 'svc-a', stage: 'implement' },
  repos: [
    {
      repo: 'svc-a',
      stages: [
        { stage: 'preflight', state: 'executed', startedAt: '2026-07-28T03:00:00Z', endedAt: '2026-07-28T03:00:00Z' },
        { stage: 'implement', state: 'running', startedAt: '2026-07-28T03:00:00Z' },
        { stage: 'deliver-dev', state: 'pending' },
        { stage: 'publish', state: 'pending' },
        { stage: 'pull-request', state: 'pending' },
      ],
      taskState: 'not-implemented',
      done: false,
      succeeded: false,
    },
  ],
  usage: { inputTokens: 1000, outputTokens: 200, costUsd: 0.05 },
  requested: {
    created: { user: 'alice' },
    createdAt: '2026-07-28T03:00:00Z',
    updated: { user: '', autonomous: true, onBehalfOf: 'alice' },
    updatedAt: '2026-07-28T03:10:00Z',
  },
  createdAt: '2026-07-28T03:00:00Z',
  startedAt: '2026-07-28T03:00:00Z',
  updatedAt: '2026-07-28T03:10:00Z',
  deliveredCommit: 'abc1234',
} satisfies ExecutionView;
pin('execution_view', executionView);

pin('slot_overview', {
  capacity: 3,
  occupied: 1,
  overloadActive: false,
  restartPending: false,
  active: [executionView],
  deferred: [],
  queuedStarts: [{ runId: 'run_y', title: 'Later run', by: { user: 'alice' }, at: '2026-07-28T03:10:00Z' }],
} satisfies SlotOverview);

pin('start_outcome', {
  started: false,
  notStarted: 'already delivered',
  taskStates: { 'svc-a': 'delivered' },
  taskEvidence: {
    'svc-a': [
      'delivery dlv_1 merged at 2026-07-28T05:00:00Z; the todo text still asks for exactly this work (editorial edits aside)',
    ],
  },
  suggestion: { executionId: 'exec_2', reason: 'longest idle', score: 7 },
} satisfies StartOutcome);

pin('restart_state', {
  pending: true,
  requestedBy: { user: '', autonomous: true, onBehalfOf: 'alice' },
  requestedAt: '2026-07-28T03:10:00Z',
  deadline: '2026-07-28T04:00:00Z',
  queuedStarts: [{ runId: 'run_x', title: 'Ship it', by: { user: 'alice' }, at: '2026-07-28T03:10:00Z' }],
} satisfies RestartState);

pin('delivery', {
  id: 'dlv_1',
  repo: 'svc-a',
  branch: 'fix/login-flow',
  fromCommit: 'a1',
  toCommit: 'a2',
  prNumber: 41,
  prUrl: 'https://example.invalid/pr/41',
  createdAt: '2026-07-28T03:00:00Z',
  mergedAt: '2026-07-28T04:00:00Z',
  stage: 'merged',
} satisfies Delivery);

pin('notice', {
  id: 'not_1',
  kind: 'delivery-gap',
  repo: 'svc-a',
  text: 'delivery not yet set up',
  nextStep: 'run the service setup once',
  count: 3,
  firstAt: '2026-07-28T03:00:00Z',
  lastAt: '2026-07-28T03:10:00Z',
  read: false,
} satisfies ServiceNotice);

pin('ports', [
  { port: 8781, service: 'devlab', routed: true, bound: true, conflict: false },
  { port: 8542, service: 'prizm', routed: true, bound: true, conflict: true },
] satisfies PortAllocation[]);

pin('calendar', {
  from: '2026-07-20T02:00:00Z',
  to: '2026-08-27T03:00:00Z',
  occurrences: [
    { runId: 'run_x', runTitle: 'Ship it', kind: 'todo', at: '2026-07-28T04:00:00Z', schedule: 'once' },
    { runId: 'run_a', runTitle: 'Nightly sweep', kind: 'auto', at: '2026-07-20T02:00:00Z', resultId: 'exec_0', succeeded: true },
  ],
} satisfies RunCalendar);

pin('coverage', {
  covered: { ax_1: ['run_a'] },
  index: { ax_1: 'axiome/architecture/minimalism.md' },
  axioms: { ax_1: 'Minimalism' },
  pending: true,
} satisfies RunCoverage);

pin('service_config', {
  maxConcurrency: 3,
  defaultTimeBudget: '3h0m0s',
  automergeWindow: '720h0m0s',
} satisfies ServiceConfig);

pin('usage', { inputTokens: 123456, outputTokens: 6543, costUsd: 12.34 } satisfies UsageView);

pin('finding', {
  state: 'implemented-undelivered',
  evidence: ['mercury-dev ahead of main @abc1234', 'no open delivery'],
  observedAt: '2026-07-28T03:00:00Z',
  openPr: { number: 41, url: 'https://example.invalid/pr/41', headBranch: 'fix/login-flow' },
} satisfies Finding);

import { test } from 'node:test';
import assert from 'node:assert/strict';
import { deriveJobs, isReportExecution, stepStatus, PIPELINE_STAGES, REPORT_STAGES, type Job } from './mercuryPipeline.ts';
import type { RepoResult, RunStep } from '@/types';

const repo = (over: Partial<RepoResult>): RepoResult => ({ repo: 'r', ok: true, deployed: false, steps: [], ...over });
const step = (name: string, status: RunStep['status'], over: Partial<RunStep> = {}): RunStep => ({ name, status, at: '', ...over });
const byKey = (jobs: Job[]) => Object.fromEntries(PIPELINE_STAGES.map((s, i) => [s.key, jobs[i].status]));

test('full-mode success: every stage is executed', () => {
  const m = byKey(
    deriveJobs(repo({ deployed: true, prUrl: 'x', steps: [step('implement', 'ok'), step('dev-deploy', 'ok'), step('push', 'ok'), step('pr', 'ok')] }), PIPELINE_STAGES),
  );
  assert.equal(m['implement'], 'success');
  assert.equal(m['dev-deploy'], 'success');
  assert.equal(m['push'], 'success');
  assert.equal(m['pr'], 'success');
  assert.equal(m['merge'], 'manual');
  assert.equal(m['prod-deploy'], 'success');
});

test('a not-applicable dev-deploy lets the chain continue (never shown as success)', () => {
  const m = byKey(
    deriveJobs(repo({ prUrl: 'x', steps: [step('implement', 'ok'), step('dev-deploy', 'not-applicable'), step('push', 'ok'), step('pr', 'ok')] }), PIPELINE_STAGES),
  );
  assert.equal(m['dev-deploy'], 'not-applicable');
  assert.notEqual(m['dev-deploy'], 'success');
  assert.equal(m['push'], 'success'); // the chain continued past the skip
  assert.equal(m['pr'], 'success');
  assert.equal(m['prod-deploy'], 'pending');
});

test('a failed dev-deploy halts: the recorded downstream is not-executed', () => {
  const m = byKey(
    deriveJobs(repo({ ok: false, steps: [step('implement', 'ok'), step('dev-deploy', 'failed'), step('push', 'not-executed'), step('pr', 'not-executed')] }), PIPELINE_STAGES),
  );
  assert.equal(m['dev-deploy'], 'failed');
  assert.equal(m['push'], 'not-executed');
  assert.equal(m['pr'], 'not-executed');
  assert.equal(m['merge'], 'not-executed');
  assert.equal(m['prod-deploy'], 'not-executed');
});

test('a failed step halts even when no downstream step was recorded (derived not-executed)', () => {
  const m = byKey(deriveJobs(repo({ ok: false, steps: [step('implement', 'ok'), step('dev-deploy', 'failed')] }), PIPELINE_STAGES));
  assert.equal(m['push'], 'not-executed');
  assert.equal(m['pr'], 'not-executed');
});

test('a finished repo with nothing to push bypasses as not-applicable, not not-executed', () => {
  const m = byKey(deriveJobs(repo({ steps: [step('implement', 'ok')] }), PIPELINE_STAGES));
  assert.equal(m['implement'], 'success');
  assert.equal(m['dev-deploy'], 'not-applicable');
  assert.equal(m['push'], 'not-applicable');
  assert.equal(m['pr'], 'not-applicable');
  for (const k of Object.keys(m)) assert.notEqual(m[k], 'not-executed');
});

test('a still-running repo shows unreached stages as pending', () => {
  const m = byKey(deriveJobs(repo({ running: true, steps: [step('implement', 'ok', { running: true })] }), PIPELINE_STAGES));
  assert.equal(m['implement'], 'running');
  assert.equal(m['dev-deploy'], 'pending');
  assert.equal(m['push'], 'pending');
});

test('a clone failure before any step shows implement failed, rest not-executed', () => {
  const m = byKey(deriveJobs(repo({ ok: false, error: 'clone: boom', steps: [] }), PIPELINE_STAGES));
  assert.equal(m['implement'], 'failed');
  assert.equal(m['dev-deploy'], 'not-executed');
});

test('a report execution collapses to the single Analyze stage', () => {
  const jobs = deriveJobs(repo({ steps: [step('analyze', 'ok')] }), REPORT_STAGES);
  assert.equal(jobs.length, 1);
  assert.equal(jobs[0].status, 'success');
});

test('stepStatus falls back to the legacy ok bool', () => {
  assert.equal(stepStatus({ name: 'push', ok: true, at: '' } as RunStep), 'ok');
  assert.equal(stepStatus({ name: 'pr', ok: false, at: '' } as RunStep), 'failed');
  assert.equal(stepStatus({ name: 'dev-deploy', status: 'not-applicable', ok: true, at: '' } as RunStep), 'not-applicable');
});

test('isReportExecution distinguishes report from an all-failed sweep', () => {
  assert.equal(isReportExecution([repo({ steps: [step('analyze', 'ok')] })]), true);
  assert.equal(isReportExecution([repo({ steps: [step('implement', 'ok')] })]), false);
  assert.equal(isReportExecution([repo({ ok: false, error: 'clone', steps: [] })]), false);
});

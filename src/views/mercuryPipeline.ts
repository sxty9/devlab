import type { RepoResult, RunStep, RunStepStatus } from '@/types';

/** The pure pipeline-derivation for one execution — the honest mapping from a repo's recorded steps onto
 *  the canonical stage chain, kept apart from the React rendering so it can be reasoned about and unit
 *  tested on its own (the styling lives with the view; the evaluation lives here). */

/** One repo flowing through the pipeline reads, per stage, as one of these. The four honest TERMINAL
 *  states — success, failed, not-applicable, not-executed — are what make "did this actually happen?"
 *  legible at a glance; running/pending/manual are the live and manual-gate states orthogonal to them. */
export type JobStatus = 'success' | 'failed' | 'not-applicable' | 'not-executed' | 'manual' | 'pending' | 'running';

export interface PipelineStage {
  key: string;
  label: string;
  manual?: boolean;
}

/** The full dev/prod pipeline. A report-mode execution (read-only analysis) collapses to a single stage. */
export const PIPELINE_STAGES: PipelineStage[] = [
  { key: 'implement', label: 'Implement' },
  { key: 'dev-deploy', label: 'Dev-Deploy' },
  { key: 'push', label: 'Push' },
  { key: 'pr', label: 'PR' },
  { key: 'merge', label: 'Merge', manual: true },
  { key: 'prod-deploy', label: 'Prod-Deploy' },
];
export const REPORT_STAGES: PipelineStage[] = [{ key: 'analyze', label: 'Analyze' }];
export const RUN_STEP_KEYS = ['implement', 'dev-deploy', 'push', 'pr'];

export interface Job {
  status: JobStatus;
  log?: string;
  href?: string;
}

/** A step's honest status, tolerating a legacy record that carried only the old `ok` bool (the server now
 *  always sends `status`; a mock or cached document might still be pre-status). */
export function stepStatus(step: RunStep): RunStepStatus {
  if (step.status) return step.status;
  return step.ok ? 'ok' : 'failed';
}

/** deriveJobs maps one repo's recorded steps onto the canonical stage chain, honestly. A recorded step is
 *  success / failed / not-applicable / not-executed by its status (or `running` while in flight). Once a
 *  step FAILS (or is itself not-executed) the chain is TERMINAL: every later stage with no step of its
 *  own reads as not-executed — never a skip, and never green. A not-applicable step does NOT make the
 *  chain terminal; it continues. A stage with no step that was never reached is a deliberate bypass on a
 *  finished repo (not-applicable — e.g. no changes → no push/PR) and pending on a still-running one. Merge
 *  is a manual gate while a PR is open; Prod-Deploy is pending until the PR merges and the repo deploys. */
export function deriveJobs(repo: RepoResult, stages: PipelineStage[]): Job[] {
  const byName = new Map((repo.steps ?? []).map((s) => [s.name, s]));
  const anyRunStep = RUN_STEP_KEYS.some((k) => byName.has(k));
  const live = !!repo.running;
  const unreached: JobStatus = live ? 'pending' : 'not-applicable';
  let terminal = false; // a step FAILED (or was not-executed) upstream → the chain stopped
  return stages.map((stage) => {
    const step = byName.get(stage.key);
    if (step) {
      if (step.running) return { status: 'running', log: step.log || undefined };
      const st = stepStatus(step);
      if (st === 'failed') {
        terminal = true;
        return { status: 'failed', log: step.log || undefined };
      }
      if (st === 'not-executed') {
        terminal = true;
        return { status: 'not-executed', log: step.log || undefined };
      }
      if (st === 'not-applicable') return { status: 'not-applicable', log: step.log || undefined };
      return { status: 'success', log: step.log || undefined };
    }
    if (stage.key === 'merge') {
      if (repo.prUrl) return { status: 'manual', href: repo.prUrl };
      return { status: terminal ? 'not-executed' : unreached };
    }
    if (stage.key === 'prod-deploy') {
      if (repo.deployed) return { status: 'success' };
      if (repo.prUrl) return { status: 'pending' };
      return { status: terminal ? 'not-executed' : unreached };
    }
    if (!RUN_STEP_KEYS.includes(stage.key) || terminal) return { status: terminal ? 'not-executed' : unreached };
    // No recorded step for this run stage and nothing has failed yet: the failure point when the repo
    // errored before recording any step (clone/setup), otherwise a bypassed (or not-yet-reached) stage.
    if (repo.error && !anyRunStep) {
      terminal = true;
      return { status: 'failed', log: repo.error };
    }
    return { status: unreached };
  });
}

/** A report execution iff at least one repo actually analyzed AND none ran a real pipeline step — so a
 *  run where every repo failed before any step (e.g. a clone/DNS outage → empty steps) is NOT mistaken
 *  for report and collapsed to a single Analyze stage, which would hide the failures. The live row counts
 *  too, so a live report run reads as report from its first step. */
export function isReportExecution(rows: RepoResult[]): boolean {
  const hasAnalyze = rows.some((r) => (r.steps ?? []).some((s) => s.name === 'analyze'));
  const hasRunStep = rows.some((r) => (r.steps ?? []).some((s) => RUN_STEP_KEYS.includes(s.name)));
  return hasAnalyze && !hasRunStep;
}

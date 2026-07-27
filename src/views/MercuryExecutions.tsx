import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { getDataSource } from '@/data';
import { useToast } from '@/ui/Toast';
import { Button } from '@/ui/Button';
import { Modal } from '@/ui/Modal';
import { ErrorBoundary } from '@/ui/ErrorBoundary';
import { cn } from '@/lib/cn';
import { renderMarkdown } from '@/lib/markdown';
import { RocketIcon, RefreshIcon, ChevronRightIcon, PlayIcon } from '@/ui/icons';
import type { RunActive, RunExecution, RunInFlight, RunResult, RunResultRef, RepoResult, RunStep, RunTrigger, RunType } from '@/types';
import { Person } from '@/ui/Person';

/** Shared execution-history kit for Mercury's parallel surfaces — Automatische Läufe and Konkrete
 *  ToDos. Both run on the SAME machinery (store, executor, results), so their history is rendered by
 *  ONE set of components here rather than duplicated per view; each surface simply asks for its own
 *  kind via `type`. This is the Reuse-before-Build home that keeps the two tabs symmetric. */

/** Uniform error-to-string, mirroring the rest of the Mercury surface. */
const msg = (e: unknown) => String((e as Error)?.message ?? e);

/** A localized timestamp, or an em dash when absent/unparseable. */
export function fmtDateTime(iso?: string): string {
  if (!iso) return '—';
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return '—';
  return d.toLocaleString('de-DE');
}

export const fmtNum = (n: number) => n.toLocaleString('de-DE');
export const fmtCost = (n: number) => `$${n.toFixed(4)}`;

/** Compact input/output token + cost readout, shown wherever an execution/result appears. */
export function TokenStat({ input, output, cost }: { input?: number; output?: number; cost?: number }) {
  if (input == null && output == null && cost == null) return null;
  return (
    <span className="flex flex-wrap items-center gap-x-2 gap-y-0.5 text-caption text-text-tertiary">
      {input != null && <span title="Eingabe-Tokens">↓ {fmtNum(input)}</span>}
      {output != null && <span title="Ausgabe-Tokens">↑ {fmtNum(output)}</span>}
      {cost != null && <span className="font-medium text-text-secondary">{fmtCost(cost)}</span>}
    </span>
  );
}

/** The Claude engine that drove an execution — the model (a Holistic requirement: every AI answer is
 *  labelled with its model) and, when it differs from the default, the effort tier. */
export function ModelStat({ model, effort }: { model: string; effort?: string }) {
  return (
    <span className="flex items-center gap-1.5 text-caption text-text-tertiary" title="Model · Effort">
      <span className="font-medium text-text-secondary">{model}</span>
      {effort && <span>· {effort}</span>}
    </span>
  );
}

/** Who set an execution going, from the single Person component: an autonomous run is shown as the
 *  runner (a bot, never a person) and still names the person it acted for — so a person's ToDo run by
 *  the runner names BOTH. A manual run-now shows its caller. An execution with no recorded origin
 *  shows as unknown, never guessed. */
export function ExecutionOrigin({ trigger, requestedBy, className }: { trigger?: RunTrigger; requestedBy?: string; className?: string }) {
  const auto = !!trigger?.auto;
  return (
    <span className={cn('flex flex-wrap items-center gap-x-1.5 gap-y-0.5 text-caption text-text-tertiary', className)}>
      {auto ? (
        <>
          <Person autonomous autonomousLabel="Autonomous" size="sm" />
          {requestedBy && (
            <>
              <span aria-hidden>· for</span>
              <Person username={requestedBy} size="sm" />
            </>
          )}
        </>
      ) : (
        // Manual run-now → the caller who triggered it; a legacy result (no origin) reads as unknown.
        <Person username={trigger?.by} size="sm" />
      )}
    </span>
  );
}

/** A green/red status pill. */
export function OkPill({ ok }: { ok: boolean }) {
  return (
    <span
      className={cn('shrink-0 rounded px-1.5 py-0.5 text-caption font-medium', ok ? 'bg-success/15 text-success' : 'bg-danger/15 text-danger')}
    >
      {ok ? 'OK' : 'Fehler'}
    </span>
  );
}

/** Shown in a pane when nothing is selected. */
export function EmptyPlaceholder({ text }: { text: string }) {
  return (
    <div className="flex h-full flex-col items-center justify-center gap-3 text-center">
      <span className="flex h-12 w-12 items-center justify-center rounded-2xl bg-surface-raised shadow-elev-1 ring-1 ring-separator">
        <RocketIcon className="h-6 w-6 text-accent" />
      </span>
      <p className="max-w-xs text-footnote text-text-tertiary">{text}</p>
    </div>
  );
}

/** One step (analyze/implement/push/pr/deploy). analyze/implement carry the agent's report (`log`),
 *  shown as a collapsible Bericht. While a step is `running`, its log is the agent's LIVE transcript:
 *  the step auto-expands, renders the transcript preformatted (not markdown) and follows the tail. */
export function StepRow({ step }: { step: RunStep }) {
  const running = !!step.running;
  const [open, setOpen] = useState(false);
  const hasReport = !!step.log;
  const show = running || open; // a running step is always expanded — the point is to watch it
  const logRef = useRef<HTMLPreElement>(null);

  // Keep the live transcript scrolled to the newest line as it streams in.
  useEffect(() => {
    if (running && logRef.current) logRef.current.scrollTop = logRef.current.scrollHeight;
  }, [running, step.log]);

  return (
    <div>
      <button
        type="button"
        disabled={!hasReport && !running}
        onClick={() => hasReport && setOpen((o) => !o)}
        className={cn('flex w-full items-center gap-2 rounded-md px-1 py-1 text-left transition', hasReport || running ? 'hover:bg-fill/10' : 'cursor-default')}
      >
        {hasReport || running ? (
          <ChevronRightIcon className={cn('h-3.5 w-3.5 shrink-0 text-text-tertiary transition-transform duration-fast', show && 'rotate-90')} />
        ) : (
          <span className="w-3.5 shrink-0" />
        )}
        <span className={cn('h-2 w-2 shrink-0 rounded-full', running ? 'animate-pulse bg-warning' : step.ok ? 'bg-success' : 'bg-danger')} />
        <span className="flex-1 truncate text-footnote font-medium text-text-secondary">{step.name}</span>
        <span className="shrink-0 text-caption text-text-tertiary">{running ? 'läuft…' : hasReport ? 'Bericht' : ''}</span>
      </button>
      {show &&
        (running ? (
          <pre
            ref={logRef}
            className="dl-scroll mt-1 max-h-80 overflow-auto whitespace-pre-wrap rounded-md border border-separator bg-bg-base p-3 font-mono text-caption text-text-secondary"
          >
            {step.log ? step.log : 'Der Agent arbeitet…'}
          </pre>
        ) : (
          hasReport && (
            <div
              className="dl-markdown dl-scroll mt-1 max-h-80 overflow-auto rounded-md border border-separator bg-bg-base p-3 text-caption text-text-secondary"
              dangerouslySetInnerHTML={{ __html: renderMarkdown(step.log ?? '') }}
            />
          )
        ))}
    </div>
  );
}

/** One repository within an execution: status, deploy/PR, per-repo tokens, and its steps. A `running`
 *  repo (the one in flight, from RunResult.live) wears an amber "läuft" pill instead of a status. */
export function RepoBlock({ repo }: { repo: RepoResult }) {
  const running = !!repo.running;
  return (
    <div className={cn('rounded-card border bg-surface p-3', running ? 'border-warning/40' : 'border-separator')}>
      <div className="flex flex-wrap items-center gap-2">
        <span className="min-w-0 flex-1 truncate font-mono text-footnote font-medium text-text-primary">{repo.repo}</span>
        {running ? (
          <span className="flex shrink-0 items-center gap-1.5 rounded bg-warning/15 px-1.5 py-0.5 text-caption font-medium text-warning">
            <span className="h-1.5 w-1.5 animate-pulse rounded-full bg-warning" /> läuft
          </span>
        ) : (
          <OkPill ok={repo.ok} />
        )}
        {!running && (
          <span
            className={cn(
              'rounded px-1.5 py-0.5 text-caption font-medium',
              repo.deployed ? 'bg-success/15 text-success' : 'bg-fill/15 text-text-tertiary',
            )}
          >
            {repo.deployed ? 'deployed' : 'nicht deployed'}
          </span>
        )}
        {repo.prUrl && (
          <a href={repo.prUrl} target="_blank" rel="noreferrer" className="text-caption font-medium text-accent hover:underline">
            PR öffnen
          </a>
        )}
      </div>

      {/* Name the delivered state (the dev branch this run grew and the exact commit dev serves) so the
          execution view always says, unambiguously, what dev is running — even when the run added nothing.
          The stacked PR base is shown too, so a reviewer sees the delivery sits on the prior work. */}
      {!running && repo.devBranch && (
        <div className="mt-1.5 flex flex-wrap items-center gap-x-3 gap-y-0.5 font-mono text-caption text-text-tertiary">
          <span>
            Ausgelieferter Stand:{' '}
            <span className="text-text-secondary">
              {repo.devBranch}
              {repo.devCommit ? `@${repo.devCommit.slice(0, 8)}` : ''}
            </span>
          </span>
          {repo.prBase && (
            <span>
              PR-Basis: <span className="text-text-secondary">{repo.prBase}</span>
            </span>
          )}
        </div>
      )}

      <div className="mt-1.5">
        <TokenStat input={repo.inputTokens} output={repo.outputTokens} cost={repo.costUsd} />
      </div>

      {repo.error && <p className="mt-2 rounded-md bg-danger/10 px-2.5 py-1.5 text-caption text-danger">{repo.error}</p>}

      {/* Steps can be null when a repo failed before any step ran (clone/network) — the backend
          marshals the empty slice as null. Guard it, else `null.length` blanks the whole view. */}
      {repo.steps && repo.steps.length > 0 && (
        <div className="mt-3 flex flex-col gap-1.5 border-t border-separator pt-3">
          {repo.steps.map((step, i) => (
            <StepRow key={`${step.name}:${i}`} step={step} />
          ))}
        </div>
      )}
    </div>
  );
}

/** The run's Promptstellung for one execution: the exact prompt the agent was driven by, snapshotted at
 *  run time and rendered as Markdown. Collapsed by default — an auto run's prompt folds in every axiom
 *  and can be long, so it stays portioned until asked for, mirroring a step's Bericht. Renders nothing
 *  when the execution predates prompt capture. Execution-level (shown once), never repeated per repo. */
export function PromptDisclosure({ prompt }: { prompt?: string }) {
  const [open, setOpen] = useState(false);
  if (!prompt) return null;
  return (
    <div className="rounded-card border border-separator bg-surface">
      <button
        type="button"
        onClick={() => setOpen((o) => !o)}
        className="flex w-full items-center gap-2 rounded-card px-3 py-2 text-left transition hover:bg-fill/10"
      >
        <ChevronRightIcon className={cn('h-3.5 w-3.5 shrink-0 text-text-tertiary transition-transform duration-fast', open && 'rotate-90')} />
        <span className="flex-1 truncate text-footnote font-medium text-text-secondary">Promptstellung</span>
      </button>
      {open && (
        <div
          className="dl-markdown dl-scroll mx-3 mb-3 max-h-80 overflow-auto rounded-md border border-separator bg-bg-base p-3 text-caption text-text-secondary"
          dangerouslySetInnerHTML={{ __html: renderMarkdown(prompt) }}
        />
      )}
    </div>
  );
}

// ── GitLab-inspired pipeline view of one execution ────────────────────────────
// One run execution = a pipeline. Stages run left→right; each repo is a job flowing through them, shown
// as a status pill sitting on the pipeline line (GitLab's stages→jobs graph). The Merge stage is a manual
// gate (a human/auto-merge outside the run); Prod-Deploy is downstream (green once the repo is deployed).
//
// The SAME pipeline renders a LIVE run: the repo in flight (RunResult.live) is appended as a row, its
// running stage pulses (`running`), and clicking it drops into the agent's live transcript — so watching
// a run is not a separate view but the pipeline itself, moving. Stages the running repo has not reached
// yet read as `pending`, not `skipped`.

type JobStatus = 'success' | 'failed' | 'skipped' | 'manual' | 'pending' | 'running';

interface PipelineStage {
  key: string;
  label: string;
  manual?: boolean;
}

// The full dev/prod pipeline. A report-mode execution (read-only analysis) collapses to a single stage.
const PIPELINE_STAGES: PipelineStage[] = [
  { key: 'implement', label: 'Implement' },
  { key: 'dev-deploy', label: 'Dev-Deploy' },
  { key: 'push', label: 'Push' },
  { key: 'pr', label: 'PR' },
  { key: 'merge', label: 'Merge', manual: true },
  { key: 'prod-deploy', label: 'Prod-Deploy' },
];
const REPORT_STAGES: PipelineStage[] = [{ key: 'analyze', label: 'Analyze' }];
const RUN_STEP_KEYS = ['implement', 'dev-deploy', 'push', 'pr'];

interface Job {
  status: JobStatus;
  log?: string;
  href?: string;
}

// deriveJobs maps one repo's recorded steps onto the canonical stage chain, GitLab-style: a recorded step
// is success/failed by its ok flag (or `running` while in flight); once something fails the later run
// stages are skipped; a run stage with no step is either a bypass (no changes → no push/PR) or, when the
// repo errored before any step ran (e.g. a clone failure leaves no steps), the failure point. Merge is a
// manual gate while a PR is open; Prod-Deploy is pending until the PR merges and the repo reports
// deployed. For a repo still RUNNING, stages it has not reached yet are `pending`, not `skipped`.
function deriveJobs(repo: RepoResult, stages: PipelineStage[]): Job[] {
  const byName = new Map((repo.steps ?? []).map((s) => [s.name, s]));
  const anyRunStep = RUN_STEP_KEYS.some((k) => byName.has(k));
  const live = !!repo.running;
  // For a still-running repo, a stage with no recorded step has not happened YET, so it is pending — the
  // agent will get to it — rather than a deliberately skipped bypass.
  const unreached: JobStatus = live ? 'pending' : 'skipped';
  let terminal = false;
  return stages.map((stage) => {
    const step = byName.get(stage.key);
    if (step) {
      if (step.running) return { status: 'running', log: step.log || undefined };
      if (!step.ok) terminal = true;
      return { status: step.ok ? 'success' : 'failed', log: step.log || undefined };
    }
    if (stage.key === 'merge') {
      if (repo.prUrl) return { status: 'manual', href: repo.prUrl };
      return { status: terminal ? 'skipped' : unreached };
    }
    if (stage.key === 'prod-deploy') {
      if (repo.deployed) return { status: 'success' };
      if (repo.prUrl) return { status: 'pending' };
      return { status: terminal ? 'skipped' : unreached };
    }
    if (!RUN_STEP_KEYS.includes(stage.key) || terminal) return { status: terminal ? 'skipped' : unreached };
    // No recorded step for this run stage and nothing has failed yet: the failure point when the repo
    // errored before recording any step (clone/setup), otherwise a bypassed (or not-yet-reached) stage.
    if (repo.error && !anyRunStep) {
      terminal = true;
      return { status: 'failed', log: repo.error };
    }
    return { status: unreached };
  });
}

const JOB_STYLES: Record<JobStatus, { dot: string; pill: string; label: string }> = {
  running: { dot: 'bg-warning', pill: 'border-warning/40 bg-warning/10 text-warning', label: 'läuft' },
  success: { dot: 'bg-success', pill: 'border-success/30 bg-success/10 text-success', label: 'OK' },
  failed: { dot: 'bg-danger', pill: 'border-danger/30 bg-danger/10 text-danger', label: 'Fehler' },
  manual: { dot: 'bg-accent', pill: 'border-accent/40 bg-accent/10 text-accent', label: 'manuell' },
  pending: { dot: 'bg-warning', pill: 'border-warning/30 bg-warning/10 text-warning', label: 'ausstehend' },
  skipped: { dot: 'bg-fill', pill: 'border-separator bg-surface text-text-tertiary', label: 'übersprungen' },
};

function JobPill({ job, selected, onSelect }: { job: Job; selected: boolean; onSelect: () => void }) {
  const st = JOB_STYLES[job.status];
  const clickable = !!(job.log || job.href);
  return (
    <button
      type="button"
      disabled={!clickable}
      onClick={onSelect}
      className={cn(
        'relative z-10 flex items-center gap-1.5 rounded-full border px-2.5 py-1 text-caption font-medium transition',
        st.pill,
        job.status === 'running' && 'ring-1 ring-warning/40',
        selected && 'ring-2 ring-accent/40',
        clickable ? 'cursor-pointer hover:brightness-110' : 'cursor-default',
      )}
    >
      {job.status === 'manual' ? (
        <PlayIcon className="h-3 w-3 shrink-0" />
      ) : (
        <span className={cn('h-2 w-2 shrink-0 rounded-full', st.dot, job.status === 'running' && 'animate-pulse')} />
      )}
      <span className="truncate">{st.label}</span>
    </button>
  );
}

/** The whole execution rendered as a GitLab-style pipeline: repos (rows) × stages (columns). Works for a
 *  finished run and a LIVE one alike — the in-flight repo (result.live) is appended as a pulsing row, and
 *  the detail pane follows the job the agent is working right now (its live transcript) until the viewer
 *  picks a job themselves. */
export function ExecutionPipeline({ result }: { result: RunResult }) {
  // Finished repos, then the one in flight (if any) as the trailing, live row.
  const rows = useMemo(() => [...(result.repos ?? []), ...(result.live ? [result.live] : [])], [result.repos, result.live]);

  // Report execution iff at least one repo actually analyzed AND none ran a real pipeline step. This must
  // NOT treat a run where every repo failed before any step (e.g. a clone/DNS outage → empty steps) as
  // "report" — that would collapse to a single Analyze stage and hide the failures. Such a run has no
  // analyze step anywhere, so it correctly falls through to the pipeline, where the failure shows as
  // implement=failed. The live row counts too, so a live report run reads as report from its first step.
  const hasAnalyze = rows.some((r) => (r.steps ?? []).some((s) => s.name === 'analyze'));
  const hasRunStep = rows.some((r) => (r.steps ?? []).some((s) => RUN_STEP_KEYS.includes(s.name)));
  const isReport = hasAnalyze && !hasRunStep;
  const stages = isReport ? REPORT_STAGES : PIPELINE_STAGES;

  // Derive every row's jobs once, then locate the job in flight (first running one) to follow live.
  const grid = useMemo(() => rows.map((repo) => deriveJobs(repo, stages)), [rows, stages]);
  const runningAt = useMemo(() => {
    for (let ri = 0; ri < grid.length; ri++) {
      const si = grid[ri].findIndex((j) => j.status === 'running');
      if (si >= 0) return { repo: ri, stage: si };
    }
    return null;
  }, [grid]);

  const [sel, setSel] = useState<{ repo: number; stage: number } | null>(null);
  const pickedRef = useRef(false); // once the viewer clicks a job, stop auto-following the live one

  // Default the detail pane to the job in flight and keep following it as the agent advances — until the
  // viewer selects a job themselves, after which their choice stands.
  useEffect(() => {
    if (!pickedRef.current && runningAt) setSel(runningAt);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [runningAt?.repo, runningAt?.stage]);

  if (rows.length === 0) {
    return <p className="text-footnote text-text-tertiary">Keine Repositories in dieser Ausführung.</p>;
  }

  const selRepo = sel && rows[sel.repo] ? rows[sel.repo] : null;
  const selJob = sel && grid[sel.repo] ? grid[sel.repo][sel.stage] : null;
  const legend: JobStatus[] = runningAt
    ? ['running', 'success', 'failed', 'manual', 'pending', 'skipped']
    : ['success', 'failed', 'manual', 'pending', 'skipped'];

  return (
    <div className="flex flex-col gap-3">
      <div className="flex flex-wrap items-center gap-x-3 gap-y-1 text-caption text-text-tertiary">
        {legend.map((s) => (
          <span key={s} className="inline-flex items-center gap-1.5">
            <span className={cn('h-2 w-2 rounded-full', JOB_STYLES[s].dot, s === 'running' && 'animate-pulse')} />
            {JOB_STYLES[s].label}
          </span>
        ))}
      </div>

      <div className="dl-scroll overflow-x-auto rounded-card border border-separator bg-surface">
        <div className="min-w-max">
          <div className="flex items-center border-b border-separator px-3 py-2">
            <div className="w-44 shrink-0 text-caption font-semibold uppercase tracking-wide text-text-tertiary">Repository</div>
            {stages.map((st) => (
              <div key={st.key} className="w-32 shrink-0 text-caption font-semibold uppercase tracking-wide text-text-tertiary">
                {st.label}
              </div>
            ))}
          </div>

          {rows.map((repo, ri) => {
            const jobs = grid[ri];
            return (
              <div
                key={`${repo.repo}:${ri}`}
                className={cn(
                  'flex items-center border-b border-separator px-3 py-2.5 last:border-0',
                  repo.running && 'bg-warning/5',
                )}
              >
                <div className="flex w-44 shrink-0 items-center gap-2 pr-2">
                  <span
                    className={cn(
                      'h-2 w-2 shrink-0 rounded-full',
                      repo.running ? 'animate-pulse bg-warning' : repo.ok ? 'bg-success' : 'bg-danger',
                    )}
                  />
                  <span className="min-w-0 truncate font-mono text-footnote font-medium text-text-primary">{repo.repo}</span>
                </div>
                <div className="relative flex items-center">
                  <span className="pointer-events-none absolute inset-x-2 top-1/2 h-px -translate-y-1/2 bg-separator" aria-hidden />
                  {jobs.map((job, si) => (
                    <div key={si} className="flex w-32 shrink-0 items-center">
                      <JobPill
                        job={job}
                        selected={sel?.repo === ri && sel?.stage === si}
                        onSelect={() => {
                          pickedRef.current = true;
                          setSel(sel?.repo === ri && sel?.stage === si ? null : { repo: ri, stage: si });
                        }}
                      />
                    </div>
                  ))}
                </div>
              </div>
            );
          })}
        </div>
      </div>

      {selRepo && selJob && (selJob.log || selJob.href) && (
        <PipelineJobDetail repo={selRepo.repo} stageLabel={stages[sel!.stage].label} job={selJob} />
      )}
    </div>
  );
}

/** The detail pane under the pipeline for one selected job: its report (or, for the job in flight, the
 *  agent's LIVE transcript — auto-scrolled to the newest line as it streams). */
function PipelineJobDetail({ repo, stageLabel, job }: { repo: string; stageLabel: string; job: Job }) {
  const running = job.status === 'running';
  const logRef = useRef<HTMLPreElement>(null);
  useEffect(() => {
    if (running && logRef.current) logRef.current.scrollTop = logRef.current.scrollHeight;
  }, [running, job.log]);
  return (
    <div className={cn('rounded-card border bg-bg-base p-3', running ? 'border-warning/40' : 'border-separator')}>
      <div className="mb-1.5 flex items-center justify-between gap-2">
        <span className="flex items-center gap-1.5 text-caption font-semibold uppercase tracking-wide text-text-tertiary">
          {running && <span className="h-1.5 w-1.5 animate-pulse rounded-full bg-warning" />}
          {repo} · {stageLabel}
        </span>
        {job.href && (
          <a href={job.href} target="_blank" rel="noreferrer" className="shrink-0 text-caption font-medium text-accent hover:underline">
            PR öffnen ↗
          </a>
        )}
      </div>
      {job.log && (
        <pre
          ref={logRef}
          className="dl-scroll max-h-80 overflow-auto whitespace-pre-wrap font-mono text-caption text-text-secondary"
        >
          {job.log}
        </pre>
      )}
    </div>
  );
}

/** The full execution document as a page: title, totals, then the repos as a GitLab-style pipeline or a
 *  detailed list (toggle). The right pane of an aggregate ExecutionHistory. */
export function ExecutionDetail({ runId, resultId, hideHeader }: { runId: string; resultId: string; hideHeader?: boolean }) {
  const source = useMemo(() => getDataSource(), []);
  const [res, setRes] = useState<RunResult | null>(null);
  const [err, setErr] = useState(false);

  useEffect(() => {
    let cancelled = false;
    setRes(null);
    setErr(false);
    source
      .mercuryRunResult(runId, resultId)
      .then((r) => !cancelled && setRes(r))
      .catch(() => !cancelled && setErr(true));
    return () => {
      cancelled = true;
    };
  }, [source, runId, resultId]);

  if (err) return <p className="px-8 py-7 text-footnote text-text-secondary">Diese Ausführung konnte nicht geladen werden.</p>;
  if (!res) return <p className="px-8 py-7 text-footnote text-text-tertiary">Lädt…</p>;

  return <ExecutionDetailBody res={res} hideHeader={hideHeader} />;
}

/** The execution body: title, totals, prompt, then the repos as a GitLab-style pipeline (default) or a
 *  detailed per-repo list, toggled by the viewer. Both read the same recorded steps. `hideHeader` drops
 *  the title/date/status block and the outer padding for embedding under chrome that already carries
 *  them (the calendar's detail modal), leaving the totals, prompt and pipeline. */
function ExecutionDetailBody({ res, hideHeader }: { res: RunResult; hideHeader?: boolean }) {
  const [view, setView] = useState<'pipeline' | 'list'>('pipeline');
  return (
    <article className={cn('mx-auto max-w-5xl', hideHeader ? '' : 'px-8 py-7')}>
      {!hideHeader && (
        <div className="flex items-start justify-between gap-4">
          <div className="min-w-0">
            <h1 className="text-title3 font-semibold tracking-tight text-text-primary">{res.runName ?? 'Ausführung'}</h1>
            <p className="mt-1 text-footnote text-text-secondary">
              {fmtDateTime(res.startedAt)}
              {res.finishedAt ? ` – ${new Date(res.finishedAt).toLocaleTimeString('de-DE', { hour: '2-digit', minute: '2-digit' })}` : ''}
            </p>
            <ExecutionOrigin className="mt-1.5" trigger={res.trigger} requestedBy={res.requestedBy} />
          </div>
          <OkPill ok={res.ok} />
        </div>
      )}

      <div className={cn('flex flex-wrap items-center justify-between gap-3', !hideHeader && 'mt-4')}>
        <div className="flex flex-wrap items-center gap-x-4 gap-y-1 rounded-card border border-separator bg-surface px-3 py-2">
          {res.model && <ModelStat model={res.model} effort={res.effort} />}
          <span className="text-caption text-text-tertiary">{res.numTurns} Turns</span>
          <TokenStat input={res.inputTokens} output={res.outputTokens} cost={res.costUsd} />
        </div>
        <div className="inline-flex rounded-md border border-separator bg-surface p-0.5 text-footnote">
          {(['pipeline', 'list'] as const).map((v) => (
            <button
              key={v}
              type="button"
              onClick={() => setView(v)}
              className={cn(
                'rounded px-2.5 py-1 font-medium transition',
                view === v ? 'bg-accent/15 text-text-primary' : 'text-text-tertiary hover:text-text-secondary',
              )}
            >
              {v === 'pipeline' ? 'Pipeline' : 'Liste'}
            </button>
          ))}
        </div>
      </div>

      {res.prompt && (
        <div className="mt-4">
          <PromptDisclosure prompt={res.prompt} />
        </div>
      )}

      <section className="mt-6">
        {view === 'pipeline' ? (
          <ExecutionPipeline result={res} />
        ) : (
          <div className="flex flex-col gap-4">
            {(res.repos ?? []).length === 0 ? (
              <p className="text-footnote text-text-tertiary">Keine Repositories in dieser Ausführung.</p>
            ) : (
              (res.repos ?? []).map((repo, i) => <RepoBlock key={`${repo.repo}:${i}`} repo={repo} />)
            )}
          </div>
        )}
      </section>
    </article>
  );
}

/** One row in the aggregate execution list: run, start, status, repos and the token/cost readout. */
function ExecutionRow({ ex, selected, onSelect }: { ex: RunExecution; selected: boolean; onSelect: () => void }) {
  return (
    <button
      type="button"
      onClick={onSelect}
      className={cn(
        'flex w-full flex-col gap-1 rounded-md px-2.5 py-2 text-left transition duration-fast',
        selected ? 'bg-accent/15' : 'hover:bg-fill/10',
      )}
    >
      <div className="flex items-center gap-1.5">
        <span className={cn('min-w-0 flex-1 truncate text-footnote font-medium', selected ? 'text-text-primary' : 'text-text-secondary')}>
          {ex.runName}
        </span>
        <OkPill ok={ex.ok} />
      </div>
      <span className="text-caption text-text-tertiary">
        {fmtDateTime(ex.at)} · {ex.repoCount} Repos
      </span>
      <ExecutionOrigin trigger={ex.trigger} requestedBy={ex.requestedBy} />
      <TokenStat input={ex.inputTokens} output={ex.outputTokens} cost={ex.costUsd} />
    </button>
  );
}

/** The execution history for one surface: every completed execution of its `type` (newest first) with
 *  token/cost, and a detail pane that loads the full result document. Reused identically by the Läufe
 *  and ToDos tabs — only `type` differs. */
export function ExecutionHistory({ type }: { type: RunType }) {
  const source = useMemo(() => getDataSource(), []);
  const { toast } = useToast();
  const [executions, setExecutions] = useState<RunExecution[] | null>(null);
  const [failed, setFailed] = useState<string | null>(null);
  const [sel, setSel] = useState<{ runId: string; resultId: string } | null>(null);
  const [refreshing, setRefreshing] = useState(false);

  useEffect(() => {
    let cancelled = false;
    source
      .mercuryRunExecutions(type)
      .then((r) => !cancelled && setExecutions(r.executions))
      .catch((e) => !cancelled && setFailed(msg(e)));
    return () => {
      cancelled = true;
    };
  }, [source, type]);

  const refresh = async () => {
    if (refreshing) return;
    setRefreshing(true);
    try {
      const r = await source.mercuryRunExecutions(type);
      setExecutions(r.executions);
    } catch (e) {
      toast({ title: 'Aktualisieren fehlgeschlagen', description: msg(e), variant: 'danger' });
    } finally {
      setRefreshing(false);
    }
  };

  if (failed) {
    return (
      <div className="flex min-h-0 flex-1 items-center justify-center bg-bg-base px-6">
        <p className="max-w-md text-center text-footnote text-text-secondary">{failed}</p>
      </div>
    );
  }

  return (
    <>
      <div className="flex w-96 shrink-0 flex-col border-r border-separator bg-surface">
        <div className="flex items-center justify-between gap-2 border-b border-separator px-3 py-2">
          <span className="text-footnote font-medium text-text-primary">Ausführungen</span>
          <Button variant="ghost" size="sm" disabled={refreshing} onClick={refresh}>
            <RefreshIcon className={cn('h-4 w-4', refreshing && 'animate-spin')} /> Aktualisieren
          </Button>
        </div>
        <div className="dl-scroll flex-1 overflow-y-auto p-1.5">
          {executions === null ? (
            <p className="px-2.5 py-3 text-caption text-text-tertiary">Lädt…</p>
          ) : executions.length === 0 ? (
            <p className="px-2.5 py-3 text-caption text-text-tertiary">Noch keine Ausführungen.</p>
          ) : (
            <div className="flex flex-col gap-0.5">
              {executions.map((ex) => (
                <ExecutionRow
                  key={ex.resultId}
                  ex={ex}
                  selected={sel?.resultId === ex.resultId}
                  onSelect={() => setSel({ runId: ex.runId, resultId: ex.resultId })}
                />
              ))}
            </div>
          )}
        </div>
      </div>

      <div className="dl-scroll min-h-0 flex-1 overflow-y-auto bg-bg-base">
        {/* A single unreadable/partial execution must not blank the surface — contain it to this pane and
            let picking another (healthy) execution recover via resetKeys. */}
        <ErrorBoundary resetKeys={[sel?.runId, sel?.resultId]}>
          {sel ? (
            <ExecutionDetail key={`${sel.runId}:${sel.resultId}`} runId={sel.runId} resultId={sel.resultId} />
          ) : (
            <EmptyPlaceholder text="Wähle links eine Ausführung, um Bericht, Schritte und Token-Verbrauch zu sehen." />
          )}
        </ErrorBoundary>
      </div>
    </>
  );
}

/** The full execution document inline (compact) — totals row, then each repo. Used UNDER an expanded
 *  row of a per-entity ExecutionList, where the entity (run/ToDo) is already the page. */
function InlineExecutionDetail({ runId, resultId }: { runId: string; resultId: string }) {
  const source = useMemo(() => getDataSource(), []);
  const [res, setRes] = useState<RunResult | null>(null);
  const [err, setErr] = useState(false);

  useEffect(() => {
    let cancelled = false;
    setRes(null);
    setErr(false);
    source
      .mercuryRunResult(runId, resultId)
      .then((r) => !cancelled && setRes(r))
      .catch(() => !cancelled && setErr(true));
    return () => {
      cancelled = true;
    };
  }, [source, runId, resultId]);

  if (err) return <p className="px-2 py-2 text-caption text-text-secondary">Diese Ausführung konnte nicht geladen werden.</p>;
  if (!res) return <p className="px-2 py-2 text-caption text-text-tertiary">Lädt…</p>;

  return (
    <div className="mt-1.5 flex flex-col gap-3 pl-5">
      <div className="flex flex-wrap items-center gap-x-4 gap-y-1 rounded-card border border-separator bg-surface px-3 py-2">
        <span className="text-caption text-text-tertiary">
          {fmtDateTime(res.startedAt)}
          {res.finishedAt ? ` – ${new Date(res.finishedAt).toLocaleTimeString('de-DE', { hour: '2-digit', minute: '2-digit' })}` : ''}
        </span>
        {res.model && <ModelStat model={res.model} effort={res.effort} />}
        <span className="text-caption text-text-tertiary">{res.numTurns} Turns</span>
        <TokenStat input={res.inputTokens} output={res.outputTokens} cost={res.costUsd} />
      </div>
      <PromptDisclosure prompt={res.prompt} />
      {(res.repos ?? []).length === 0 ? (
        <p className="text-caption text-text-tertiary">Keine Repositories in dieser Ausführung.</p>
      ) : (
        (res.repos ?? []).map((repo, i) => <RepoBlock key={`${repo.repo}:${i}`} repo={repo} />)
      )}
    </div>
  );
}

/** The executions of ONE entity (run or ToDo): a row per result (time, status, token/cost); selecting
 *  one expands the full result document inline. The per-entity drill-down that lives in a detail pane
 *  (distinct from the cross-entity ExecutionHistory tab). */
export function ExecutionList({ runId, results }: { runId: string; results: RunResultRef[] | null }) {
  const [openId, setOpenId] = useState<string | null>(null);

  if (results === null) return <p className="text-footnote text-text-tertiary">Lädt…</p>;
  if (results.length === 0) return <p className="text-footnote text-text-tertiary">Noch keine Ausführungen.</p>;

  return (
    <ul className="flex flex-col gap-1.5">
      {results.map((r) => {
        const open = openId === r.resultId;
        return (
          <li key={r.resultId}>
            <button
              type="button"
              onClick={() => setOpenId(open ? null : r.resultId)}
              className={cn(
                'flex w-full flex-wrap items-center gap-x-3 gap-y-1 rounded-md px-2 py-1.5 text-left transition duration-fast',
                open ? 'bg-accent/10' : 'hover:bg-fill/10',
              )}
            >
              <ChevronRightIcon className={cn('h-3.5 w-3.5 shrink-0 text-text-tertiary transition-transform duration-fast', open && 'rotate-90')} />
              <span className="text-footnote text-text-secondary">{fmtDateTime(r.at)}</span>
              <OkPill ok={r.ok} />
              <ExecutionOrigin trigger={r.trigger} requestedBy={r.requestedBy} />
              <TokenStat input={r.inputTokens} output={r.outputTokens} cost={r.costUsd} />
            </button>
            {open && <InlineExecutionDetail key={r.resultId} runId={runId} resultId={r.resultId} />}
          </li>
        );
      })}
    </ul>
  );
}

/** Poll the server for the run executing right now (its id + live result id), or null. This is the
 *  single source of truth for "a run is live": read on mount it RESTORES the running state after a page
 *  reload (so a just-started run no longer looks like it never started), and it drives the live-follow
 *  view. refetch() forces an immediate re-check — e.g. right after starting a run — so the UI reacts
 *  without waiting for the next tick. Reflects an actually-running process: empty again after a restart. */
export function useActiveRun(): { active: RunActive[]; inflight: RunInFlight[]; refetch: () => void } {
  const source = useMemo(() => getDataSource(), []);
  const [active, setActive] = useState<RunActive[]>([]);
  const [inflight, setInflight] = useState<RunInFlight[]>([]);
  const [bump, setBump] = useState(0);

  useEffect(() => {
    let cancelled = false;
    let timer: number | undefined;
    const poll = async () => {
      try {
        const r = await source.mercuryRunActive();
        if (!cancelled) {
          setActive(r.active);
          setInflight(r.inflight ?? []);
        }
      } catch {
        /* transient — keep the last known state */
      }
      if (!cancelled) timer = window.setTimeout(poll, 2500);
    };
    void poll();
    const onFocus = () => setBump((b) => b + 1); // re-check when the tab regains focus
    window.addEventListener('focus', onFocus);
    return () => {
      cancelled = true;
      if (timer) window.clearTimeout(timer);
      window.removeEventListener('focus', onFocus);
    };
  }, [source, bump]);

  return { active, inflight, refetch: useCallback(() => setBump((b) => b + 1), []) };
}

/** Poll a run's result document, following it live while `live` (every 2s); once it settles it fetches
 *  the final state once and stops. The single source of the live result for every follower. */
function useLiveResult(runId: string, resultId: string, live: boolean): { res: RunResult | null; err: boolean } {
  const source = useMemo(() => getDataSource(), []);
  const [res, setRes] = useState<RunResult | null>(null);
  const [err, setErr] = useState(false);
  const resRef = useRef<RunResult | null>(null);

  useEffect(() => {
    let cancelled = false;
    let timer: number | undefined;
    resRef.current = null;
    setRes(null);
    setErr(false);
    const poll = async () => {
      try {
        const r = await source.mercuryRunResult(runId, resultId);
        if (!cancelled) {
          resRef.current = r;
          setRes(r);
          setErr(false);
        }
      } catch {
        if (!cancelled && !resRef.current) setErr(true); // only surface if we never got a document
      }
      if (!cancelled && live) timer = window.setTimeout(poll, 2000);
    };
    void poll();
    return () => {
      cancelled = true;
      if (timer) window.clearTimeout(timer);
    };
  }, [source, runId, resultId, live]);

  return { res, err };
}

/** Follow a run LIVE: poll its in-flight result document and render it as the moving pipeline — repos
 *  already finished, the one in flight (its running stage and the agent's streaming transcript), and the
 *  totals. Watching a run IS the pipeline here: the running stage pulses and its live transcript opens in
 *  the detail pane. `state` labels a paused (usage-limit) run; otherwise the label follows `live`. */
export function LiveExecution({
  runId,
  resultId,
  live,
  state,
}: {
  runId: string;
  resultId: string;
  live: boolean;
  state?: 'executing' | 'suspended';
}) {
  const { res, err } = useLiveResult(runId, resultId, live);
  const suspended = state === 'suspended';

  if (!res) {
    return <p className="text-footnote text-text-tertiary">{err ? 'Live-Ausführung wird vorbereitet…' : 'Lädt…'}</p>;
  }

  const label = suspended ? 'Pausiert' : live ? 'Läuft gerade' : 'Gerade beendet';
  const dotLive = suspended || live;
  const hasRows = (res.repos?.length ?? 0) > 0 || !!res.live;
  return (
    <div className={cn('flex flex-col gap-3 rounded-card border p-3', dotLive ? 'border-warning/30 bg-warning/5' : 'border-separator')}>
      <div className="flex flex-wrap items-center gap-x-3 gap-y-1">
        <span className="flex items-center gap-1.5 text-footnote font-medium text-text-primary">
          <span className={cn('h-2 w-2 rounded-full', dotLive ? 'animate-pulse bg-warning' : 'bg-text-tertiary')} /> {label}
        </span>
        <span className="text-caption text-text-tertiary">seit {fmtDateTime(res.startedAt)}</span>
        {res.model && <ModelStat model={res.model} effort={res.effort} />}
        <span className="text-caption text-text-tertiary">{res.numTurns} Turns</span>
        <TokenStat input={res.inputTokens} output={res.outputTokens} cost={res.costUsd} />
      </div>
      <PromptDisclosure prompt={res.prompt} />
      {hasRows ? <ExecutionPipeline result={res} /> : <p className="text-caption text-text-tertiary">Der Lauf startet…</p>}
    </div>
  );
}

// ── Aktive Läufe — the transparent live overview ──────────────────────────────
// Replaces the thin "Lauf aktiv" badge with a list of everything the system is working right now
// (executing + suspended-on-limit), each openable into a live session — the moving pipeline with the
// agent's streaming transcript. Shared verbatim by both Mercury surfaces (Läufe and ToDos), since a run
// of either kind can be the one in flight.

/** A coarse human duration ("3 min", "1 h 12 min", "8 s"). */
function fmtDuration(ms: number): string {
  const s = Math.max(0, Math.floor(ms / 1000));
  const h = Math.floor(s / 3600);
  const m = Math.floor((s % 3600) / 60);
  if (h > 0) return `${h} h ${m} min`;
  if (m > 0) return `${m} min`;
  return `${s} s`;
}
const fmtSince = (iso?: string) => {
  if (!iso) return '—';
  const t = new Date(iso).getTime();
  return Number.isNaN(t) ? '—' : fmtDuration(Date.now() - t);
};
const fmtUntil = (iso?: string) => {
  if (!iso) return '—';
  const t = new Date(iso).getTime();
  if (Number.isNaN(t)) return '—';
  const d = t - Date.now();
  return d <= 0 ? 'jetzt' : `in ${fmtDuration(d)}`;
};

/** Completed vs total repos this execution — an exact denominator only when known (ToDos). */
const progressText = (r: RunInFlight) => (r.reposTotal ? `${r.reposDone}/${r.reposTotal} Repos` : `${r.reposDone} Repos`);

function TypeChip({ type }: { type: RunType }) {
  return (
    <span className="shrink-0 rounded bg-fill/15 px-1.5 py-0.5 text-caption font-medium text-text-tertiary">
      {type === 'todo' ? 'ToDo' : 'Auto'}
    </span>
  );
}

function StatePill({ state }: { state: RunInFlight['state'] }) {
  if (state === 'executing') {
    return (
      <span className="flex shrink-0 items-center gap-1 rounded bg-warning/15 px-1.5 py-0.5 text-caption font-medium text-warning">
        <span className="h-1.5 w-1.5 animate-pulse rounded-full bg-warning" /> läuft
      </span>
    );
  }
  return (
    <span className="shrink-0 rounded bg-accent/15 px-1.5 py-0.5 text-caption font-medium text-accent">pausiert</span>
  );
}

/** One row in the overview list: state, name, kind, and a portioned line of live detail (repo/step for a
 *  running run; resume time for a paused one) plus its running spend. */
function OverviewRow({ row, selected, onSelect }: { row: RunInFlight; selected: boolean; onSelect: () => void }) {
  const executing = row.state === 'executing';
  return (
    <button
      type="button"
      onClick={onSelect}
      className={cn(
        'flex w-full flex-col gap-1 rounded-md px-2.5 py-2 text-left transition duration-fast',
        selected ? 'bg-accent/15' : 'hover:bg-fill/10',
      )}
    >
      <div className="flex items-center gap-1.5">
        <StatePill state={row.state} />
        <span className={cn('min-w-0 flex-1 truncate text-footnote font-medium', selected ? 'text-text-primary' : 'text-text-secondary')}>
          {row.runName || 'Lauf'}
        </span>
        <TypeChip type={row.type} />
      </div>
      <span className="truncate text-caption text-text-tertiary">
        {executing
          ? `seit ${fmtSince(row.startedAt)} · ${progressText(row)}${row.currentStep ? ` · ${row.currentStep}` : ''}`
          : `Fortsetzung ${fmtUntil(row.resumeAt)}${row.attempts ? ` · Versuch ${row.attempts}` : ''}`}
      </span>
      <TokenStat input={row.inputTokens} output={row.outputTokens} cost={row.costUsd} />
    </button>
  );
}

/** The right pane of the overview: the focused run as a live session — its title, a kill-switch while it
 *  executes, and the moving pipeline (the agent's live transcript opens in-place). */
function RunSession({ row, onCancel, cancelling }: { row: RunInFlight; onCancel: (runId: string) => void; cancelling: boolean }) {
  const executing = row.state === 'executing';
  return (
    <div className="flex min-h-0 flex-col gap-3">
      <div className="flex flex-wrap items-center gap-2">
        <h3 className="min-w-0 flex-1 truncate text-body font-semibold text-text-primary">{row.runName || 'Lauf'}</h3>
        <TypeChip type={row.type} />
        {executing && (
          <Button variant="danger" size="sm" disabled={cancelling} onClick={() => onCancel(row.runId)}>
            {cancelling ? 'Bricht ab…' : 'Abbrechen'}
          </Button>
        )}
      </div>
      {row.resultId ? (
        <LiveExecution runId={row.runId} resultId={row.resultId} live={executing} state={row.state} />
      ) : (
        <p className="rounded-card border border-separator bg-surface px-3 py-6 text-center text-footnote text-text-tertiary">
          Der Lauf wird vorbereitet…
        </p>
      )}
    </div>
  );
}

// Persist the open overview + watched run so a browser reload drops the viewer back into the same live
// session (Zustandserhalt beim Reload). Session-scoped and best-effort — a blocked storage is non-fatal.
const WATCH_KEY = 'dl.mercury.watch';
function readWatch(): { open: boolean; runId: string | null } {
  try {
    const raw = sessionStorage.getItem(WATCH_KEY);
    if (raw) return JSON.parse(raw) as { open: boolean; runId: string | null };
  } catch {
    /* ignore */
  }
  return { open: false, runId: null };
}
function writeWatch(v: { open: boolean; runId: string | null }) {
  try {
    sessionStorage.setItem(WATCH_KEY, JSON.stringify(v));
  } catch {
    /* ignore */
  }
}

/** The live "Aktive Läufe" overview: a compact bar (what is running, with a kill-switch) that opens a
 *  list of every in-flight run, each inspectable as a live Claude-Code-style session. `inflight` is server
 *  truth from useActiveRun; `onCancel` aborts the executing run. Renders nothing when nothing is in
 *  flight. Reused identically by the Läufe and ToDos surfaces. */
export function ActiveRunsOverview({
  inflight,
  onCancel,
  cancelling,
  className,
}: {
  inflight: RunInFlight[];
  onCancel: (runId: string) => void;
  cancelling: boolean;
  className?: string;
}) {
  const [restored] = useState(readWatch); // lazy: read session storage once, on mount
  const [open, setOpen] = useState(restored.open);
  const [selId, setSelId] = useState<string | null>(restored.runId);

  const selected = inflight.find((r) => r.runId === selId) ?? null;

  // When the overview is open with no valid selection, focus the first in-flight run.
  useEffect(() => {
    if (open && !selected && inflight.length > 0) setSelId(inflight[0].runId);
  }, [open, selected, inflight]);

  // Nothing left in flight → close (and the bar unmounts below).
  useEffect(() => {
    if (inflight.length === 0 && open) setOpen(false);
  }, [inflight.length, open]);

  // Persist for reload restore.
  useEffect(() => {
    writeWatch({ open, runId: selId });
  }, [open, selId]);

  if (inflight.length === 0) return null;

  const executing = inflight.some((r) => r.state === 'executing');
  const only = inflight.length === 1 ? inflight[0] : null;
  const summary = only ? `${only.state === 'executing' ? 'Läuft' : 'Pausiert'}: ${only.runName || 'Lauf'}` : `${inflight.length} Läufe aktiv`;
  const nExec = inflight.filter((r) => r.state === 'executing').length;
  const nSusp = inflight.length - nExec;
  const description = [nExec ? `${nExec} läuft` : '', nSusp ? `${nSusp} pausiert` : ''].filter(Boolean).join(' · ');

  return (
    <>
      <div className={cn('flex items-center justify-between gap-2 rounded-md bg-warning/10 px-2 py-1.5', className)}>
        <button
          type="button"
          onClick={() => setOpen(true)}
          className="flex min-w-0 items-center gap-1.5 text-caption font-medium text-text-secondary transition hover:text-text-primary"
        >
          <span className="h-2 w-2 shrink-0 animate-pulse rounded-full bg-warning" />
          <span className="truncate">{summary}</span>
        </button>
        {executing && (
          <Button
            variant="danger"
            size="sm"
            disabled={cancelling}
            onClick={() => onCancel(inflight.find((r) => r.state === 'executing')!.runId)}
          >
            {cancelling ? 'Bricht ab…' : 'Abbrechen'}
          </Button>
        )}
      </div>

      <Modal open={open} onClose={() => setOpen(false)} title="Aktive Läufe" description={description} size="xl">
        <div className="flex min-h-[55vh] flex-col gap-4 sm:flex-row">
          <div className="dl-scroll flex shrink-0 flex-col gap-0.5 overflow-y-auto sm:w-64 sm:max-h-[60vh]">
            {inflight.map((row) => (
              <OverviewRow key={row.runId} row={row} selected={row.runId === selId} onSelect={() => setSelId(row.runId)} />
            ))}
          </div>
          <div className="min-w-0 flex-1 border-t border-separator pt-4 sm:border-l sm:border-t-0 sm:pl-4 sm:pt-0">
            {selected ? (
              <RunSession key={selected.runId} row={selected} onCancel={onCancel} cancelling={cancelling} />
            ) : (
              <EmptyPlaceholder text="Wähle links einen Lauf, um ihn live zu verfolgen." />
            )}
          </div>
        </div>
      </Modal>
    </>
  );
}

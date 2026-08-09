// PipelineStages — the repository row of an execution: its stages as badges on one line, plus the
// honest per-repo extras (preflight observation, block, one-grip delivery).
//
// It renders EXACTLY the server's stage array (B-17/B-35): one badge per recorded stage, in the
// recorded order, labelled with the stage's own name. There is no stage list in this client, no
// step-name heuristic and no guessed shape — which is also why an archive execution, whose stages
// carry historical names, renders unchanged. The four terminal states differ by their MARK, not by
// their words (REQ-030.5): filled green, filled red, a hollow ring, a dim fill.
import type { ReactNode } from 'react';
import { cn } from '@/lib/cn';
import { renderMarkdown } from '@/lib/markdown';
import { Button } from '@/ui/Button';
import { badgeTone } from '@/ui/tint';
import type { RepoPipeline, StageView } from '@/types';
import { SessionPane } from './Session';
import { blockSummary, needsDelivery, repoOutcome, stageBadge, stageHasDetail, stageLabel, TASK_STATE, TERMINAL_STATES, stagesOf } from './logic';

/** The shared pass/fail chip. */
export function OkPill({ ok }: { ok: boolean }) {
  return <span className={cn('shrink-0 rounded px-1.5 py-0.5 text-caption font-medium', badgeTone[ok ? 'success' : 'danger'])}>{ok ? 'Succeeded' : 'Failed'}</span>;
}

/** A status chip in the one shared tone map. */
export function TonePill({ label, tone, pulse, className }: { label: string; tone: keyof typeof badgeTone; pulse?: boolean; className?: string }) {
  return (
    <span className={cn('flex shrink-0 items-center gap-1 rounded px-1.5 py-0.5 text-caption font-medium', badgeTone[tone], className)}>
      {pulse && <span className="h-1.5 w-1.5 animate-pulse rounded-full bg-current" />}
      {label}
    </span>
  );
}

/** What the four marks mean — shown once per surface, so the marks alone suffice on every row. */
export function StageLegend({ className }: { className?: string }) {
  return (
    <div className={cn('flex flex-wrap items-center gap-x-3 gap-y-1 text-caption text-text-tertiary', className)}>
      {TERMINAL_STATES.map((s) => {
        const b = stageBadge({ state: s });
        return (
          <span key={s} className="inline-flex items-center gap-1.5">
            <span className={cn('h-2 w-2 rounded-full', b.dot)} />
            {b.label}
          </span>
        );
      })}
    </div>
  );
}

/** One stage as a badge. Clickable exactly when it has something to show (report, transcript, skip
 *  reason, evidence) — a badge without content is inert rather than a button into the void. */
function StageBadge({ stage, selected, onSelect }: { stage: StageView; selected?: boolean; onSelect?: () => void }) {
  const b = stageBadge(stage);
  const clickable = !!onSelect && stageHasDetail(stage);
  return (
    <button
      type="button"
      disabled={!clickable}
      onClick={onSelect}
      title={stage.reason || stage.evidence || undefined}
      className={cn(
        'flex items-center gap-1.5 rounded-full border px-2.5 py-1 text-caption font-medium transition',
        b.pill,
        selected && 'ring-2 ring-accent/40',
        clickable ? 'cursor-pointer hover:brightness-110' : 'cursor-default',
      )}
    >
      <span className={cn('h-2 w-2 shrink-0 rounded-full', b.dot, b.pulse && 'animate-pulse')} />
      <span className="truncate">{stageLabel(String(stage.stage))}</span>
    </button>
  );
}

/** The stage array of one repository, in recorded order. An execution that has not reached a
 *  repository yet carries no stages there — which is stated, never faked as "pending". */
export function PipelineStages({
  pipeline,
  selected,
  onSelect,
}: {
  pipeline: RepoPipeline;
  /** Index of the stage whose detail is open. */
  selected?: number;
  onSelect?: (index: number) => void;
}) {
  const stages = stagesOf(pipeline);
  if (stages.length === 0) {
    return <p className="text-caption text-text-tertiary">No stage recorded yet.</p>;
  }
  return (
    <div className="flex flex-wrap items-center gap-1.5">
      {stages.map((sv, i) => (
        <StageBadge
          key={`${String(sv.stage)}:${i}`}
          stage={sv}
          selected={selected === i}
          onSelect={onSelect ? () => onSelect(i) : undefined}
        />
      ))}
    </div>
  );
}

/** The open stage's detail: its evidence or short skip reason, its link, and its record. For the
 *  stage the server MARKED as carrying an agent session, that record is the session itself — open
 *  while it runs, readable afterwards, and writable by whoever may (REQ-036/038). Every other
 *  stage shows its recorded log as sanitized Markdown. A skip states its reason plainly; it can
 *  never be mistaken for a success.
 *
 *  Which stage carries a session is the SERVER's word (stage.session), never a name this client
 *  recognises — an archived execution with historical stage names therefore renders unchanged. */
export function StageDetail({ repo, stage, runId, resultId }: { repo: string; stage: StageView; runId?: string; resultId?: string }) {
  const running = stage.state === 'running';
  const note = stage.reason || stage.evidence;
  const session = !!stage.session && !!runId && !!resultId;
  return (
    <div className={cn('mt-2 rounded-card border bg-bg-base p-3', running ? 'border-warning/40' : 'border-separator')}>
      <div className="mb-1.5 flex flex-wrap items-center justify-between gap-2">
        <span className="flex items-center gap-1.5 text-caption font-semibold uppercase tracking-wide text-text-tertiary">
          {running && <span className="h-1.5 w-1.5 animate-pulse rounded-full bg-warning" />}
          {repo} · {stageLabel(String(stage.stage))}
        </span>
        {stage.link && (
          <a href={stage.link} target="_blank" rel="noreferrer" className="shrink-0 text-caption font-medium text-accent hover:underline">
            Open ↗
          </a>
        )}
      </div>
      {note && <p className="mb-1.5 text-caption text-text-secondary">{note}</p>}
      {session ? (
        <SessionPane runId={runId} resultId={resultId} repo={repo} />
      ) : (
        stage.log && (
          <div
            className="dl-markdown dl-scroll max-h-80 overflow-auto text-caption text-text-secondary"
            dangerouslySetInnerHTML={{ __html: renderMarkdown(stage.log) }}
          />
        )
      )}
    </div>
  );
}

/** One repository inside an execution: name, outcome, the preflight observation, the stage badges,
 *  its block (reason + attempts, resumable right here — REQ-032.4) and, for work that is
 *  implemented but not delivered, the one-grip delivery (REQ-020.4). */
export function RepoPipelineCard({
  pipeline,
  selectedStage,
  onSelectStage,
  onResume,
  resuming,
  onDeliver,
  delivering,
  children,
}: {
  pipeline: RepoPipeline;
  selectedStage?: number;
  onSelectStage?: (index: number) => void;
  /** Resume this blocked repository's execution from here (REQ-032.4). */
  onResume?: () => void;
  resuming?: boolean;
  /** Deliver already-implemented work in one grip (REQ-020.4). */
  onDeliver?: () => void;
  delivering?: boolean;
  /** The open stage's detail pane, rendered by the caller under this row. */
  children?: ReactNode;
}) {
  const outcome = repoOutcome(pipeline);
  const task = pipeline.taskState ? TASK_STATE[pipeline.taskState] : null;
  const undelivered = needsDelivery(pipeline.taskState);
  return (
    <div className={cn('rounded-card border bg-surface p-3', outcome.pulse ? 'border-warning/40' : pipeline.block ? 'border-danger/40' : 'border-separator')}>
      <div className="flex flex-wrap items-center gap-2">
        <span className="min-w-0 flex-1 truncate font-mono text-footnote font-medium text-text-primary">{pipeline.repo}</span>
        {task && <TonePill label={task.label} tone={task.tone} />}
        <TonePill label={outcome.label} tone={outcome.tone} pulse={outcome.pulse} />
      </div>

      <div className="mt-2">
        <PipelineStages pipeline={pipeline} selected={selectedStage} onSelect={onSelectStage} />
      </div>

      {/* A blocked repository holds its block visibly and is resumed from exactly here; the other
          repositories of the execution are unaffected (REQ-032.4). */}
      {pipeline.block && (
        <div className="mt-2 flex flex-wrap items-center gap-2 rounded-md bg-danger/10 px-2.5 py-1.5">
          <span className="min-w-0 flex-1 text-caption text-danger">{blockSummary(pipeline.block)}</span>
          {onResume && (
            <Button variant="secondary" size="sm" disabled={resuming} onClick={onResume}>
              {resuming ? 'Resuming…' : 'Resume'}
            </Button>
          )}
        </div>
      )}

      {/* Implemented but not delivered: the ONE grip that ships it — the chain then walks only the
          missing part of the way, it never re-implements what is already there (REQ-020.4). */}
      {undelivered && onDeliver && (
        <div className="mt-2 flex flex-wrap items-center gap-2 rounded-md bg-warning/10 px-2.5 py-1.5">
          <span className="min-w-0 flex-1 text-caption text-warning">The work is in the repository but has not been delivered.</span>
          <Button variant="primary" size="sm" disabled={delivering} onClick={onDeliver}>
            {delivering ? 'Delivering…' : 'Deliver now'}
          </Button>
        </div>
      )}

      {children}
    </div>
  );
}

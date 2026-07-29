// ExecutionDetail — the ONE detail of one execution, shared by the history AND the calendar
// (REQ-012: a past run opens at the same depth wherever it is clicked, through the same access
// point — no second data path). It reads one result document and renders it: who asked for it, the
// prompt it was driven by (REQ-037.4), its report as sanitized Markdown (REQ-038), its consumption,
// and per repository the SERVER's stage array (B-17/B-35).
//
// Nothing here can black out: a document that cannot be loaded says so, an empty one says so, an
// unknown stage state still gets a defined badge — every entry of the history renders a defined
// state (REQ-037.5).
import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { getDataSource } from '@/data';
import { cn } from '@/lib/cn';
import { fmtDateTime, fmtTime } from '@/lib/format';
import { renderMarkdown } from '@/lib/markdown';
import { ChevronRightIcon } from '@/ui/icons';
import { Person } from '@/ui/Person';
import { useLiveTopic } from '@/state/live';
import type { Authorship, RunResult, StageView } from '@/types';
import { RepoPipelineCard, StageDetail, StageLegend, TonePill } from './PipelineStages';
import { UsageBadge } from './UsageBadge';
import { errMsg, executionOutcome, provenanceChips, runningStage } from './logic';

export interface ExecutionDetailProps {
  runId: string;
  resultId: string;
  /** Drop the title block and the page padding — for embedding under chrome that already carries
   *  them (the calendar's modal). */
  hideHeader?: boolean;
  /** Deliver already-implemented work of one repository in one grip (REQ-020.4). */
  onDeliver?: (repo: string) => void;
  /** Resume the execution from a blocked repository's own row (REQ-032.4). */
  onResume?: (repo: string) => void;
  /** An action of this document is in flight. */
  busy?: boolean;
}

export function ExecutionDetail({ runId, resultId, hideHeader, onDeliver, onResume, busy }: ExecutionDetailProps) {
  const source = useMemo(() => getDataSource(), []);
  const [res, setRes] = useState<RunResult | null>(null);
  const [failed, setFailed] = useState<string | null>(null);

  const gotRef = useRef(false);

  const load = useCallback(async () => {
    try {
      setRes(await source.mercuryRunResult(runId, resultId));
      gotRef.current = true;
      setFailed(null);
    } catch (e) {
      // Only surface the failure while nothing is on screen — a transient tick must not blank a
      // document the viewer is already reading.
      if (!gotRef.current) setFailed(errMsg(e));
    }
  }, [source, runId, resultId]);

  useEffect(() => {
    gotRef.current = false;
    setRes(null);
    setFailed(null);
    void load();
  }, [load]);

  // A running execution's stages, transcript and consumption arrive over the one live stream; a
  // merge stamps the document (B-8). No polling (REQ-034).
  useLiveTopic('progress', load);
  useLiveTopic('deliveries', load);

  if (failed) {
    return <p className={cn('text-footnote text-text-secondary', hideHeader ? '' : 'px-8 py-7')}>This execution could not be loaded: {failed}</p>;
  }
  if (!res) {
    return <p className={cn('text-footnote text-text-tertiary', hideHeader ? '' : 'px-8 py-7')}>Loading…</p>;
  }
  return <ExecutionDetailBody res={res} hideHeader={hideHeader} onDeliver={onDeliver} onResume={onResume} busy={busy} />;
}

/** The rendered document — split from the read so the loading and failure states stay trivial. */
function ExecutionDetailBody({
  res,
  hideHeader,
  onDeliver,
  onResume,
  busy,
}: {
  res: RunResult;
  hideHeader?: boolean;
  onDeliver?: (repo: string) => void;
  onResume?: (repo: string) => void;
  busy?: boolean;
}) {
  const outcome = executionOutcome(res);
  // One open stage at a time, keyed by repo — while a stage runs its row follows the agent.
  const [open, setOpen] = useState<{ repo: string; index: number } | null>(null);
  const chips = provenanceChips(res);

  return (
    <article className={cn('mx-auto max-w-4xl', hideHeader ? '' : 'px-8 py-7')}>
      {!hideHeader && (
        <div className="flex items-start justify-between gap-4">
          <div className="min-w-0">
            <h1 className="text-title3 font-semibold tracking-tight text-text-primary">{res.runTitle || 'Execution'}</h1>
            <p className="mt-1 text-footnote text-text-secondary">
              {fmtDateTime(res.startedAt)}
              {res.endedAt ? ` – ${fmtTime(res.endedAt)}` : ''}
            </p>
            <RequestedBy requested={res.requested} className="mt-1.5" />
          </div>
          <TonePill label={outcome.label} tone={outcome.tone} pulse={outcome.pulse} />
        </div>
      )}

      <div className={cn('flex flex-wrap items-center gap-x-4 gap-y-1 rounded-card border border-separator bg-surface px-3 py-2', hideHeader ? '' : 'mt-4')}>
        {hideHeader && <TonePill label={outcome.label} tone={outcome.tone} pulse={outcome.pulse} />}
        {/* Every AI answer names its model (Holistic labelling duty). */}
        {res.model && <span className="text-caption font-medium text-text-secondary">{res.model}</span>}
        {res.mergedAt && <span className="text-caption text-text-tertiary">merged {fmtDateTime(res.mergedAt)}</span>}
        {chips.map((c) => (
          <span key={c} className="rounded bg-fill/15 px-1.5 py-0.5 text-caption text-text-tertiary">
            {c}
          </span>
        ))}
        <UsageBadge usage={res.usage} />
      </div>

      {res.prompt && <PromptDisclosure prompt={res.prompt} className="mt-4" />}

      {res.report && (
        <section className="mt-4">
          <p className="mb-1.5 text-caption font-semibold uppercase tracking-wide text-text-tertiary">Report</p>
          <div
            className="dl-markdown dl-scroll max-h-[28rem] overflow-auto rounded-card border border-separator bg-surface p-3 text-footnote text-text-secondary"
            dangerouslySetInnerHTML={{ __html: renderMarkdown(res.report) }}
          />
        </section>
      )}

      <section className="mt-5">
        <div className="mb-2 flex flex-wrap items-center justify-between gap-2">
          <p className="text-caption font-semibold uppercase tracking-wide text-text-tertiary">
            Repositories ({res.repos.length})
          </p>
          {res.repos.length > 0 && <StageLegend />}
        </div>
        {res.repos.length === 0 ? (
          <p className="text-footnote text-text-tertiary">This execution touched no repository.</p>
        ) : (
          <div className="flex flex-col gap-2">
            {res.repos.map((rp, i) => {
              const live = runningStage(rp);
              const liveIndex = live ? rp.stages.indexOf(live) : -1;
              // The running stage is what one wants to see; the viewer's own pick wins over it.
              const index = open?.repo === rp.repo ? open.index : liveIndex;
              const stage: StageView | undefined = index >= 0 ? rp.stages[index] : undefined;
              return (
                <RepoPipelineCard
                  key={`${rp.repo}:${i}`}
                  pipeline={rp}
                  selectedStage={index >= 0 ? index : undefined}
                  onSelectStage={(next) => setOpen((cur) => (cur?.repo === rp.repo && cur.index === next ? null : { repo: rp.repo, index: next }))}
                  onResume={onResume && rp.block ? () => onResume(rp.repo) : undefined}
                  resuming={busy}
                  onDeliver={onDeliver ? () => onDeliver(rp.repo) : undefined}
                  delivering={busy}
                >
                  {stage && <StageDetail repo={rp.repo} stage={stage} />}
                </RepoPipelineCard>
              );
            })}
          </div>
        )}
      </section>
    </article>
  );
}

/** Who asked for this execution — through the ONE Person component, so a human caller, the
 *  autonomous runner and an author-less archive record render uniformly (REQ-041). An autonomous
 *  run still names the person it acted for. Takes the authorship itself, so a stored result and a
 *  live execution view render it identically. */
export function RequestedBy({ requested, className }: { requested?: Authorship; className?: string }) {
  const actor = requested?.created;
  if (!actor) return null;
  return (
    <span className={cn('flex flex-wrap items-center gap-x-1.5 gap-y-0.5 text-caption text-text-tertiary', className)}>
      <Person username={actor.user} autonomous={actor.autonomous} autonomousLabel="Autonomous run" size="sm" />
      {actor.autonomous && actor.onBehalfOf && (
        <>
          <span aria-hidden>· for</span>
          <Person username={actor.onBehalfOf} size="sm" />
        </>
      )}
    </span>
  );
}

/** The exact prompt this execution was driven by, snapshotted at start (REQ-037.4). Collapsed:
 *  an automatic run folds the whole constitution in, so it stays portioned until asked for. */
function PromptDisclosure({ prompt, className }: { prompt: string; className?: string }) {
  const [open, setOpen] = useState(false);
  return (
    <div className={cn('rounded-card border border-separator bg-surface', className)}>
      <button
        type="button"
        onClick={() => setOpen((o) => !o)}
        className="flex w-full items-center gap-2 rounded-card px-3 py-2 text-left transition hover:bg-fill/10"
      >
        <ChevronRightIcon className={cn('h-3.5 w-3.5 shrink-0 text-text-tertiary transition-transform duration-fast', open && 'rotate-90')} />
        <span className="flex-1 truncate text-footnote font-medium text-text-secondary">Prompt</span>
      </button>
      {open && (
        <pre className="dl-scroll mx-3 mb-3 max-h-80 overflow-auto whitespace-pre-wrap rounded-md border border-separator bg-bg-base p-3 font-mono text-caption text-text-secondary">
          {prompt}
        </pre>
      )}
    </div>
  );
}

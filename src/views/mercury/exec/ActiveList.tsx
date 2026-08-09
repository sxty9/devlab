// ActiveList — everything the system is working on right now, as a LIST (REQ-013.1/036): never "the
// active run" in the singular. Each row names its repository progress, its current stage and its
// consumption; opening one drops into that execution live — the moving stage array with the agent's
// transcript, the way one watches a Claude-Code session. Watching is therefore not a separate tool:
// it is this pipeline, in motion.
//
// The slot overview (S7) sits above the list, so places, set-aside work and overloads are visible at
// a glance. Every dataset is read once on mount and then refreshed by the ONE live stream — there is
// no polling anywhere on this surface (REQ-034), and a resting view causes no requests at all.
import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { getDataSource } from '@/data';
import { cn } from '@/lib/cn';
import { fmtDateTime } from '@/lib/format';
import { Button } from '@/ui/Button';
import { ErrorBoundary } from '@/ui/ErrorBoundary';
import { useToast } from '@/ui/Toast';
import { useLiveTopic } from '@/state/live';
import type { ExecutionView, RestartState, SlotOverview } from '@/types';
import { RequestedBy } from './ExecutionDetail';
import { RepoPipelineCard, StageDetail, StageLegend, TonePill } from './PipelineStages';
import { SlotPanel } from './SlotPanel';
import { UsageBadge } from './UsageBadge';
import { errMsg, fmtSince, pauseSummary, phaseBadge, repoProgress, runningStage, stageLabel, stagesOf } from './logic';

// The watched execution survives a browser reload: the same view, the same session (REQ-035).
const WATCH_KEY = 'mercury.active.watch';

function readWatch(): string | null {
  try {
    return window.localStorage.getItem(WATCH_KEY);
  } catch {
    return null; // storage unavailable (private mode) — the list still works
  }
}
function writeWatch(id: string | null) {
  try {
    if (id) window.localStorage.setItem(WATCH_KEY, id);
    else window.localStorage.removeItem(WATCH_KEY);
  } catch {
    /* best-effort persistence */
  }
}

export function ActiveList() {
  const source = useMemo(() => getDataSource(), []);
  const { toast } = useToast();

  const [active, setActive] = useState<ExecutionView[] | null>(null);
  const [restart, setRestart] = useState<RestartState | null>(null);
  const [slots, setSlots] = useState<SlotOverview | null>(null);
  const [failed, setFailed] = useState<string | null>(null);
  const [selId, setSelId] = useState<string | null>(readWatch);
  const [busyRunId, setBusyRunId] = useState<string | null>(null);

  const gotRef = useRef(false);

  const loadActive = useCallback(async () => {
    try {
      const r = await source.mercuryRunActive();
      setActive(r.active);
      setRestart(r.restart);
      gotRef.current = true;
      setFailed(null);
    } catch (e) {
      // A transient tick never blanks a list that is already on screen.
      if (!gotRef.current) setFailed(errMsg(e));
    }
  }, [source]);

  const loadSlots = useCallback(async () => {
    try {
      setSlots(await source.mercurySlots());
    } catch {
      /* keep the last good projection */
    }
  }, [source]);

  useEffect(() => {
    void loadActive();
    void loadSlots();
  }, [loadActive, loadSlots]);

  // The four topics this surface lives on: transitions, the floor, stage/transcript progress and
  // the restart gate. Publishers are the scheduler, the recorder and the handlers (S12).
  useLiveTopic('active', loadActive);
  useLiveTopic('slots', loadSlots);
  useLiveTopic('progress', loadActive);
  useLiveTopic('restart', () => {
    void loadActive();
    void loadSlots();
  });

  useEffect(() => writeWatch(selId), [selId]);

  const act = useCallback(
    async (runId: string, what: 'cancel' | 'defer' | 'resume') => {
      setBusyRunId(runId);
      try {
        if (what === 'cancel') await source.mercuryCancelRun(runId);
        else if (what === 'defer') await source.mercuryDeferRun(runId);
        else await source.mercuryResumeRun(runId);
        toast({ title: what === 'cancel' ? 'Execution aborted' : what === 'defer' ? 'Set aside' : 'Resumed', variant: 'success' });
        await Promise.all([loadActive(), loadSlots()]);
      } catch (e) {
        toast({ title: 'Action failed', description: errMsg(e), variant: 'danger' });
      } finally {
        setBusyRunId(null);
      }
    },
    [source, toast, loadActive, loadSlots],
  );

  if (failed) {
    return (
      <div className="flex h-full min-h-0 w-full items-center justify-center bg-bg-base px-6">
        <p className="max-w-md text-center text-footnote text-text-secondary">{failed}</p>
      </div>
    );
  }
  if (active === null) {
    return (
      <div className="flex h-full min-h-0 w-full items-center justify-center bg-bg-base">
        <p className="text-footnote text-text-tertiary">Loading…</p>
      </div>
    );
  }

  const selected = active.find((v) => v.id === selId) ?? null;

  return (
    <div className="flex h-full min-h-0 w-full">
      {/* LEFT — the floor and everything on it. */}
      <div className="flex w-96 shrink-0 flex-col border-r border-separator bg-surface">
        <div className="border-b border-separator p-3">
          <SlotPanel overview={slots} onResume={(runId) => void act(runId, 'resume')} busyRunId={busyRunId} />
        </div>
        <div className="dl-scroll flex-1 overflow-y-auto p-1.5">
          {active.length === 0 ? (
            <p className="px-2.5 py-3 text-caption text-text-tertiary">
              {restart?.pending ? 'A restart is pending — queued starts fire afterwards.' : 'Nothing is executing right now.'}
            </p>
          ) : (
            <div className="flex flex-col gap-1">
              {active.map((v) => (
                <ActiveRow
                  key={v.id}
                  view={v}
                  selected={v.id === selId}
                  onSelect={() => setSelId(v.id === selId ? null : v.id)}
                />
              ))}
            </div>
          )}
        </div>
      </div>

      {/* RIGHT — the focused execution as a live session. A single broken record must not take the
          surface down: it is contained to this pane and recovers when another row is picked. */}
      <div className="dl-scroll min-h-0 flex-1 overflow-y-auto bg-bg-base">
        <ErrorBoundary resetKeys={[selId]}>
          {selected ? (
            <LiveSession
              key={selected.id}
              view={selected}
              busy={busyRunId === selected.runId}
              onAct={(what) => void act(selected.runId, what)}
            />
          ) : (
            <div className="flex h-full min-h-0 items-center justify-center p-8">
              <p className="max-w-md text-center text-footnote text-text-tertiary">
                {active.length === 0 ? 'Nothing is executing right now.' : 'Pick an execution on the left to follow it live.'}
              </p>
            </div>
          )}
        </ErrorBoundary>
      </div>
    </div>
  );
}

/** One row of the list: phase, run, kind, the portioned live detail (repo progress and the stage in
 *  work; for a paused one its reason and resume attempts) and its running consumption. */
function ActiveRow({ view, selected, onSelect }: { view: ExecutionView; selected: boolean; onSelect: () => void }) {
  const badge = phaseBadge(view.phase);
  const repo = view.repos.find((rp) => runningStage(rp)) ?? null;
  const stage = repo ? runningStage(repo) : null;
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
        <TonePill label={badge.label} tone={badge.tone} pulse={badge.pulse} />
        <span className={cn('min-w-0 flex-1 truncate text-footnote font-medium', selected ? 'text-text-primary' : 'text-text-secondary')}>
          {view.runTitle}
        </span>
        <span className="shrink-0 rounded bg-fill/15 px-1.5 py-0.5 text-caption font-medium text-text-tertiary">
          {view.kind === 'todo' ? 'Todo' : 'Auto'}
        </span>
        {view.overload && <TonePill label="overload" tone="warning" />}
      </div>
      <span className="truncate text-caption text-text-tertiary">
        {view.pause
          ? pauseSummary(view.pause)
          : `${repoProgress(view)}${repo ? ` · ${repo.repo}` : ''}${stage ? ` · ${stageLabel(String(stage.stage))}` : ''} · ${fmtSince(view.startedAt || view.createdAt)} ago`}
      </span>
      <UsageBadge usage={view.usage} />
    </button>
  );
}

/** The focused execution as a live session: its title, the actions that address exactly THIS
 *  execution (abort it, set it aside, resume it), and its moving stage array — rendered by the very
 *  same components the stored history renders (one execution kit, B-34). The stages come with the
 *  execution view itself, so following a run opens no second channel and starts no poll. */
function LiveSession({ view, busy, onAct }: { view: ExecutionView; busy: boolean; onAct: (what: 'cancel' | 'defer' | 'resume') => void }) {
  const badge = phaseBadge(view.phase);
  const running = view.phase === 'running';
  const resumable = view.phase === 'paused' || view.phase === 'blocked' || view.phase === 'interrupted';
  // One open stage at a time; while a stage runs its row follows the agent by itself.
  const [open, setOpen] = useState<{ repo: string; index: number } | null>(null);
  return (
    <article className="mx-auto max-w-4xl px-8 py-7">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div className="min-w-0">
          <h1 className="text-title3 font-semibold tracking-tight text-text-primary">{view.runTitle}</h1>
          <p className="mt-1 flex flex-wrap items-center gap-2 text-footnote text-text-secondary">
            <TonePill label={badge.label} tone={badge.tone} pulse={badge.pulse} />
            <span>{repoProgress(view)}</span>
            <span className="text-text-tertiary">since {fmtDateTime(view.startedAt || view.createdAt)}</span>
          </p>
          {view.reason && <p className="mt-1 text-caption text-text-tertiary">{view.reason}</p>}
          {view.pause && <p className="mt-1 text-caption text-text-tertiary">{pauseSummary(view.pause)}</p>}
          {view.continuation && (
            <p className="mt-1 font-mono text-caption text-text-tertiary">
              Continues at {view.continuation.repo} · {view.continuation.stage}
            </p>
          )}
        </div>
        <div className="flex shrink-0 flex-wrap items-center justify-end gap-1.5">
          {running && (
            <Button variant="secondary" size="sm" disabled={busy} onClick={() => onAct('defer')}>
              Set aside
            </Button>
          )}
          {resumable && (
            <Button variant="primary" size="sm" disabled={busy} onClick={() => onAct('resume')}>
              Resume
            </Button>
          )}
          {running && (
            <Button variant="danger" size="sm" disabled={busy} onClick={() => onAct('cancel')}>
              {busy ? 'Aborting…' : 'Abort'}
            </Button>
          )}
        </div>
      </div>

      <div className="mt-4 flex flex-wrap items-center gap-x-4 gap-y-1 rounded-card border border-separator bg-surface px-3 py-2">
        <RequestedBy requested={view.requested} />
        <UsageBadge usage={view.usage} />
      </div>

      <section className="mt-5">
        <div className="mb-2 flex flex-wrap items-center justify-between gap-2">
          <p className="text-caption font-semibold uppercase tracking-wide text-text-tertiary">Repositories ({view.repos.length})</p>
          {view.repos.length > 0 && <StageLegend />}
        </div>
        {view.repos.length === 0 ? (
          <p className="text-footnote text-text-tertiary">The execution is being prepared…</p>
        ) : (
          <div className="flex flex-col gap-2">
            {view.repos.map((rp, i) => {
              const live = runningStage(rp);
              const liveIndex = live ? stagesOf(rp).indexOf(live) : -1;
              const index = open?.repo === rp.repo ? open.index : liveIndex;
              const stage = index >= 0 ? rp.stages[index] : undefined;
              return (
                <RepoPipelineCard
                  key={`${rp.repo}:${i}`}
                  pipeline={rp}
                  selectedStage={index >= 0 ? index : undefined}
                  onSelectStage={(next) => setOpen((cur) => (cur?.repo === rp.repo && cur.index === next ? null : { repo: rp.repo, index: next }))}
                  onResume={rp.block ? () => onAct('resume') : undefined}
                  resuming={busy}
                >
                  {stage && <StageDetail repo={rp.repo} stage={stage} runId={view.runId} resultId={view.id} />}
                </RepoPipelineCard>
              );
            })}
          </div>
        )}
      </section>
    </article>
  );
}

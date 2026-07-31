// StartDialog — the start decision on a contended floor (REQ-015, C F3), plus useRunStart: the ONE
// start flow of this surface.
//
// Every branch of the server's start outcome is answered honestly: started, resumed, queued behind
// a pending restart, refused with a reason (fully delivered work does not start at all, K-3) — and
// the one branch a decision can still resolve opens this dialog: queue, set one running execution
// aside (with the server's reasoned suggestion, which the user may overrule — REQ-015.4), or
// temporarily overload. Overload is offered only where the server would grant it: one extra place
// at most, never across repository exclusivity — and a refusal on that ground comes back named.
import { useCallback, useMemo, useState, type ReactNode } from 'react';
import { getDataSource } from '@/data';
import type { StartPlacement } from '@/data/source';
import { Modal } from '@/ui/Modal';
import { Button } from '@/ui/Button';
import { useToast } from '@/ui/Toast';
import { cn } from '@/lib/cn';
import type { ExecutionView, SlotOverview, StartOutcome } from '@/types';
import { ResumeDialog } from './ResumeDialog';
import { TonePill } from './PipelineStages';
import { classifyStartOutcome, errMsg, fmtSince, placementOptions, repoProgress } from './logic';

/** The run a start addresses. */
export interface StartTarget {
  id: string;
  title?: string;
}

export function StartDialog({
  open,
  target,
  reason,
  overview,
  suggestionId,
  suggestionReason,
  busy,
  onClose,
  onChoose,
}: {
  open: boolean;
  target: StartTarget | null;
  /** The server's own words for why the start did not go through. */
  reason?: string;
  overview: SlotOverview | null;
  /** The execution the server suggests setting aside, with its justification. */
  suggestionId?: string;
  suggestionReason?: string;
  busy?: boolean;
  onClose: () => void;
  onChoose: (placement: StartPlacement) => void;
}) {
  const options = placementOptions(overview);
  const running = overview?.active.filter((a) => a.phase === 'running') ?? [];
  const [deferId, setDeferId] = useState<string>('');
  const chosenDefer = deferId || suggestionId || running[0]?.id || '';

  if (!target) return null;
  // The block is either a full floor or a busy repository — the server's own reason (shown first
  // inside) says which, so the title states only what holds for both.
  return (
    <Modal
      open={open}
      onClose={onClose}
      title="The start needs a decision"
      description={target.title}
      size="lg"
      footer={
        <Button variant="ghost" size="sm" onClick={onClose}>
          Cancel
        </Button>
      }
    >
      <div className="flex flex-col gap-3">
        {reason && <p className="text-footnote text-text-secondary">{reason}</p>}

        <Choice
          title="Queue it"
          detail="The execution is recorded now and starts by itself as soon as a place frees up."
          action="Queue"
          busy={busy}
          onChoose={() => onChoose({ kind: 'queue' })}
        />

        {options.defer && (
          <Choice
            title="Set a running execution aside"
            detail="Its place frees up and this start takes it. The set-aside execution keeps its progress and continues later at the same spot."
            action="Set aside & start"
            busy={busy}
            onChoose={() => onChoose({ kind: 'defer', deferExecutionId: chosenDefer })}
          >
            <ul className="mt-2 flex flex-col gap-1">
              {running.map((v) => (
                <li key={v.id}>
                  <label
                    className={cn(
                      'flex cursor-pointer flex-wrap items-center gap-2 rounded-md border px-2.5 py-1.5 transition',
                      chosenDefer === v.id ? 'border-accent/50 bg-accent/[0.07]' : 'border-separator bg-surface hover:bg-fill/10',
                    )}
                  >
                    <input
                      type="radio"
                      name="defer-target"
                      className="accent-accent"
                      checked={chosenDefer === v.id}
                      onChange={() => setDeferId(v.id)}
                    />
                    <span className="min-w-0 flex-1 truncate text-footnote text-text-secondary">{v.runTitle}</span>
                    <span className="text-caption text-text-tertiary">
                      {repoProgress(v)} · {fmtSince(v.startedAt || v.createdAt)} ago
                    </span>
                    {v.id === suggestionId && <TonePill label="suggested" tone="accent" />}
                  </label>
                  {v.id === suggestionId && suggestionReason && (
                    <p className="mt-0.5 pl-6 text-caption text-text-tertiary">{suggestionReason}</p>
                  )}
                </li>
              ))}
            </ul>
          </Choice>
        )}

        {options.overload && (
          <Choice
            title="Overload once"
            detail="Grants ONE temporary extra place for this start. It disappears when the execution ends, it never adds up, and it never overrides repository exclusivity."
            action="Overload & start"
            busy={busy}
            onChoose={() => onChoose({ kind: 'overload' })}
          />
        )}
      </div>
    </Modal>
  );
}

/** One decision: what it does, what follows from it, and the button that takes it. */
function Choice({
  title,
  detail,
  action,
  busy,
  onChoose,
  children,
}: {
  title: string;
  detail: string;
  action: string;
  busy?: boolean;
  onChoose: () => void;
  children?: ReactNode;
}) {
  return (
    <div className="rounded-card border border-separator bg-surface p-3">
      <div className="flex flex-wrap items-start justify-between gap-2">
        <div className="min-w-0">
          <p className="text-footnote font-medium text-text-primary">{title}</p>
          <p className="mt-0.5 text-caption text-text-secondary">{detail}</p>
        </div>
        <Button variant="primary" size="sm" disabled={busy} onClick={onChoose}>
          {action}
        </Button>
      </div>
      {children}
    </div>
  );
}

/** The ONE start flow: it drives resume-vs-fresh, the slot decision and the honest reporting of
 *  every outcome, so no surface re-implements a start. `render()` mounts both dialogs. */
export interface RunStartFlow {
  /** Start (or continue) one run. Resumable work asks first; a contended floor asks after. */
  start: (target: StartTarget) => Promise<void>;
  busyRunId: string | null;
  render: () => ReactNode;
}

export function useRunStart(onStarted?: () => void): RunStartFlow {
  const source = useMemo(() => getDataSource(), []);
  const { toast } = useToast();
  const [busyRunId, setBusyRunId] = useState<string | null>(null);
  const [resume, setResume] = useState<{ target: StartTarget; execution: ExecutionView } | null>(null);
  const [contended, setContended] = useState<{ target: StartTarget; reason?: string; overview: SlotOverview | null; outcome: StartOutcome } | null>(null);

  const submit = useCallback(
    async (target: StartTarget, opts?: { placement?: StartPlacement; fresh?: boolean }) => {
      setBusyRunId(target.id);
      try {
        const outcome = await source.mercuryRunNow(target.id, opts);
        const verdict = classifyStartOutcome(outcome);
        if (verdict.kind === 'contended') {
          // The decision the floor still allows — offered with the server's own projection.
          let overview: SlotOverview | null = null;
          try {
            overview = await source.mercurySlots();
          } catch {
            /* the dialog still offers queueing without the projection */
          }
          setResume(null);
          setContended({ target, reason: verdict.detail, overview, outcome });
          return;
        }
        setResume(null);
        setContended(null);
        toast({
          title: verdict.title,
          description: verdict.detail,
          variant: verdict.kind === 'started' || verdict.kind === 'resumed' ? 'success' : 'default',
        });
        onStarted?.();
      } catch (e) {
        toast({ title: 'Start failed', description: errMsg(e), variant: 'danger' });
      } finally {
        setBusyRunId(null);
      }
    },
    [source, toast, onStarted],
  );

  const start = useCallback(
    async (target: StartTarget) => {
      setBusyRunId(target.id);
      let live: ExecutionView | undefined;
      try {
        const { active } = await source.mercuryRunActive();
        live = active.find((v) => v.runId === target.id);
      } catch {
        /* without the active list the plain start still answers honestly */
      } finally {
        setBusyRunId(null);
      }
      // Work that is still alive but not progressing is a decision, never a silent overwrite.
      if (live && (live.phase === 'paused' || live.phase === 'blocked' || live.phase === 'interrupted')) {
        setResume({ target, execution: live });
        return;
      }
      await submit(target);
    },
    [source, submit],
  );

  const render = useCallback(
    () => (
      <>
        <ResumeDialog
          open={!!resume}
          execution={resume?.execution ?? null}
          runTitle={resume?.target.title}
          busy={!!busyRunId}
          onClose={() => setResume(null)}
          onChoose={(fresh) => {
            const target = resume?.target;
            if (target) void submit(target, { fresh });
          }}
        />
        <StartDialog
          open={!!contended}
          target={contended?.target ?? null}
          reason={contended?.reason}
          overview={contended?.overview ?? null}
          suggestionId={contended?.outcome.suggestion?.executionId}
          suggestionReason={contended?.outcome.suggestion?.reason}
          busy={!!busyRunId}
          onClose={() => setContended(null)}
          onChoose={(placement) => {
            const target = contended?.target;
            if (target) void submit(target, { placement });
          }}
        />
      </>
    ),
    [resume, contended, busyRunId, submit],
  );

  return { start, busyRunId, render };
}

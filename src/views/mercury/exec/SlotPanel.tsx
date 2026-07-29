// SlotPanel — the slot overview at a glance (S7/F3): how many places exist and how many are
// taken, whether a temporary overload is alive, what is set aside and exactly where it would
// continue, and which starts are queued behind a pending restart.
//
// It is a read-only projection of the server's own SlotOverview — the panel counts nothing itself.
// Every set-aside execution offers its resumption right here (REQ-015/040.1), and a paused one
// states its reason and its resume attempts rather than a bare "paused" (REQ-016.2).
import { cn } from '@/lib/cn';
import { Button } from '@/ui/Button';
import { fmtDateTime } from '@/lib/format';
import { Person } from '@/ui/Person';
import type { ExecutionView, SlotOverview } from '@/types';
import { TonePill } from './PipelineStages';
import { fmtUntil, pauseSummary, phaseBadge } from './logic';

export function SlotPanel({
  overview,
  onResume,
  busyRunId,
  className,
}: {
  overview: SlotOverview | null;
  /** Resume one set-aside execution (its run id — the resumption access point). */
  onResume?: (runId: string) => void;
  busyRunId?: string | null;
  className?: string;
}) {
  if (!overview) {
    return <p className={cn('text-caption text-text-tertiary', className)}>Loading slots…</p>;
  }
  const free = Math.max(0, overview.capacity - overview.occupied);
  return (
    <div className={cn('flex flex-col gap-2', className)}>
      <div className="flex flex-wrap items-center gap-2">
        <span className="text-caption font-semibold uppercase tracking-wide text-text-tertiary">Slots</span>
        <span className="text-footnote text-text-secondary">
          {overview.occupied} of {overview.capacity} occupied · {free} free
        </span>
        {overview.overloadActive && <TonePill label="overloaded" tone="warning" />}
        {overview.restartPending && <TonePill label="restart pending" tone="accent" pulse />}
      </div>

      {/* The places are the portioned truth of the floor: one mark per place, taken or free. */}
      <div className="flex flex-wrap gap-1">
        {Array.from({ length: Math.max(overview.capacity, overview.occupied) }, (_, i) => (
          <span
            key={i}
            className={cn('h-2.5 w-6 rounded-sm', i < overview.occupied ? 'bg-warning' : 'bg-fill/15', i >= overview.capacity && 'ring-1 ring-warning/60')}
          />
        ))}
      </div>

      {/* A pending restart lets running work drain and holds new starts — they fire by themselves
          afterwards, and the caller was told so at the time (REQ-018, F13). */}
      {overview.restartPending && (
        <p className="rounded-md bg-accent/10 px-2.5 py-1.5 text-caption text-accent">
          A restart is pending: running executions drain, new starts are queued and fire afterwards.
        </p>
      )}

      {overview.queuedStarts.length > 0 && (
        <div>
          <p className="mb-1 text-caption font-semibold uppercase tracking-wide text-text-tertiary">
            Queued starts ({overview.queuedStarts.length})
          </p>
          <ul className="flex flex-col gap-1">
            {overview.queuedStarts.map((q, i) => (
              <li key={`${q.runId}:${i}`} className="flex flex-wrap items-center gap-x-2 gap-y-0.5 text-caption text-text-secondary">
                <span className="min-w-0 truncate">{q.title || q.runId}</span>
                <Person username={q.by.user} autonomous={q.by.autonomous} size="sm" />
                <span className="text-text-tertiary">{fmtDateTime(q.at)}</span>
              </li>
            ))}
          </ul>
        </div>
      )}

      {overview.deferred.length > 0 && (
        <div>
          <p className="mb-1 text-caption font-semibold uppercase tracking-wide text-text-tertiary">
            Set aside ({overview.deferred.length})
          </p>
          <ul className="flex flex-col gap-1">
            {overview.deferred.map((v) => (
              <DeferredRow key={v.id} view={v} onResume={onResume} busy={busyRunId === v.runId} />
            ))}
          </ul>
        </div>
      )}
    </div>
  );
}

/** One set-aside execution: its reason, its resume attempts, the exact continuation point — and
 *  its resumption. Progress is kept, so continuing picks up where it stopped (REQ-015.1). */
function DeferredRow({ view, onResume, busy }: { view: ExecutionView; onResume?: (runId: string) => void; busy?: boolean }) {
  const badge = phaseBadge(view.phase);
  return (
    <li className="flex flex-wrap items-center gap-2 rounded-md border border-separator bg-surface px-2.5 py-1.5">
      <TonePill label={badge.label} tone={badge.tone} />
      <span className="min-w-0 flex-1 truncate text-footnote text-text-secondary">{view.runTitle}</span>
      {view.pause && <span className="text-caption text-text-tertiary">{pauseSummary(view.pause)}</span>}
      {view.pause?.notBefore && <span className="text-caption text-text-tertiary">resumes {fmtUntil(view.pause.notBefore)}</span>}
      {view.continuation && (
        <span className="font-mono text-caption text-text-tertiary">
          continues at {view.continuation.repo} · {view.continuation.stage}
        </span>
      )}
      {onResume && (
        <Button variant="secondary" size="sm" disabled={busy} onClick={() => onResume(view.runId)}>
          {busy ? 'Resuming…' : 'Resume'}
        </Button>
      )}
    </li>
  );
}

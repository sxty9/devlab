// ResumeDialog — the resume-vs-fresh decision (C F1, REQ-019). A run whose previous execution was
// set aside, blocked or cut short by a process death still holds its work state: an existing pull
// request is adopted, finished repositories stay finished. Continuing is therefore the default; a
// fresh start is the explicit, named alternative — and it marks the previous execution DISCARDED
// visibly, it never disappears silently.
import { Modal } from '@/ui/Modal';
import { Button } from '@/ui/Button';
import { fmtDateTime } from '@/lib/format';
import type { ExecutionView } from '@/types';
import { TonePill } from './PipelineStages';
import { fmtSince, pauseSummary, phaseBadge, repoProgress } from './logic';

export function ResumeDialog({
  open,
  execution,
  runTitle,
  busy,
  onClose,
  onChoose,
}: {
  open: boolean;
  /** The execution that is still alive — the thing that would be continued. */
  execution: ExecutionView | null;
  runTitle?: string;
  busy?: boolean;
  onClose: () => void;
  /** fresh=false continues under the SAME execution id; fresh=true discards it and starts anew. */
  onChoose: (fresh: boolean) => void;
}) {
  if (!execution) return null;
  const badge = phaseBadge(execution.phase);
  return (
    <Modal
      open={open}
      onClose={onClose}
      title="Continue or start fresh?"
      description={runTitle || execution.runTitle}
      size="lg"
      footer={
        <>
          <Button variant="ghost" size="sm" onClick={onClose}>
            Cancel
          </Button>
          <Button variant="secondary" size="sm" disabled={busy} onClick={() => onChoose(true)}>
            Start fresh
          </Button>
          <Button variant="primary" size="sm" disabled={busy} onClick={() => onChoose(false)}>
            Continue
          </Button>
        </>
      }
    >
      <div className="flex flex-col gap-3">
        <div className="flex flex-wrap items-center gap-2">
          <TonePill label={badge.label} tone={badge.tone} pulse={badge.pulse} />
          <span className="text-footnote text-text-secondary">{repoProgress(execution)}</span>
          <span className="text-caption text-text-tertiary">started {fmtDateTime(execution.startedAt || execution.createdAt)}</span>
          <span className="text-caption text-text-tertiary">· {fmtSince(execution.startedAt || execution.createdAt)} ago</span>
        </div>

        {execution.reason && <p className="text-footnote text-text-secondary">{execution.reason}</p>}
        {execution.pause && <p className="text-footnote text-text-secondary">{pauseSummary(execution.pause)}</p>}
        {execution.continuation && (
          <p className="font-mono text-caption text-text-tertiary">
            Continues at {execution.continuation.repo} · {execution.continuation.stage}
          </p>
        )}

        <dl className="flex flex-col gap-2 rounded-card border border-separator bg-surface p-3 text-footnote">
          <div>
            <dt className="font-medium text-text-primary">Continue</dt>
            <dd className="text-text-secondary">
              Keeps this execution and its id: finished repositories stay finished, an open pull request is adopted instead of duplicated.
            </dd>
          </div>
          <div>
            <dt className="font-medium text-text-primary">Start fresh</dt>
            <dd className="text-text-secondary">
              Marks this execution as discarded — visibly, in its own record — and begins a new one from the current state of the repositories.
              Work already published stays published.
            </dd>
          </div>
        </dl>
      </div>
    </Modal>
  );
}

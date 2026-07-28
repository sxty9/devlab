// RunsView — the automatic-runs surface: the shared TaskSurface (one machinery, REQ-005)
// instantiated for kind=auto, plus this kind's own toolbar — AI planning over the not-yet-
// covered axioms (fill), AI regrouping of the whole set (finetune, both reviewable before
// anything is written) and the restorable config history (B-32).
import { useEffect, useMemo, useState } from 'react';
import { getDataSource } from '@/data';
import { useToast } from '@/ui/Toast';
import { Button } from '@/ui/Button';
import { Modal } from '@/ui/Modal';
import { Person } from '@/ui/Person';
import { fmtDateTime } from '@/lib/format';
import { LightbulbIcon, RefreshIcon } from '@/ui/icons';
import { TaskSurface } from './Surface';
import { errMsg, scheduleSummary } from './logic';
import type { PlannedRun, RunProposal, RunSnapshotMeta } from '@/types';

export function RunsView() {
  const source = useMemo(() => getDataSource(), []);
  const { toast } = useToast();
  const [aiBusy, setAiBusy] = useState<'fill' | 'finetune' | null>(null);
  const [proposal, setProposal] = useState<{ mode: 'fill' | 'replace'; title: string; proposal: RunProposal } | null>(null);
  const [showHistory, setShowHistory] = useState(false);

  const plan = async (which: 'fill' | 'finetune') => {
    if (aiBusy) return;
    setAiBusy(which);
    try {
      if (which === 'fill') {
        setProposal({ mode: 'fill', title: 'AI proposal: cover the open axioms', proposal: await source.mercuryRunAiFill() });
      } else {
        setProposal({ mode: 'replace', title: 'AI proposal: regroup the runs', proposal: await source.mercuryRunAiFinetune() });
      }
    } catch (e) {
      toast({ title: 'AI analysis failed', description: errMsg(e), variant: 'danger' });
    } finally {
      setAiBusy(null);
    }
  };

  return (
    <TaskSurface
      kind="auto"
      newLabel="New run"
      emptyText="No runs yet. Create one, or let the AI fill the open axioms."
      toolbar={({ reload }) => (
        <>
          <div className="flex flex-wrap gap-1.5">
            <Button variant="secondary" size="sm" disabled={aiBusy !== null} onClick={() => void plan('fill')}>
              <LightbulbIcon className="h-4 w-4" /> {aiBusy === 'fill' ? 'Analyzing…' : 'AI fill'}
            </Button>
            <Button variant="secondary" size="sm" disabled={aiBusy !== null} onClick={() => void plan('finetune')}>
              <RefreshIcon className="h-4 w-4" /> {aiBusy === 'finetune' ? 'Analyzing…' : 'AI fine-tune'}
            </Button>
            <Button variant="ghost" size="sm" onClick={() => setShowHistory(true)}>
              History
            </Button>
          </div>
          {proposal && (
            <ProposalModal
              mode={proposal.mode}
              title={proposal.title}
              proposal={proposal.proposal}
              onClose={() => setProposal(null)}
              onApplied={async () => {
                await reload();
                setProposal(null);
              }}
            />
          )}
          {showHistory && (
            <HistoryModal
              onClose={() => setShowHistory(false)}
              onRestored={async () => {
                await reload();
                setShowHistory(false);
              }}
            />
          )}
        </>
      )}
    />
  );
}

/** Review + apply an AI proposal. `fill` extends the run set; `replace` swaps it for the plan.
 *  Nothing is written until the user applies (the proposal endpoints are read-only). */
function ProposalModal({
  mode,
  title,
  proposal,
  onClose,
  onApplied,
}: {
  mode: 'fill' | 'replace';
  title: string;
  proposal: RunProposal;
  onClose: () => void;
  onApplied: () => void | Promise<void>;
}) {
  const source = useMemo(() => getDataSource(), []);
  const { toast } = useToast();
  const [applying, setApplying] = useState(false);
  const runs = proposal.proposal.runs;

  const apply = async () => {
    if (applying) return;
    setApplying(true);
    try {
      await source.mercuryApplyRunProposal(mode, proposal.proposal);
      toast({ title: mode === 'fill' ? 'Runs filled' : 'Runs replaced', variant: 'success' });
      await onApplied();
    } catch (e) {
      toast({ title: 'Applying failed', description: errMsg(e), variant: 'danger' });
      setApplying(false);
    }
  };

  const axiomNames = (r: PlannedRun) => r.axiomIds.map((id) => proposal.axioms[id] ?? id);

  return (
    <Modal
      open
      onClose={onClose}
      title={title}
      description={`${runs.length} ${runs.length === 1 ? 'run' : 'runs'} proposed`}
      size="lg"
      footer={
        <>
          <Button variant="secondary" size="sm" disabled={applying} onClick={onClose}>
            Cancel
          </Button>
          <Button variant="primary" size="sm" disabled={applying || runs.length === 0} onClick={apply}>
            {applying ? 'Applying…' : mode === 'replace' ? 'Replace all runs' : 'Apply'}
          </Button>
        </>
      }
    >
      {runs.length === 0 ? (
        <p className="text-footnote text-text-secondary">The proposal contains no runs.</p>
      ) : (
        <ul className="flex flex-col gap-3">
          {runs.map((r, i) => {
            const names = axiomNames(r);
            return (
              <li key={i} className="rounded-card border border-separator bg-surface p-3">
                <div className="flex items-center justify-between gap-2">
                  <span className="truncate text-footnote font-medium text-text-primary">{r.name}</span>
                  <span className="shrink-0 text-caption text-text-tertiary">{scheduleSummary(r.schedule)}</span>
                </div>
                <p className="mt-1 text-caption text-text-secondary">
                  {r.axiomIds.length} {r.axiomIds.length === 1 ? 'axiom' : 'axioms'}: {names.slice(0, 3).join(', ')}
                  {names.length > 3 ? ` +${names.length - 3}` : ''}
                </p>
                {r.rationale && <p className="mt-1.5 text-caption text-text-tertiary">{r.rationale}</p>}
              </li>
            );
          })}
        </ul>
      )}
    </Modal>
  );
}

/** The run-config history: each mutation is a restorable snapshot (newest first), carrying its
 *  actor (REQ-041 through the one Person component). */
function HistoryModal({ onClose, onRestored }: { onClose: () => void; onRestored: () => void | Promise<void> }) {
  const source = useMemo(() => getDataSource(), []);
  const { toast } = useToast();
  const [snapshots, setSnapshots] = useState<RunSnapshotMeta[] | null>(null);
  const [restoring, setRestoring] = useState<string | null>(null);

  useEffect(() => {
    let cancelled = false;
    source
      .mercuryRunHistory()
      .then((r) => {
        if (!cancelled) setSnapshots([...r.snapshots].sort((a, b) => (a.ts < b.ts ? 1 : a.ts > b.ts ? -1 : 0)));
      })
      .catch((e) => {
        if (!cancelled) {
          toast({ title: 'Could not load the history', description: errMsg(e), variant: 'danger' });
          setSnapshots([]);
        }
      });
    return () => {
      cancelled = true;
    };
  }, [source, toast]);

  const restore = async (ts: string) => {
    if (restoring) return;
    setRestoring(ts);
    try {
      await source.mercuryRestoreRunHistory(ts);
      toast({ title: 'Configuration restored', variant: 'success' });
      await onRestored();
    } catch (e) {
      toast({ title: 'Restoring failed', description: errMsg(e), variant: 'danger' });
      setRestoring(null);
    }
  };

  return (
    <Modal open onClose={onClose} title="History" description="Earlier run configurations, restorable" size="lg">
      {snapshots === null ? (
        <p className="text-footnote text-text-tertiary">Loading…</p>
      ) : snapshots.length === 0 ? (
        <p className="text-footnote text-text-tertiary">No changes recorded yet.</p>
      ) : (
        <ul className="flex flex-col gap-2">
          {snapshots.map((s) => (
            <li key={s.ts} className="flex items-center gap-3 rounded-card border border-separator bg-surface p-3">
              <div className="min-w-0 flex-1">
                <p className="truncate text-footnote font-medium text-text-primary">{s.action}</p>
                <div className="mt-0.5 flex flex-wrap items-center gap-x-1.5 text-caption text-text-tertiary">
                  <span>{fmtDateTime(s.ts)}</span>
                  <span aria-hidden>·</span>
                  {/* Reuse the actor the history already carries; "autonomous" is its system marker. */}
                  <Person
                    username={s.actor === '?' || s.actor === 'autonomous' ? undefined : s.actor}
                    autonomous={s.actor === 'autonomous'}
                    size="sm"
                  />
                  <span aria-hidden>·</span>
                  <span>
                    {s.runCount} {s.runCount === 1 ? 'run' : 'runs'}
                  </span>
                </div>
              </div>
              <Button variant="secondary" size="sm" disabled={restoring !== null} onClick={() => restore(s.ts)}>
                {restoring === s.ts ? 'Restoring…' : 'Restore'}
              </Button>
            </li>
          ))}
        </ul>
      )}
    </Modal>
  );
}

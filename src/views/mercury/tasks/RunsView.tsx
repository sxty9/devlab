// RunsView — the automatic-runs surface: the shared TaskSurface (one machinery, REQ-005)
// instantiated for kind=auto, plus this kind's own toolbar — AI planning over the not-yet-
// covered axioms (fill), AI regrouping of the whole set (finetune, both reviewable before
// anything is written) and the restorable config history (B-32).
//
// The two AI accesses are never WAITED for: a plan over the whole constitution is minutes of model
// work, and a call that hangs on an open connection dies with it. They take the work on and answer
// at once; this surface reads the state back — when it mounts, after a reload and on every tick of
// the one live stream (REQ-034), never on a timer. So it says "analysing" only while work really
// is under way, shows a proposal only when one is really there, and names a failure instead of
// calling it "failed".
import { useCallback, useEffect, useMemo, useState } from 'react';
import { getDataSource } from '@/data';
import { useToast } from '@/ui/Toast';
import { Button } from '@/ui/Button';
import { Modal } from '@/ui/Modal';
import { Person } from '@/ui/Person';
import { fmtDateTime } from '@/lib/format';
import { useLiveTopic } from '@/state/live';
import { LightbulbIcon, RefreshIcon, XIcon } from '@/ui/icons';
import { TaskSurface } from './Surface';
import {
  accessProposal,
  anyProposalBusy,
  errMsg,
  presentProposal,
  readProposals,
  requestProposal,
  scheduleSummary,
  type ProposalStates,
} from './logic';
import type { PlannedRun, RunPlan, RunProposalKind, RunSnapshotMeta } from '@/types';

const NO_PROPOSALS: ProposalStates = { fill: null, finetune: null };

/** The mode each kind's proposal is applied in (fill extends the set, finetune replaces it). */
const APPLY_MODE: Record<RunProposalKind, 'fill' | 'replace'> = { fill: 'fill', finetune: 'replace' };

const REVIEW_TITLE: Record<RunProposalKind, string> = {
  fill: 'AI proposal: cover the open axioms',
  finetune: 'AI proposal: regroup the runs',
};

export function RunsView() {
  const source = useMemo(() => getDataSource(), []);
  const { toast } = useToast();
  const [proposals, setProposals] = useState<ProposalStates>(NO_PROPOSALS);
  const [showHistory, setShowHistory] = useState(false);

  // Reading NEVER starts an analysis: returning to this surface (or reloading the page) shows what
  // is running or waiting for review instead of setting off work nobody asked for.
  const refresh = useCallback(async () => {
    try {
      setProposals(await readProposals(source));
    } catch {
      /* keep the last known state — a failed read is not a failed analysis */
    }
  }, [source]);

  useEffect(() => {
    void refresh();
  }, [refresh]);
  // The ONE live mechanism carries the outcome here — from this session or any other.
  useLiveTopic('runs', () => {
    void refresh();
  });

  const busy = anyProposalBusy(proposals);

  // Pressing the button is the DELIBERATE act of starting over: the access point never restarts by
  // itself (a request over a failure reports that failure), so a failure on screen is put aside
  // first and only then is a new analysis asked for.
  const request = async (kind: RunProposalKind) => {
    if (busy) return;
    try {
      const state = await requestProposal(source, kind, proposals[kind]);
      setProposals((prev) => ({ ...prev, [kind]: state }));
    } catch (e) {
      toast({ title: 'The AI analysis could not be started', description: errMsg(e), variant: 'danger' });
    }
  };

  // Cancelling while it runs, closing the review and dismissing a failure are ONE act: this
  // proposal is put aside. So a returning user never meets a proposal they already dismissed.
  const abandon = useCallback(
    async (kind: RunProposalKind) => {
      setProposals((prev) => ({ ...prev, [kind]: null }));
      try {
        await accessProposal(source, kind, 'cancel');
      } catch (e) {
        toast({ title: 'Could not stop the analysis', description: errMsg(e), variant: 'danger' });
      }
      await refresh();
    },
    [source, toast, refresh],
  );

  const fill = presentProposal(proposals.fill);
  const finetune = presentProposal(proposals.finetune);
  const review: { kind: RunProposalKind; plan: RunPlan; axioms: Record<string, string> } | null =
    fill.ready ? { kind: 'fill', plan: fill.ready, axioms: proposals.fill?.axioms ?? {} }
    : finetune.ready ? { kind: 'finetune', plan: finetune.ready, axioms: proposals.finetune?.axioms ?? {} }
    : null;

  return (
    <TaskSurface
      kind="auto"
      newLabel="New run"
      emptyText="No runs yet. Create one, or let the AI fill the open axioms."
      toolbar={({ reload }) => (
        <>
          <div className="flex flex-wrap items-center gap-1.5">
            <Button variant="secondary" size="sm" disabled={busy} onClick={() => void request('fill')}>
              <LightbulbIcon className="h-4 w-4" /> {fill.busy ? 'Analyzing…' : 'AI fill'}
            </Button>
            <Button variant="secondary" size="sm" disabled={busy} onClick={() => void request('finetune')}>
              <RefreshIcon className="h-4 w-4" /> {finetune.busy ? 'Analyzing…' : 'AI fine-tune'}
            </Button>
            <Button variant="ghost" size="sm" onClick={() => setShowHistory(true)}>
              History
            </Button>
            {fill.busy && <StopAnalysis onStop={() => void abandon('fill')} />}
            {finetune.busy && <StopAnalysis onStop={() => void abandon('finetune')} />}
          </div>
          {fill.failure && <ProposalFailure reason={fill.failure} onDismiss={() => void abandon('fill')} />}
          {finetune.failure && (
            <ProposalFailure reason={finetune.failure} onDismiss={() => void abandon('finetune')} />
          )}
          {review && (
            <ProposalModal
              mode={APPLY_MODE[review.kind]}
              title={REVIEW_TITLE[review.kind]}
              plan={review.plan}
              axioms={review.axioms}
              onClose={() => void abandon(review.kind)}
              onApplied={async () => {
                await reload();
                await refresh();
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

/** The way out of a running analysis: the work stops, and nothing claims it succeeded or failed. */
function StopAnalysis({ onStop }: { onStop: () => void }) {
  return (
    <Button variant="ghost" size="sm" onClick={onStop}>
      <XIcon className="h-4 w-4" /> Stop
    </Button>
  );
}

/** A failed analysis, with the reason it failed — never a bare "failed". It stays until the user
 *  puts it aside or starts a new one. */
function ProposalFailure({ reason, onDismiss }: { reason: string; onDismiss: () => void }) {
  return (
    <div className="flex items-start gap-2 rounded-md border border-danger/30 bg-danger/[0.06] px-2.5 py-2">
      <p className="min-w-0 flex-1 text-caption text-danger">{reason}</p>
      <Button variant="ghost" size="sm" onClick={onDismiss}>
        Dismiss
      </Button>
    </div>
  );
}

/** Review + apply an AI proposal. `fill` extends the run set; `replace` swaps it for the plan.
 *  Nothing is written until the user applies (the proposal accesses write nothing). */
function ProposalModal({
  mode,
  title,
  plan,
  axioms,
  onClose,
  onApplied,
}: {
  mode: 'fill' | 'replace';
  title: string;
  plan: RunPlan;
  axioms: Record<string, string>;
  onClose: () => void;
  onApplied: () => void | Promise<void>;
}) {
  const source = useMemo(() => getDataSource(), []);
  const { toast } = useToast();
  const [applying, setApplying] = useState(false);
  const runs = plan.runs;

  const apply = async () => {
    if (applying) return;
    setApplying(true);
    try {
      await source.mercuryApplyRunProposal(mode, plan);
      toast({ title: mode === 'fill' ? 'Runs filled' : 'Runs replaced', variant: 'success' });
      await onApplied();
    } catch (e) {
      toast({ title: 'Applying failed', description: errMsg(e), variant: 'danger' });
      setApplying(false);
    }
  };

  const axiomNames = (r: PlannedRun) => r.axiomIds.map((id) => axioms[id] ?? id);

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

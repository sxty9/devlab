// DeliveriesView — the delivery ledger (REQ-024/040.2, F12): per repository what has been shipped,
// each delivery with its commit span, its pull request, the stage it reached and when. Rolling one
// back is triggered from here as a COUNTER-BOOKING — the ledger only grows, history is never
// rewritten (REQ-025) — and the deliberate dev reset of a repository lives at the same place
// (REQ-022.4).
//
// Both are dangerous actions, so both state their effect, the state they leave behind and the way
// back before they run (REQ-040.6). The view refreshes on the `deliveries` topic — no polling.
//
// EVERY managed repository has a section here, not only the ones the ledger has already named: work
// that was committed but never delivered leaves no ledger row, and that is precisely the repository
// whose dev state may need the reset. A section without rows therefore says what the ledger holds
// (nothing) — and no section claims what the dev branch of a repository carries: that is a property
// only the repository itself can attest, and it is not on this wire.
import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { getDataSource } from '@/data';
import { cn } from '@/lib/cn';
import { fmtDateTime } from '@/lib/format';
import { Button } from '@/ui/Button';
import { Modal } from '@/ui/Modal';
import { useToast } from '@/ui/Toast';
import { badgeTone } from '@/ui/tint';
import { useLiveTopic } from '@/state/live';
import type { Delivery, Repo } from '@/types';
import { maintenanceHold, noticeAt, noticeText, type NoticeRecord } from '../notices';
import {
  canRollback,
  commitRange,
  deliveryBadge,
  groupDeliveriesByRepo,
  resetConfirmation,
  rollbackConfirmation,
  shortSha,
  type Consequence,
} from './deliveries';

/** Uniform error-to-string, mirroring the rest of the Mercury surface. */
const msg = (e: unknown) => String((e as Error)?.message ?? e);

type Pending =
  | { kind: 'rollback'; delivery: Delivery; consequence: Consequence }
  | { kind: 'reset'; repo: string; consequence: Consequence };

export function DeliveriesView() {
  const source = useMemo(() => getDataSource(), []);
  const { toast } = useToast();
  const [list, setList] = useState<Delivery[] | null>(null);
  const [repos, setRepos] = useState<Repo[]>([]);
  const [failed, setFailed] = useState<string | null>(null);
  const [pending, setPending] = useState<Pending | null>(null);
  const [busy, setBusy] = useState(false);
  const [hold, setHold] = useState<NoticeRecord | null>(null);

  const gotRef = useRef(false);

  const load = useCallback(async () => {
    try {
      setList(await source.mercuryDeliveries());
      gotRef.current = true;
      setFailed(null);
    } catch (e) {
      // A transient tick never blanks a ledger that is already on screen.
      if (!gotRef.current) setFailed(msg(e));
    }
  }, [source]);

  // Why nothing here moves is a question the ledger's own rows cannot answer: while the maintenance
  // stands still (its writing half unarmed) every delivery simply stays open. The service says so in
  // the notice feed, and this surface reads THAT — through the one notices access point, in the
  // service's own wording — instead of inventing a second description of the same state.
  const loadHold = useCallback(async () => {
    try {
      const { notices } = await source.mercuryRunNotices();
      setHold(maintenanceHold(notices));
    } catch {
      /* keep what is known — a failed read never claims the maintenance is running */
    }
  }, [source]);

  // The managed repositories, through the ONE repo access point: they decide which sections exist,
  // so a repository the ledger never named still has its reset. A failed read leaves the ledger's
  // own repositories as the sections — fewer sections, never a wrong one.
  const loadRepos = useCallback(async () => {
    try {
      setRepos(await source.repos());
    } catch {
      /* keep what is known */
    }
  }, [source]);

  useEffect(() => {
    void load();
    void loadRepos();
    void loadHold();
  }, [load, loadRepos, loadHold]);
  useLiveTopic('deliveries', load);
  useLiveTopic('notices', loadHold);

  const confirm = useCallback(async () => {
    if (!pending || busy) return;
    setBusy(true);
    try {
      if (pending.kind === 'rollback') {
        // The outcome is the server's own honest line: counter-booked, reversing PR opened, PR
        // closed, or a conflict that raised a todo for the manual counter-booking.
        const out = await source.mercuryRollbackDelivery(pending.delivery.id);
        toast({
          title: out.todoId ? 'Manual counter-booking needed' : 'Rolled back',
          description: out.outcome,
          variant: out.todoId ? 'default' : 'success',
        });
      } else {
        await source.mercuryRepoReset(pending.repo);
        toast({ title: 'Dev state reset', description: `${pending.repo} is back at its default branch.`, variant: 'success' });
      }
      setPending(null);
      await load();
    } catch (e) {
      toast({ title: pending.kind === 'rollback' ? 'Rollback failed' : 'Reset failed', description: msg(e), variant: 'danger' });
    } finally {
      setBusy(false);
    }
  }, [pending, busy, source, toast, load]);

  if (failed) {
    return (
      <div className="flex h-full min-h-0 w-full items-center justify-center bg-bg-base px-6">
        <p className="max-w-md text-center text-footnote text-text-secondary">{failed}</p>
      </div>
    );
  }
  if (list === null) {
    return (
      <div className="flex h-full min-h-0 w-full items-center justify-center bg-bg-base">
        <p className="text-footnote text-text-tertiary">Loading…</p>
      </div>
    );
  }

  // A repository is named the way the ledger names it — its GitHub full name — so the ledger's rows
  // and the managed set land in ONE section per repository instead of two under two spellings.
  const groups = groupDeliveriesByRepo(list, repos.map((r) => r.fullName));

  return (
    <div className="dl-scroll min-h-0 w-full flex-1 overflow-y-auto bg-bg-base">
      <div className="mx-auto max-w-4xl px-8 py-7">
        <h1 className="text-title3 font-semibold tracking-tight text-text-primary">Deliveries</h1>

        {/* The standstill, in the service's own words and with the moment it last reported it — so
            nobody has to guess why open deliveries stay open. */}
        {hold && (
          <p className="mt-4 rounded-md border border-warning/40 bg-warning/10 px-3 py-2 text-caption text-warning">
            {noticeText(hold)} <span className="text-text-tertiary">Last reported {fmtDateTime(noticeAt(hold))}.</span>
          </p>
        )}

        {groups.length === 0 ? (
          <p className="mt-4 text-footnote text-text-tertiary">No repository to deliver to yet.</p>
        ) : (
          <div className="mt-5 flex flex-col gap-5">
            {groups.map((g) => (
              <section key={g.repo}>
                <div className="mb-2 flex flex-wrap items-center gap-2">
                  <h2 className="min-w-0 truncate font-mono text-footnote font-medium text-text-primary">{g.repo}</h2>
                  {/* Whether the TIP is sound is stated first — a failed tip is not the same as a
                      settled ledger, and the summary says which it is (WHAT-4). */}
                  <span className={cn('text-caption', g.failedTip ? 'font-medium text-danger' : 'text-text-tertiary')}>
                    {g.failedTip
                      ? 'tip failed — no new order branches past it'
                      : g.deliveries.length === 0
                        ? 'nothing delivered yet'
                        : g.latestOpen
                          ? `${g.openCount} open · last delivered ${shortSha(g.latestOpen.toCommit)}`
                          : 'every delivery settled'}
                  </span>
                  <Button
                    variant="ghost"
                    size="sm"
                    className="ml-auto"
                    onClick={() => setPending({ kind: 'reset', repo: g.repo, consequence: resetConfirmation(g.repo) })}
                  >
                    Reset dev state
                  </Button>
                </div>
                {/* The broken layer, named with what it klemmt on, so the way back is obvious without a
                    reload or a question: resolve the tip (re-run the order) or roll it back below. */}
                {g.failedTip && (
                  <p className="mb-2 rounded-md border border-danger/40 bg-danger/10 px-3 py-2 text-caption text-danger">
                    Delivery <span className="font-mono">{g.failedTip.id}</span> on{' '}
                    <span className="font-mono">{g.failedTip.branch}</span> failed and is the tip:{' '}
                    {g.failedTip.failedReason || 'the delivery did not complete'}. Resolve it by running the order again,
                    or roll it back to make the last sound layer the tip.
                  </p>
                )}
                {g.deliveries.length > 0 && (
                  <ul className="flex flex-col gap-1.5">
                    {g.deliveries.map((d) => (
                      <DeliveryRow
                        key={d.id}
                        delivery={d}
                        onRollback={() => setPending({ kind: 'rollback', delivery: d, consequence: rollbackConfirmation(d) })}
                      />
                    ))}
                  </ul>
                )}
              </section>
            ))}
          </div>
        )}
      </div>

      <ConsequenceDialog
        pending={pending}
        busy={busy}
        onClose={() => setPending(null)}
        onConfirm={() => void confirm()}
      />
    </div>
  );
}

/** One delivery: stage, commit span, branch, pull request, time — and its rollback where one is
 *  still possible. A reversal names the delivery it counter-books. */
function DeliveryRow({ delivery, onRollback }: { delivery: Delivery; onRollback: () => void }) {
  const badge = deliveryBadge(delivery);
  return (
    <li className="flex flex-wrap items-center gap-x-3 gap-y-1 rounded-card border border-separator bg-surface px-3 py-2">
      <span className={cn('shrink-0 rounded px-1.5 py-0.5 text-caption font-medium', badgeTone[badge.tone])}>{badge.label}</span>
      <span className="shrink-0 font-mono text-caption text-text-secondary">{commitRange(delivery)}</span>
      <span className="min-w-0 flex-1 truncate font-mono text-caption text-text-tertiary">{delivery.branch}</span>
      {delivery.reversalOf && <span className="shrink-0 text-caption text-text-tertiary">counter-books {delivery.reversalOf}</span>}
      {delivery.prUrl && (
        <a href={delivery.prUrl} target="_blank" rel="noreferrer" className="shrink-0 text-caption font-medium text-accent hover:underline">
          {delivery.prNumber ? `PR #${delivery.prNumber}` : 'PR'} ↗
        </a>
      )}
      <span className="shrink-0 text-caption text-text-tertiary">{fmtDateTime(delivery.mergedAt || delivery.createdAt)}</span>
      {canRollback(delivery) && (
        <Button variant="ghost" size="sm" onClick={onRollback}>
          Roll back
        </Button>
      )}
      {/* A failed row states, on its own line, WHAT it klemmt on — the reason it did not ship. */}
      {delivery.failedReason && <span className="w-full text-caption text-danger">{delivery.failedReason}</span>}
      {/* A SELF-ENDING obstacle: the pull request is being retried, not given up on. The line states
          what is stuck, since when, how often it has been tried and when the next attempt falls — so
          the wait is visible without alarming anyone, because it clears itself. */}
      {delivery.retrying && (
        <span className="w-full text-caption text-warning">
          Retrying — {delivery.retryReason || 'a passing obstacle'}
          {typeof delivery.retryAttempts === 'number' ? `; attempt ${delivery.retryAttempts}` : ''}
          {delivery.retrySince ? `, since ${fmtDateTime(delivery.retrySince)}` : ''}
          {delivery.retryNextAt ? `, next ${fmtDateTime(delivery.retryNextAt)}` : ''}. It clears itself once the obstacle
          passes — no action needed.
        </span>
      )}
      {/* A DURABLE obstacle: no repetition can clear it, so the pull request waits for a person to
          release it. Stated plainly so the one delivery that truly needs a hand is not lost among the
          retrying ones. */}
      {delivery.blocked && (
        <span className="w-full text-caption text-danger">
          Blocked — {delivery.blockedReason || 'a durable obstacle a person must resolve'}. Waiting for a release.
        </span>
      )}
    </li>
  );
}

/** The one confirmation of this surface: what the action does, what state follows, and the way back
 *  (REQ-040.6) — shared by the rollback and the dev reset, so both read identically. */
function ConsequenceDialog({
  pending,
  busy,
  onClose,
  onConfirm,
}: {
  pending: Pending | null;
  busy: boolean;
  onClose: () => void;
  onConfirm: () => void;
}) {
  if (!pending) return null;
  const rollback = pending.kind === 'rollback';
  const { effect, result, undo } = pending.consequence;
  return (
    <Modal
      open
      onClose={onClose}
      title={rollback ? 'Roll this delivery back?' : 'Reset the dev state?'}
      description={rollback ? pending.delivery.repo : pending.repo}
      size="md"
      footer={
        <>
          <Button variant="ghost" size="sm" onClick={onClose}>
            Cancel
          </Button>
          <Button variant="danger" size="sm" disabled={busy} onClick={onConfirm}>
            {busy ? 'Working…' : rollback ? 'Roll back' : 'Reset'}
          </Button>
        </>
      }
    >
      <dl className="flex flex-col gap-2 text-footnote">
        <div>
          <dt className="font-medium text-text-primary">What it does</dt>
          <dd className="text-text-secondary">{effect}</dd>
        </div>
        <div>
          <dt className="font-medium text-text-primary">What follows</dt>
          <dd className="text-text-secondary">{result}</dd>
        </div>
        <div>
          <dt className="font-medium text-text-primary">The way back</dt>
          <dd className="text-text-secondary">{undo}</dd>
        </div>
      </dl>
    </Modal>
  );
}

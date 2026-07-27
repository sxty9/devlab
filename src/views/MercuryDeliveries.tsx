import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { getDataSource } from '@/data';
import { useToast } from '@/ui/Toast';
import { Modal } from '@/ui/Modal';
import { Button } from '@/ui/Button';
import { cn } from '@/lib/cn';
import { badgeTone } from '@/ui/tint';
import { GitBranchIcon, GitCommitIcon, RefreshIcon } from '@/ui/icons';
import { fmtDateTime, EmptyPlaceholder } from './MercuryExecutions';
import { groupDeliveriesByRepo, canRollback, summarizeRollbackOutcome, DELIVERY_STATUS, shortSha } from './mercuryDeliveries';
import type { Delivery } from '@/types';

/** Uniform error-to-string, mirroring the rest of the Mercury surface. */
const msg = (e: unknown) => String((e as Error)?.message ?? e);

function StatusBadge({ status }: { status: Delivery['status'] }) {
  const { label, tint } = DELIVERY_STATUS[status];
  return <span className={cn('shrink-0 rounded px-1.5 py-0.5 text-caption font-medium', badgeTone[tint])}>{label}</span>;
}

/** MercuryDeliveries — the "Lieferungen" surface: the addressable record of what every run shipped, per
 *  repository. It reads the delivery ledger (both Automatische Läufe and Konkrete ToDos land here) and is
 *  the single place to see how far each delivery got, what dev currently serves, and to roll a delivery
 *  back or reset a repo's dev state. Self-contained like GlobalCalendarView; polls every 60s. */
export default function MercuryDeliveries() {
  const source = useMemo(() => getDataSource(), []);
  const { toast } = useToast();
  const [deliveries, setDeliveries] = useState<Delivery[] | null>(null);
  const [failed, setFailed] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [rollbackTarget, setRollbackTarget] = useState<Delivery | null>(null);
  const [resetRepo, setResetRepo] = useState<string | null>(null);
  const gotDataRef = useRef(false);

  const load = useCallback(async () => {
    try {
      const res = await source.mercuryDeliveries();
      setDeliveries(res.deliveries);
      gotDataRef.current = true;
      setFailed(null);
    } catch (e) {
      // Once a list is on screen a transient poll error must not blank it out.
      if (!gotDataRef.current) setFailed(msg(e));
    }
  }, [source]);

  useEffect(() => {
    void load();
    const iv = window.setInterval(() => void load(), 60000);
    return () => window.clearInterval(iv);
  }, [load]);

  const groups = useMemo(() => groupDeliveriesByRepo(deliveries ?? []), [deliveries]);

  const doRollback = useCallback(async () => {
    if (!rollbackTarget) return;
    setBusy(true);
    try {
      const out = await source.mercuryRollbackDelivery(rollbackTarget.id);
      const s = summarizeRollbackOutcome(out);
      toast(s);
      setRollbackTarget(null);
      await load();
    } catch (e) {
      toast({ title: 'Rückrollen fehlgeschlagen', description: msg(e), variant: 'danger' });
    } finally {
      setBusy(false);
    }
  }, [rollbackTarget, source, toast, load]);

  const doReset = useCallback(async () => {
    if (!resetRepo) return;
    setBusy(true);
    try {
      await source.mercuryResetRepo(resetRepo);
      toast({ title: 'dev-Stand zurückgesetzt', description: `${resetRepo}: dev-Zweig auf den Standardzweig zurückgesetzt und neu ausgeliefert.`, variant: 'success' });
      setResetRepo(null);
      await load();
    } catch (e) {
      toast({ title: 'Zurücksetzen fehlgeschlagen', description: msg(e), variant: 'danger' });
    } finally {
      setBusy(false);
    }
  }, [resetRepo, source, toast, load]);

  if (failed) {
    return (
      <div className="flex h-full min-h-0 w-full items-center justify-center bg-bg-base px-6">
        <p className="max-w-md text-center text-footnote text-text-secondary">{failed}</p>
      </div>
    );
  }
  if (deliveries === null) {
    return (
      <div className="flex h-full min-h-0 w-full items-center justify-center bg-bg-base">
        <p className="text-footnote text-text-tertiary">Lädt…</p>
      </div>
    );
  }

  return (
    <div className="dl-scroll min-h-0 flex-1 overflow-y-auto bg-bg-base">
      <div className="mx-auto max-w-3xl px-6 py-6">
        <div className="mb-5 flex items-center gap-3">
          <h1 className="text-title3 font-semibold tracking-tight text-text-primary">Lieferungen</h1>
          <button
            type="button"
            onClick={() => void load()}
            className="ml-auto inline-flex items-center gap-1.5 rounded-md px-2 py-1 text-caption text-text-secondary transition duration-fast hover:bg-fill/10 hover:text-text-primary"
          >
            <RefreshIcon className="h-3.5 w-3.5" />
            Aktualisieren
          </button>
        </div>

        {groups.length === 0 ? (
          <EmptyPlaceholder text="Noch keine Lieferungen. Sobald ein Lauf Arbeit an ein Repository ausliefert, erscheint sie hier — mit ihrem Stand und der Möglichkeit, sie zurückzurollen." />
        ) : (
          <div className="flex flex-col gap-5">
            {groups.map((g) => (
              <section key={g.repo} className="overflow-hidden rounded-card border border-separator bg-surface-raised">
                <header className="flex flex-wrap items-center gap-x-3 gap-y-1 border-b border-separator px-4 py-3">
                  <GitBranchIcon className="h-4 w-4 shrink-0 text-text-tertiary" />
                  <span className="font-medium text-text-primary">{g.repo}</span>
                  <span className="text-caption text-text-tertiary">
                    {g.devServes ? (
                      <>
                        dev serviert <span className="font-mono">{shortSha(g.devServes.toCommit)}</span> · {g.openCount} offene {g.openCount === 1 ? 'Lieferung' : 'Lieferungen'}
                      </>
                    ) : (
                      <>dev = Standardzweig</>
                    )}
                  </span>
                  <Button variant="ghost" size="sm" className="ml-auto text-danger hover:bg-danger/10" onClick={() => setResetRepo(g.repo)}>
                    <RefreshIcon className="h-3.5 w-3.5" />
                    dev-Stand zurücksetzen
                  </Button>
                </header>
                <ul className="divide-y divide-separator">
                  {g.deliveries.map((d) => (
                    <li key={d.id} className="flex flex-wrap items-center gap-x-3 gap-y-1.5 px-4 py-2.5">
                      <StatusBadge status={d.status} />
                      <div className="min-w-0 flex-1">
                        <div className="truncate text-footnote text-text-primary">{d.runName || d.runId}</div>
                        <div className="flex items-center gap-1.5 text-caption text-text-tertiary">
                          <GitCommitIcon className="h-3 w-3 shrink-0" />
                          <span className="font-mono">
                            {shortSha(d.fromCommit)}..{shortSha(d.toCommit)}
                          </span>
                          <span>·</span>
                          <span>{fmtDateTime(d.createdAt)}</span>
                          {d.revertOf && <span className="text-text-tertiary">· Umkehrung</span>}
                          {d.revertedBy && <span className="text-text-tertiary">· zurückgerollt von {d.revertedBy}</span>}
                        </div>
                      </div>
                      {d.prUrl && (
                        <a
                          href={d.prUrl}
                          target="_blank"
                          rel="noreferrer"
                          className="shrink-0 text-caption text-accent hover:underline"
                        >
                          {d.prNumber ? `PR #${d.prNumber}` : 'PR'}
                        </a>
                      )}
                      {canRollback(d) && (
                        <Button variant="ghost" size="sm" className="shrink-0 text-danger hover:bg-danger/10" onClick={() => setRollbackTarget(d)}>
                          Zurückrollen
                        </Button>
                      )}
                    </li>
                  ))}
                </ul>
              </section>
            ))}
          </div>
        )}
      </div>

      <RollbackModal delivery={rollbackTarget} busy={busy} onConfirm={() => void doRollback()} onClose={() => !busy && setRollbackTarget(null)} />
      <ResetModal repo={resetRepo} busy={busy} onConfirm={() => void doReset()} onClose={() => !busy && setResetRepo(null)} />
    </div>
  );
}

/** The rollback confirm — spells out what happens, what is different afterwards, and that it is reversible
 *  (req: dangerous actions are recognizable and described as recoverable). The wording adapts to whether
 *  the delivery was already merged (a reversing PR) or is still open (its PR is closed). */
function RollbackModal({ delivery, busy, onConfirm, onClose }: { delivery: Delivery | null; busy: boolean; onConfirm: () => void; onClose: () => void }) {
  const merged = delivery?.status === 'merged';
  return (
    <Modal
      open={delivery !== null}
      onClose={onClose}
      title="Lieferung zurückrollen"
      description={delivery ? `${delivery.runName || delivery.runId} — ${delivery.repo}` : ''}
      footer={
        <>
          <Button variant="ghost" onClick={onClose} disabled={busy}>
            Abbrechen
          </Button>
          <Button variant="danger" onClick={onConfirm} disabled={busy}>
            {busy ? 'Rollt zurück…' : 'Zurückrollen'}
          </Button>
        </>
      }
    >
      <div className="flex flex-col gap-3 text-footnote text-text-secondary">
        <p>
          <span className="font-medium text-text-primary">Was geschieht:</span> Die Änderungen dieser Lieferung werden auf dem dev-Stand durch einen
          neuen, umkehrenden Commit gegengebucht. Die Historie bleibt erhalten — nichts wird überschrieben oder erzwungen.
          {merged
            ? ' Da die Lieferung bereits zusammengeführt ist, wird zusätzlich ein umkehrender PR geöffnet, der dieselbe Kette wie jede andere Änderung durchläuft.'
            : ' Der noch offene PR dieser Lieferung wird mit Begründung geschlossen.'}
        </p>
        <p>
          <span className="font-medium text-text-primary">Danach:</span> Der dev-Stand enthält die Wirkung dieser Lieferung nicht mehr; dev wird neu
          ausgeliefert.
        </p>
        <p>
          <span className="font-medium text-text-primary">Rückholbar:</span> Die Lieferung bleibt als Datensatz erhalten und lässt sich durch einen
          erneuten Lauf wiederherstellen.
        </p>
        <p className="text-caption text-text-tertiary">
          Baut spätere Arbeit auf dieser Lieferung auf, wird statt eines riskanten automatischen Reverts ein konkretes ToDo angelegt, das die
          Gegenbuchung von Hand vornimmt.
        </p>
      </div>
    </Modal>
  );
}

/** The dev-reset confirm — the explicit way back. Destructive but recoverable, and spelled out as such. */
function ResetModal({ repo, busy, onConfirm, onClose }: { repo: string | null; busy: boolean; onConfirm: () => void; onClose: () => void }) {
  return (
    <Modal
      open={repo !== null}
      onClose={onClose}
      title="dev-Stand zurücksetzen"
      description={repo ?? ''}
      footer={
        <>
          <Button variant="ghost" onClick={onClose} disabled={busy}>
            Abbrechen
          </Button>
          <Button variant="danger" onClick={onConfirm} disabled={busy}>
            {busy ? 'Setzt zurück…' : 'Zurücksetzen'}
          </Button>
        </>
      }
    >
      <div className="flex flex-col gap-3 text-footnote text-text-secondary">
        <p>
          <span className="font-medium text-text-primary">Was geschieht:</span> Der dev-Zweig von {repo} wird auf den Standardzweig des Repositories
          zurückgesetzt und neu veröffentlicht.
        </p>
        <p>
          <span className="font-medium text-text-primary">Danach:</span> Alle noch nicht zusammengeführten Mercury-Lieferungen verschwinden vom
          dev-Stand; bereits zusammengeführte Arbeit bleibt (sie ist Teil des Standardzweigs). dev wird neu ausgeliefert.
        </p>
        <p>
          <span className="font-medium text-text-primary">Rückholbar:</span> Die Lieferungen bleiben als Datensätze und auf ihren eigenen Zweigen und
          PRs erhalten und lassen sich erneut ausführen. Dies ist der ausdrückliche Weg zurück — es geschieht nie als Nebeneffekt eines Laufs.
        </p>
      </div>
    </Modal>
  );
}

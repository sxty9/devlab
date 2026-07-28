// CoverageView — the honest axiom→run coverage (REQ-004): every axiom with the runs that carry
// it; the UNCOVERED ones first and unmistakable. While the automatic assignment is scheduled or
// running, uncovered axioms are marked as "assignment in progress" — transient, not permanent —
// and a failed assignment leaves them visibly uncovered (the notice feed carries the reason).
import { useCallback, useEffect, useMemo, useState } from 'react';
import { getDataSource } from '@/data';
import { useLiveTopic } from '@/state/live';
import { cn } from '@/lib/cn';
import { CheckIcon, MercuryIcon } from '@/ui/icons';
import type { Run, RunCoverage } from '@/types';

const msg = (e: unknown) => String((e as Error)?.message ?? e);

export function CoverageView() {
  const source = useMemo(() => getDataSource(), []);
  const [cov, setCov] = useState<RunCoverage | null>(null);
  const [runs, setRuns] = useState<Run[]>([]);
  const [failed, setFailed] = useState<string | null>(null);

  const reload = useCallback(() => {
    return Promise.all([source.mercuryRunCoverage(), source.mercuryRuns()])
      .then(([c, r]) => {
        setCov(c);
        setRuns(r.runs);
        setFailed(null);
      })
      .catch((e: unknown) => setFailed(msg(e)));
  }, [source]);

  useEffect(() => {
    void reload();
  }, [reload]);
  // Coverage changes when runs change (edit/delete/restore) and when the background assignment
  // lands (its outcome arrives as a notice) — subscribe to both, refetch through the one path.
  useLiveTopic('runs', () => void reload());
  useLiveTopic('notices', () => void reload());

  if (failed) {
    return (
      <div className="flex h-full min-h-0 items-center justify-center px-6">
        <p className="max-w-md text-center text-footnote text-text-secondary">{failed}</p>
      </div>
    );
  }
  if (!cov) return <p className="px-6 py-5 text-footnote text-text-tertiary">Loading…</p>;

  const runTitle = new Map(runs.map((r) => [r.id, r.title]));
  const ids = Object.keys(cov.axioms).sort((a, b) => (cov.index[a] ?? a).localeCompare(cov.index[b] ?? b));
  const uncovered = ids.filter((id) => !(cov.covered[id]?.length > 0));
  const covered = ids.filter((id) => cov.covered[id]?.length > 0);

  return (
    <div className="dl-scroll h-full min-h-0 overflow-y-auto">
      <div className="mx-auto max-w-3xl px-8 py-7">
        <div className="flex items-center justify-between gap-4">
          <h1 className="text-title3 font-semibold tracking-tight text-text-primary">Coverage</h1>
          <p className="text-caption text-text-tertiary">
            {covered.length} of {ids.length} axioms carried by a run
          </p>
        </div>

        {uncovered.length > 0 ? (
          <section className="mt-5">
            <h2 className="text-caption font-semibold uppercase tracking-wide text-warning">Uncovered ({uncovered.length})</h2>
            <ul className="mt-2 flex flex-col gap-1.5">
              {uncovered.map((id) => (
                <li key={id} className="rounded-md border border-warning/30 bg-warning/[0.06] px-3 py-2">
                  <div className="flex items-center justify-between gap-3">
                    <p className="min-w-0 truncate text-footnote text-text-primary">{cov.axioms[id]}</p>
                    {cov.pending ? (
                      <span className="shrink-0 animate-pulse text-caption text-text-tertiary">assignment in progress…</span>
                    ) : (
                      <span className="shrink-0 text-caption font-medium text-warning">no run</span>
                    )}
                  </div>
                  <p className="mt-0.5 truncate font-mono text-caption text-text-tertiary">{cov.index[id]}</p>
                </li>
              ))}
            </ul>
          </section>
        ) : (
          ids.length > 0 && (
            <p className="mt-5 flex items-center gap-1.5 rounded-md border border-success/30 bg-success/[0.06] px-3 py-2 text-caption font-medium text-success">
              <CheckIcon className="h-3.5 w-3.5" /> Every axiom is carried by a run
            </p>
          )
        )}

        {covered.length > 0 && (
          <section className="mt-6">
            <h2 className="text-caption font-semibold uppercase tracking-wide text-text-tertiary">Covered ({covered.length})</h2>
            <ul className="mt-2 flex flex-col gap-1">
              {covered.map((id) => (
                <li key={id} className="flex items-baseline justify-between gap-3 rounded-md px-3 py-1.5 hover:bg-fill/5">
                  <div className="min-w-0">
                    <p className="truncate text-footnote text-text-primary">{cov.axioms[id]}</p>
                    <p className="truncate font-mono text-caption text-text-tertiary">{cov.index[id]}</p>
                  </div>
                  <p className={cn('shrink-0 text-caption text-text-secondary')}>
                    {(cov.covered[id] ?? []).map((runId) => runTitle.get(runId) ?? runId).join(', ')}
                  </p>
                </li>
              ))}
            </ul>
          </section>
        )}

        {ids.length === 0 && (
          <div className="mt-10 flex flex-col items-center gap-3 text-center">
            <span className="flex h-12 w-12 items-center justify-center rounded-2xl bg-surface-raised shadow-elev-1 ring-1 ring-separator">
              <MercuryIcon className="h-6 w-6 text-accent" />
            </span>
            <p className="text-footnote text-text-tertiary">No axioms yet — coverage starts with the first one.</p>
          </div>
        )}
      </div>
    </div>
  );
}

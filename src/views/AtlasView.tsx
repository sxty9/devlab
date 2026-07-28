import { useEffect, useMemo, useState } from 'react';
import { useSession } from '@/state/session';
import { getDataSource } from '@/data';
import { IconCard } from '@/ui/IconCard';
import { Splash } from '@/shell/Splash';
import { SitemapIcon } from '@/ui/icons';
import { cn } from '@/lib/cn';
import type { AtlasAllocation, AtlasGraph, AtlasNode, Repo } from '@/types';

/** A node's tint carries its state: fully declared, or missing a manifest or a route. */
function tintOf(n: AtlasNode): Repo['tint'] {
  if (!n.hasManifest || !n.hasRoute) return 'warning';
  return n.repo ? 'accent' : 'gpu';
}

function subtitleOf(n: AtlasNode): string {
  return n.port ? `:${n.port}` : 'nicht geroutet';
}

/** Atlas — the model of how Holistic services interact.
 *
 *  What ships here is the structural layer: the services this host actually runs, read off their own
 *  rights manifests and Caddy routes, together with the inconsistencies between them. It is derived
 *  rather than maintained — a service that installs itself appears here without Atlas changing.
 *
 *  Modelling the processes that run across these services (BPMN, lanes = services) builds on this
 *  layer and is the next stage. */
export function AtlasView() {
  const { repos, openRepo } = useSession();
  const source = useMemo(() => getDataSource(), []);

  const [graph, setGraph] = useState<AtlasGraph | null>(null);
  const [ports, setPorts] = useState<AtlasAllocation | null>(null);
  const [failed, setFailed] = useState(false);

  useEffect(() => {
    let cancelled = false;
    source
      .atlas()
      .then((g) => !cancelled && setGraph(g))
      .catch(() => !cancelled && setFailed(true));
    // The port ledger is a secondary panel: a read failure hides it rather than failing the whole view.
    source
      .atlasPorts()
      .then((p) => !cancelled && setPorts(p))
      .catch(() => undefined);
    return () => {
      cancelled = true;
    };
  }, [source]);

  if (failed) {
    return (
      <div className="flex min-h-0 flex-1 items-center justify-center bg-bg-base">
        <p className="text-footnote text-text-secondary">Die Landschaft konnte nicht gelesen werden.</p>
      </div>
    );
  }
  if (!graph) return <Splash />;

  const openable = (n: AtlasNode) => n.repo !== '' && repos.some((r) => r.id === n.repo);

  return (
    <div className="dl-scroll min-h-0 flex-1 overflow-y-auto bg-bg-base">
      <div className="mx-auto flex w-full max-w-5xl flex-col gap-9 px-6 py-10">
        <section>
          <h2 className="mb-2.5 text-caption font-semibold uppercase tracking-wide text-text-tertiary">
            Deployed services
          </h2>
          <div className="grid grid-cols-2 gap-3 sm:grid-cols-3 md:grid-cols-4 lg:grid-cols-6">
            {graph.nodes.map((n) => (
              <IconCard
                key={n.id}
                icon={SitemapIcon}
                tint={tintOf(n)}
                title={n.id}
                subtitle={subtitleOf(n)}
                disabled={!openable(n)}
                onClick={() => openable(n) && openRepo(n.repo)}
              />
            ))}
          </div>
        </section>

        {ports && (
          <section>
            <h2 className="mb-2.5 flex items-baseline gap-2 text-caption font-semibold uppercase tracking-wide text-text-tertiary">
              Port allocation
              <span className="font-mono text-footnote font-normal normal-case tracking-normal text-text-tertiary">
                band {ports.band[0]}–{ports.band[1]}
              </span>
            </h2>
            <ul className="flex flex-col gap-px overflow-hidden rounded-card border border-separator bg-surface shadow-elev-1">
              {ports.held.map((h) => {
                const doubled = h.ids.length > 1;
                return (
                  <li key={h.port} className="flex items-center gap-2.5 px-3.5 py-2.5">
                    <span className="w-14 shrink-0 font-mono text-footnote text-text-secondary">:{h.port}</span>
                    <span className={cn('text-footnote', doubled ? 'text-danger' : 'text-text-primary')}>
                      {h.ids.join(', ')}
                    </span>
                    {doubled && (
                      <span className="ml-auto rounded-full bg-danger/10 px-2 py-0.5 text-caption font-medium text-danger">
                        double-booked
                      </span>
                    )}
                  </li>
                );
              })}
            </ul>
            <div className="mt-2.5 flex flex-wrap items-baseline gap-1.5">
              <span className="mr-1 text-caption uppercase tracking-wide text-text-tertiary">Free</span>
              {ports.free.length === 0 ? (
                <span className="text-footnote text-text-secondary">none in band</span>
              ) : (
                ports.free.map((p) => (
                  <span
                    key={p}
                    className="rounded-md px-1.5 py-0.5 font-mono text-caption text-text-secondary ring-1 ring-inset ring-separator"
                  >
                    {p}
                  </span>
                ))
              )}
            </div>
          </section>
        )}

        {graph.findings.length > 0 && (
          <section>
            <h2 className="mb-2.5 text-caption font-semibold uppercase tracking-wide text-text-tertiary">
              Findings
            </h2>
            <ul className="flex flex-col gap-px overflow-hidden rounded-card border border-separator bg-surface shadow-elev-1">
              {graph.findings.map((f) => (
                <li key={f.message} className="flex items-center gap-2.5 px-3.5 py-2.5">
                  <span
                    className={cn(
                      'h-1.5 w-1.5 shrink-0 rounded-full',
                      f.severity === 'error' ? 'bg-danger' : 'bg-warning',
                    )}
                  />
                  <span className="text-footnote text-text-secondary">{f.message}</span>
                </li>
              ))}
            </ul>
          </section>
        )}
      </div>
    </div>
  );
}

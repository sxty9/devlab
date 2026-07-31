import { useEffect, useMemo, useState } from 'react';
import { useSession } from '@/state/session';
import { getDataSource } from '@/data';
import { IconCard } from '@/ui/IconCard';
import { Splash } from '@/shell/Splash';
import { SitemapIcon } from '@/ui/icons';
import { cn } from '@/lib/cn';
import { AtlasPorts } from '@/views/AtlasPorts';
import type { AtlasGraph, AtlasNode, Repo } from '@/types';

/** A node's tint carries its state: fully declared, or missing a manifest or a route. */
function tintOf(n: AtlasNode): Repo['tint'] {
  if (!n.hasManifest || !n.hasRoute) return 'warning';
  return n.repo ? 'accent' : 'gpu';
}

function subtitleOf(n: AtlasNode): string {
  return n.port ? `:${n.port}` : 'not routed';
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
  const [failed, setFailed] = useState(false);

  useEffect(() => {
    let cancelled = false;
    source
      .atlas()
      .then((g) => !cancelled && setGraph(g))
      .catch(() => !cancelled && setFailed(true));
    return () => {
      cancelled = true;
    };
  }, [source]);

  if (failed) {
    return (
      <div className="flex min-h-0 flex-1 items-center justify-center bg-bg-base">
        <p className="text-footnote text-text-secondary">The landscape could not be read.</p>
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

        <section>
          <h2 className="mb-2.5 text-caption font-semibold uppercase tracking-wide text-text-tertiary">
            Ports
          </h2>
          <AtlasPorts />
        </section>
      </div>
    </div>
  );
}

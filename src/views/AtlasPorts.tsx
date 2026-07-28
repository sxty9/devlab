import { useEffect, useMemo, useState } from 'react';
import { getDataSource } from '@/data';
import { cn } from '@/lib/cn';
import type { PortAllocation } from '@/types';

/** AtlasPorts — the observed port ledger (F9, REQ-044).
 *
 *  Derived fresh from the host's own routes and bound sockets on every read — nothing is stored,
 *  so the view cannot drift from what is deployed. A double-booked port (two services routed to
 *  one port) arrives named on each holder and is shown as the conflict it is, never hidden. */
export function AtlasPorts() {
  const source = useMemo(() => getDataSource(), []);
  const [allocs, setAllocs] = useState<PortAllocation[] | null>(null);
  const [failed, setFailed] = useState(false);

  useEffect(() => {
    let cancelled = false;
    source
      .atlasPorts()
      .then((a) => !cancelled && setAllocs(a))
      .catch(() => !cancelled && setFailed(true));
    return () => {
      cancelled = true;
    };
  }, [source]);

  if (failed) {
    return <p className="text-footnote text-text-secondary">The port ledger could not be read.</p>;
  }
  if (!allocs) {
    return <p className="text-footnote text-text-tertiary">Reading ports…</p>;
  }
  if (allocs.length === 0) {
    return <p className="text-footnote text-text-tertiary">No routed or bound ports observed.</p>;
  }

  return (
    <div className="overflow-x-auto rounded-card border border-separator bg-surface shadow-elev-1">
      <table className="w-full text-footnote">
        <thead>
          <tr className="border-b border-separator text-left text-caption uppercase tracking-wide text-text-tertiary">
            <th className="px-3.5 py-2 font-semibold">Port</th>
            <th className="px-3.5 py-2 font-semibold">Service</th>
            <th className="px-3.5 py-2 font-semibold">Routed</th>
            <th className="px-3.5 py-2 font-semibold">Bound</th>
            <th className="px-3.5 py-2 font-semibold" />
          </tr>
        </thead>
        <tbody>
          {allocs.map((a, i) => (
            <tr
              key={`${a.port}-${a.service || i}`}
              className={cn('border-b border-separator last:border-b-0', a.conflict && 'bg-danger/5')}
            >
              <td className="px-3.5 py-2 font-mono text-text-primary">{a.port}</td>
              <td className="px-3.5 py-2 text-text-primary">
                {a.service || <span className="text-text-tertiary">— bound, not routed</span>}
              </td>
              <td className="px-3.5 py-2 text-text-secondary">{a.routed ? 'yes' : '—'}</td>
              <td className="px-3.5 py-2 text-text-secondary">{a.bound ? 'yes' : '—'}</td>
              <td className="px-3.5 py-2">
                {a.conflict && (
                  <span className="inline-flex items-center gap-1.5 rounded-full bg-danger/10 px-2 py-0.5 text-caption font-medium text-danger">
                    <span className="h-1.5 w-1.5 rounded-full bg-danger" />
                    double-booked
                  </span>
                )}
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

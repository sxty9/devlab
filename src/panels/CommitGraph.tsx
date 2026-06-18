import { useState } from 'react';
import type { Commit } from '@/types';
import { cn } from '@/lib/cn';

// Lane colors (dark-theme RGB of accent / gpu / success / warning / ssd).
const LANE_COLORS = ['#0a84ff', '#bf5af2', '#30d158', '#ffd60a', '#40c8e0'];
const laneColor = (lane: number) => LANE_COLORS[lane % LANE_COLORS.length];

const X0 = 12;
const LANE_W = 14;
const ROW_H = 46;
const x = (lane: number) => X0 + lane * LANE_W;

function refTone(ref: string): string {
  if (ref === 'HEAD') return 'bg-accent/20 text-accent';
  if (ref === 'main' || ref === 'master') return 'bg-success/15 text-success';
  if (ref.startsWith('preview')) return 'bg-gpu/15 text-gpu';
  return 'bg-fill/10 text-text-secondary';
}

/** An IntelliJ-style commit log: a coloured lane graph gutter + commit metadata per row. */
export function CommitGraph({ commits }: { commits: Commit[] }) {
  const [selected, setSelected] = useState<string | null>(null);

  const maxLane = commits.reduce(
    (m, c) => Math.max(m, c.dotLane, ...c.lines.map((l) => Math.max(l.from, l.to))),
    0,
  );
  const gutterW = x(maxLane) + 12;

  return (
    <ul role="list">
      {commits.map((c) => {
        const isSel = selected === c.hash;
        return (
          <li key={c.hash}>
            <button
              type="button"
              onClick={() => setSelected(c.hash)}
              title={`${c.hash} · ${c.author}`}
              className={cn(
                'flex w-full items-stretch gap-2 text-left transition-colors',
                isSel ? 'bg-accent/15' : 'hover:bg-fill/[0.06]',
              )}
            >
              <svg width={gutterW} height={ROW_H} className="shrink-0" style={{ width: gutterW }} aria-hidden>
                {c.lines.map((l, i) =>
                  l.from === l.to ? (
                    <line key={i} x1={x(l.from)} y1={0} x2={x(l.to)} y2={ROW_H} stroke={laneColor(l.lane)} strokeWidth={1.6} />
                  ) : (
                    <path
                      key={i}
                      d={`M ${x(l.from)} 0 C ${x(l.from)} ${ROW_H * 0.5}, ${x(l.to)} ${ROW_H * 0.5}, ${x(l.to)} ${ROW_H}`}
                      fill="none"
                      stroke={laneColor(l.lane)}
                      strokeWidth={1.6}
                    />
                  ),
                )}
                <circle cx={x(c.dotLane)} cy={ROW_H / 2} r={4.5} fill={laneColor(c.dotLane)} stroke="#1c1c1e" strokeWidth={2} />
              </svg>

              <span className="flex min-w-0 flex-1 flex-col justify-center py-1.5 pr-3">
                <span className="flex items-center gap-1.5">
                  {c.refs?.map((r) => (
                    <span key={r} className={cn('shrink-0 rounded-sm px-1.5 text-[10px] font-semibold leading-4', refTone(r))}>
                      {r}
                    </span>
                  ))}
                  <span className="truncate text-footnote text-text-primary">{c.message}</span>
                </span>
                <span className="mt-0.5 flex items-center gap-2 text-[11px] text-text-tertiary">
                  <span className="font-mono">{c.hash}</span>
                  <span className="truncate">{c.author}</span>
                  <span>· {c.time}</span>
                </span>
              </span>
            </button>
          </li>
        );
      })}
    </ul>
  );
}

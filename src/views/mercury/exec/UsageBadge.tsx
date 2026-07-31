// UsageBadge — the consumption readout of one execution: input tokens, output tokens and their
// monetary equivalent, climbing LIVE while the agent works (REQ-017, F7). It is informative only:
// there is no cap, no remaining budget and no "x of y spent" anywhere on this surface — the only
// binding limits are the collective usage-limit pause and the time budget.
import { cn } from '@/lib/cn';
import { fmtCost, fmtNum } from '@/lib/format';
import type { UsageView } from '@/types';
import { usageParts } from './logic';

export function UsageBadge({ usage, className }: { usage?: UsageView; className?: string }) {
  const { input, output, costUsd, any } = usageParts(usage);
  if (!any) return null;
  return (
    <span className={cn('flex flex-wrap items-center gap-x-2 gap-y-0.5 text-caption text-text-tertiary', className)}>
      <span aria-label="input tokens">↓ {fmtNum(input)}</span>
      <span aria-label="output tokens">↑ {fmtNum(output)}</span>
      <span className="font-medium text-text-secondary">{fmtCost(costUsd)}</span>
    </span>
  );
}

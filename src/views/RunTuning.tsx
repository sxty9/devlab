import { useEffect, useMemo, useState } from 'react';
import { getDataSource } from '@/data';
import type { AiModelCatalog } from '@/types';

/** The native claude reasoning-effort levels, low → max — the same ladder the KI tab (ClaudePanel) offers.
 *  Kept here as the single frontend source so the two never drift. */
export const NATIVE_EFFORTS = ['low', 'medium', 'high', 'xhigh', 'max'] as const;

/** The effort ladder a run or todo may pick: the native levels plus "ultracode", the maximal tier — the
 *  executor runs it at max effort plus multi-agent orchestration. Mirrors runEffortAllowed in the backend. */
export const RUN_EFFORTS = [...NATIVE_EFFORTS, 'ultracode'] as const;

/** The per-repo time-budget ladder a run or todo may pick, beside the explicit no-cap "off" and the empty
 *  default (the service default, 3h). Values are Go duration strings the backend parses; the editor also
 *  keeps any stored value outside this ladder so editing an existing run never silently drops it. */
export const RUN_TIME_BUDGETS = ['30m', '1h', '2h', '3h', '4h', '6h', '8h'] as const;

/** The human label for a stored time-budget value, shared by the editors and the execution view so the
 *  no-cap choice reads the same everywhere: "off" → "no limit", a duration verbatim. Empty is the service
 *  default — callers show nothing, since an un-chosen budget is not a deviation worth surfacing. */
export function budgetLabel(v: string | undefined): string {
  if (!v) return '';
  return v === 'off' ? 'no limit' : v;
}

const selectClass =
  'w-full rounded-md border border-separator bg-surface px-3 py-2 text-footnote text-text-primary outline-none focus:border-accent/50';
const labelClass = 'mb-1.5 text-caption font-semibold uppercase tracking-wide text-text-tertiary';

/** Model + effort + time-budget selectors shared by the Lauf and ToDo editors, so both surfaces tune the
 *  executor the same way (symmetric siblings, one component). The model choices are the aigentic catalog
 *  the KI tab already reads — the single source of truth — plus an explicit "Default" that leaves the
 *  runner default (opus). Effort offers the full ladder incl. ultracode; "Default" leaves the runner
 *  default (max). Time budget offers a duration ladder plus "No limit" (no cap); "Default" leaves the
 *  service default (3h). All three start empty, so an untouched run behaves exactly as before. */
export function RunTuningFields({
  model,
  effort,
  timeBudget,
  onModelChange,
  onEffortChange,
  onTimeBudgetChange,
}: {
  model: string;
  effort: string;
  timeBudget: string;
  onModelChange: (v: string) => void;
  onEffortChange: (v: string) => void;
  onTimeBudgetChange: (v: string) => void;
}) {
  const source = useMemo(() => getDataSource(), []);
  const [models, setModels] = useState<AiModelCatalog['claude']>([]);

  useEffect(() => {
    let cancelled = false;
    source
      .assistantModels()
      .then((c) => !cancelled && setModels(c.claude))
      .catch(() => {}); // AI not enabled / offline → only "Default" is offered
    return () => {
      cancelled = true;
    };
  }, [source]);

  // A stored model the catalog doesn't list (a since-removed model, or the catalog failed to load) must
  // still be selectable, so editing an existing run never silently drops it on save.
  const options = useMemo(() => {
    if (!model || models.some((m) => m.id === model)) return models;
    return [...models, { id: model, label: model }];
  }, [models, model]);

  // Keep a stored budget outside the ladder (e.g. "90m" from an API client) selectable, mirroring the
  // model field, so editing a run never silently drops it. "off" and "" have their own options.
  const budgetOptions = useMemo(() => {
    const base: string[] = [...RUN_TIME_BUDGETS];
    if (timeBudget && timeBudget !== 'off' && !base.includes(timeBudget)) base.push(timeBudget);
    return base;
  }, [timeBudget]);

  return (
    <div className="flex flex-wrap gap-4">
      <div className="min-w-[12rem] flex-1">
        <p className={labelClass}>Model</p>
        <select value={model} onChange={(e) => onModelChange(e.target.value)} className={selectClass}>
          <option value="">Default</option>
          {options.map((m) => (
            <option key={m.id} value={m.id}>
              {m.label}
            </option>
          ))}
        </select>
      </div>
      <div className="min-w-[12rem] flex-1">
        <p className={labelClass}>Effort</p>
        <select value={effort} onChange={(e) => onEffortChange(e.target.value)} className={selectClass}>
          <option value="">Default</option>
          {RUN_EFFORTS.map((x) => (
            <option key={x} value={x}>
              {x}
            </option>
          ))}
        </select>
      </div>
      <div className="min-w-[12rem] flex-1">
        <p className={labelClass}>Time budget</p>
        <select
          value={timeBudget}
          onChange={(e) => onTimeBudgetChange(e.target.value)}
          className={selectClass}
        >
          <option value="">Default</option>
          {budgetOptions.map((b) => (
            <option key={b} value={b}>
              {b}
            </option>
          ))}
          <option value="off">No limit</option>
        </select>
      </div>
    </div>
  );
}

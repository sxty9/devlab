import { useEffect, useMemo, useState } from 'react';
import { getDataSource } from '@/data';
import { BUDGET_CHOICES, parseGoDuration, storedBudgetLabel } from '@/views/mercury/tasks/logic';
import type { AiModelCatalog, RunTuning } from '@/types';

/** The native claude reasoning-effort levels, low → max — the same ladder the KI tab (ClaudePanel) offers.
 *  Kept here as the single frontend source so the two never drift. */
export const NATIVE_EFFORTS = ['low', 'medium', 'high', 'xhigh', 'max'] as const;

/** The effort ladder a run or todo may pick: the native levels plus "ultracode", the maximal tier — the
 *  executor runs it at max effort plus multi-agent orchestration. Mirrors runEffortAllowed in the backend. */
export const RUN_EFFORTS = [...NATIVE_EFFORTS, 'ultracode'] as const;

/** The version shape the backend accepts (runModelRe) — anything else would be refused on save. */
const VERSION_RE = /^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$/;

const selectClass =
  'w-full rounded-md border border-separator bg-surface px-3 py-2 text-footnote text-text-primary outline-none focus:border-accent/50';
const labelClass = 'mb-1.5 text-caption font-semibold uppercase tracking-wide text-text-tertiary';

/** The ONE tuning ladder shared by the run and todo editors (REQ-009/REQ-010.1): model (with version),
 *  effort, and the time budget as the equal third tunable — same access point, same storage, same
 *  execution path for both kinds. Every field starts empty; empty REFERS to the service default
 *  (REQ-010.2), so an untouched run follows later default changes. The model choices are the aigentic
 *  catalog the KI tab already reads; the governing default budget is read from the service config and
 *  NAMED on the reference option, so "Default" is never a mystery value. */
export function RunTuningFields({
  value,
  onChange,
}: {
  value: RunTuning;
  onChange: (t: RunTuning) => void;
}) {
  const source = useMemo(() => getDataSource(), []);
  const [models, setModels] = useState<AiModelCatalog['claude']>([]);
  const [defaultBudget, setDefaultBudget] = useState<string | undefined>(undefined);

  useEffect(() => {
    let cancelled = false;
    source
      .assistantModels()
      .then((c) => !cancelled && setModels(c.claude))
      .catch(() => {}); // AI not enabled / offline → only "Default" is offered
    source
      .serviceConfig()
      .then((c) => !cancelled && setDefaultBudget(c.defaultTimeBudget))
      .catch(() => {}); // config unreadable → the reference option stays unnamed
    return () => {
      cancelled = true;
    };
  }, [source]);

  const model = value.model ?? '';
  const modelVersion = value.modelVersion ?? '';
  const effort = value.effort ?? '';
  const budget = value.timeBudget ?? '';

  const set = (patch: Partial<RunTuning>) => {
    const next: RunTuning = { ...value, ...patch };
    // Empty fields REFER to the default — drop them so the wire carries a reference, not "".
    if (!next.model) delete next.model;
    if (!next.modelVersion) delete next.modelVersion;
    if (!next.effort) delete next.effort;
    if (next.timeBudget === undefined || next.timeBudget === '') delete next.timeBudget;
    onChange(next);
  };

  // A stored model the catalog doesn't list (a since-removed model, or the catalog failed to load) must
  // still be selectable, so editing an existing run never silently drops it on save.
  const modelOptions = useMemo(() => {
    if (!model || models.some((m) => m.id === model)) return models;
    return [...models, { id: model, label: model }];
  }, [models, model]);

  // Same for a stored budget outside the ladder (a "45m" set over MCP stays visible and keepable).
  const budgetOptions = useMemo(() => {
    if (!budget || BUDGET_CHOICES.some((c) => c.value === budget)) return BUDGET_CHOICES;
    const parsed = parseGoDuration(budget);
    return [...BUDGET_CHOICES, { value: budget, label: parsed === null ? budget : storedBudgetLabel(budget) }];
  }, [budget]);

  const versionInvalid = modelVersion !== '' && !VERSION_RE.test(modelVersion);

  return (
    <div className="flex flex-col gap-4">
      <div className="flex flex-wrap gap-4">
        <div className="min-w-[12rem] flex-1">
          <p className={labelClass}>Model</p>
          <select value={model} onChange={(e) => set({ model: e.target.value })} className={selectClass}>
            <option value="">Default</option>
            {modelOptions.map((m) => (
              <option key={m.id} value={m.id}>
                {m.label}
              </option>
            ))}
          </select>
        </div>
        <div className="min-w-[10rem] flex-1">
          <p className={labelClass}>Model version</p>
          <input
            value={modelVersion}
            onChange={(e) => set({ modelVersion: e.target.value.trim() })}
            placeholder="Latest"
            className={selectClass}
            aria-invalid={versionInvalid}
          />
          {versionInvalid && (
            <p className="mt-1.5 text-caption text-danger">Letters, digits, dot, underscore, hyphen only.</p>
          )}
        </div>
      </div>
      <div className="flex flex-wrap gap-4">
        <div className="min-w-[12rem] flex-1">
          <p className={labelClass}>Effort</p>
          <select value={effort} onChange={(e) => set({ effort: e.target.value })} className={selectClass}>
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
          <select value={budget} onChange={(e) => set({ timeBudget: e.target.value })} className={selectClass}>
            <option value="">{storedBudgetLabel(undefined, defaultBudget)}</option>
            {budgetOptions.map((c) => (
              <option key={c.value} value={c.value}>
                {c.label}
              </option>
            ))}
          </select>
        </div>
      </div>
    </div>
  );
}

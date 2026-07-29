// ExecutionsView — the execution history: what is FINISHED, newest first (REQ-037).
//
// Membership is not decided here. An execution enters the history exactly when the task surfaces
// consider it closed — ended AND every delivery of it settled (merged, rolled back, or closed with
// a reason) — so this view applies the very selector those surfaces apply (tasks/select.ts) instead
// of restating the rule. Until then the execution stays in the open list: list ∩ history = ∅
// (REQ-011.2, B-8).
//
// Nothing here derives a stage or a shape: every entry renders the server's stage array through the
// shared execution kit, so a failed, a killed and an archive record all render a DEFINED state and
// no click ends in a black screen (REQ-037.5). A record that did not fully succeed can be triggered
// again from here — there are no corpses (REQ-037.2).
import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { getDataSource } from '@/data';
import { cn } from '@/lib/cn';
import { fmtDateTime } from '@/lib/format';
import { Button } from '@/ui/Button';
import { ErrorBoundary } from '@/ui/ErrorBoundary';
import { useToast } from '@/ui/Toast';
import { useLiveTopic } from '@/state/live';
import type { RunResult } from '@/types';
import { executionCompleted, openDeliveryExecutionIds } from '../tasks/select';
import { ExecutionDetail, RequestedBy } from './ExecutionDetail';
import { TonePill } from './PipelineStages';
import { UsageBadge } from './UsageBadge';
import { errMsg, executionOutcome, provenanceChips, retriable, sortExecutions } from './logic';
import { useRunStart } from './StartDialog';

// The opened execution survives a browser reload: same view, same entry (REQ-035).
const SEL_KEY = 'mercury.history.selected';

function readSel(): string | null {
  try {
    return window.localStorage.getItem(SEL_KEY);
  } catch {
    return null;
  }
}
function writeSel(id: string | null) {
  try {
    if (id) window.localStorage.setItem(SEL_KEY, id);
    else window.localStorage.removeItem(SEL_KEY);
  } catch {
    /* best-effort persistence */
  }
}

export function ExecutionsView() {
  const source = useMemo(() => getDataSource(), []);
  const { toast } = useToast();
  const [all, setAll] = useState<RunResult[] | null>(null);
  const [failed, setFailed] = useState<string | null>(null);
  const [selId, setSelId] = useState<string | null>(readSel);
  const [busy, setBusy] = useState(false);

  const gotRef = useRef(false);

  const load = useCallback(async () => {
    try {
      // Both kinds share one machinery, so the history is one list (REQ-005).
      setAll((await source.mercuryRunExecutions()).executions);
      gotRef.current = true;
      setFailed(null);
    } catch (e) {
      // A transient tick never blanks a history that is already on screen.
      if (!gotRef.current) setFailed(errMsg(e));
    }
  }, [source]);

  useEffect(() => {
    void load();
  }, [load]);

  // A merge stamps the completion (B-8) and therefore moves an execution into this list; stage
  // progress ends one. Both arrive over the one live stream — no polling (REQ-034).
  useLiveTopic('progress', load);
  useLiveTopic('deliveries', load);

  const start = useRunStart(load);
  useEffect(() => writeSel(selId), [selId]);

  // A blocked repository is resumed from its own row in the document (REQ-032.4) — the same
  // resumption access point the Active surface uses.
  const resume = useCallback(
    async (runId: string) => {
      setBusy(true);
      try {
        await source.mercuryResumeRun(runId);
        toast({ title: 'Resumed', variant: 'success' });
        await load();
      } catch (e) {
        toast({ title: 'Resuming failed', description: errMsg(e), variant: 'danger' });
      } finally {
        setBusy(false);
      }
    },
    [source, toast, load],
  );

  const history = useMemo(() => {
    const list = all ?? [];
    const open = openDeliveryExecutionIds(list);
    return sortExecutions(list.filter((res) => executionCompleted(res, open)));
  }, [all]);

  if (failed) {
    return (
      <div className="flex h-full min-h-0 w-full items-center justify-center bg-bg-base px-6">
        <p className="max-w-md text-center text-footnote text-text-secondary">{failed}</p>
      </div>
    );
  }
  if (all === null) {
    return (
      <div className="flex h-full min-h-0 w-full items-center justify-center bg-bg-base">
        <p className="text-footnote text-text-tertiary">Loading…</p>
      </div>
    );
  }

  const selected = history.find((res) => res.id === selId) ?? null;
  const openCount = all.length - history.length;

  return (
    <div className="flex h-full min-h-0 w-full">
      {/* LEFT — the finished executions, newest first. */}
      <div className="flex w-96 shrink-0 flex-col border-r border-separator bg-surface">
        <div className="flex items-center justify-between gap-2 border-b border-separator px-3 py-2">
          <span className="text-footnote font-medium text-text-primary">History ({history.length})</span>
          {openCount > 0 && <span className="text-caption text-text-tertiary">{openCount} still open</span>}
        </div>
        <div className="dl-scroll flex-1 overflow-y-auto p-1.5">
          {history.length === 0 ? (
            <p className="px-2.5 py-3 text-caption text-text-tertiary">
              {openCount > 0 ? 'Nothing finished yet — open executions stay in Active until every delivery is settled.' : 'No executions yet.'}
            </p>
          ) : (
            <div className="flex flex-col gap-0.5">
              {history.map((res) => (
                <HistoryRow key={res.id} res={res} selected={res.id === selId} onSelect={() => setSelId(res.id)} />
              ))}
            </div>
          )}
        </div>
      </div>

      {/* RIGHT — the full document. One unreadable record is contained to this pane and recovers as
          soon as another entry is picked (no black screen, REQ-037.5). */}
      <div className="dl-scroll min-h-0 flex-1 overflow-y-auto bg-bg-base">
        <ErrorBoundary resetKeys={[selId]}>
          {selected ? (
            <>
              <div className="flex flex-wrap items-center justify-end gap-2 px-8 pt-6">
                {retriable(selected) && (
                  <Button
                    variant="secondary"
                    size="sm"
                    disabled={start.busyRunId === selected.runId}
                    onClick={() => void start.start({ id: selected.runId, title: selected.runTitle })}
                  >
                    Run again
                  </Button>
                )}
              </div>
              <ExecutionDetail
                key={selected.id}
                runId={selected.runId}
                resultId={selected.id}
                onDeliver={() => void start.start({ id: selected.runId, title: selected.runTitle })}
                onResume={() => void resume(selected.runId)}
                busy={busy || start.busyRunId === selected.runId}
              />
            </>
          ) : (
            <div className="flex h-full min-h-0 items-center justify-center p-8">
              <p className="max-w-md text-center text-footnote text-text-tertiary">
                Pick an execution to see its stages, its report and its consumption.
              </p>
            </div>
          )}
        </ErrorBoundary>
      </div>

      {start.render()}
    </div>
  );
}

/** One history row: outcome, run, time, repositories, provenance and consumption. */
function HistoryRow({ res, selected, onSelect }: { res: RunResult; selected: boolean; onSelect: () => void }) {
  const outcome = executionOutcome(res);
  const chips = provenanceChips(res);
  return (
    <button
      type="button"
      onClick={onSelect}
      className={cn(
        'flex w-full flex-col gap-1 rounded-md px-2.5 py-2 text-left transition duration-fast',
        selected ? 'bg-accent/15' : 'hover:bg-fill/10',
      )}
    >
      <div className="flex items-center gap-1.5">
        <span className={cn('min-w-0 flex-1 truncate text-footnote font-medium', selected ? 'text-text-primary' : 'text-text-secondary')}>
          {res.runTitle || res.runId}
        </span>
        <span className="shrink-0 rounded bg-fill/15 px-1.5 py-0.5 text-caption font-medium text-text-tertiary">
          {res.kind === 'todo' ? 'Todo' : 'Auto'}
        </span>
        <TonePill label={outcome.label} tone={outcome.tone} pulse={outcome.pulse} />
      </div>
      <span className="text-caption text-text-tertiary">
        {fmtDateTime(res.endedAt || res.startedAt)} · {res.repos.length} {res.repos.length === 1 ? 'repo' : 'repos'}
        {chips.length > 0 ? ` · ${chips.join(' · ')}` : ''}
      </span>
      <RequestedBy requested={res.requested} />
      <UsageBadge usage={res.usage} />
    </button>
  );
}

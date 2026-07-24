import { useEffect, useMemo, useState } from 'react';
import { getDataSource } from '@/data';
import { useToast } from '@/ui/Toast';
import { Button } from '@/ui/Button';
import { cn } from '@/lib/cn';
import { RocketIcon, RefreshIcon, ChevronRightIcon } from '@/ui/icons';
import type { RunExecution, RunResult, RunResultRef, RepoResult, RunStep, RunType } from '@/types';

/** Shared execution-history kit for Mercury's parallel surfaces — Automatische Läufe and Konkrete
 *  ToDos. Both run on the SAME machinery (store, executor, results), so their history is rendered by
 *  ONE set of components here rather than duplicated per view; each surface simply asks for its own
 *  kind via `type`. This is the Reuse-before-Build home that keeps the two tabs symmetric. */

/** Uniform error-to-string, mirroring the rest of the Mercury surface. */
const msg = (e: unknown) => String((e as Error)?.message ?? e);

/** A localized timestamp, or an em dash when absent/unparseable. */
export function fmtDateTime(iso?: string): string {
  if (!iso) return '—';
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return '—';
  return d.toLocaleString('de-DE');
}

export const fmtNum = (n: number) => n.toLocaleString('de-DE');
export const fmtCost = (n: number) => `$${n.toFixed(4)}`;

/** Compact input/output token + cost readout, shown wherever an execution/result appears. */
export function TokenStat({ input, output, cost }: { input?: number; output?: number; cost?: number }) {
  if (input == null && output == null && cost == null) return null;
  return (
    <span className="flex flex-wrap items-center gap-x-2 gap-y-0.5 text-caption text-text-tertiary">
      {input != null && <span title="Eingabe-Tokens">↓ {fmtNum(input)}</span>}
      {output != null && <span title="Ausgabe-Tokens">↑ {fmtNum(output)}</span>}
      {cost != null && <span className="font-medium text-text-secondary">{fmtCost(cost)}</span>}
    </span>
  );
}

/** A green/red status pill. */
export function OkPill({ ok }: { ok: boolean }) {
  return (
    <span
      className={cn('shrink-0 rounded px-1.5 py-0.5 text-caption font-medium', ok ? 'bg-success/15 text-success' : 'bg-danger/15 text-danger')}
    >
      {ok ? 'OK' : 'Fehler'}
    </span>
  );
}

/** Shown in a pane when nothing is selected. */
export function EmptyPlaceholder({ text }: { text: string }) {
  return (
    <div className="flex h-full flex-col items-center justify-center gap-3 text-center">
      <span className="flex h-12 w-12 items-center justify-center rounded-2xl bg-surface-raised shadow-elev-1 ring-1 ring-separator">
        <RocketIcon className="h-6 w-6 text-accent" />
      </span>
      <p className="max-w-xs text-footnote text-text-tertiary">{text}</p>
    </div>
  );
}

/** One step (analyze/implement/push/pr/deploy). analyze/implement carry the agent's report (`log`),
 *  shown as a collapsible Bericht. */
export function StepRow({ step }: { step: RunStep }) {
  const [open, setOpen] = useState(false);
  const hasReport = !!step.log;

  return (
    <div>
      <button
        type="button"
        disabled={!hasReport}
        onClick={() => hasReport && setOpen((o) => !o)}
        className={cn('flex w-full items-center gap-2 rounded-md px-1 py-1 text-left transition', hasReport ? 'hover:bg-fill/10' : 'cursor-default')}
      >
        {hasReport ? (
          <ChevronRightIcon className={cn('h-3.5 w-3.5 shrink-0 text-text-tertiary transition-transform duration-fast', open && 'rotate-90')} />
        ) : (
          <span className="w-3.5 shrink-0" />
        )}
        <span className={cn('h-2 w-2 shrink-0 rounded-full', step.ok ? 'bg-success' : 'bg-danger')} />
        <span className="flex-1 truncate text-footnote font-medium text-text-secondary">{step.name}</span>
        {hasReport && <span className="shrink-0 text-caption text-text-tertiary">Bericht</span>}
      </button>
      {open && hasReport && (
        <pre className="dl-scroll mt-1 max-h-80 overflow-auto whitespace-pre-wrap rounded-md border border-separator bg-bg-base p-3 font-mono text-caption text-text-secondary">
          {step.log}
        </pre>
      )}
    </div>
  );
}

/** One repository within an execution: status, deploy/PR, per-repo tokens, and its steps. */
export function RepoBlock({ repo }: { repo: RepoResult }) {
  return (
    <div className="rounded-card border border-separator bg-surface p-3">
      <div className="flex flex-wrap items-center gap-2">
        <span className="min-w-0 flex-1 truncate font-mono text-footnote font-medium text-text-primary">{repo.repo}</span>
        <OkPill ok={repo.ok} />
        <span
          className={cn(
            'rounded px-1.5 py-0.5 text-caption font-medium',
            repo.deployed ? 'bg-success/15 text-success' : 'bg-fill/15 text-text-tertiary',
          )}
        >
          {repo.deployed ? 'deployed' : 'nicht deployed'}
        </span>
        {repo.prUrl && (
          <a href={repo.prUrl} target="_blank" rel="noreferrer" className="text-caption font-medium text-accent hover:underline">
            PR öffnen
          </a>
        )}
      </div>

      <div className="mt-1.5">
        <TokenStat input={repo.inputTokens} output={repo.outputTokens} cost={repo.costUsd} />
      </div>

      {repo.error && <p className="mt-2 rounded-md bg-danger/10 px-2.5 py-1.5 text-caption text-danger">{repo.error}</p>}

      {repo.steps.length > 0 && (
        <div className="mt-3 flex flex-col gap-1.5 border-t border-separator pt-3">
          {repo.steps.map((step, i) => (
            <StepRow key={`${step.name}:${i}`} step={step} />
          ))}
        </div>
      )}
    </div>
  );
}

/** The full execution document as a page: title, totals, then each repo with its steps and reports.
 *  The right pane of an aggregate ExecutionHistory. */
export function ExecutionDetail({ runId, resultId }: { runId: string; resultId: string }) {
  const source = useMemo(() => getDataSource(), []);
  const [res, setRes] = useState<RunResult | null>(null);
  const [err, setErr] = useState(false);

  useEffect(() => {
    let cancelled = false;
    setRes(null);
    setErr(false);
    source
      .mercuryRunResult(runId, resultId)
      .then((r) => !cancelled && setRes(r))
      .catch(() => !cancelled && setErr(true));
    return () => {
      cancelled = true;
    };
  }, [source, runId, resultId]);

  if (err) return <p className="px-8 py-7 text-footnote text-text-secondary">Diese Ausführung konnte nicht geladen werden.</p>;
  if (!res) return <p className="px-8 py-7 text-footnote text-text-tertiary">Lädt…</p>;

  return (
    <article className="mx-auto max-w-3xl px-8 py-7">
      <div className="flex items-start justify-between gap-4">
        <div className="min-w-0">
          <h1 className="text-title3 font-semibold tracking-tight text-text-primary">{res.runName ?? 'Ausführung'}</h1>
          <p className="mt-1 text-footnote text-text-secondary">
            {fmtDateTime(res.startedAt)}
            {res.finishedAt ? ` – ${new Date(res.finishedAt).toLocaleTimeString('de-DE', { hour: '2-digit', minute: '2-digit' })}` : ''}
          </p>
        </div>
        <OkPill ok={res.ok} />
      </div>

      <div className="mt-4 flex flex-wrap items-center gap-x-4 gap-y-1 rounded-card border border-separator bg-surface px-3 py-2">
        <span className="text-caption text-text-tertiary">{res.numTurns} Turns</span>
        <TokenStat input={res.inputTokens} output={res.outputTokens} cost={res.costUsd} />
      </div>

      <section className="mt-6 flex flex-col gap-4">
        {res.repos.length === 0 ? (
          <p className="text-footnote text-text-tertiary">Keine Repositories in dieser Ausführung.</p>
        ) : (
          res.repos.map((repo, i) => <RepoBlock key={`${repo.repo}:${i}`} repo={repo} />)
        )}
      </section>
    </article>
  );
}

/** One row in the aggregate execution list: run, start, status, repos and the token/cost readout. */
function ExecutionRow({ ex, selected, onSelect }: { ex: RunExecution; selected: boolean; onSelect: () => void }) {
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
          {ex.runName}
        </span>
        <OkPill ok={ex.ok} />
      </div>
      <span className="text-caption text-text-tertiary">
        {fmtDateTime(ex.at)} · {ex.repoCount} Repos
      </span>
      <TokenStat input={ex.inputTokens} output={ex.outputTokens} cost={ex.costUsd} />
    </button>
  );
}

/** The execution history for one surface: every completed execution of its `type` (newest first) with
 *  token/cost, and a detail pane that loads the full result document. Reused identically by the Läufe
 *  and ToDos tabs — only `type` differs. */
export function ExecutionHistory({ type }: { type: RunType }) {
  const source = useMemo(() => getDataSource(), []);
  const { toast } = useToast();
  const [executions, setExecutions] = useState<RunExecution[] | null>(null);
  const [failed, setFailed] = useState<string | null>(null);
  const [sel, setSel] = useState<{ runId: string; resultId: string } | null>(null);
  const [refreshing, setRefreshing] = useState(false);

  useEffect(() => {
    let cancelled = false;
    source
      .mercuryRunExecutions(type)
      .then((r) => !cancelled && setExecutions(r.executions))
      .catch((e) => !cancelled && setFailed(msg(e)));
    return () => {
      cancelled = true;
    };
  }, [source, type]);

  const refresh = async () => {
    if (refreshing) return;
    setRefreshing(true);
    try {
      const r = await source.mercuryRunExecutions(type);
      setExecutions(r.executions);
    } catch (e) {
      toast({ title: 'Aktualisieren fehlgeschlagen', description: msg(e), variant: 'danger' });
    } finally {
      setRefreshing(false);
    }
  };

  if (failed) {
    return (
      <div className="flex min-h-0 flex-1 items-center justify-center bg-bg-base px-6">
        <p className="max-w-md text-center text-footnote text-text-secondary">{failed}</p>
      </div>
    );
  }

  return (
    <>
      <div className="flex w-96 shrink-0 flex-col border-r border-separator bg-surface">
        <div className="flex items-center justify-between gap-2 border-b border-separator px-3 py-2">
          <span className="text-footnote font-medium text-text-primary">Ausführungen</span>
          <Button variant="ghost" size="sm" disabled={refreshing} onClick={refresh}>
            <RefreshIcon className={cn('h-4 w-4', refreshing && 'animate-spin')} /> Aktualisieren
          </Button>
        </div>
        <div className="dl-scroll flex-1 overflow-y-auto p-1.5">
          {executions === null ? (
            <p className="px-2.5 py-3 text-caption text-text-tertiary">Lädt…</p>
          ) : executions.length === 0 ? (
            <p className="px-2.5 py-3 text-caption text-text-tertiary">Noch keine Ausführungen.</p>
          ) : (
            <div className="flex flex-col gap-0.5">
              {executions.map((ex) => (
                <ExecutionRow
                  key={ex.resultId}
                  ex={ex}
                  selected={sel?.resultId === ex.resultId}
                  onSelect={() => setSel({ runId: ex.runId, resultId: ex.resultId })}
                />
              ))}
            </div>
          )}
        </div>
      </div>

      <div className="dl-scroll min-h-0 flex-1 overflow-y-auto bg-bg-base">
        {sel ? (
          <ExecutionDetail key={`${sel.runId}:${sel.resultId}`} runId={sel.runId} resultId={sel.resultId} />
        ) : (
          <EmptyPlaceholder text="Wähle links eine Ausführung, um Bericht, Schritte und Token-Verbrauch zu sehen." />
        )}
      </div>
    </>
  );
}

/** The full execution document inline (compact) — totals row, then each repo. Used UNDER an expanded
 *  row of a per-entity ExecutionList, where the entity (run/ToDo) is already the page. */
function InlineExecutionDetail({ runId, resultId }: { runId: string; resultId: string }) {
  const source = useMemo(() => getDataSource(), []);
  const [res, setRes] = useState<RunResult | null>(null);
  const [err, setErr] = useState(false);

  useEffect(() => {
    let cancelled = false;
    setRes(null);
    setErr(false);
    source
      .mercuryRunResult(runId, resultId)
      .then((r) => !cancelled && setRes(r))
      .catch(() => !cancelled && setErr(true));
    return () => {
      cancelled = true;
    };
  }, [source, runId, resultId]);

  if (err) return <p className="px-2 py-2 text-caption text-text-secondary">Diese Ausführung konnte nicht geladen werden.</p>;
  if (!res) return <p className="px-2 py-2 text-caption text-text-tertiary">Lädt…</p>;

  return (
    <div className="mt-1.5 flex flex-col gap-3 pl-5">
      <div className="flex flex-wrap items-center gap-x-4 gap-y-1 rounded-card border border-separator bg-surface px-3 py-2">
        <span className="text-caption text-text-tertiary">
          {fmtDateTime(res.startedAt)}
          {res.finishedAt ? ` – ${new Date(res.finishedAt).toLocaleTimeString('de-DE', { hour: '2-digit', minute: '2-digit' })}` : ''}
        </span>
        <span className="text-caption text-text-tertiary">{res.numTurns} Turns</span>
        <TokenStat input={res.inputTokens} output={res.outputTokens} cost={res.costUsd} />
      </div>
      {res.repos.length === 0 ? (
        <p className="text-caption text-text-tertiary">Keine Repositories in dieser Ausführung.</p>
      ) : (
        res.repos.map((repo, i) => <RepoBlock key={`${repo.repo}:${i}`} repo={repo} />)
      )}
    </div>
  );
}

/** The executions of ONE entity (run or ToDo): a row per result (time, status, token/cost); selecting
 *  one expands the full result document inline. The per-entity drill-down that lives in a detail pane
 *  (distinct from the cross-entity ExecutionHistory tab). */
export function ExecutionList({ runId, results }: { runId: string; results: RunResultRef[] | null }) {
  const [openId, setOpenId] = useState<string | null>(null);

  if (results === null) return <p className="text-footnote text-text-tertiary">Lädt…</p>;
  if (results.length === 0) return <p className="text-footnote text-text-tertiary">Noch keine Ausführungen.</p>;

  return (
    <ul className="flex flex-col gap-1.5">
      {results.map((r) => {
        const open = openId === r.resultId;
        return (
          <li key={r.resultId}>
            <button
              type="button"
              onClick={() => setOpenId(open ? null : r.resultId)}
              className={cn(
                'flex w-full flex-wrap items-center gap-x-3 gap-y-1 rounded-md px-2 py-1.5 text-left transition duration-fast',
                open ? 'bg-accent/10' : 'hover:bg-fill/10',
              )}
            >
              <ChevronRightIcon className={cn('h-3.5 w-3.5 shrink-0 text-text-tertiary transition-transform duration-fast', open && 'rotate-90')} />
              <span className="text-footnote text-text-secondary">{fmtDateTime(r.at)}</span>
              <OkPill ok={r.ok} />
              <TokenStat input={r.inputTokens} output={r.outputTokens} cost={r.costUsd} />
            </button>
            {open && <InlineExecutionDetail key={r.resultId} runId={runId} resultId={r.resultId} />}
          </li>
        );
      })}
    </ul>
  );
}

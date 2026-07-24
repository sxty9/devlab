import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { getDataSource } from '@/data';
import { useToast } from '@/ui/Toast';
import { Button } from '@/ui/Button';
import { cn } from '@/lib/cn';
import { renderMarkdown } from '@/lib/markdown';
import { RocketIcon, RefreshIcon, ChevronRightIcon } from '@/ui/icons';
import type { RunActive, RunExecution, RunResult, RunResultRef, RepoResult, RunStep, RunType } from '@/types';

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
 *  shown as a collapsible Bericht. While a step is `running`, its log is the agent's LIVE transcript:
 *  the step auto-expands, renders the transcript preformatted (not markdown) and follows the tail. */
export function StepRow({ step }: { step: RunStep }) {
  const running = !!step.running;
  const [open, setOpen] = useState(false);
  const hasReport = !!step.log;
  const show = running || open; // a running step is always expanded — the point is to watch it
  const logRef = useRef<HTMLPreElement>(null);

  // Keep the live transcript scrolled to the newest line as it streams in.
  useEffect(() => {
    if (running && logRef.current) logRef.current.scrollTop = logRef.current.scrollHeight;
  }, [running, step.log]);

  return (
    <div>
      <button
        type="button"
        disabled={!hasReport && !running}
        onClick={() => hasReport && setOpen((o) => !o)}
        className={cn('flex w-full items-center gap-2 rounded-md px-1 py-1 text-left transition', hasReport || running ? 'hover:bg-fill/10' : 'cursor-default')}
      >
        {hasReport || running ? (
          <ChevronRightIcon className={cn('h-3.5 w-3.5 shrink-0 text-text-tertiary transition-transform duration-fast', show && 'rotate-90')} />
        ) : (
          <span className="w-3.5 shrink-0" />
        )}
        <span className={cn('h-2 w-2 shrink-0 rounded-full', running ? 'animate-pulse bg-warning' : step.ok ? 'bg-success' : 'bg-danger')} />
        <span className="flex-1 truncate text-footnote font-medium text-text-secondary">{step.name}</span>
        <span className="shrink-0 text-caption text-text-tertiary">{running ? 'läuft…' : hasReport ? 'Bericht' : ''}</span>
      </button>
      {show &&
        (running ? (
          <pre
            ref={logRef}
            className="dl-scroll mt-1 max-h-80 overflow-auto whitespace-pre-wrap rounded-md border border-separator bg-bg-base p-3 font-mono text-caption text-text-secondary"
          >
            {step.log ? step.log : 'Der Agent arbeitet…'}
          </pre>
        ) : (
          hasReport && (
            <div
              className="dl-markdown dl-scroll mt-1 max-h-80 overflow-auto rounded-md border border-separator bg-bg-base p-3 text-caption text-text-secondary"
              dangerouslySetInnerHTML={{ __html: renderMarkdown(step.log ?? '') }}
            />
          )
        ))}
    </div>
  );
}

/** One repository within an execution: status, deploy/PR, per-repo tokens, and its steps. A `running`
 *  repo (the one in flight, from RunResult.live) wears an amber "läuft" pill instead of a status. */
export function RepoBlock({ repo }: { repo: RepoResult }) {
  const running = !!repo.running;
  return (
    <div className={cn('rounded-card border bg-surface p-3', running ? 'border-warning/40' : 'border-separator')}>
      <div className="flex flex-wrap items-center gap-2">
        <span className="min-w-0 flex-1 truncate font-mono text-footnote font-medium text-text-primary">{repo.repo}</span>
        {running ? (
          <span className="flex shrink-0 items-center gap-1.5 rounded bg-warning/15 px-1.5 py-0.5 text-caption font-medium text-warning">
            <span className="h-1.5 w-1.5 animate-pulse rounded-full bg-warning" /> läuft
          </span>
        ) : (
          <OkPill ok={repo.ok} />
        )}
        {!running && (
          <span
            className={cn(
              'rounded px-1.5 py-0.5 text-caption font-medium',
              repo.deployed ? 'bg-success/15 text-success' : 'bg-fill/15 text-text-tertiary',
            )}
          >
            {repo.deployed ? 'deployed' : 'nicht deployed'}
          </span>
        )}
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

/** The run's Promptstellung for one execution: the exact prompt the agent was driven by, snapshotted at
 *  run time and rendered as Markdown. Collapsed by default — an auto run's prompt folds in every axiom
 *  and can be long, so it stays portioned until asked for, mirroring a step's Bericht. Renders nothing
 *  when the execution predates prompt capture. Execution-level (shown once), never repeated per repo. */
export function PromptDisclosure({ prompt }: { prompt?: string }) {
  const [open, setOpen] = useState(false);
  if (!prompt) return null;
  return (
    <div className="rounded-card border border-separator bg-surface">
      <button
        type="button"
        onClick={() => setOpen((o) => !o)}
        className="flex w-full items-center gap-2 rounded-card px-3 py-2 text-left transition hover:bg-fill/10"
      >
        <ChevronRightIcon className={cn('h-3.5 w-3.5 shrink-0 text-text-tertiary transition-transform duration-fast', open && 'rotate-90')} />
        <span className="flex-1 truncate text-footnote font-medium text-text-secondary">Promptstellung</span>
      </button>
      {open && (
        <div
          className="dl-markdown dl-scroll mx-3 mb-3 max-h-80 overflow-auto rounded-md border border-separator bg-bg-base p-3 text-caption text-text-secondary"
          dangerouslySetInnerHTML={{ __html: renderMarkdown(prompt) }}
        />
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

      {res.prompt && (
        <div className="mt-4">
          <PromptDisclosure prompt={res.prompt} />
        </div>
      )}

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
      <PromptDisclosure prompt={res.prompt} />
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

/** Poll the server for the run executing right now (its id + live result id), or null. This is the
 *  single source of truth for "a run is live": read on mount it RESTORES the running state after a page
 *  reload (so a just-started run no longer looks like it never started), and it drives the live-follow
 *  view. refetch() forces an immediate re-check — e.g. right after starting a run — so the UI reacts
 *  without waiting for the next tick. Reflects an actually-running process: empty again after a restart. */
export function useActiveRun(): { active: RunActive | null; refetch: () => void } {
  const source = useMemo(() => getDataSource(), []);
  const [active, setActive] = useState<RunActive | null>(null);
  const [bump, setBump] = useState(0);

  useEffect(() => {
    let cancelled = false;
    let timer: number | undefined;
    const poll = async () => {
      try {
        const r = await source.mercuryRunActive();
        if (!cancelled) setActive(r.active);
      } catch {
        /* transient — keep the last known state */
      }
      if (!cancelled) timer = window.setTimeout(poll, 2500);
    };
    void poll();
    const onFocus = () => setBump((b) => b + 1); // re-check when the tab regains focus
    window.addEventListener('focus', onFocus);
    return () => {
      cancelled = true;
      if (timer) window.clearTimeout(timer);
      window.removeEventListener('focus', onFocus);
    };
  }, [source, bump]);

  return { active, refetch: useCallback(() => setBump((b) => b + 1), []) };
}

/** Follow a run LIVE: poll its in-flight result document and render it — the totals, the repos already
 *  finished, and the one in flight (its running steps and the agent's streaming output). Polls only
 *  while `live`; once the run settles it fetches the final state once and stops, so the finished result
 *  stays on screen. Reuses RepoBlock/StepRow, so a live run and a past one look the same — just moving. */
export function LiveExecution({ runId, resultId, live }: { runId: string; resultId: string; live: boolean }) {
  const source = useMemo(() => getDataSource(), []);
  const [res, setRes] = useState<RunResult | null>(null);
  const [err, setErr] = useState(false);
  const resRef = useRef<RunResult | null>(null);

  useEffect(() => {
    let cancelled = false;
    let timer: number | undefined;
    const poll = async () => {
      try {
        const r = await source.mercuryRunResult(runId, resultId);
        if (!cancelled) {
          resRef.current = r;
          setRes(r);
          setErr(false);
        }
      } catch {
        if (!cancelled && !resRef.current) setErr(true); // only surface if we never got a document
      }
      if (!cancelled && live) timer = window.setTimeout(poll, 2000);
    };
    void poll();
    return () => {
      cancelled = true;
      if (timer) window.clearTimeout(timer);
    };
  }, [source, runId, resultId, live]);

  if (!res) {
    return <p className="text-footnote text-text-tertiary">{err ? 'Live-Ausführung wird vorbereitet…' : 'Lädt…'}</p>;
  }

  const repos = res.repos ?? [];
  return (
    <div className="flex flex-col gap-3 rounded-card border border-warning/30 bg-warning/5 p-3">
      <div className="flex flex-wrap items-center gap-x-3 gap-y-1">
        <span className="flex items-center gap-1.5 text-footnote font-medium text-text-primary">
          <span className="h-2 w-2 animate-pulse rounded-full bg-warning" /> {live ? 'Läuft gerade' : 'Gerade beendet'}
        </span>
        <span className="text-caption text-text-tertiary">seit {fmtDateTime(res.startedAt)}</span>
        <span className="text-caption text-text-tertiary">{res.numTurns} Turns</span>
        <TokenStat input={res.inputTokens} output={res.outputTokens} cost={res.costUsd} />
      </div>
      <PromptDisclosure prompt={res.prompt} />
      {repos.map((repo, i) => (
        <RepoBlock key={`${repo.repo}:${i}`} repo={repo} />
      ))}
      {res.live && <RepoBlock key="live" repo={res.live} />}
      {repos.length === 0 && !res.live && <p className="text-caption text-text-tertiary">Der Lauf startet…</p>}
    </div>
  );
}

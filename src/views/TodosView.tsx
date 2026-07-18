import { useCallback, useEffect, useMemo, useRef, useState, type ReactNode } from 'react';
import { getDataSource } from '@/data';
import { useToast } from '@/ui/Toast';
import { Button } from '@/ui/Button';
import { cn } from '@/lib/cn';
import { RocketIcon, PlusIcon, RefreshIcon, ChevronRightIcon, PlayIcon, CheckIcon } from '@/ui/icons';
import { MercuryCalendar } from './MercuryCalendar';
import type { Run, RunInput, RunResultRef, RunResult, RepoResult, RunStep, Repo, RunCalendar } from '@/types';

/** Uniform error-to-string, mirroring the rest of the Mercury surface. */
const msg = (e: unknown) => String((e as Error)?.message ?? e);

type TabId = 'liste' | 'kalender';
const TABS: { id: TabId; label: string }[] = [
  { id: 'liste', label: 'Liste' },
  { id: 'kalender', label: 'Kalender' },
];

/** A new repo becomes a GitHub repo and a deploy-allowlist key; the backend bounds it the same way. */
const NEW_REPO_RE = /^[A-Za-z0-9._-]{1,100}$/;

/** A localized timestamp, or an em dash when absent/unparseable. */
function fmtDateTime(iso?: string): string {
  if (!iso) return '—';
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return '—';
  return d.toLocaleString('de-DE');
}

const fmtNum = (n: number) => n.toLocaleString('de-DE');
const fmtCost = (n: number) => `$${n.toFixed(4)}`;

/** A ToDo's single target: an existing Holistic repo, or a repo to be created. */
function targetLabel(todo: Run, repos: Repo[]): string {
  if (todo.newRepo) return `neu: ${todo.newRepo}`;
  if (todo.repo) return repos.find((r) => r.id === todo.repo)?.name ?? todo.repo;
  return '—';
}

/** A ToDo without a due date only ever runs on demand. */
const dueLabel = (todo: Run) => (todo.dueAt ? fmtDateTime(todo.dueAt) : 'manuell');

/** ISO → the `YYYY-MM-DDTHH:mm` a datetime-local input wants, in the viewer's timezone. */
function isoToLocalInput(iso?: string): string {
  if (!iso) return '';
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return '';
  const pad = (n: number) => String(n).padStart(2, '0');
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}`;
}

/** A datetime-local value (local time, no offset) → ISO; '' → null, i.e. "manual only". */
function localInputToIso(v: string): string | null {
  if (!v) return null;
  const d = new Date(v);
  if (Number.isNaN(d.getTime())) return null;
  return d.toISOString();
}

/** Compact input/output token + cost readout, shown wherever an execution/result appears. */
function TokenStat({ input, output, cost }: { input?: number; output?: number; cost?: number }) {
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
function OkPill({ ok }: { ok: boolean }) {
  return (
    <span
      className={cn('shrink-0 rounded px-1.5 py-0.5 text-caption font-medium', ok ? 'bg-success/15 text-success' : 'bg-danger/15 text-danger')}
    >
      {ok ? 'OK' : 'Fehler'}
    </span>
  );
}

/** TodosView — Mercury's "Konkrete ToDos". A ToDo is the same machinery as an automatic run (same
 *  store, executor, results), but one-time and hand-planned: a free-text task against exactly ONE
 *  repo (existing or to-be-created), with an optional due date. The axioms reach the agent through
 *  the repo's CLAUDE.md, so there is no axiom picker here. Owns its own data; the parent only gives
 *  it a height box. */
export default function TodosView() {
  const source = useMemo(() => getDataSource(), []);
  const { toast } = useToast();

  const [tab, setTab] = useState<TabId>('liste');

  const [todos, setTodos] = useState<Run[] | null>(null);
  const [repos, setRepos] = useState<Repo[]>([]);
  const [failed, setFailed] = useState<string | null>(null);
  // Bumped on every ToDo mutation; the calendar depends on it to refetch.
  const [dataVersion, setDataVersion] = useState(0);

  const [selectedId, setSelectedId] = useState<string | null>(null);
  const [mode, setMode] = useState<'view' | 'create' | 'edit'>('view');
  const [refreshing, setRefreshing] = useState(false);

  // A ToDo may be executing (started via "Jetzt ausführen"); the cancel affordance appears while so.
  const [running, setRunning] = useState(false);
  const [cancelling, setCancelling] = useState(false);
  const runTimeoutRef = useRef<number | null>(null);

  // Runs without a `type` predate ToDos and are automatic runs — they belong to RunsView.
  const reload = useCallback(async () => {
    try {
      const l = await source.mercuryRuns();
      setTodos(l.runs.filter((r) => r.type === 'todo'));
      setDataVersion((v) => v + 1);
    } catch (e) {
      toast({ title: 'ToDos konnten nicht geladen werden', description: msg(e), variant: 'danger' });
    }
  }, [source, toast]);

  // First load owns the full-pane error state; a failing repo list only costs the target picker.
  useEffect(() => {
    let cancelled = false;
    void (async () => {
      try {
        const l = await source.mercuryRuns();
        if (cancelled) return;
        setTodos(l.runs.filter((r) => r.type === 'todo'));
      } catch (e) {
        if (!cancelled) setFailed(msg(e));
        return;
      }
      try {
        const rs = await source.repos();
        if (!cancelled) setRepos(rs);
      } catch (e) {
        if (!cancelled) toast({ title: 'Repos konnten nicht geladen werden', description: msg(e), variant: 'danger' });
      }
    })();
    return () => {
      cancelled = true;
    };
  }, [source, toast]);

  useEffect(
    () => () => {
      if (runTimeoutRef.current) window.clearTimeout(runTimeoutRef.current);
    },
    [],
  );

  // Mark a ToDo as in-flight and auto-clear after a ceiling (execution is detached; we can't observe
  // completion precisely). Cancel or the ceiling ends the "aktiv" banner.
  const startRunning = useCallback(() => {
    setRunning(true);
    if (runTimeoutRef.current) window.clearTimeout(runTimeoutRef.current);
    runTimeoutRef.current = window.setTimeout(() => setRunning(false), 180000);
  }, []);

  const cancelRun = useCallback(async () => {
    if (cancelling) return;
    setCancelling(true);
    try {
      await source.mercuryCancelRun();
      toast({ title: 'Lauf abgebrochen', variant: 'default' });
      if (runTimeoutRef.current) window.clearTimeout(runTimeoutRef.current);
      setRunning(false);
    } catch (e) {
      toast({ title: 'Abbrechen fehlgeschlagen', description: msg(e), variant: 'danger' });
    } finally {
      setCancelling(false);
    }
  }, [cancelling, source, toast]);

  const refresh = useCallback(async () => {
    if (refreshing) return;
    setRefreshing(true);
    await reload();
    setRefreshing(false);
  }, [refreshing, reload]);

  const handleSaved = useCallback(
    async (id: string) => {
      setSelectedId(id);
      setMode('view');
      await reload();
    },
    [reload],
  );

  const handleDeleted = useCallback(async () => {
    setSelectedId(null);
    setMode('view');
    await reload();
  }, [reload]);

  if (failed) {
    return (
      <div className="flex h-full min-h-0 items-center justify-center bg-bg-base px-6">
        <p className="max-w-md text-center text-footnote text-text-secondary">{failed}</p>
      </div>
    );
  }
  if (!todos) {
    return (
      <div className="flex h-full min-h-0 items-center justify-center bg-bg-base">
        <p className="text-footnote text-text-tertiary">Lädt…</p>
      </div>
    );
  }

  const selectedTodo = selectedId ? todos.find((t) => t.id === selectedId) ?? null : null;

  let rightPane: ReactNode;
  if (mode === 'create') {
    rightPane = <TodoEditor base={null} repos={repos} onCancel={() => setMode('view')} onSaved={handleSaved} />;
  } else if (mode === 'edit' && selectedTodo) {
    rightPane = (
      <TodoEditor key={selectedTodo.id} base={selectedTodo} repos={repos} onCancel={() => setMode('view')} onSaved={handleSaved} />
    );
  } else if (selectedTodo) {
    rightPane = (
      <TodoDetail
        key={`${selectedTodo.id}:${selectedTodo.updatedAt}`}
        todo={selectedTodo}
        repos={repos}
        onEdit={() => setMode('edit')}
        onDeleted={handleDeleted}
        onRunStarted={startRunning}
        onExecuted={reload}
      />
    );
  } else {
    rightPane = <EmptyPlaceholder text="Wähle links ein ToDo oder lege ein neues an." />;
  }

  return (
    // w-full: MercuryView's <main> is a flex ROW — without it this view shrinks to its content width.
    <div className="flex h-full min-h-0 w-full flex-col">
      <header className="flex items-center gap-3 border-b border-separator bg-surface px-3 py-2">
        <div className="inline-flex items-center gap-0.5 rounded-md bg-fill/10 p-0.5">
          {TABS.map((t) => (
            <button
              key={t.id}
              type="button"
              onClick={() => setTab(t.id)}
              className={cn(
                'rounded px-3 py-1 text-caption font-medium transition duration-fast',
                tab === t.id ? 'bg-surface-raised text-text-primary shadow-elev-1' : 'text-text-secondary hover:text-text-primary',
              )}
            >
              {t.label}
            </button>
          ))}
        </div>
      </header>

      <div className="flex min-h-0 flex-1">
        {tab === 'kalender' ? (
          <TodoCalendar dataVersion={dataVersion} />
        ) : (
          <>
            {/* LEFT — toolbar + todo list */}
            <div className="flex w-80 shrink-0 flex-col border-r border-separator bg-surface">
              <div className="flex flex-col gap-2 border-b border-separator p-2">
                <Button
                  variant="primary"
                  size="sm"
                  onClick={() => {
                    setSelectedId(null);
                    setMode('create');
                  }}
                >
                  <PlusIcon className="h-4 w-4" /> Neues ToDo
                </Button>
                <div className="flex items-center gap-1.5">
                  <Button variant="ghost" size="sm" disabled={refreshing} onClick={refresh}>
                    <RefreshIcon className={cn('h-4 w-4', refreshing && 'animate-spin')} /> Aktualisieren
                  </Button>
                </div>
                {running && (
                  <div className="flex items-center justify-between gap-2 rounded-md bg-warning/10 px-2 py-1.5">
                    <span className="flex items-center gap-1.5 text-caption text-text-secondary">
                      <span className="h-2 w-2 animate-pulse rounded-full bg-warning" /> Lauf aktiv
                    </span>
                    <Button variant="danger" size="sm" disabled={cancelling} onClick={cancelRun}>
                      {cancelling ? 'Bricht ab…' : 'Abbrechen'}
                    </Button>
                  </div>
                )}
              </div>

              <div className="dl-scroll flex-1 overflow-y-auto p-1.5">
                {todos.length === 0 ? (
                  <p className="px-2.5 py-3 text-caption text-text-tertiary">
                    Noch keine ToDos. Lege eins an für einen Ad-hoc-Fix oder einen neuen Service.
                  </p>
                ) : (
                  <div className="flex flex-col gap-0.5">
                    {todos.map((todo) => (
                      <TodoRow
                        key={todo.id}
                        todo={todo}
                        repos={repos}
                        selected={mode !== 'create' && selectedId === todo.id}
                        onSelect={() => {
                          setSelectedId(todo.id);
                          setMode('view');
                        }}
                      />
                    ))}
                  </div>
                )}
              </div>
            </div>

            {/* RIGHT — detail, editor, or placeholder */}
            <div className="dl-scroll min-h-0 flex-1 overflow-y-auto bg-bg-base">{rightPane}</div>
          </>
        )}
      </div>
    </div>
  );
}

/** One row in the left list: name, target, due date, plus done/disabled pills. */
function TodoRow({ todo, repos, selected, onSelect }: { todo: Run; repos: Repo[]; selected: boolean; onSelect: () => void }) {
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
          {todo.name}
        </span>
        {todo.done && (
          <span className="flex shrink-0 items-center gap-1 rounded bg-success/15 px-1.5 py-0.5 text-caption font-medium text-success">
            <CheckIcon className="h-3 w-3" /> Erledigt
          </span>
        )}
        {!todo.enabled && (
          <span className="shrink-0 rounded bg-fill/15 px-1.5 py-0.5 text-caption font-medium text-text-tertiary">deaktiviert</span>
        )}
      </div>
      <span className="truncate text-caption text-text-tertiary">{targetLabel(todo, repos)}</span>
      <span className="text-caption text-text-tertiary">Termin: {dueLabel(todo)}</span>
    </button>
  );
}

/** The reading pane for a selected ToDo: target, due date, done state, the task, a prompt preview,
 *  the run-now action and the execution list (with token/cost). */
function TodoDetail({
  todo,
  repos,
  onEdit,
  onDeleted,
  onRunStarted,
  onExecuted,
}: {
  todo: Run;
  repos: Repo[];
  onEdit: () => void;
  onDeleted: () => void | Promise<void>;
  onRunStarted: () => void;
  onExecuted: () => void | Promise<void>;
}) {
  const source = useMemo(() => getDataSource(), []);
  const { toast } = useToast();
  const [busy, setBusy] = useState(false);
  const [runningNow, setRunningNow] = useState(false);
  const [promptOpen, setPromptOpen] = useState(false);
  const [results, setResults] = useState<RunResultRef[] | null>(null);
  const pollRef = useRef<number | null>(null);
  // Poll-detected new results mean the backend finished (and may have checked the ToDo off).
  const countRef = useRef<number | null>(null);
  const onExecutedRef = useRef(onExecuted);
  onExecutedRef.current = onExecuted;

  const refreshResults = useCallback(async () => {
    try {
      const r = await source.mercuryRunResults(todo.id);
      setResults(r.results);
      if (countRef.current !== null && r.results.length > countRef.current) {
        countRef.current = r.results.length;
        await onExecutedRef.current();
      } else {
        countRef.current = r.results.length;
      }
    } catch {
      /* keep the last good list; a transient poll error is not worth a toast */
    }
  }, [source, todo.id]);

  useEffect(() => {
    let cancelled = false;
    source
      .mercuryRunResults(todo.id)
      .then((r) => {
        if (cancelled) return;
        setResults(r.results);
        countRef.current = r.results.length;
      })
      .catch(() => {
        if (cancelled) return;
        setResults([]);
        countRef.current = 0;
      });
    return () => {
      cancelled = true;
    };
  }, [source, todo.id]);

  useEffect(
    () => () => {
      if (pollRef.current) window.clearInterval(pollRef.current);
    },
    [],
  );

  const del = async () => {
    if (busy || !window.confirm(`ToDo „${todo.name}“ löschen?`)) return;
    setBusy(true);
    try {
      await source.mercuryDeleteRun(todo.id);
      toast({ title: 'ToDo gelöscht', variant: 'default' });
      await onDeleted();
    } catch (e) {
      toast({ title: 'Löschen fehlgeschlagen', description: msg(e), variant: 'danger' });
      setBusy(false);
    }
  };

  const runNow = async () => {
    if (runningNow) return;
    setRunningNow(true);
    try {
      await source.mercuryRunNow(todo.id);
      toast({ title: 'ToDo gestartet', variant: 'success' });
      onRunStarted();
      // Poll this ToDo's executions so a fresh result surfaces while it runs (~2 min ceiling).
      if (pollRef.current) window.clearInterval(pollRef.current);
      let ticks = 0;
      pollRef.current = window.setInterval(() => {
        ticks += 1;
        void refreshResults();
        if (ticks >= 24 && pollRef.current) {
          window.clearInterval(pollRef.current);
          pollRef.current = null;
        }
      }, 5000);
      void refreshResults();
    } catch (e) {
      // 503 "nicht konfiguriert" / 409 "läuft bereits" surface here.
      toast({ title: 'Start fehlgeschlagen', description: msg(e), variant: 'danger' });
    } finally {
      setRunningNow(false);
    }
  };

  return (
    <article className="mx-auto max-w-3xl px-8 py-7">
      <div className="flex items-start justify-between gap-4">
        <div className="min-w-0">
          <h1 className="text-title3 font-semibold tracking-tight text-text-primary">{todo.name}</h1>
          <p className="mt-1 text-footnote text-text-secondary">{targetLabel(todo, repos)}</p>
        </div>
        <div className="flex shrink-0 flex-wrap items-center justify-end gap-1.5">
          <Button variant="primary" size="sm" disabled={runningNow} onClick={runNow}>
            <PlayIcon className="h-3.5 w-3.5" /> {runningNow ? 'Startet…' : 'Jetzt ausführen'}
          </Button>
          <Button variant="secondary" size="sm" onClick={onEdit}>
            Bearbeiten
          </Button>
          <Button variant="secondary" size="sm" disabled={busy} onClick={del}>
            Löschen
          </Button>
        </div>
      </div>

      <div className="mt-4 flex flex-wrap items-center gap-2 text-caption text-text-tertiary">
        <span className={cn('rounded px-1.5 py-0.5 font-medium', todo.enabled ? 'bg-success/15 text-success' : 'bg-fill/15 text-text-tertiary')}>
          {todo.enabled ? 'Aktiv' : 'Deaktiviert'}
        </span>
        {todo.done && (
          <span className="flex items-center gap-1 rounded bg-success/15 px-1.5 py-0.5 font-medium text-success">
            <CheckIcon className="h-3 w-3" /> Erledigt
          </span>
        )}
        <span>Termin: {dueLabel(todo)}</span>
      </div>

      <section className="mt-6">
        <p className="mb-1.5 text-caption font-semibold uppercase tracking-wide text-text-tertiary">Aufgabe</p>
        {todo.task ? (
          <p className="whitespace-pre-wrap rounded-card border border-separator bg-surface p-3 text-footnote text-text-secondary">{todo.task}</p>
        ) : (
          <p className="text-footnote text-text-tertiary">Keine Aufgabenbeschreibung.</p>
        )}
      </section>

      <section className="mt-6">
        <button
          type="button"
          onClick={() => setPromptOpen((o) => !o)}
          className="flex items-center gap-1.5 text-footnote font-medium text-text-secondary transition hover:text-text-primary"
        >
          <ChevronRightIcon className={cn('h-3.5 w-3.5 transition-transform duration-fast', promptOpen && 'rotate-90')} />
          Prompt-Vorschau
        </button>
        {promptOpen && (
          <pre className="dl-scroll mt-2 max-h-96 overflow-auto whitespace-pre-wrap rounded-card border border-separator bg-surface p-3 font-mono text-caption text-text-secondary">
            {todo.prompt || 'Kein Prompt hinterlegt.'}
          </pre>
        )}
      </section>

      <section className="mt-6 border-t border-separator pt-4">
        <p className="mb-1.5 text-caption font-semibold uppercase tracking-wide text-text-tertiary">Ausführungen</p>
        <ExecutionList todoId={todo.id} results={results} />
      </section>
    </article>
  );
}

/** The ToDo's executions: a row per result (time, status, token/cost); selecting one loads the full
 *  result document and renders each repo's steps and agent reports inline. */
function ExecutionList({ todoId, results }: { todoId: string; results: RunResultRef[] | null }) {
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
            {open && <ExecutionDetail key={r.resultId} todoId={todoId} resultId={r.resultId} />}
          </li>
        );
      })}
    </ul>
  );
}

/** The full execution document for one result: totals, then each repo with its steps and reports. */
function ExecutionDetail({ todoId, resultId }: { todoId: string; resultId: string }) {
  const source = useMemo(() => getDataSource(), []);
  const [res, setRes] = useState<RunResult | null>(null);
  const [err, setErr] = useState(false);

  useEffect(() => {
    let cancelled = false;
    setRes(null);
    setErr(false);
    source
      .mercuryRunResult(todoId, resultId)
      .then((r) => !cancelled && setRes(r))
      .catch(() => !cancelled && setErr(true));
    return () => {
      cancelled = true;
    };
  }, [source, todoId, resultId]);

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

/** One repository within an execution: status, deploy/PR, per-repo tokens, and its steps. */
function RepoBlock({ repo }: { repo: RepoResult }) {
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

/** One step (analyze/implement/push/pr/deploy). analyze/implement carry the agent's report (`log`),
 *  shown as a collapsible Bericht. */
function StepRow({ step }: { step: RunStep }) {
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

/** Create/update form for a ToDo: name, the free-text task, the single target (existing repo OR a
 *  new one), the optional one-time due date, and the active flag. */
function TodoEditor({
  base,
  repos,
  onCancel,
  onSaved,
}: {
  base: Run | null;
  repos: Repo[];
  onCancel: () => void;
  onSaved: (id: string) => void | Promise<void>;
}) {
  const source = useMemo(() => getDataSource(), []);
  const { toast } = useToast();
  const [name, setName] = useState(base?.name ?? '');
  const [task, setTask] = useState(base?.task ?? '');
  const [targetKind, setTargetKind] = useState<'existing' | 'new'>(base?.newRepo ? 'new' : 'existing');
  const [repoId, setRepoId] = useState(base?.repo ?? '');
  const [newRepo, setNewRepo] = useState(base?.newRepo ?? '');
  const [due, setDue] = useState(isoToLocalInput(base?.dueAt));
  const [enabled, setEnabled] = useState(base?.enabled ?? true);
  const [busy, setBusy] = useState(false);

  const sortedRepos = useMemo(() => [...repos].sort((a, b) => a.name.localeCompare(b.name, 'de')), [repos]);

  const newRepoOk = NEW_REPO_RE.test(newRepo.trim());
  const targetOk = targetKind === 'existing' ? repoId.trim().length > 0 : newRepoOk;
  const valid = name.trim().length > 0 && task.trim().length > 0 && targetOk;

  const save = async () => {
    if (!valid || busy) return;
    setBusy(true);
    // Exactly one target: the backend rejects both-or-neither, so only ever send the chosen one.
    const body: RunInput = {
      name: name.trim(),
      type: 'todo',
      enabled,
      task: task.trim(),
      dueAt: localInputToIso(due),
      ...(targetKind === 'existing' ? { repo: repoId.trim() } : { newRepo: newRepo.trim() }),
    };
    try {
      const saved = base ? await source.mercuryUpdateRun(base.id, body) : await source.mercuryCreateRun(body);
      toast({ title: base ? 'ToDo gespeichert' : 'ToDo angelegt', variant: 'success' });
      await onSaved(saved.id);
    } catch (e) {
      toast({ title: 'Speichern fehlgeschlagen', description: msg(e), variant: 'danger' });
      setBusy(false);
    }
  };

  return (
    <div className="mx-auto flex max-w-2xl flex-col gap-5 px-8 py-7">
      <h1 className="text-title3 font-semibold tracking-tight text-text-primary">{base ? 'ToDo bearbeiten' : 'Neues ToDo'}</h1>

      <div>
        <p className="mb-1.5 text-caption font-semibold uppercase tracking-wide text-text-tertiary">Name</p>
        <input
          autoFocus
          value={name}
          onChange={(e) => setName(e.target.value)}
          placeholder="Kurzer Titel des ToDos"
          className="w-full rounded-md border border-separator bg-surface px-3 py-2 text-footnote text-text-primary outline-none focus:border-accent/50"
        />
        {name.trim().length === 0 && <p className="mt-1.5 text-caption text-danger">Name ist erforderlich.</p>}
      </div>

      <div>
        <p className="mb-1.5 text-caption font-semibold uppercase tracking-wide text-text-tertiary">Aufgabe</p>
        <textarea
          value={task}
          onChange={(e) => setTask(e.target.value)}
          rows={8}
          placeholder="Behebe den Login-Bug: nach dem Reload geht der gewählte Tab verloren."
          className="dl-scroll w-full resize-y rounded-md border border-separator bg-surface px-3 py-2 text-footnote text-text-primary outline-none focus:border-accent/50"
        />
        <p className="mt-1.5 text-caption text-text-tertiary">
          Beschreibe die Aufgabe konkret — daraus wird der Prompt. Axiome und Regeln erreichen den Agenten über die CLAUDE.md des Repos.
        </p>
        {task.trim().length === 0 && <p className="mt-1.5 text-caption text-danger">Eine Aufgabenbeschreibung ist erforderlich.</p>}
      </div>

      <div>
        <p className="mb-1.5 text-caption font-semibold uppercase tracking-wide text-text-tertiary">Ziel</p>
        <div className="flex flex-col gap-3 rounded-card border border-separator bg-surface p-3">
          <div className="inline-flex w-fit items-center gap-0.5 rounded-md bg-fill/10 p-0.5">
            {(
              [
                { id: 'existing', label: 'Vorhandenes Repo' },
                { id: 'new', label: 'Neues Repo anlegen' },
              ] as const
            ).map((opt) => (
              <button
                key={opt.id}
                type="button"
                onClick={() => setTargetKind(opt.id)}
                className={cn(
                  'rounded px-3 py-1 text-caption font-medium transition duration-fast',
                  targetKind === opt.id ? 'bg-surface-raised text-text-primary shadow-elev-1' : 'text-text-secondary hover:text-text-primary',
                )}
              >
                {opt.label}
              </button>
            ))}
          </div>

          {targetKind === 'existing' ? (
            <>
              <select
                value={repoId}
                onChange={(e) => setRepoId(e.target.value)}
                className="w-full rounded-md border border-separator bg-surface px-2.5 py-1.5 text-footnote text-text-primary outline-none focus:border-accent/50"
              >
                <option value="">Repo wählen…</option>
                {sortedRepos.map((r) => (
                  <option key={r.id} value={r.id}>
                    {r.name} ({r.fullName})
                  </option>
                ))}
              </select>
              {sortedRepos.length === 0 && <p className="text-caption text-text-tertiary">Keine Repos verfügbar.</p>}
              {repoId.trim().length === 0 && <p className="text-caption text-danger">Wähle ein Repo.</p>}
            </>
          ) : (
            <>
              <input
                value={newRepo}
                onChange={(e) => setNewRepo(e.target.value)}
                placeholder="mein-neuer-service"
                className="w-full rounded-md border border-separator bg-surface px-2.5 py-1.5 font-mono text-footnote text-text-primary outline-none focus:border-accent/50"
              />
              {!newRepoOk && (
                <p className="text-caption text-danger">
                  Ungültiger Repo-Name (erlaubt: Buchstaben, Ziffern, Punkt, Unterstrich, Bindestrich; max. 100 Zeichen).
                </p>
              )}
            </>
          )}
        </div>
      </div>

      <div>
        <p className="mb-1.5 text-caption font-semibold uppercase tracking-wide text-text-tertiary">Termin</p>
        <div className="flex flex-wrap items-center gap-2">
          <input
            type="datetime-local"
            value={due}
            onChange={(e) => setDue(e.target.value)}
            className="rounded-md border border-separator bg-surface px-2.5 py-1.5 text-footnote text-text-primary outline-none focus:border-accent/50"
          />
          {due && (
            <Button variant="ghost" size="sm" onClick={() => setDue('')}>
              Termin entfernen
            </Button>
          )}
        </div>
        <p className="mt-1.5 text-caption text-text-tertiary">
          {due ? 'Das ToDo läuft einmalig zu diesem Zeitpunkt.' : 'Ohne Termin läuft das ToDo nur über „Jetzt ausführen“.'}
        </p>
      </div>

      <label className="flex cursor-pointer items-center gap-2.5">
        <input type="checkbox" checked={enabled} onChange={(e) => setEnabled(e.target.checked)} className="h-4 w-4 accent-accent" />
        <span className="text-footnote text-text-primary">Aktiv</span>
      </label>

      <div className="flex items-center gap-2">
        <Button variant="primary" size="sm" disabled={!valid || busy} onClick={save}>
          {busy ? 'Speichert…' : 'Speichern'}
        </Button>
        <Button variant="secondary" size="sm" disabled={busy} onClick={onCancel}>
          Abbrechen
        </Button>
      </div>
    </div>
  );
}

// ── Kalender ──────────────────────────────────────────────────────────────────

/** The auto-updating ToDo-Kalender, rendered by the shared MercuryCalendar. Owns the data: polls
 *  every 60s and refetches whenever a ToDo changes (via `dataVersion`). Only terminated ToDos have a
 *  scheduled moment — ToDos without a due date never appear. A single kind here, so no colour split. */
function TodoCalendar({ dataVersion }: { dataVersion: number }) {
  const source = useMemo(() => getDataSource(), []);
  const [cal, setCal] = useState<RunCalendar | null>(null);
  const [failed, setFailed] = useState<string | null>(null);
  const gotDataRef = useRef(false);

  useEffect(() => {
    let cancelled = false;
    const load = async () => {
      try {
        const c = await source.mercuryRunCalendar(30, 'todo');
        if (!cancelled) {
          setCal(c);
          gotDataRef.current = true;
          setFailed(null);
        }
      } catch (e) {
        // Once a calendar is on screen a transient poll error must not blank it out.
        if (!cancelled && !gotDataRef.current) setFailed(msg(e));
      }
    };
    void load();
    const iv = window.setInterval(() => void load(), 60000);
    return () => {
      cancelled = true;
      window.clearInterval(iv);
    };
  }, [source, dataVersion]);

  if (failed) {
    return (
      <div className="flex min-h-0 flex-1 items-center justify-center bg-bg-base px-6">
        <p className="max-w-md text-center text-footnote text-text-secondary">{failed}</p>
      </div>
    );
  }
  if (!cal) {
    return (
      <div className="flex min-h-0 flex-1 items-center justify-center bg-bg-base">
        <p className="text-footnote text-text-tertiary">Lädt…</p>
      </div>
    );
  }

  return <MercuryCalendar occurrences={cal.occurrences} heading="ToDo-Kalender" />;
}

/** Shown in a pane when nothing is selected. */
function EmptyPlaceholder({ text }: { text: string }) {
  return (
    <div className="flex h-full flex-col items-center justify-center gap-3 text-center">
      <span className="flex h-12 w-12 items-center justify-center rounded-2xl bg-surface-raised shadow-elev-1 ring-1 ring-separator">
        <RocketIcon className="h-6 w-6 text-accent" />
      </span>
      <p className="max-w-xs text-footnote text-text-tertiary">{text}</p>
    </div>
  );
}

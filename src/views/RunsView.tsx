import { useCallback, useEffect, useMemo, useRef, useState, type ReactNode } from 'react';
import { getDataSource } from '@/data';
import { useToast } from '@/ui/Toast';
import { Button } from '@/ui/Button';
import { Modal } from '@/ui/Modal';
import { ErrorBoundary } from '@/ui/ErrorBoundary';
import { cn } from '@/lib/cn';
import { PlusIcon, LightbulbIcon, RefreshIcon, ChevronRightIcon, PlayIcon } from '@/ui/icons';
import { MercuryCalendar } from './MercuryCalendar';
import { ExecutionHistory, LiveExecution, TokenStat, EmptyPlaceholder, useActiveRun, ActiveRunBanner } from './MercuryExecutions';
import { fmtDateTime } from '@/lib/format';
import { Segmented } from '@/ui/Segmented';
import type {
  Run,
  RunActive,
  RunInput,
  RunSchedule,
  RunList,
  RunCoverage,
  PlannedRun,
  RunProposal,
  RunSnapshotMeta,
  RunResultRef,
  RunCalendar,
} from '@/types';

/** Uniform error-to-string, mirroring the rest of the Mercury surface. */
const msg = (e: unknown) => String((e as Error)?.message ?? e);

// Weekdays are stored in the Go convention (0=Sonntag … 6=Samstag) but always DISPLAYED Mo→So.
const WEEKDAYS: { num: number; label: string }[] = [
  { num: 1, label: 'Mo' },
  { num: 2, label: 'Di' },
  { num: 3, label: 'Mi' },
  { num: 4, label: 'Do' },
  { num: 5, label: 'Fr' },
  { num: 6, label: 'Sa' },
  { num: 0, label: 'So' },
];

/** A human schedule line: `täglich 03:00` or `wöchentlich Mo, Do · 03:00`. */
function scheduleSummary(s: RunSchedule | undefined | null): string {
  if (!s) return '—';
  if (s.kind === 'daily') return `täglich ${s.timeOfDay}`;
  const days = WEEKDAYS.filter((w) => s.weekdays?.includes(w.num)).map((w) => w.label);
  if (days.length === 0) return `wöchentlich · ${s.timeOfDay}`;
  return `wöchentlich ${days.join(', ')} · ${s.timeOfDay}`;
}

/** The category of an axiom, derived from its scheme path (drop the namespace and the file). */
function categoryLabel(path: string): string {
  const segs = path.split('/').filter(Boolean);
  return segs.slice(1, -1).join(' / ');
}

type TabId = 'laeufe' | 'kalender' | 'history';
const TABS: { id: TabId; label: string }[] = [
  { id: 'laeufe', label: 'Läufe' },
  { id: 'kalender', label: 'Kalender' },
  { id: 'history', label: 'History' },
];

/** RunsView — Mercury's "Automatische Läufe". A three-tab surface: **Läufe** (the two-pane
 *  list/detail/editor over the scheduled-run model, with AI planning + run-now), **Kalender** (an
 *  auto-updating agenda of upcoming firings) and **History** (completed executions with token/cost).
 *  It owns its own data and state; the parent only gives it a height box. */
export default function RunsView() {
  const source = useMemo(() => getDataSource(), []);
  const { toast } = useToast();

  const [tab, setTab] = useState<TabId>('laeufe');

  const [list, setList] = useState<RunList | null>(null);
  const [coverage, setCoverage] = useState<RunCoverage | null>(null);
  const [failed, setFailed] = useState<string | null>(null);
  // Bumped on every run-config mutation; the calendar depends on it to refetch.
  const [dataVersion, setDataVersion] = useState(0);

  const [selectedId, setSelectedId] = useState<string | null>(null);
  const [mode, setMode] = useState<'view' | 'create' | 'edit'>('view');

  const [aiBusy, setAiBusy] = useState<'fill' | 'finetune' | null>(null);
  const [proposal, setProposal] = useState<{ mode: 'fill' | 'replace'; title: string; proposal: RunProposal } | null>(null);
  const [showHistory, setShowHistory] = useState(false);

  // The run executing right now is SERVER truth (via useActiveRun), so the "Lauf aktiv" state — and the
  // live-follow view — survive a page reload instead of living only in this component. The global cancel
  // shows whenever a run is live.
  const { active, restartPending, refetch: refetchActive } = useActiveRun();
  const running = active != null;
  const [cancelling, setCancelling] = useState(false);

  // Post-mutation refresh: never throws (toasts on failure) so callers can await it after a success.
  const reload = useCallback(async () => {
    try {
      const [l, c] = await Promise.all([source.mercuryRuns(), source.mercuryRunCoverage()]);
      setList(l);
      setCoverage(c);
      setDataVersion((v) => v + 1);
    } catch (e) {
      toast({ title: 'Läufe konnten nicht geladen werden', description: msg(e), variant: 'danger' });
    }
  }, [source, toast]);

  // When a run finishes (active clears), refresh the list so lastResult/next-fire/ToDo-done update.
  const prevActiveRef = useRef<string | null>(null);
  useEffect(() => {
    const cur = active?.runId ?? null;
    if (prevActiveRef.current && !cur) void reload();
    prevActiveRef.current = cur;
  }, [active, reload]);

  // First load owns the full-screen error state.
  useEffect(() => {
    let cancelled = false;
    void (async () => {
      try {
        const [l, c] = await Promise.all([source.mercuryRuns(), source.mercuryRunCoverage()]);
        if (!cancelled) {
          setList(l);
          setCoverage(c);
        }
      } catch (e) {
        if (!cancelled) setFailed(msg(e));
      }
    })();
    return () => {
      cancelled = true;
    };
  }, [source]);

  const cancelRun = useCallback(async () => {
    if (cancelling) return;
    setCancelling(true);
    try {
      await source.mercuryCancelRun();
      toast({ title: 'Lauf abgebrochen', variant: 'default' });
      refetchActive();
    } catch (e) {
      toast({ title: 'Abbrechen fehlgeschlagen', description: msg(e), variant: 'danger' });
    } finally {
      setCancelling(false);
    }
  }, [cancelling, source, toast, refetchActive]);

  const runFill = useCallback(async () => {
    if (aiBusy) return;
    setAiBusy('fill');
    try {
      const p = await source.mercuryRunAiFill();
      setProposal({ mode: 'fill', title: 'KI-Vorschlag: Läufe auffüllen', proposal: p });
    } catch (e) {
      toast({ title: 'KI-Analyse fehlgeschlagen', description: msg(e), variant: 'danger' });
    } finally {
      setAiBusy(null);
    }
  }, [aiBusy, source, toast]);

  const runFinetune = useCallback(async () => {
    if (aiBusy) return;
    setAiBusy('finetune');
    try {
      const p = await source.mercuryRunAiFinetune();
      setProposal({ mode: 'replace', title: 'KI-Vorschlag: Läufe fine-tunen', proposal: p });
    } catch (e) {
      toast({ title: 'KI-Analyse fehlgeschlagen', description: msg(e), variant: 'danger' });
    } finally {
      setAiBusy(null);
    }
  }, [aiBusy, source, toast]);

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
  if (!list || !coverage) {
    return (
      <div className="flex h-full min-h-0 items-center justify-center bg-bg-base">
        <p className="text-footnote text-text-tertiary">Lädt…</p>
      </div>
    );
  }

  // RunsView is the "Automatische Läufe" surface; ToDos are its symmetric sibling (TodosView filters to
  // type==='todo'). Mirror that split here so a ToDo — which carries no schedule or axioms — never leaks
  // into the Läufe list, where it belongs to neither the display nor the axiom-centric detail pane.
  const runs = list.runs.filter((r) => r.type !== 'todo');
  const selectedRun = selectedId ? runs.find((r) => r.id === selectedId) ?? null : null;

  let rightPane: ReactNode;
  if (mode === 'create') {
    rightPane = <RunEditor base={null} coverage={coverage} onCancel={() => setMode('view')} onSaved={handleSaved} />;
  } else if (mode === 'edit' && selectedRun) {
    rightPane = (
      <RunEditor key={selectedRun.id} base={selectedRun} coverage={coverage} onCancel={() => setMode('view')} onSaved={handleSaved} />
    );
  } else if (selectedRun) {
    rightPane = (
      <RunDetail
        key={`${selectedRun.id}:${selectedRun.promptHash ?? ''}:${selectedRun.updatedAt}`}
        run={selectedRun}
        axioms={list.axioms}
        active={active && active.runId === selectedRun.id ? active : null}
        restartPending={restartPending}
        onEdit={() => setMode('edit')}
        onDeleted={handleDeleted}
        onRecomposed={reload}
        onRunStarted={refetchActive}
      />
    );
  } else {
    rightPane = <EmptyPlaceholder text="Wähle links einen Lauf oder lege einen neuen an." />;
  }

  return (
    // w-full: MercuryView's <main> is a flex ROW, so without it this view shrinks to its content
    // width and leaves the rest of the pane empty (most visibly on the Kalender tab).
    <div className="flex h-full min-h-0 w-full flex-col">
      <header className="flex items-center gap-3 border-b border-separator bg-surface px-3 py-2">
        <Segmented value={tab} options={TABS.map((t) => ({ value: t.id, label: t.label }))} onChange={setTab} />
        {running && <ActiveRunBanner className="ml-auto" cancelling={cancelling} onCancel={cancelRun} />}
      </header>

      <div className="flex min-h-0 flex-1">
        {tab === 'laeufe' && (
          <>
            {/* LEFT — toolbar + run list */}
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
                  <PlusIcon className="h-4 w-4" /> Neuer Lauf
                </Button>
                <div className="flex flex-wrap gap-1.5">
                  <Button variant="secondary" size="sm" disabled={aiBusy !== null} onClick={runFill}>
                    <LightbulbIcon className="h-4 w-4" /> {aiBusy === 'fill' ? 'Analysiere…' : 'Mit KI auffüllen'}
                  </Button>
                  <Button variant="secondary" size="sm" disabled={aiBusy !== null} onClick={runFinetune}>
                    <RefreshIcon className="h-4 w-4" /> {aiBusy === 'finetune' ? 'Analysiere…' : 'Mit KI fine-tunen'}
                  </Button>
                  <Button variant="ghost" size="sm" onClick={() => setShowHistory(true)}>
                    Verlauf
                  </Button>
                </div>
              </div>

              <div className="dl-scroll flex-1 overflow-y-auto p-1.5">
                {runs.length === 0 ? (
                  <p className="px-2.5 py-3 text-caption text-text-tertiary">
                    Noch keine Läufe. Lege einen an oder nutze „Mit KI auffüllen“.
                  </p>
                ) : (
                  <div className="flex flex-col gap-0.5">
                    {runs.map((run) => (
                      <RunRow
                        key={run.id}
                        run={run}
                        selected={mode !== 'create' && selectedId === run.id}
                        onSelect={() => {
                          setSelectedId(run.id);
                          setMode('view');
                        }}
                      />
                    ))}
                  </div>
                )}
              </div>
            </div>

            {/* RIGHT — detail, editor, or placeholder. Boundary-wrapped so a single dead/partial run only
                fails its own pane (recovering when another is picked, via resetKeys) while the list stays live. */}
            <div className="dl-scroll min-h-0 flex-1 overflow-y-auto bg-bg-base">
              <ErrorBoundary resetKeys={[selectedId, mode]}>{rightPane}</ErrorBoundary>
            </div>
          </>
        )}

        {tab === 'kalender' && <CalendarView dataVersion={dataVersion} />}
        {tab === 'history' && <ExecutionHistory type="auto" />}
      </div>

      {proposal && (
        <ProposalModal
          mode={proposal.mode}
          title={proposal.title}
          proposal={proposal.proposal}
          onClose={() => setProposal(null)}
          onApplied={async () => {
            await reload();
            setProposal(null);
          }}
        />
      )}

      {showHistory && (
        <HistoryModal
          onClose={() => setShowHistory(false)}
          onRestored={async () => {
            await reload();
            setShowHistory(false);
          }}
        />
      )}
    </div>
  );
}

/** One row in the left list: name, schedule, next fire, plus disabled/stale pills. */
function RunRow({ run, selected, onSelect }: { run: Run; selected: boolean; onSelect: () => void }) {
  const next = run.enabled ? fmtDateTime(run.nextFireAt) : '—';
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
          {run.name}
        </span>
        {!run.enabled && (
          <span className="shrink-0 rounded bg-fill/15 px-1.5 py-0.5 text-caption font-medium text-text-tertiary">deaktiviert</span>
        )}
        {run.suspended && (
          <span className="shrink-0 rounded bg-accent/15 px-1.5 py-0.5 text-caption font-medium text-accent">pausiert</span>
        )}
        {run.stale && <span className="shrink-0 rounded bg-warning/15 px-1.5 py-0.5 text-caption font-medium text-warning">veraltet</span>}
      </div>
      <span className="text-caption text-text-tertiary">{scheduleSummary(run.schedule)}</span>
      <span className="text-caption text-text-tertiary">
        {run.suspended ? `pausiert (Abo-Limit) — Fortsetzung: ${fmtDateTime(run.suspended.resumeAt)}` : `nächster Lauf: ${next}`}
      </span>
    </button>
  );
}

/** The reading pane for a selected run: schedule, state, axiom chips, a lazy prompt preview, the
 *  run-now action and the execution list (with token/cost). `key` remounts it on snapshot change. */
function RunDetail({
  run,
  axioms,
  active,
  restartPending,
  onEdit,
  onDeleted,
  onRecomposed,
  onRunStarted,
}: {
  run: Run;
  axioms: Record<string, string>;
  active: RunActive | null;
  restartPending: boolean;
  onEdit: () => void;
  onDeleted: () => void | Promise<void>;
  onRecomposed: () => void | Promise<void>;
  onRunStarted: () => void;
}) {
  const source = useMemo(() => getDataSource(), []);
  const { toast } = useToast();
  const [busy, setBusy] = useState(false);
  const [runningNow, setRunningNow] = useState(false);
  const [promptOpen, setPromptOpen] = useState(false);
  const [prompt, setPrompt] = useState<string | null>(null);
  const [promptLoading, setPromptLoading] = useState(false);
  const [results, setResults] = useState<RunResultRef[] | null>(null);
  const isLive = active?.runId === run.id;

  const refreshResults = useCallback(async () => {
    try {
      const r = await source.mercuryRunResults(run.id);
      setResults(r.results);
    } catch {
      /* keep the last good list; a transient poll error is not worth a toast */
    }
  }, [source, run.id]);

  useEffect(() => {
    let cancelled = false;
    source
      .mercuryRunResults(run.id)
      .then((r) => !cancelled && setResults(r.results))
      .catch(() => !cancelled && setResults([]));
    return () => {
      cancelled = true;
    };
  }, [source, run.id]);

  // When this run stops being live, pull the freshly-finished execution into the history list below.
  const wasLive = useRef(false);
  useEffect(() => {
    if (wasLive.current && !isLive) void refreshResults();
    wasLive.current = isLive;
  }, [isLive, refreshResults]);

  const togglePrompt = async () => {
    const next = !promptOpen;
    setPromptOpen(next);
    if (next && prompt === null && !promptLoading) {
      setPromptLoading(true);
      try {
        const r = await source.mercuryRunPrompt(run.id);
        setPrompt(r.prompt);
      } catch (e) {
        toast({ title: 'Prompt konnte nicht geladen werden', description: msg(e), variant: 'danger' });
        setPromptOpen(false);
      } finally {
        setPromptLoading(false);
      }
    }
  };

  const del = async () => {
    if (busy || !window.confirm(`Lauf „${run.name}“ löschen?`)) return;
    setBusy(true);
    try {
      await source.mercuryDeleteRun(run.id);
      toast({ title: 'Lauf gelöscht', variant: 'default' });
      await onDeleted();
    } catch (e) {
      toast({ title: 'Löschen fehlgeschlagen', description: msg(e), variant: 'danger' });
      setBusy(false);
    }
  };

  const recompose = async () => {
    if (busy) return;
    setBusy(true);
    try {
      await source.mercuryRecomposeRun(run.id);
      toast({ title: 'Prompt neu komponiert', variant: 'success' });
      await onRecomposed();
    } catch (e) {
      toast({ title: 'Neu komponieren fehlgeschlagen', description: msg(e), variant: 'danger' });
      setBusy(false);
    }
  };

  const runNow = async () => {
    if (runningNow) return;
    setRunningNow(true);
    try {
      const r = await source.mercuryRunNow(run.id);
      if (r.queued) {
        // A restart is draining: the run was not started now but will start by itself afterwards.
        toast({ title: 'Neustart läuft', description: r.message ?? 'Der Lauf wurde eingereiht und startet nach dem Neustart automatisch.', variant: 'default' });
      } else {
        toast({ title: 'Lauf gestartet', variant: 'success' });
      }
      onRunStarted(); // re-check server activity now → the live-follow view opens without waiting for a tick
    } catch (e) {
      // 503 "nicht konfiguriert" / 409 "läuft bereits" surface here.
      toast({ title: 'Start fehlgeschlagen', description: msg(e), variant: 'danger' });
    } finally {
      setRunningNow(false);
    }
  };

  const next = run.enabled ? fmtDateTime(run.nextFireAt) : '—';
  // A legacy/damaged run can carry no axioms at all (Go marshals the empty slice as null); guard so the
  // detail pane counts and lists them safely instead of throwing on null.
  const axiomIds = run.axiomIds ?? [];

  return (
    <article className="mx-auto max-w-3xl px-8 py-7">
      <div className="flex items-start justify-between gap-4">
        <div className="min-w-0">
          <h1 className="text-title3 font-semibold tracking-tight text-text-primary">{run.name}</h1>
          <p className="mt-1 text-footnote text-text-secondary">{scheduleSummary(run.schedule)}</p>
        </div>
        <div className="flex shrink-0 flex-wrap items-center justify-end gap-1.5">
          <Button variant="primary" size="sm" disabled={runningNow} onClick={runNow}>
            <PlayIcon className="h-3.5 w-3.5" /> {runningNow ? 'Startet…' : 'Jetzt ausführen'}
          </Button>
          {run.stale && (
            <Button variant="secondary" size="sm" disabled={busy} onClick={recompose}>
              <RefreshIcon className="h-4 w-4" /> Neu komponieren
            </Button>
          )}
          <Button variant="secondary" size="sm" onClick={onEdit}>
            Bearbeiten
          </Button>
          <Button variant="secondary" size="sm" disabled={busy} onClick={del}>
            Löschen
          </Button>
        </div>
      </div>

      <div className="mt-4 flex flex-wrap items-center gap-2 text-caption text-text-tertiary">
        <span
          className={cn(
            'rounded px-1.5 py-0.5 font-medium',
            run.enabled ? 'bg-success/15 text-success' : 'bg-fill/15 text-text-tertiary',
          )}
        >
          {run.enabled ? 'Aktiv' : 'Deaktiviert'}
        </span>
        {run.suspended && (
          <span className="rounded bg-accent/15 px-1.5 py-0.5 font-medium text-accent">pausiert · Abo-Limit</span>
        )}
        {restartPending && (
          <span className="rounded bg-warning/15 px-1.5 py-0.5 font-medium text-warning">Neustart läuft</span>
        )}
        {run.stale && <span className="rounded bg-warning/15 px-1.5 py-0.5 font-medium text-warning">veraltet</span>}
        <span>{run.suspended ? `Fortsetzung: ${fmtDateTime(run.suspended.resumeAt)}` : `nächster Lauf: ${next}`}</span>
      </div>

      <section className="mt-6">
        <p className="mb-1.5 text-caption font-semibold uppercase tracking-wide text-text-tertiary">Axiome ({axiomIds.length})</p>
        {axiomIds.length === 0 ? (
          <p className="text-footnote text-text-tertiary">Keine Axiome zugeordnet.</p>
        ) : (
          <div className="flex flex-wrap gap-1.5">
            {axiomIds.map((id) => (
              <span key={id} className="rounded-md bg-fill/10 px-2 py-0.5 text-caption text-text-secondary">
                {axioms[id] ?? id}
              </span>
            ))}
          </div>
        )}
      </section>

      <section className="mt-6">
        <button
          type="button"
          onClick={togglePrompt}
          className="flex items-center gap-1.5 text-footnote font-medium text-text-secondary transition hover:text-text-primary"
        >
          <ChevronRightIcon className={cn('h-3.5 w-3.5 transition-transform duration-fast', promptOpen && 'rotate-90')} />
          Prompt-Vorschau
        </button>
        {promptOpen && (
          <div className="mt-2">
            {promptLoading ? (
              <p className="text-footnote text-text-tertiary">Lädt…</p>
            ) : (
              <pre className="dl-scroll max-h-96 overflow-auto whitespace-pre-wrap rounded-card border border-separator bg-surface p-3 font-mono text-caption text-text-secondary">
                {prompt}
              </pre>
            )}
          </div>
        )}
      </section>

      <section className="mt-6 border-t border-separator pt-4">
        <p className="mb-1.5 text-caption font-semibold uppercase tracking-wide text-text-tertiary">Ausführungen</p>
        {isLive && active?.resultId && (
          <div className="mb-3">
            <LiveExecution runId={run.id} resultId={active.resultId} live />
          </div>
        )}
        {results === null ? (
          <p className="text-footnote text-text-tertiary">Lädt…</p>
        ) : results.length === 0 ? (
          <p className="text-footnote text-text-tertiary">
            {isLive ? 'Läuft gerade – siehe oben.' : 'Noch keine Ausführungen — starte einen Lauf mit „Jetzt ausführen“ oder warte den Zeitplan ab.'}
          </p>
        ) : (
          <ul className="flex flex-col gap-1.5">
            {results.map((r) => (
              <li key={r.resultId} className="flex flex-wrap items-center gap-x-3 gap-y-1 text-footnote text-text-secondary">
                <span className={cn('h-2 w-2 shrink-0 rounded-full', r.ok ? 'bg-success' : 'bg-danger')} />
                <span>{fmtDateTime(r.at)}</span>
                <span className="text-text-tertiary">· {r.repoCount} Repos</span>
                <TokenStat input={r.inputTokens} output={r.outputTokens} cost={r.costUsd} />
              </li>
            ))}
          </ul>
        )}
      </section>
    </article>
  );
}

/** Create/update form for a run: name, active, schedule and the axiom picker. */
function RunEditor({
  base,
  coverage,
  onCancel,
  onSaved,
}: {
  base: Run | null;
  coverage: RunCoverage;
  onCancel: () => void;
  onSaved: (id: string) => void | Promise<void>;
}) {
  const source = useMemo(() => getDataSource(), []);
  const { toast } = useToast();
  const [name, setName] = useState(base?.name ?? '');
  const [enabled, setEnabled] = useState(base?.enabled ?? true);
  const [schedule, setSchedule] = useState<RunSchedule>(base?.schedule ?? { kind: 'daily', timeOfDay: '03:00' });
  const [axiomIds, setAxiomIds] = useState<string[]>(base?.axiomIds ?? []);
  const [busy, setBusy] = useState(false);

  const weeklyOk = schedule.kind !== 'weekly' || (schedule.weekdays?.length ?? 0) > 0;
  const valid = name.trim().length > 0 && axiomIds.length > 0 && weeklyOk;

  const toggleAxiom = (id: string) =>
    setAxiomIds((prev) => (prev.includes(id) ? prev.filter((x) => x !== id) : [...prev, id]));

  const save = async () => {
    if (!valid || busy) return;
    setBusy(true);
    const cleanSchedule: RunSchedule =
      schedule.kind === 'weekly'
        ? { kind: 'weekly', timeOfDay: schedule.timeOfDay, weekdays: [...(schedule.weekdays ?? [])].sort((a, b) => a - b) }
        : { kind: 'daily', timeOfDay: schedule.timeOfDay };
    const body: RunInput = { name: name.trim(), enabled, schedule: cleanSchedule, axiomIds };
    try {
      const run = base ? await source.mercuryUpdateRun(base.id, body) : await source.mercuryCreateRun(body);
      toast({ title: base ? 'Lauf gespeichert' : 'Lauf angelegt', variant: 'success' });
      await onSaved(run.id);
    } catch (e) {
      toast({ title: 'Speichern fehlgeschlagen', description: msg(e), variant: 'danger' });
      setBusy(false);
    }
  };

  return (
    <div className="mx-auto flex max-w-2xl flex-col gap-5 px-8 py-7">
      <h1 className="text-title3 font-semibold tracking-tight text-text-primary">{base ? 'Lauf bearbeiten' : 'Neuer Lauf'}</h1>

      <div>
        <p className="mb-1.5 text-caption font-semibold uppercase tracking-wide text-text-tertiary">Name</p>
        <input
          autoFocus
          value={name}
          onChange={(e) => setName(e.target.value)}
          placeholder="Name des Laufs"
          className="w-full rounded-md border border-separator bg-surface px-3 py-2 text-footnote text-text-primary outline-none focus:border-accent/50"
        />
      </div>

      <label className="flex cursor-pointer items-center gap-2.5">
        <input type="checkbox" checked={enabled} onChange={(e) => setEnabled(e.target.checked)} className="h-4 w-4 accent-accent" />
        <span className="text-footnote text-text-primary">Aktiv</span>
      </label>

      <div>
        <p className="mb-1.5 text-caption font-semibold uppercase tracking-wide text-text-tertiary">Zeitplan</p>
        <ScheduleFields schedule={schedule} onChange={setSchedule} />
        {!weeklyOk && <p className="mt-1.5 text-caption text-danger">Mindestens einen Wochentag wählen.</p>}
      </div>

      <div>
        <p className="mb-1.5 text-caption font-semibold uppercase tracking-wide text-text-tertiary">Axiome ({axiomIds.length})</p>
        <AxiomPicker coverage={coverage} editingId={base?.id} selectedIds={axiomIds} onToggle={toggleAxiom} />
        {axiomIds.length === 0 && <p className="mt-1.5 text-caption text-danger">Mindestens ein Axiom wählen.</p>}
      </div>

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

/** Schedule editor: kind + time, and — only for weekly — the seven weekday toggles (Mo→So). */
function ScheduleFields({ schedule, onChange }: { schedule: RunSchedule; onChange: (s: RunSchedule) => void }) {
  return (
    <div className="flex flex-col gap-3 rounded-card border border-separator bg-surface p-3">
      <div className="flex flex-wrap items-center gap-3">
        <select
          value={schedule.kind}
          onChange={(e) => {
            const kind = e.target.value as RunSchedule['kind'];
            onChange(
              kind === 'weekly'
                ? { kind, timeOfDay: schedule.timeOfDay, weekdays: schedule.weekdays ?? [] }
                : { kind, timeOfDay: schedule.timeOfDay },
            );
          }}
          className="rounded-md border border-separator bg-surface px-2.5 py-1.5 text-footnote text-text-primary outline-none focus:border-accent/50"
        >
          <option value="daily">Täglich</option>
          <option value="weekly">Wöchentlich</option>
        </select>
        <div className="flex items-center gap-2">
          <span className="text-footnote text-text-secondary">um</span>
          <input
            type="time"
            value={schedule.timeOfDay}
            onChange={(e) => onChange({ ...schedule, timeOfDay: e.target.value })}
            className="rounded-md border border-separator bg-surface px-2.5 py-1.5 text-footnote text-text-primary outline-none focus:border-accent/50"
          />
        </div>
      </div>
      {schedule.kind === 'weekly' && (
        <div className="flex flex-wrap gap-1.5">
          {WEEKDAYS.map((w) => {
            const on = schedule.weekdays?.includes(w.num) ?? false;
            return (
              <button
                key={w.num}
                type="button"
                onClick={() => {
                  const set = new Set(schedule.weekdays ?? []);
                  if (set.has(w.num)) set.delete(w.num);
                  else set.add(w.num);
                  onChange({ ...schedule, weekdays: [...set].sort((a, b) => a - b) });
                }}
                className={cn(
                  'h-8 w-10 rounded-md text-caption font-medium transition duration-fast',
                  on ? 'bg-accent text-accent-fg' : 'bg-fill/10 text-text-secondary hover:bg-fill/15',
                )}
              >
                {w.label}
              </button>
            );
          })}
        </div>
      )}
    </div>
  );
}

/** Axiom picker: every axiom (title + category), a text filter, and a coverage badge showing how
 *  many OTHER runs already back each axiom. */
function AxiomPicker({
  coverage,
  editingId,
  selectedIds,
  onToggle,
}: {
  coverage: RunCoverage;
  editingId?: string;
  selectedIds: string[];
  onToggle: (id: string) => void;
}) {
  const [filter, setFilter] = useState('');

  const items = useMemo(
    () =>
      Object.keys(coverage.axioms)
        .map((id) => ({
          id,
          title: coverage.axioms[id] ?? id,
          category: categoryLabel(coverage.index[id] ?? ''),
          others: (coverage.covered[id] ?? []).filter((r) => r !== editingId),
        }))
        .sort((a, b) => a.title.localeCompare(b.title, 'de')),
    [coverage, editingId],
  );

  const q = filter.trim().toLowerCase();
  const shown = q ? items.filter((it) => it.title.toLowerCase().includes(q) || it.category.toLowerCase().includes(q)) : items;

  return (
    <div className="rounded-card border border-separator bg-surface">
      <div className="border-b border-separator p-2">
        <input
          value={filter}
          onChange={(e) => setFilter(e.target.value)}
          placeholder="Axiome filtern…"
          className="w-full rounded-md border border-separator bg-bg-base px-2.5 py-1.5 text-footnote text-text-primary outline-none focus:border-accent/50"
        />
      </div>
      <div className="dl-scroll max-h-80 overflow-y-auto p-1.5">
        {shown.length === 0 ? (
          <p className="px-2 py-3 text-caption text-text-tertiary">Keine Axiome gefunden.</p>
        ) : (
          shown.map((it) => {
            const checked = selectedIds.includes(it.id);
            return (
              <label key={it.id} className="flex cursor-pointer items-start gap-2.5 rounded-md px-2 py-1.5 transition hover:bg-fill/10">
                <input type="checkbox" checked={checked} onChange={() => onToggle(it.id)} className="mt-0.5 h-4 w-4 shrink-0 accent-accent" />
                <span className="flex min-w-0 flex-1 flex-col gap-0.5">
                  <span className="flex items-center gap-1.5">
                    <span className="truncate text-footnote text-text-primary">{it.title}</span>
                    {it.others.length > 0 && (
                      <span className="shrink-0 rounded bg-warning/15 px-1.5 py-0.5 text-caption font-medium text-warning">
                        in {it.others.length} {it.others.length === 1 ? 'Lauf' : 'Läufen'}
                      </span>
                    )}
                  </span>
                  {it.category && <span className="truncate text-caption text-text-tertiary">{it.category}</span>}
                </span>
              </label>
            );
          })
        )}
      </div>
    </div>
  );
}

// ── Kalender ──────────────────────────────────────────────────────────────────

/** The auto-updating Laufkalender: a compact month grid plus an agenda of upcoming firings, grouped
 *  by day. Polls every 60s and refetches whenever the run config changes (via `dataVersion`). */
function CalendarView({ dataVersion }: { dataVersion: number }) {
  const source = useMemo(() => getDataSource(), []);
  const [cal, setCal] = useState<RunCalendar | null>(null);
  const [failed, setFailed] = useState<string | null>(null);
  const gotDataRef = useRef(false);

  useEffect(() => {
    let cancelled = false;
    const load = async () => {
      try {
        const c = await source.mercuryRunCalendar(30);
        if (!cancelled) {
          setCal(c);
          gotDataRef.current = true;
          setFailed(null);
        }
      } catch (e) {
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

  return <MercuryCalendar occurrences={cal.occurrences} showTypes heading="Laufkalender" />;
}

// ── Proposal review + config history ──────────────────────────────────────────

/** Review + apply an AI proposal. `fill` extends the run set; `replace` swaps it for the plan. */
function ProposalModal({
  mode,
  title,
  proposal,
  onClose,
  onApplied,
}: {
  mode: 'fill' | 'replace';
  title: string;
  proposal: RunProposal;
  onClose: () => void;
  onApplied: () => void | Promise<void>;
}) {
  const source = useMemo(() => getDataSource(), []);
  const { toast } = useToast();
  const [applying, setApplying] = useState(false);
  const runs = proposal.proposal.runs;

  const apply = async () => {
    if (applying) return;
    setApplying(true);
    try {
      await source.mercuryApplyRunProposal(mode, proposal.proposal);
      toast({ title: mode === 'fill' ? 'Läufe aufgefüllt' : 'Läufe übernommen', variant: 'success' });
      await onApplied();
    } catch (e) {
      toast({ title: 'Übernehmen fehlgeschlagen', description: msg(e), variant: 'danger' });
      setApplying(false);
    }
  };

  const axiomNames = (r: PlannedRun) => r.axiomIds.map((id) => proposal.axioms[id] ?? id);

  return (
    <Modal
      open
      onClose={onClose}
      title={title}
      description={`${runs.length} ${runs.length === 1 ? 'Lauf' : 'Läufe'} vorgeschlagen`}
      size="lg"
      footer={
        <>
          <Button variant="secondary" size="sm" disabled={applying} onClick={onClose}>
            Abbrechen
          </Button>
          <Button variant="primary" size="sm" disabled={applying || runs.length === 0} onClick={apply}>
            {applying ? 'Übernehme…' : 'Übernehmen'}
          </Button>
        </>
      }
    >
      {runs.length === 0 ? (
        <p className="text-footnote text-text-secondary">Der Vorschlag enthält keine Läufe.</p>
      ) : (
        <ul className="flex flex-col gap-3">
          {runs.map((r, i) => {
            const names = axiomNames(r);
            return (
              <li key={i} className="rounded-card border border-separator bg-surface p-3">
                <div className="flex items-center justify-between gap-2">
                  <span className="truncate text-footnote font-medium text-text-primary">{r.name}</span>
                  <span className="shrink-0 text-caption text-text-tertiary">{scheduleSummary(r.schedule)}</span>
                </div>
                <p className="mt-1 text-caption text-text-secondary">
                  {r.axiomIds.length} {r.axiomIds.length === 1 ? 'Axiom' : 'Axiome'}: {names.slice(0, 3).join(', ')}
                  {names.length > 3 ? ` +${names.length - 3}` : ''}
                </p>
                {r.rationale && <p className="mt-1.5 text-caption text-text-tertiary">{r.rationale}</p>}
              </li>
            );
          })}
        </ul>
      )}
    </Modal>
  );
}

/** The run-config history: each mutation is a restorable snapshot (newest first). */
function HistoryModal({ onClose, onRestored }: { onClose: () => void; onRestored: () => void | Promise<void> }) {
  const source = useMemo(() => getDataSource(), []);
  const { toast } = useToast();
  const [snapshots, setSnapshots] = useState<RunSnapshotMeta[] | null>(null);
  const [restoring, setRestoring] = useState<string | null>(null);

  useEffect(() => {
    let cancelled = false;
    source
      .mercuryRunHistory()
      .then((r) => {
        if (!cancelled) setSnapshots([...r.snapshots].sort((a, b) => (a.ts < b.ts ? 1 : a.ts > b.ts ? -1 : 0)));
      })
      .catch((e) => {
        if (!cancelled) {
          toast({ title: 'Verlauf konnte nicht geladen werden', description: msg(e), variant: 'danger' });
          setSnapshots([]);
        }
      });
    return () => {
      cancelled = true;
    };
  }, [source, toast]);

  const restore = async (ts: string) => {
    if (restoring) return;
    setRestoring(ts);
    try {
      await source.mercuryRestoreRunHistory(ts);
      toast({ title: 'Konfiguration wiederhergestellt', variant: 'success' });
      await onRestored();
    } catch (e) {
      toast({ title: 'Wiederherstellen fehlgeschlagen', description: msg(e), variant: 'danger' });
      setRestoring(null);
    }
  };

  return (
    <Modal open onClose={onClose} title="Verlauf" description="Frühere Konfigurationen der Läufe" size="lg">
      {snapshots === null ? (
        <p className="text-footnote text-text-tertiary">Lädt…</p>
      ) : snapshots.length === 0 ? (
        <p className="text-footnote text-text-tertiary">Noch keine Änderungen aufgezeichnet.</p>
      ) : (
        <ul className="flex flex-col gap-2">
          {snapshots.map((s) => (
            <li key={s.ts} className="flex items-center gap-3 rounded-card border border-separator bg-surface p-3">
              <div className="min-w-0 flex-1">
                <p className="truncate text-footnote font-medium text-text-primary">{s.action}</p>
                <p className="mt-0.5 text-caption text-text-tertiary">
                  {fmtDateTime(s.ts)} · {s.actor} · {s.runCount} {s.runCount === 1 ? 'Lauf' : 'Läufe'}
                </p>
              </div>
              <Button variant="secondary" size="sm" disabled={restoring !== null} onClick={() => restore(s.ts)}>
                {restoring === s.ts ? 'Stellt her…' : 'Wiederherstellen'}
              </Button>
            </li>
          ))}
        </ul>
      )}
    </Modal>
  );
}

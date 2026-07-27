import { useCallback, useEffect, useMemo, useRef, useState, type ReactNode } from 'react';
import { getDataSource } from '@/data';
import { useToast } from '@/ui/Toast';
import { Button } from '@/ui/Button';
import { Authorship } from '@/ui/Authorship';
import { Person } from '@/ui/Person';
import { cn } from '@/lib/cn';
import { humanSize, toBase64 } from '@/lib/file';
import { PlusIcon, RefreshIcon, ChevronRightIcon, PlayIcon, CheckIcon, FileIcon, XIcon } from '@/ui/icons';
import { MercuryCalendar } from './MercuryCalendar';
import { ExecutionList, ExecutionHistory, LiveExecution, EmptyPlaceholder, fmtDateTime, useActiveRun } from './MercuryExecutions';
import type { Run, RunActive, RunInput, RunTarget, RunAttachment, RunResultRef, Repo, RunCalendar } from '@/types';

/** A single-file cap that matches the backend (25 MiB). */
const MAX_ATTACHMENT_BYTES = 25 * 1024 * 1024;
/** A ToDo is a focused task, not a media library — matches the backend ceiling. */
const MAX_ATTACHMENTS = 20;

/** True for a MIME the browser can show inline as an image thumbnail. */
const isImageMime = (m?: string) => !!m && m.startsWith('image/');

/** A newly picked file held in the editor until the ToDo is saved (then uploaded to its id). */
type PendingAttachment = { name: string; b64: string; size: number; mime: string };

/** Uniform error-to-string, mirroring the rest of the Mercury surface. */
const msg = (e: unknown) => String((e as Error)?.message ?? e);

type TabId = 'liste' | 'kalender' | 'history';
const TABS: { id: TabId; label: string }[] = [
  { id: 'liste', label: 'Liste' },
  { id: 'kalender', label: 'Kalender' },
  { id: 'history', label: 'History' },
];

/** A new repo becomes a GitHub repo and a deploy-allowlist key; the backend bounds it the same way. */
const NEW_REPO_RE = /^[A-Za-z0-9._-]{1,100}$/;

/** A ToDo's destinations, normalizing a legacy single-target record into the list — existing repos by
 *  display name, to-be-created ones prefixed "neu:". */
function targetsOf(todo: Run): RunTarget[] {
  if (todo.targets?.length) return todo.targets;
  if (todo.newRepo || todo.repo) return [{ repo: todo.repo, newRepo: todo.newRepo }];
  return [];
}

function targetLabels(todo: Run, repos: Repo[]): string[] {
  return targetsOf(todo).map((t) => (t.newRepo ? `neu: ${t.newRepo}` : repos.find((r) => r.id === t.repo)?.name ?? t.repo ?? '—'));
}

/** A one-line summary of a ToDo's targets (— when none). */
function targetLabel(todo: Run, repos: Repo[]): string {
  const labels = targetLabels(todo, repos);
  return labels.length ? labels.join(', ') : '—';
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

/** The earliest Termin the picker allows: the current minute. A ToDo runs once at its due moment,
 *  so a moment already in the past would never fire — the form forbids it. */
function nowLocalInput(): string {
  return isoToLocalInput(new Date().toISOString());
}

/** A sensible default Termin for a fresh ToDo: the next full hour, always comfortably in the future. */
function defaultDueLocalInput(): string {
  const d = new Date();
  d.setHours(d.getHours() + 1, 0, 0, 0);
  return isoToLocalInput(d.toISOString());
}

/** One attachment tile: an image thumbnail or a file glyph, with the name and size. `href` (when set)
 *  opens the raw bytes in a new tab; `preview` is a data/URL for the image thumbnail; `onRemove` adds a
 *  remove affordance. Shared by the editor (add/remove) and the detail pane (read-only). */
function AttachmentCard({
  name,
  mime,
  size,
  preview,
  href,
  onRemove,
  uploadedBy,
}: {
  name: string;
  mime?: string;
  size: number;
  preview?: string;
  href?: string;
  onRemove?: () => void;
  /** Who uploaded this medium (already carried on the attachment) — surfaced, not a second field. */
  uploadedBy?: string;
}) {
  const body = (
    <>
      <span className="flex h-10 w-10 shrink-0 items-center justify-center overflow-hidden rounded bg-fill/10">
        {preview && isImageMime(mime) ? (
          <img src={preview} alt={name} className="h-full w-full object-cover" />
        ) : (
          <FileIcon className="h-5 w-5 text-text-tertiary" />
        )}
      </span>
      <span className="flex min-w-0 flex-1 flex-col">
        <span className="truncate text-footnote text-text-secondary">{name}</span>
        <span className="flex items-center gap-1.5 text-caption text-text-tertiary">
          {humanSize(size)}
          {uploadedBy && (
            <>
              <span aria-hidden>·</span>
              <Person username={uploadedBy} size="sm" />
            </>
          )}
        </span>
      </span>
    </>
  );
  return (
    <div className="flex items-center gap-2 rounded-card border border-separator bg-surface p-2">
      {href ? (
        <a href={href} target="_blank" rel="noreferrer" className="flex min-w-0 flex-1 items-center gap-2 hover:opacity-80">
          {body}
        </a>
      ) : (
        <div className="flex min-w-0 flex-1 items-center gap-2">{body}</div>
      )}
      {onRemove && (
        <button
          type="button"
          onClick={onRemove}
          aria-label={`${name} entfernen`}
          className="shrink-0 rounded p-1 text-text-tertiary transition hover:bg-fill/10 hover:text-danger"
        >
          <XIcon className="h-3.5 w-3.5" />
        </button>
      )}
    </div>
  );
}

/** The editor's media section: attach images/documents to a ToDo. Already-saved attachments and freshly
 *  picked ones are shown together; both are committed when the ToDo is saved (uploads/removals then). */
function MediaField({
  existing,
  pending,
  rawUrl,
  onPick,
  onRemoveExisting,
  onRemovePending,
}: {
  existing: RunAttachment[];
  pending: PendingAttachment[];
  rawUrl: (attachmentId: string) => string | undefined;
  onPick: (files: FileList) => void;
  onRemoveExisting: (id: string) => void;
  onRemovePending: (index: number) => void;
}) {
  const inputRef = useRef<HTMLInputElement>(null);
  const full = existing.length + pending.length >= MAX_ATTACHMENTS;
  return (
    <div>
      <p className="mb-1.5 text-caption font-semibold uppercase tracking-wide text-text-tertiary">Medien</p>
      <div className="flex flex-col gap-2">
        {existing.map((a) => (
          <AttachmentCard
            key={a.id}
            name={a.filename}
            mime={a.mime}
            size={a.size}
            preview={isImageMime(a.mime) ? rawUrl(a.id) : undefined}
            href={rawUrl(a.id)}
            onRemove={() => onRemoveExisting(a.id)}
          />
        ))}
        {pending.map((p, i) => (
          <AttachmentCard
            key={`pending:${i}:${p.name}`}
            name={p.name}
            mime={p.mime}
            size={p.size}
            preview={isImageMime(p.mime) ? `data:${p.mime};base64,${p.b64}` : undefined}
            onRemove={() => onRemovePending(i)}
          />
        ))}
        <input
          ref={inputRef}
          type="file"
          multiple
          accept="image/*,application/pdf,text/*,.md,.csv,.json,.doc,.docx,.xls,.xlsx,.ppt,.pptx"
          className="hidden"
          onChange={(e) => {
            if (e.target.files?.length) onPick(e.target.files);
            e.target.value = ''; // allow re-picking the same file
          }}
        />
        <button
          type="button"
          disabled={full}
          onClick={() => inputRef.current?.click()}
          className="flex w-fit items-center gap-1 rounded-md px-1 py-1 text-caption font-medium text-accent transition duration-fast hover:text-accent/80 disabled:cursor-not-allowed disabled:text-text-tertiary"
        >
          <PlusIcon className="h-3.5 w-3.5" /> Medien anhängen
        </button>
      </div>
    </div>
  );
}

/** The detail pane's read-only media grid — each tile opens the file. */
function DetailMedia({ todoId, attachments }: { todoId: string; attachments: RunAttachment[] }) {
  const source = useMemo(() => getDataSource(), []);
  if (attachments.length === 0) return null;
  return (
    <section className="mt-6">
      <p className="mb-1.5 text-caption font-semibold uppercase tracking-wide text-text-tertiary">Medien</p>
      <div className="grid grid-cols-1 gap-2 sm:grid-cols-2">
        {attachments.map((a) => (
          <AttachmentCard
            key={a.id}
            name={a.filename}
            mime={a.mime}
            size={a.size}
            preview={isImageMime(a.mime) ? source.mercuryAttachmentRawUrl(todoId, a.id) : undefined}
            href={source.mercuryAttachmentRawUrl(todoId, a.id)}
            uploadedBy={a.uploadedBy}
          />
        ))}
      </div>
    </section>
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

  // The ToDo executing right now is SERVER truth (via useActiveRun) — so the "aktiv" state and the
  // live-follow view survive a page reload. The cancel affordance shows whenever a run is live.
  const { active, refetch: refetchActive } = useActiveRun();
  const running = active != null;
  const [cancelling, setCancelling] = useState(false);

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

  // When a run finishes (active clears), refresh so the ToDo's done/last-result state updates.
  const prevActiveRef = useRef<string | null>(null);
  useEffect(() => {
    const cur = active?.runId ?? null;
    if (prevActiveRef.current && !cur) void reload();
    prevActiveRef.current = cur;
  }, [active, reload]);

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
        active={active && active.runId === selectedTodo.id ? active : null}
        onEdit={() => setMode('edit')}
        onDeleted={handleDeleted}
        onRunStarted={refetchActive}
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
        {tab === 'liste' && (
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
        {tab === 'kalender' && <TodoCalendar dataVersion={dataVersion} />}
        {tab === 'history' && <ExecutionHistory type="todo" />}
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
  active,
  onEdit,
  onDeleted,
  onRunStarted,
  onExecuted,
}: {
  todo: Run;
  repos: Repo[];
  active: RunActive | null;
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
  const isLive = active?.runId === todo.id;
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

  // When this ToDo stops being live, pull the finished execution into the list (and check it off).
  const wasLive = useRef(false);
  useEffect(() => {
    if (wasLive.current && !isLive) void refreshResults();
    wasLive.current = isLive;
  }, [isLive, refreshResults]);

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
      onRunStarted(); // re-check server activity now → the live-follow view opens without waiting for a tick
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

      <Authorship className="mt-3" createdBy={todo.createdBy} updatedBy={todo.updatedBy} />

      <section className="mt-6">
        <p className="mb-1.5 text-caption font-semibold uppercase tracking-wide text-text-tertiary">Aufgabe</p>
        {todo.task ? (
          <p className="whitespace-pre-wrap rounded-card border border-separator bg-surface p-3 text-footnote text-text-secondary">{todo.task}</p>
        ) : (
          <p className="text-footnote text-text-tertiary">Keine Aufgabenbeschreibung.</p>
        )}
      </section>

      <DetailMedia todoId={todo.id} attachments={todo.attachments ?? []} />

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
        {isLive && active?.resultId && (
          <div className="mb-3">
            <LiveExecution runId={todo.id} resultId={active.resultId} live />
          </div>
        )}
        <ExecutionList runId={todo.id} results={results} />
      </section>
    </article>
  );
}

/** One editable target row: an existing repo, or a new one to create. Mapped to a RunTarget on save. */
type TargetRow = { kind: 'existing' | 'new'; repo: string; newRepo: string };

/** The target rows a ToDo opens with: its stored targets (or a legacy single target folded in), else
 *  one empty existing-repo row to start from. */
function initialTargetRows(base: Run | null): TargetRow[] {
  const targets = base ? targetsOf(base) : [];
  if (targets.length === 0) return [{ kind: 'existing', repo: '', newRepo: '' }];
  return targets.map((t) =>
    t.newRepo ? { kind: 'new' as const, repo: '', newRepo: t.newRepo } : { kind: 'existing' as const, repo: t.repo ?? '', newRepo: '' },
  );
}

const rowValid = (r: TargetRow) => (r.kind === 'existing' ? r.repo.trim().length > 0 : NEW_REPO_RE.test(r.newRepo.trim()));

/** Create/update form for a ToDo: name, the free-text task, one or more targets (each an existing repo
 *  or a new one to create), the optional one-time due date, and the active flag. */
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
  const [rows, setRows] = useState<TargetRow[]>(() => initialTargetRows(base));
  // New ToDos get a sensible default Termin (next full hour); editing preserves the stored one
  // exactly — including "no Termin" (manual), which must never silently gain a schedule.
  const [due, setDue] = useState(() => (base ? isoToLocalInput(base.dueAt) : defaultDueLocalInput()));
  const [enabled, setEnabled] = useState(base?.enabled ?? true);
  const [busy, setBusy] = useState(false);

  // Media the AI takes into account. Existing (saved) attachments and newly picked ones are both
  // committed on save: removals and uploads run against the saved ToDo id, so a brand-new ToDo can
  // carry media too (it is created first, then its files are uploaded).
  const [existingAtts] = useState<RunAttachment[]>(base?.attachments ?? []);
  const [removedIds, setRemovedIds] = useState<Set<string>>(() => new Set());
  const [pending, setPending] = useState<PendingAttachment[]>([]);
  const visibleAtts = useMemo(() => existingAtts.filter((a) => !removedIds.has(a.id)), [existingAtts, removedIds]);

  const pickFiles = useCallback(
    async (files: FileList) => {
      const taken = new Set([...visibleAtts.map((a) => a.filename.toLowerCase()), ...pending.map((p) => p.name.toLowerCase())]);
      let count = visibleAtts.length + pending.length;
      const added: PendingAttachment[] = [];
      for (const file of Array.from(files)) {
        if (count >= MAX_ATTACHMENTS) {
          toast({ title: 'Zu viele Anhänge', description: `Höchstens ${MAX_ATTACHMENTS} Dateien je ToDo.`, variant: 'danger' });
          break;
        }
        if (file.size > MAX_ATTACHMENT_BYTES) {
          toast({ title: 'Datei zu groß', description: `${file.name} überschreitet 25 MB.`, variant: 'danger' });
          continue;
        }
        if (taken.has(file.name.toLowerCase())) {
          toast({ title: 'Name bereits vergeben', description: file.name, variant: 'danger' });
          continue;
        }
        try {
          const b64 = await toBase64(file);
          added.push({ name: file.name, b64, size: file.size, mime: file.type });
          taken.add(file.name.toLowerCase());
          count += 1;
        } catch {
          toast({ title: 'Datei konnte nicht gelesen werden', description: file.name, variant: 'danger' });
        }
      }
      if (added.length) setPending((p) => [...p, ...added]);
    },
    [visibleAtts, pending, toast],
  );

  const sortedRepos = useMemo(() => [...repos].sort((a, b) => a.name.localeCompare(b.name, 'de')), [repos]);
  // Pinned at mount so the picker's floor is stable while the form is open; save() re-checks live.
  const minDue = useMemo(() => nowLocalInput(), []);

  const targetsOk = rows.length > 0 && rows.every(rowValid);
  const setRow = (i: number, patch: Partial<TargetRow>) => setRows((rs) => rs.map((r, j) => (j === i ? { ...r, ...patch } : r)));
  const addRow = () => setRows((rs) => [...rs, { kind: 'existing', repo: '', newRepo: '' }]);
  const removeRow = (i: number) => setRows((rs) => rs.filter((_, j) => j !== i));
  // A Termin in the past would never fire (a ToDo runs once, at its due moment). Both strings are
  // zero-padded YYYY-MM-DDTHH:mm, so a lexicographic compare is a chronological one.
  const dueInPast = due !== '' && due < minDue;
  const valid = name.trim().length > 0 && task.trim().length > 0 && targetsOk && !dueInPast;

  const save = async () => {
    if (!valid || busy) return;
    // Re-check against a fresh clock: a form left open across the minute boundary must not slip a
    // past Termin through the mount-pinned floor above.
    const dueIso = localInputToIso(due);
    if (dueIso && dueIso < new Date().toISOString()) {
      toast({ title: 'Der Termin darf nicht in der Vergangenheit liegen.', variant: 'danger' });
      return;
    }
    setBusy(true);
    // One or more targets: each row is exactly an existing repo OR a new one. The backend re-validates
    // and collapses duplicates.
    const targets: RunTarget[] = rows.map((r) => (r.kind === 'existing' ? { repo: r.repo.trim() } : { newRepo: r.newRepo.trim() }));
    const body: RunInput = {
      name: name.trim(),
      type: 'todo',
      enabled,
      task: task.trim(),
      dueAt: dueIso,
      targets,
    };
    try {
      const saved = base ? await source.mercuryUpdateRun(base.id, body) : await source.mercuryCreateRun(body);
      // Commit media against the saved id: removals first (frees the name), then uploads. A media
      // failure does not undo the saved ToDo — surface it and carry on so the rest still applies.
      for (const attId of removedIds) {
        try {
          await source.mercuryDeleteAttachment(saved.id, attId);
        } catch (e) {
          toast({ title: 'Anhang konnte nicht entfernt werden', description: msg(e), variant: 'danger' });
        }
      }
      for (const p of pending) {
        try {
          await source.mercuryUploadAttachment(saved.id, p.name, p.b64);
        } catch (e) {
          toast({ title: `Anhang „${p.name}“ fehlgeschlagen`, description: msg(e), variant: 'danger' });
        }
      }
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

      <MediaField
        existing={visibleAtts}
        pending={pending}
        rawUrl={(attId) => (base ? source.mercuryAttachmentRawUrl(base.id, attId) : undefined)}
        onPick={pickFiles}
        onRemoveExisting={(attId) => setRemovedIds((s) => new Set(s).add(attId))}
        onRemovePending={(i) => setPending((p) => p.filter((_, j) => j !== i))}
      />

      <div>
        <p className="mb-1.5 text-caption font-semibold uppercase tracking-wide text-text-tertiary">Ziele</p>
        <div className="flex flex-col gap-2">
          {rows.map((row, i) => (
            <div key={i} className="flex flex-col gap-2 rounded-card border border-separator bg-surface p-3">
              <div className="flex items-center gap-2">
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
                      onClick={() => setRow(i, { kind: opt.id })}
                      className={cn(
                        'rounded px-3 py-1 text-caption font-medium transition duration-fast',
                        row.kind === opt.id ? 'bg-surface-raised text-text-primary shadow-elev-1' : 'text-text-secondary hover:text-text-primary',
                      )}
                    >
                      {opt.label}
                    </button>
                  ))}
                </div>
                <div className="flex-1" />
                {rows.length > 1 && (
                  <Button variant="ghost" size="sm" onClick={() => removeRow(i)}>
                    Entfernen
                  </Button>
                )}
              </div>

              {row.kind === 'existing' ? (
                <>
                  <select
                    value={row.repo}
                    onChange={(e) => setRow(i, { repo: e.target.value })}
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
                  {row.repo.trim().length === 0 && <p className="text-caption text-danger">Wähle ein Repo.</p>}
                </>
              ) : (
                <>
                  <input
                    value={row.newRepo}
                    onChange={(e) => setRow(i, { newRepo: e.target.value })}
                    placeholder="mein-neuer-service"
                    className="w-full rounded-md border border-separator bg-surface px-2.5 py-1.5 font-mono text-footnote text-text-primary outline-none focus:border-accent/50"
                  />
                  {!rowValid(row) && (
                    <p className="text-caption text-danger">
                      Ungültiger Repo-Name (erlaubt: Buchstaben, Ziffern, Punkt, Unterstrich, Bindestrich; max. 100 Zeichen).
                    </p>
                  )}
                </>
              )}
            </div>
          ))}
          <button
            type="button"
            onClick={addRow}
            className="flex w-fit items-center gap-1 rounded-md px-1 py-1 text-caption font-medium text-accent transition duration-fast hover:text-accent/80"
          >
            <PlusIcon className="h-3.5 w-3.5" /> Weiteres Ziel
          </button>
        </div>
      </div>

      <div>
        <p className="mb-1.5 text-caption font-semibold uppercase tracking-wide text-text-tertiary">Termin</p>
        <div className="flex flex-wrap items-center gap-2">
          <input
            type="datetime-local"
            value={due}
            min={minDue}
            onChange={(e) => setDue(e.target.value)}
            className={cn(
              'rounded-md border bg-surface px-2.5 py-1.5 text-footnote text-text-primary outline-none focus:border-accent/50',
              dueInPast ? 'border-danger' : 'border-separator',
            )}
          />
          {due && (
            <Button variant="ghost" size="sm" onClick={() => setDue('')}>
              Termin entfernen
            </Button>
          )}
        </div>
        {dueInPast ? (
          <p className="mt-1.5 text-caption text-danger">Der Termin darf nicht in der Vergangenheit liegen.</p>
        ) : (
          <p className="mt-1.5 text-caption text-text-tertiary">
            {due ? 'Das ToDo läuft einmalig zu diesem Zeitpunkt.' : 'Ohne Termin läuft das ToDo nur über „Jetzt ausführen“.'}
          </p>
        )}
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

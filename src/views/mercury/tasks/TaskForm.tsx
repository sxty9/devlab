// TaskForm — the ONE create/edit form for BOTH kinds (auto runs and todos) on one machinery
// (REQ-005; dissolves the RunsView/TodosView structural twin, B-32/B-33). The kind decides
// which sections apply: auto = schedule + axiom picker + active switch; todo = task text,
// targets (existing or to-be-created repos, REQ-006), media (file dialog and clipboard as
// equal input paths of one access point, REQ-007) and the execution moment ("now"
// preselected, no past moments, REQ-008). The three tunables ride the shared ladder
// (ui/RunTuning, REQ-009/010).
import { useCallback, useMemo, useState } from 'react';
import { getDataSource } from '@/data';
import { useToast } from '@/ui/Toast';
import { Button } from '@/ui/Button';
import { Segmented } from '@/ui/Segmented';
import { Person } from '@/ui/Person';
import { RunTuningFields } from '@/ui/RunTuning';
import { cn } from '@/lib/cn';
import { toBase64, humanSize, filesFromClipboard } from '@/lib/file';
import { FileIcon, PlusIcon, XIcon } from '@/ui/icons';
import {
  EXECUTION_MODES,
  MAX_ATTACHMENTS,
  NEW_REPO_RE,
  categoryLabel,
  defaultDueLocalInput,
  errMsg,
  initialExecutionMode,
  initialTargetRows,
  isImageMime,
  isoToLocalInput,
  localInputToIso,
  nowLocalInput,
  planAttachmentIngest,
  repoOptions,
  scheduleInvalid,
  targetRowValid,
  toRunTargets,
  type ExecutionMode,
  type TargetRow,
} from './logic';
import type { StartTarget } from '../exec/StartDialog';
import type {
  Repo,
  Run,
  RunAttachment,
  RunCoverage,
  RunInput,
  RunKind,
  RunSchedule,
  RunTuning,
} from '@/types';

const labelClass = 'mb-1.5 text-caption font-semibold uppercase tracking-wide text-text-tertiary';
const inputClass =
  'w-full rounded-md border border-separator bg-surface px-3 py-2 text-footnote text-text-primary outline-none focus:border-accent/50';

/** A newly picked file held in the editor until the task is saved (then uploaded to its id). */
interface PendingAttachment {
  name: string;
  b64: string;
  size: number;
  mime: string;
}

export interface TaskFormProps {
  kind: RunKind;
  /** null = create. */
  base: Run | null;
  /** Todo target picker choices — every repo of the instance (REQ-006.3). */
  repos?: Repo[];
  /** Auto axiom picker input. */
  coverage?: RunCoverage | null;
  onCancel: () => void;
  onSaved: (id: string) => void | Promise<void>;
  /** Starts the saved entry through the surface's ONE start flow (execution mode "now"): it asks
   *  about resuming, offers the slot decision on a contended floor, and names every outcome. The
   *  flow is mounted by the surface, so its dialogs survive this editor closing. */
  onStart?: (target: StartTarget) => void | Promise<void>;
}

export function TaskForm({ kind, base, repos = [], coverage, onCancel, onSaved, onStart }: TaskFormProps) {
  const source = useMemo(() => getDataSource(), []);
  const { toast } = useToast();

  const [title, setTitle] = useState(base?.title ?? '');
  const [tuning, setTuning] = useState<RunTuning>(base?.tuning ?? {});
  const [busy, setBusy] = useState(false);

  // auto
  const [active, setActive] = useState(base?.active ?? true);
  const [schedule, setSchedule] = useState<RunSchedule>(base?.schedule ?? { kind: 'daily', timeOfDay: '03:00' });
  const [axiomIds, setAxiomIds] = useState<string[]>(base?.axiomIds ?? []);

  // todo
  const [task, setTask] = useState(base?.task ?? '');
  const [rows, setRows] = useState<TargetRow[]>(() => initialTargetRows(base));
  const [mode, setMode] = useState<ExecutionMode>(() => initialExecutionMode(base));
  const [due, setDue] = useState(() => (base?.dueAt ? isoToLocalInput(base.dueAt) : defaultDueLocalInput()));
  const [existingAtts] = useState<RunAttachment[]>(base?.attachments ?? []);
  const [removedIds, setRemovedIds] = useState<ReadonlySet<string>>(() => new Set());
  const [pending, setPending] = useState<PendingAttachment[]>([]);

  const visibleAtts = useMemo(() => existingAtts.filter((a) => !removedIds.has(a.id)), [existingAtts, removedIds]);

  // The ONE ingest pipeline — the file dialog and the clipboard both land here (REQ-007.2).
  const pickFiles = useCallback(
    async (files: File[]) => {
      const taken = [...visibleAtts.map((a) => a.filename), ...pending.map((p) => p.name)];
      const plan = planAttachmentIngest(taken, files);
      for (const rej of plan.rejected) {
        toast({
          title:
            rej.reason === 'too-many'
              ? `At most ${MAX_ATTACHMENTS} files per todo`
              : rej.reason === 'too-large'
                ? 'File too large (max. 25 MiB)'
                : 'A file with this name is already attached',
          description: rej.name,
          variant: 'danger',
        });
      }
      const added: PendingAttachment[] = [];
      for (const file of plan.accepted) {
        try {
          added.push({ name: file.name, b64: await toBase64(file), size: file.size, mime: file.type });
        } catch {
          toast({ title: 'Could not read the file', description: file.name, variant: 'danger' });
        }
      }
      if (added.length) setPending((p) => [...p, ...added]);
    },
    [visibleAtts, pending, toast],
  );

  const sortedRepos = useMemo(() => repoOptions(repos), [repos]);
  // Pinned at mount so the picker's floor is stable while the form is open; save() re-checks live.
  const minDue = useMemo(() => nowLocalInput(), []);

  const isTodo = kind === 'todo';
  const targetsOk = !isTodo || (rows.length > 0 && rows.every(targetRowValid));
  const weeklyOk = isTodo || schedule.kind !== 'weekly' || (schedule.weekdays?.length ?? 0) > 0;
  const scheduleBad = isTodo && scheduleInvalid(mode, due, minDue);
  const valid =
    title.trim().length > 0 &&
    (isTodo ? task.trim().length > 0 && targetsOk && !scheduleBad : axiomIds.length > 0 && weeklyOk);

  const save = async () => {
    if (!valid || busy) return;
    let dueIso: string | null = null;
    if (isTodo && mode === 'scheduled') {
      // Re-check against a fresh clock so a form left open across the minute boundary cannot
      // slip a past moment through the mount-pinned floor (REQ-008.2).
      dueIso = localInputToIso(due);
      if (!dueIso || dueIso < new Date().toISOString()) {
        toast({ title: "The time can't be in the past", variant: 'danger' });
        return;
      }
    }
    setBusy(true);
    const body: RunInput = isTodo
      ? { kind, title: title.trim(), task: task.trim(), targets: toRunTargets(rows), dueAt: dueIso, tuning }
      : {
          kind,
          title: title.trim(),
          active,
          schedule:
            schedule.kind === 'weekly'
              ? { kind: 'weekly', timeOfDay: schedule.timeOfDay, weekdays: [...(schedule.weekdays ?? [])].sort((a, b) => a - b) }
              : { kind: 'daily', timeOfDay: schedule.timeOfDay },
          axiomIds,
          tuning,
        };
    try {
      const saved = base ? await source.mercuryUpdateRun(base.id, body) : await source.mercuryCreateRun(body);
      if (isTodo) {
        // Commit media against the saved id: removals first (frees the name), then uploads. A
        // media failure does not undo the saved todo — surface it and carry on.
        for (const attId of removedIds) {
          try {
            await source.mercuryDeleteAttachment(saved.id, attId);
          } catch (e) {
            toast({ title: 'Could not remove an attachment', description: errMsg(e), variant: 'danger' });
          }
        }
        for (const p of pending) {
          try {
            await source.mercuryUploadAttachment(saved.id, p.name, p.b64);
          } catch (e) {
            toast({ title: `Attachment "${p.name}" failed`, description: errMsg(e), variant: 'danger' });
          }
        }
      }
      // The saved entry is committed either way — the surface takes over the pane.
      await onSaved(saved.id);
      if (isTodo && mode === 'now' && onStart) {
        // "Now": save, then execute at once through the very same start access point the detail
        // view uses (REQ-008.1) — including the slot decision. The todo is already saved, so a
        // refused or queued start is an answer, never a lost todo.
        await onStart({ id: saved.id, title: title.trim() });
      } else {
        toast({ title: base ? 'Saved' : 'Created', variant: 'success' });
      }
    } catch (e) {
      toast({ title: 'Saving failed', description: errMsg(e), variant: 'danger' });
      setBusy(false);
    }
  };

  const setRow = (i: number, patch: Partial<TargetRow>) => setRows((rs) => rs.map((r, j) => (j === i ? { ...r, ...patch } : r)));

  return (
    // A paste anywhere in the editor attaches its clipboard files — the same pipeline as the
    // dialog, so the clipboard is an equal upload path (REQ-007.2). Text pastes carry no files
    // and fall through to the focused field untouched.
    <div
      className="mx-auto flex max-w-2xl flex-col gap-5 px-8 py-7"
      onPaste={
        isTodo
          ? (e) => {
              const files = filesFromClipboard(e);
              if (files.length === 0) return;
              e.preventDefault();
              void pickFiles(files);
            }
          : undefined
      }
    >
      <h1 className="text-title3 font-semibold tracking-tight text-text-primary">
        {base ? (isTodo ? 'Edit todo' : 'Edit run') : isTodo ? 'New todo' : 'New run'}
      </h1>

      <div>
        <p className={labelClass}>Title</p>
        <input autoFocus value={title} onChange={(e) => setTitle(e.target.value)} className={inputClass} />
        {title.trim().length === 0 && <p className="mt-1.5 text-caption text-danger">A title is required.</p>}
      </div>

      {isTodo ? (
        <>
          <div>
            <p className={labelClass}>Task</p>
            <textarea
              value={task}
              onChange={(e) => setTask(e.target.value)}
              rows={8}
              className={cn('dl-scroll resize-y', inputClass)}
            />
            {task.trim().length === 0 && <p className="mt-1.5 text-caption text-danger">A task description is required.</p>}
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
            <p className={labelClass}>Targets</p>
            <div className="flex flex-col gap-2">
              {rows.map((row, i) => (
                <div key={i} className="flex flex-col gap-2 rounded-card border border-separator bg-surface p-3">
                  <div className="flex items-center gap-2">
                    <Segmented<'existing' | 'new'>
                      className="w-fit"
                      value={row.kind}
                      options={[
                        { value: 'existing', label: 'Existing repo' },
                        { value: 'new', label: 'Create new repo' },
                      ]}
                      onChange={(k) => setRow(i, { kind: k })}
                    />
                    <div className="flex-1" />
                    {rows.length > 1 && (
                      <Button variant="ghost" size="sm" onClick={() => setRows((rs) => rs.filter((_, j) => j !== i))}>
                        Remove
                      </Button>
                    )}
                  </div>
                  {row.kind === 'existing' ? (
                    <>
                      <select value={row.repo} onChange={(e) => setRow(i, { repo: e.target.value })} className={inputClass}>
                        <option value="">Pick a repo…</option>
                        {sortedRepos.map((r) => (
                          <option key={r.id} value={r.id}>
                            {r.name} ({r.fullName})
                          </option>
                        ))}
                      </select>
                      {row.repo.trim().length === 0 && <p className="text-caption text-danger">Pick a repo.</p>}
                    </>
                  ) : (
                    <>
                      <input
                        value={row.newRepo}
                        onChange={(e) => setRow(i, { newRepo: e.target.value })}
                        placeholder="my-new-service"
                        className={cn('font-mono', inputClass)}
                      />
                      {!NEW_REPO_RE.test(row.newRepo.trim()) && (
                        <p className="text-caption text-danger">
                          Invalid repo name (letters, digits, dot, underscore, hyphen; max. 100 characters).
                        </p>
                      )}
                    </>
                  )}
                </div>
              ))}
              <button
                type="button"
                onClick={() => setRows((rs) => [...rs, { kind: 'existing', repo: '', newRepo: '' }])}
                className="flex w-fit items-center gap-1 rounded-md px-1 py-1 text-caption font-medium text-accent transition duration-fast hover:text-accent/80"
              >
                <PlusIcon className="h-3.5 w-3.5" /> Another target
              </button>
            </div>
          </div>

          <div>
            <p className={labelClass}>Execution</p>
            <Segmented<ExecutionMode>
              className="w-fit"
              value={mode}
              options={EXECUTION_MODES.map((m) => ({ value: m.id, label: m.label }))}
              onChange={setMode}
            />
            {mode === 'scheduled' && (
              <div className="mt-2">
                <input
                  type="datetime-local"
                  value={due}
                  min={minDue}
                  onChange={(e) => setDue(e.target.value)}
                  className={cn(
                    'rounded-md border bg-surface px-2.5 py-1.5 text-footnote text-text-primary outline-none focus:border-accent/50',
                    scheduleBad ? 'border-danger' : 'border-separator',
                  )}
                />
                {scheduleBad && (
                  <p className="mt-1.5 text-caption text-danger">
                    {due === '' ? 'Pick a date and time.' : "The time can't be in the past."}
                  </p>
                )}
              </div>
            )}
          </div>
        </>
      ) : (
        <>
          <label className="flex cursor-pointer items-center gap-2.5">
            <input type="checkbox" checked={active} onChange={(e) => setActive(e.target.checked)} className="h-4 w-4 accent-accent" />
            <span className="text-footnote text-text-primary">Active</span>
          </label>

          <div>
            <p className={labelClass}>Schedule</p>
            <ScheduleFields schedule={schedule} onChange={setSchedule} />
            {!weeklyOk && <p className="mt-1.5 text-caption text-danger">Pick at least one weekday.</p>}
          </div>

          <div>
            <p className={labelClass}>Axioms ({axiomIds.length})</p>
            {coverage ? (
              <AxiomPicker
                coverage={coverage}
                editingId={base?.id}
                selectedIds={axiomIds}
                onToggle={(id) => setAxiomIds((prev) => (prev.includes(id) ? prev.filter((x) => x !== id) : [...prev, id]))}
              />
            ) : (
              <p className="text-footnote text-text-tertiary">Loading the axiom catalog…</p>
            )}
            {axiomIds.length === 0 && <p className="mt-1.5 text-caption text-danger">Pick at least one axiom.</p>}
          </div>
        </>
      )}

      <RunTuningFields value={tuning} onChange={setTuning} />

      <div className="flex items-center gap-2">
        <Button variant="primary" size="sm" disabled={!valid || busy} onClick={save}>
          {busy ? 'Saving…' : 'Save'}
        </Button>
        <Button variant="secondary" size="sm" disabled={busy} onClick={onCancel}>
          Cancel
        </Button>
      </div>
    </div>
  );
}

/** Schedule editor: kind + time, and — only for weekly — the seven weekday toggles (Mon→Sun). */
function ScheduleFields({ schedule, onChange }: { schedule: RunSchedule; onChange: (s: RunSchedule) => void }) {
  return (
    <div className="flex flex-col gap-3 rounded-card border border-separator bg-surface p-3">
      <div className="flex flex-wrap items-center gap-3">
        <select
          value={schedule.kind}
          onChange={(e) => {
            const k = e.target.value as RunSchedule['kind'];
            onChange(
              k === 'weekly'
                ? { kind: k, timeOfDay: schedule.timeOfDay, weekdays: schedule.weekdays ?? [] }
                : { kind: k, timeOfDay: schedule.timeOfDay },
            );
          }}
          className="rounded-md border border-separator bg-surface px-2.5 py-1.5 text-footnote text-text-primary outline-none focus:border-accent/50"
        >
          <option value="daily">Daily</option>
          <option value="weekly">Weekly</option>
        </select>
        <div className="flex items-center gap-2">
          <span className="text-footnote text-text-secondary">at</span>
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
          {WEEKDAY_TOGGLES.map((w) => {
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

// Weekdays are stored in the Go convention (0=Sun … 6=Sat) but always DISPLAYED Mon→Sun.
const WEEKDAY_TOGGLES = [
  { num: 1, label: 'Mon' },
  { num: 2, label: 'Tue' },
  { num: 3, label: 'Wed' },
  { num: 4, label: 'Thu' },
  { num: 5, label: 'Fri' },
  { num: 6, label: 'Sat' },
  { num: 0, label: 'Sun' },
] as const;

/** Axiom picker: every axiom (title + category), a text filter, and a coverage badge showing
 *  how many OTHER runs already back each axiom. An uncovered axiom is shown honestly as
 *  uncovered; while a background assignment is pending it reads as transient. */
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
          // Uncovered = backed by NO run at all (an axiom held only by the run being edited is covered).
          uncovered: (coverage.covered[id] ?? []).length === 0,
        }))
        .sort((a, b) => a.title.localeCompare(b.title)),
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
          placeholder="Filter axioms…"
          className="w-full rounded-md border border-separator bg-bg-base px-2.5 py-1.5 text-footnote text-text-primary outline-none focus:border-accent/50"
        />
      </div>
      <div className="dl-scroll max-h-80 overflow-y-auto p-1.5">
        {shown.length === 0 ? (
          <p className="px-2 py-3 text-caption text-text-tertiary">No axioms found.</p>
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
                        in {it.others.length} {it.others.length === 1 ? 'run' : 'runs'}
                      </span>
                    )}
                    {it.uncovered &&
                      (coverage.pending ? (
                        <span className="shrink-0 rounded bg-fill/15 px-1.5 py-0.5 text-caption font-medium text-text-tertiary">
                          assignment running…
                        </span>
                      ) : (
                        <span className="shrink-0 rounded bg-danger/15 px-1.5 py-0.5 text-caption font-medium text-danger">uncovered</span>
                      ))}
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

/** One attachment tile: an image thumbnail or a file glyph, with the name and size. Shared by
 *  the editor (add/remove) and the read-only detail grid. */
export function AttachmentCard({
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
          aria-label={`Remove ${name}`}
          className="shrink-0 rounded p-1 text-text-tertiary transition hover:bg-fill/10 hover:text-danger"
        >
          <XIcon className="h-3.5 w-3.5" />
        </button>
      )}
    </div>
  );
}

/** The editor's media section: the file-dialog half of the one upload access point (the form's
 *  onPaste is the clipboard half — both feed pickFiles). */
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
  onPick: (files: File[]) => void;
  onRemoveExisting: (id: string) => void;
  onRemovePending: (index: number) => void;
}) {
  const full = existing.length + pending.length >= MAX_ATTACHMENTS;
  return (
    <div>
      <p className={labelClass}>Media</p>
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
        <label
          className={cn(
            'flex w-fit cursor-pointer items-center gap-1 rounded-md px-1 py-1 text-caption font-medium transition duration-fast',
            full ? 'cursor-not-allowed text-text-tertiary' : 'text-accent hover:text-accent/80',
          )}
        >
          <input
            type="file"
            multiple
            disabled={full}
            accept="image/*,application/pdf,text/*,.md,.csv,.json,.doc,.docx,.xls,.xlsx,.ppt,.pptx"
            className="hidden"
            onChange={(e) => {
              if (e.target.files?.length) onPick(Array.from(e.target.files));
              e.target.value = ''; // allow re-picking the same file
            }}
          />
          <PlusIcon className="h-3.5 w-3.5" /> Attach media
        </label>
      </div>
    </div>
  );
}

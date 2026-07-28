// TaskDetail — the SHARED reading pane of both kinds (REQ-005): plan facts, authorship
// (REQ-041: one Person rendering for humans, autonomous actors and unknown legacy records),
// the axiom chips (auto) or task text + media (todo), a lazy prompt preview and this entry's
// executions. Start/edit/delete ride the same access points as everywhere else.
import { useMemo, useRef, useState } from 'react';
import { getDataSource } from '@/data';
import { useToast } from '@/ui/Toast';
import { Button } from '@/ui/Button';
import { Person } from '@/ui/Person';
import { cn } from '@/lib/cn';
import { fmtDateTime } from '@/lib/format';
import { ChevronRightIcon, PlayIcon } from '@/ui/icons';
import { AttachmentCard } from './TaskForm';
import { errMsg, isImageMime, scheduleSummary, storedBudgetLabel, targetLabel, tuningChips } from './logic';
import type { Actor, Repo, Run, RunResult } from '@/types';

export interface TaskDetailProps {
  run: Run;
  repos: Repo[];
  axioms: Record<string, string>;
  live: boolean;
  /** This run's executions, newest first (the surface already holds the kind's set). */
  results: RunResult[];
  onEdit: () => void;
  onDeleted: () => void | Promise<void>;
  onRunStarted: () => void;
}

export function TaskDetail({ run, repos, axioms, live, results, onEdit, onDeleted, onRunStarted }: TaskDetailProps) {
  const source = useMemo(() => getDataSource(), []);
  const { toast } = useToast();
  const [busy, setBusy] = useState(false);
  const [starting, setStarting] = useState(false);
  const [promptOpen, setPromptOpen] = useState(false);
  const [prompt, setPrompt] = useState<string | null>(null);
  const promptLoading = useRef(false);

  const isTodo = run.kind === 'todo';

  const togglePrompt = async () => {
    const next = !promptOpen;
    setPromptOpen(next);
    if (next && prompt === null && !promptLoading.current) {
      promptLoading.current = true;
      try {
        setPrompt((await source.mercuryRunPrompt(run.id)).prompt);
      } catch (e) {
        toast({ title: 'Could not load the prompt', description: errMsg(e), variant: 'danger' });
        setPromptOpen(false);
      } finally {
        promptLoading.current = false;
      }
    }
  };

  const del = async () => {
    // A dangerous action names its effect and what remains (REQ-040): the definition goes,
    // recorded executions stay in the history.
    if (busy || !window.confirm(`Delete "${run.title}"? Its recorded executions stay in the history.`)) return;
    setBusy(true);
    try {
      await source.mercuryDeleteRun(run.id);
      toast({ title: 'Deleted', variant: 'default' });
      await onDeleted();
    } catch (e) {
      toast({ title: 'Deleting failed', description: errMsg(e), variant: 'danger' });
      setBusy(false);
    }
  };

  const runNow = async () => {
    if (starting) return;
    setStarting(true);
    try {
      const outcome = await source.mercuryRunNow(run.id);
      if (outcome.notStarted) {
        // The reasoned refusal (K-3: fully delivered work starts not at all).
        toast({ title: 'Not started', description: outcome.notStarted, variant: 'default' });
      } else if (outcome.queued) {
        toast({ title: 'Queued', description: 'A restart is pending; the run starts by itself afterwards.', variant: 'default' });
      } else {
        toast({ title: outcome.resumed ? 'Execution resumed' : 'Execution started', variant: 'success' });
        onRunStarted();
      }
    } catch (e) {
      toast({ title: 'Start failed', description: errMsg(e), variant: 'danger' });
    } finally {
      setStarting(false);
    }
  };

  const axiomIds = run.axiomIds ?? [];
  const chips = tuningChips(run.tuning);

  return (
    <article className="mx-auto max-w-3xl px-8 py-7">
      <div className="flex items-start justify-between gap-4">
        <div className="min-w-0">
          <h1 className="text-title3 font-semibold tracking-tight text-text-primary">{run.title}</h1>
          <p className="mt-1 text-footnote text-text-secondary">
            {isTodo ? targetLabel(run, repos) : scheduleSummary(run.schedule)}
          </p>
        </div>
        <div className="flex shrink-0 flex-wrap items-center justify-end gap-1.5">
          <Button variant="primary" size="sm" disabled={starting} onClick={runNow}>
            <PlayIcon className="h-3.5 w-3.5" /> {starting ? 'Starting…' : 'Run now'}
          </Button>
          <Button variant="secondary" size="sm" onClick={onEdit}>
            Edit
          </Button>
          <Button variant="secondary" size="sm" disabled={busy} onClick={del}>
            Delete
          </Button>
        </div>
      </div>

      <div className="mt-4 flex flex-wrap items-center gap-2 text-caption text-text-tertiary">
        {!isTodo && (
          <span className={cn('rounded px-1.5 py-0.5 font-medium', run.active ? 'bg-success/15 text-success' : 'bg-fill/15 text-text-tertiary')}>
            {run.active ? 'Active' : 'Inactive'}
          </span>
        )}
        {live && <span className="rounded bg-warning/15 px-1.5 py-0.5 font-medium text-warning">running</span>}
        {chips.map((c) => (
          <span key={c} className="rounded bg-fill/10 px-1.5 py-0.5 font-medium text-text-secondary">
            {c}
          </span>
        ))}
        {/* The governing budget is always named (REQ-010.4) — a reference names the default. */}
        {run.tuning.timeBudget === undefined && (
          <span className="rounded bg-fill/10 px-1.5 py-0.5 font-medium text-text-secondary">{storedBudgetLabel(undefined)} budget</span>
        )}
        {isTodo && <span>Due: {run.dueAt ? fmtDateTime(run.dueAt) : 'on demand'}</span>}
      </div>

      <TaskAuthorship className="mt-3" authorship={run.authorship} />

      {isTodo ? (
        <>
          <section className="mt-6">
            <p className="mb-1.5 text-caption font-semibold uppercase tracking-wide text-text-tertiary">Task</p>
            {run.task ? (
              <p className="whitespace-pre-wrap rounded-card border border-separator bg-surface p-3 text-footnote text-text-secondary">
                {run.task}
              </p>
            ) : (
              <p className="text-footnote text-text-tertiary">No task description.</p>
            )}
          </section>
          {(run.attachments?.length ?? 0) > 0 && (
            <section className="mt-6">
              <p className="mb-1.5 text-caption font-semibold uppercase tracking-wide text-text-tertiary">Media</p>
              <div className="grid grid-cols-1 gap-2 sm:grid-cols-2">
                {run.attachments!.map((a) => (
                  <AttachmentCard
                    key={a.id}
                    name={a.filename}
                    mime={a.mime}
                    size={a.size}
                    preview={isImageMime(a.mime) ? source.mercuryAttachmentRawUrl(run.id, a.id) : undefined}
                    href={source.mercuryAttachmentRawUrl(run.id, a.id)}
                    uploadedBy={a.uploadedBy}
                  />
                ))}
              </div>
            </section>
          )}
        </>
      ) : (
        <section className="mt-6">
          <p className="mb-1.5 text-caption font-semibold uppercase tracking-wide text-text-tertiary">Axioms ({axiomIds.length})</p>
          {axiomIds.length === 0 ? (
            <p className="text-footnote text-text-tertiary">No axioms assigned.</p>
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
      )}

      <section className="mt-6">
        <button
          type="button"
          onClick={togglePrompt}
          className="flex items-center gap-1.5 text-footnote font-medium text-text-secondary transition hover:text-text-primary"
        >
          <ChevronRightIcon className={cn('h-3.5 w-3.5 transition-transform duration-fast', promptOpen && 'rotate-90')} />
          Prompt preview
        </button>
        {promptOpen && (
          <pre className="dl-scroll mt-2 max-h-96 overflow-auto whitespace-pre-wrap rounded-card border border-separator bg-surface p-3 font-mono text-caption text-text-secondary">
            {prompt ?? 'Loading…'}
          </pre>
        )}
      </section>

      <section className="mt-6 border-t border-separator pt-4">
        <p className="mb-1.5 text-caption font-semibold uppercase tracking-wide text-text-tertiary">Executions</p>
        {live && (
          <p className="mb-3 flex items-center gap-1.5 text-footnote text-text-secondary">
            <span className="h-2 w-2 animate-pulse rounded-full bg-warning" /> Running — follow it in the Active section.
          </p>
        )}
        {results.length === 0 ? (
          <p className="text-footnote text-text-tertiary">
            {live ? 'Running — follow it in the Active section.' : 'No executions yet.'}
          </p>
        ) : (
          <ul className="flex flex-col gap-1.5">
            {results.map((r) => (
              <ExecutionRow key={r.id} result={r} />
            ))}
          </ul>
        )}
      </section>
    </article>
  );
}

/** One execution line: the server-stated repo outcomes (nothing derived client-side, B-35),
 *  time and consumption. */
function ExecutionRow({ result }: { result: RunResult }) {
  const repoCount = result.repos.length;
  const allDone = repoCount > 0 && result.repos.every((rp) => rp.done);
  const ok = allDone && result.repos.every((rp) => rp.succeeded);
  return (
    <li className="flex flex-wrap items-center gap-x-3 gap-y-1 text-footnote text-text-secondary">
      <span className={cn('h-2 w-2 shrink-0 rounded-full', !result.endedAt ? 'animate-pulse bg-warning' : ok ? 'bg-success' : 'bg-danger')} />
      <span>{fmtDateTime(result.startedAt)}</span>
      <span className="text-text-tertiary">
        · {repoCount} {repoCount === 1 ? 'repo' : 'repos'}
      </span>
      {result.legacy && <span className="rounded bg-fill/15 px-1.5 py-0.5 text-caption text-text-tertiary">archive</span>}
      {result.synthetic && <span className="rounded bg-fill/15 px-1.5 py-0.5 text-caption text-text-tertiary">reconciled</span>}
      <span className="text-caption text-text-tertiary">
        {result.usage.inputTokens + result.usage.outputTokens > 0
          ? `${result.usage.inputTokens.toLocaleString()} in · ${result.usage.outputTokens.toLocaleString()} out`
          : ''}
      </span>
    </li>
  );
}

/** Created-by / last-edited-by from the run's Authorship — through the ONE Person component,
 *  so a human, the autonomous system and an unknown legacy author render uniformly (REQ-041).
 *  Legacy records with no author/time show unknown and no timestamp — never a guessed person
 *  and never the Go zero time. */
function TaskAuthorship({ authorship, className }: { authorship: Run['authorship']; className?: string }) {
  const created = authorship.created;
  const updated = authorship.updated;
  const edited = !sameActor(created, updated);
  return (
    <div className={cn('flex flex-wrap items-center gap-x-2 gap-y-1 text-caption text-text-tertiary', className)}>
      <span className="inline-flex items-center gap-1.5">
        Created by
        <Person username={created.user} autonomous={created.autonomous} size="sm" />
        {realTime(authorship.createdAt) && <span>· {fmtDateTime(authorship.createdAt)}</span>}
      </span>
      {edited && (
        <span className="inline-flex items-center gap-1.5">
          · Last edited by
          <Person username={updated.user} autonomous={updated.autonomous} size="sm" />
          {realTime(authorship.updatedAt) && <span>· {fmtDateTime(authorship.updatedAt)}</span>}
        </span>
      )}
    </div>
  );
}

/** True for a real timestamp — absent values and the Go zero time carry no information. */
const realTime = (iso?: string): boolean => !!iso && !iso.startsWith('0001-');

const sameActor = (a: Actor, b: Actor): boolean => a.user === b.user && !!a.autonomous === !!b.autonomous;

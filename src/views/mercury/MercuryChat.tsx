import { useEffect, useMemo, useRef, useState } from 'react';
import { getDataSource } from '@/data';
import type { DataSource } from '@/data/source';
import { useToast } from '@/ui/Toast';
import { Button } from '@/ui/Button';
import { cn } from '@/lib/cn';
import { LightbulbIcon, SendIcon } from '@/ui/icons';
import type { ActionTarget, MercuryAction, RunChatMessage, RunSchedule } from '@/types';

/** Uniform error-to-string, mirroring the rest of the Mercury surface. */
const msg = (e: unknown) => String((e as Error)?.message ?? e);

/** A human schedule line for a proposed run, kept local so the chat stays self-contained. */
const WEEKDAYS: { num: number; label: string }[] = [
  { num: 1, label: 'Mon' },
  { num: 2, label: 'Tue' },
  { num: 3, label: 'Wed' },
  { num: 4, label: 'Thu' },
  { num: 5, label: 'Fri' },
  { num: 6, label: 'Sat' },
  { num: 0, label: 'Sun' },
];

function scheduleSummary(s: RunSchedule): string {
  if (s.kind === 'daily') return `daily ${s.timeOfDay}`;
  const days = WEEKDAYS.filter((w) => s.weekdays?.includes(w.num)).map((w) => w.label);
  if (days.length === 0) return `weekly · ${s.timeOfDay}`;
  return `weekly ${days.join(', ')} · ${s.timeOfDay}`;
}

const SECTION_LABEL: Record<'axiome' | 'regeln' | 'laeufe' | 'meta', string> = {
  axiome: 'Axiom',
  regeln: 'Rule',
  laeufe: 'Run rule',
  meta: 'Meta-axiom',
};

function targetsSummary(ts: ActionTarget[]): string {
  return ts.map((t) => (t.newRepo ? `new: ${t.newRepo}` : t.repo)).filter(Boolean).join(', ') || '—';
}

/** Whether an action removes something — its confirm button reads "Delete" and is styled as danger. */
function isDestructive(a: MercuryAction): boolean {
  return a.kind === 'delete_run' || a.kind === 'delete_record';
}

/** The card headline for a proposed action — a plain-language description of what will happen. */
function actionTitle(a: MercuryAction): string {
  switch (a.kind) {
    case 'create_todo':
      return `Create todo: ${a.name}`;
    case 'create_run':
      return `Create automatic run: ${a.name}`;
    case 'add_record':
      return `Add ${SECTION_LABEL[a.section].toLowerCase()}: ${a.titel}`;
    case 'edit_record':
      return `Edit record: ${a.titel}`;
    case 'delete_record':
      return 'Delete record';
    case 'delete_run':
      return 'Delete run / todo';
    case 'run_now':
      return 'Run now';
    case 'plan_runs':
      return `Apply ${a.runs.length} ${a.runs.length === 1 ? 'run' : 'runs'}`;
  }
}

/** Apply an accepted action through the SAME data-source method the Mercury UI uses — the chat opens no
 *  parallel path. add_record can bounce on the classifier's duplicate/conformance gate; `force` re-runs
 *  it past that gate (the "create anyway" choice the UI offers). */
async function dispatchAction(source: DataSource, a: MercuryAction, force = false): Promise<void> {
  switch (a.kind) {
    case 'create_todo':
      await source.mercuryCreateRun({
        title: a.name, kind: 'todo', task: a.task,
        targets: a.targets.map((t) => (t.newRepo ? { repo: t.newRepo, create: true } : { repo: t.repo ?? '' })),
        dueAt: a.dueAt ?? null,
      });
      return;
    case 'create_run':
      await source.mercuryCreateRun({ title: a.name, kind: 'auto', active: true, schedule: a.schedule, axiomIds: a.axiomIds });
      return;
    case 'add_record': {
      const res = await source.mercuryAddAxiom(a.titel, a.body, a.section, force);
      if (!force && res.duplicate) {
        throw new NeedsForceError('A very similar record already exists.');
      }
      if (!force && res.nonconform) {
        const first = res.nonconform.violations[0]?.meta;
        throw new NeedsForceError(first ? `Violates the meta-axiom "${first}".` : 'Violates a meta-axiom.');
      }
      return;
    }
    case 'edit_record':
      await source.mercuryEditAxiom(a.path, a.titel, a.body);
      return;
    case 'delete_record':
      await source.mercuryDeleteAxiom(a.path);
      return;
    case 'delete_run':
      await source.mercuryDeleteRun(a.runId);
      return;
    case 'run_now':
      await source.mercuryRunNow(a.runId);
      return;
    case 'plan_runs':
      await source.mercuryApplyRunProposal(a.mode, { runs: a.runs });
      return;
  }
}

/** Thrown by dispatchAction when adding a record hit the duplicate/conformance gate — the caller offers
 *  a "create anyway" (force) retry instead of a plain failure. */
class NeedsForceError extends Error {}

/** MercuryChat — the AI chat for ALL of Mercury (axioms, implementation rules, run rules, runs
 *  and todos). It sends the whole transcript each turn; when the model proposes a reviewable
 *  action (create a todo or run, add/edit/delete a record, run something now, replan the runs),
 *  an inline "Apply" applies it through the same access point the UI uses.
 *
 *  It owns the right edge of the Mercury view: collapsed it is a slim vertical "AI chat" tab, open it
 *  is a docked, resizable right sidebar (IDE-assistant style). The parent lays it out as a flex
 *  sibling either way, so the toggle symbol and the panel share the same (right) side. The
 *  conversation lives INSIDE this component and the parent keeps it mounted, so the history survives
 *  collapsing and reopening. */
export default function MercuryChat({
  open,
  onOpenChange,
  onApplied,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  onApplied?: () => void;
}) {
  const source = useMemo(() => getDataSource(), []);
  const { toast } = useToast();
  const [messages, setMessages] = useState<RunChatMessage[]>([]);
  const [input, setInput] = useState('');
  const [busy, setBusy] = useState(false);
  const [applying, setApplying] = useState(false);
  const [pendingAction, setPendingAction] = useState<MercuryAction | null>(null);
  const [forceHint, setForceHint] = useState<string | null>(null);
  const [width, setWidth] = useState(400);
  const resizing = useRef(false);
  const bottomRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    if (!open) return;
    bottomRef.current?.scrollIntoView({ block: 'end' });
  }, [messages, busy, open]);

  // Drag the left edge to resize. The panel is docked at the viewport's right edge, so its width is
  // simply the distance from the cursor to that edge (clamped).
  useEffect(() => {
    const onMove = (e: MouseEvent) => {
      if (!resizing.current) return;
      setWidth(Math.min(720, Math.max(320, window.innerWidth - e.clientX)));
    };
    const onUp = () => {
      resizing.current = false;
      document.body.style.userSelect = '';
    };
    window.addEventListener('mousemove', onMove);
    window.addEventListener('mouseup', onUp);
    return () => {
      window.removeEventListener('mousemove', onMove);
      window.removeEventListener('mouseup', onUp);
    };
  }, []);

  const send = async () => {
    const text = input.trim();
    if (!text || busy) return;
    const next: RunChatMessage[] = [...messages, { role: 'user', content: text }];
    setMessages(next);
    setInput('');
    setBusy(true);
    setForceHint(null);
    try {
      const r = await source.mercuryChat(next);
      setMessages([...next, { role: 'assistant', content: r.reply }]);
      setPendingAction(r.action ?? null);
    } catch (e) {
      toast({ title: 'AI chat failed', description: msg(e), variant: 'danger' });
    } finally {
      setBusy(false);
    }
  };

  const applyAction = async (force = false) => {
    if (!pendingAction || applying) return;
    setApplying(true);
    try {
      await dispatchAction(source, pendingAction, force);
      toast({ title: 'Applied', variant: 'success' });
      setPendingAction(null);
      setForceHint(null);
      onApplied?.();
      onOpenChange(false);
    } catch (e) {
      if (e instanceof NeedsForceError) {
        setForceHint(msg(e));
      } else {
        toast({ title: 'Apply failed', description: msg(e), variant: 'danger' });
      }
    } finally {
      setApplying(false);
    }
  };

  // Collapsed: a slim vertical tab docked at the same right edge the panel opens on, so the KI-Chat
  // symbol and its sidebar live on one side. Clicking it expands into the full panel below.
  if (!open) {
    return (
      <aside className="flex shrink-0 border-l border-separator bg-surface-sidebar">
        <button
          type="button"
          onClick={() => onOpenChange(true)}
          aria-label="Open AI chat"
          className="flex w-10 flex-col items-center gap-2.5 py-3 text-text-secondary transition duration-fast hover:bg-fill/10 hover:text-text-primary focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-accent/50"
        >
          <LightbulbIcon className="h-5 w-5 shrink-0 text-accent" />
          <span className="text-caption font-medium tracking-wide [writing-mode:vertical-rl]">AI chat</span>
        </button>
      </aside>
    );
  }

  return (
    <aside
      style={{ width }}
      className="relative flex h-full min-h-0 shrink-0 flex-col border-l border-separator bg-surface-sidebar"
    >
      {/* left-edge resize handle */}
      <div
        onMouseDown={() => {
          resizing.current = true;
          document.body.style.userSelect = 'none';
        }}
        className="absolute left-0 top-0 z-10 h-full w-1 cursor-col-resize hover:bg-accent/40"
      />
      <header className="flex items-center justify-between gap-2 border-b border-separator px-3 py-2">
        <div className="min-w-0">
          <p className="text-footnote font-semibold text-text-primary">AI chat — Mercury</p>
          <p className="truncate text-caption text-text-tertiary">Axioms, rules, runs & todos</p>
        </div>
        <button
          type="button"
          onClick={() => onOpenChange(false)}
          aria-label="Close chat"
          className="shrink-0 rounded-md px-2 py-1 text-body text-text-tertiary transition duration-fast hover:bg-fill/10 hover:text-text-primary"
        >
          ✕
        </button>
      </header>
      <div className="flex min-h-0 flex-1 flex-col gap-3 p-3">
        <div className="dl-scroll flex min-h-0 flex-1 flex-col gap-2 overflow-y-auto rounded-card border border-separator bg-surface p-3">
          {messages.length === 0 && !busy ? (
            <p className="m-auto max-w-xs text-center text-caption text-text-tertiary">
              Ask the AI about axioms, rules, run rules, runs or todos — or ask it to create, change or run
              something. Every action is shown to you for confirmation first.
            </p>
          ) : (
            messages.map((m, i) => (
              <div
                key={i}
                className={cn(
                  'max-w-[85%] whitespace-pre-wrap rounded-card px-3 py-2 text-footnote',
                  m.role === 'user' ? 'self-end bg-accent/15 text-text-primary' : 'self-start bg-fill/10 text-text-secondary',
                )}
              >
                {m.content}
              </div>
            ))
          )}
          {busy && (
            <div className="self-start rounded-card bg-fill/10 px-3 py-2 text-footnote text-text-tertiary">
              <span className="animate-pulse">Thinking… (can take 30–120s)</span>
            </div>
          )}
          <div ref={bottomRef} />
        </div>

        {pendingAction && (
          <div className="flex flex-col gap-2 rounded-card border border-accent/30 bg-accent/10 px-3 py-2">
            <div className="flex items-center justify-between gap-3">
              <span className="min-w-0 text-caption font-medium text-text-secondary">{actionTitle(pendingAction)}</span>
              <Button
                variant={isDestructive(pendingAction) ? 'danger' : 'primary'}
                size="sm"
                disabled={applying}
                onClick={() => void applyAction()}
              >
                {applying ? 'Applying…' : isDestructive(pendingAction) ? 'Delete' : 'Apply'}
              </Button>
            </div>
            <ActionDetail action={pendingAction} />
            {forceHint && (
              <div className="flex items-center justify-between gap-3 border-t border-accent/20 pt-2">
                <span className="min-w-0 text-caption text-text-tertiary">{forceHint} Create anyway?</span>
                <Button variant="secondary" size="sm" disabled={applying} onClick={() => void applyAction(true)}>
                  Create anyway
                </Button>
              </div>
            )}
          </div>
        )}

        <div className="flex items-end gap-2">
          <textarea
            value={input}
            onChange={(e) => setInput(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === 'Enter' && !e.shiftKey) {
                e.preventDefault();
                void send();
              }
            }}
            rows={2}
            placeholder="Ask me — or ask me to create a todo, an axiom or a run… (Enter to send, Shift+Enter for a line break)"
            className="dl-scroll flex-1 resize-none rounded-md border border-separator bg-surface px-3 py-2 text-footnote text-text-primary outline-none focus:border-accent/50"
          />
          <Button variant="primary" size="sm" disabled={busy || !input.trim()} onClick={send}>
            <SendIcon className="h-4 w-4" /> Send
          </Button>
        </div>
      </div>
    </aside>
  );
}

/** The per-kind detail lines under an action's headline — a compact preview of what it will do. */
function ActionDetail({ action: a }: { action: MercuryAction }) {
  const line = 'text-caption text-text-tertiary';
  switch (a.kind) {
    case 'create_todo':
      return (
        <div className={cn('flex flex-col gap-0.5', line)}>
          <span className="truncate">Target: {targetsSummary(a.targets)}</span>
          <span className="line-clamp-2 text-text-secondary">{a.task}</span>
        </div>
      );
    case 'create_run':
      return (
        <div className={cn('flex items-center gap-2', line)}>
          <span>
            {a.axiomIds.length} {a.axiomIds.length === 1 ? 'axiom' : 'axioms'}
          </span>
          <span>· {scheduleSummary(a.schedule)}</span>
        </div>
      );
    case 'add_record':
    case 'edit_record':
      return <p className={cn('line-clamp-3', line)}>{a.body}</p>;
    case 'delete_record':
      return <p className={cn('truncate font-mono', line)}>{a.path}</p>;
    case 'delete_run':
    case 'run_now':
      return <p className={cn('truncate font-mono', line)}>{a.runId}</p>;
    case 'plan_runs':
      return (
        <ul className="flex flex-col gap-1">
          <li className={line}>{a.mode === 'replace' ? 'Replaces the current runs.' : 'Extends the existing runs.'}</li>
          {a.runs.map((r, i) => (
            <li key={i} className={cn('flex items-center gap-2', line)}>
              <span className="min-w-0 flex-1 truncate text-text-secondary">{r.name}</span>
              <span className="shrink-0">
                {r.axiomIds.length} {r.axiomIds.length === 1 ? 'axiom' : 'axioms'}
              </span>
              <span className="shrink-0">· {scheduleSummary(r.schedule)}</span>
            </li>
          ))}
        </ul>
      );
  }
}

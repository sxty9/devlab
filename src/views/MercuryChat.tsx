import { useEffect, useMemo, useRef, useState } from 'react';
import { getDataSource } from '@/data';
import { useToast } from '@/ui/Toast';
import { Button } from '@/ui/Button';
import { cn } from '@/lib/cn';
import { LightbulbIcon, SendIcon } from '@/ui/icons';
import type { RunChatMessage, RunPlan } from '@/types';

/** Uniform error-to-string, mirroring the rest of the Mercury surface. */
const msg = (e: unknown) => String((e as Error)?.message ?? e);

/** A human schedule line for a proposed run, kept local so the chat stays self-contained. */
const WEEKDAYS: { num: number; label: string }[] = [
  { num: 1, label: 'Mo' },
  { num: 2, label: 'Di' },
  { num: 3, label: 'Mi' },
  { num: 4, label: 'Do' },
  { num: 5, label: 'Fr' },
  { num: 6, label: 'Sa' },
  { num: 0, label: 'So' },
];

function scheduleSummary(s: RunPlan['runs'][number]['schedule']): string {
  if (s.kind === 'daily') return `täglich ${s.timeOfDay}`;
  const days = WEEKDAYS.filter((w) => s.weekdays?.includes(w.num)).map((w) => w.label);
  if (days.length === 0) return `wöchentlich · ${s.timeOfDay}`;
  return `wöchentlich ${days.join(', ')} · ${s.timeOfDay}`;
}

/** MercuryChat — the KI-Chat for ALL of Mercury (Axiome, Implementierungsregeln, Laufregeln, Läufe
 *  und ToDos). It sends the whole transcript each turn; when the model answers with a reviewable
 *  run-plan, an inline "Vorschlag übernehmen" applies it (mode `replace`) and closes.
 *
 *  It owns the right edge of the Mercury view: collapsed it is a slim vertical "KI-Chat" tab, open it
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
  const [pendingPlan, setPendingPlan] = useState<RunPlan | null>(null);
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
    try {
      const r = await source.mercuryChat(next);
      setMessages([...next, { role: 'assistant', content: r.reply }]);
      setPendingPlan(r.proposal ?? null);
    } catch (e) {
      toast({ title: 'KI-Chat fehlgeschlagen', description: msg(e), variant: 'danger' });
    } finally {
      setBusy(false);
    }
  };

  const applyPlan = async () => {
    if (!pendingPlan || applying) return;
    setApplying(true);
    try {
      await source.mercuryApplyRunProposal('replace', pendingPlan);
      toast({ title: 'Läufe übernommen', variant: 'success' });
      setPendingPlan(null);
      onApplied?.();
      onOpenChange(false);
    } catch (e) {
      toast({ title: 'Übernehmen fehlgeschlagen', description: msg(e), variant: 'danger' });
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
          aria-label="KI-Chat öffnen"
          className="flex w-10 flex-col items-center gap-2.5 py-3 text-text-secondary transition duration-fast hover:bg-fill/10 hover:text-text-primary focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-accent/50"
        >
          <LightbulbIcon className="h-5 w-5 shrink-0 text-accent" />
          <span className="text-caption font-medium tracking-wide [writing-mode:vertical-rl]">KI-Chat</span>
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
          <p className="text-footnote font-semibold text-text-primary">KI-Chat — Mercury</p>
          <p className="truncate text-caption text-text-tertiary">Axiome, Regeln, Läufe & ToDos</p>
        </div>
        <button
          type="button"
          onClick={() => onOpenChange(false)}
          aria-label="Chat schließen"
          className="shrink-0 rounded-md px-2 py-1 text-body text-text-tertiary transition duration-fast hover:bg-fill/10 hover:text-text-primary"
        >
          ✕
        </button>
      </header>
      <div className="flex min-h-0 flex-1 flex-col gap-3 p-3">
        <div className="dl-scroll flex min-h-0 flex-1 flex-col gap-2 overflow-y-auto rounded-card border border-separator bg-surface p-3">
          {messages.length === 0 && !busy ? (
            <p className="m-auto max-w-xs text-center text-caption text-text-tertiary">
              Frag die KI zu Axiomen, Implementierungsregeln, Laufregeln, Läufen oder ToDos — sie kann auch einen ganzen
              Vorschlag für die Läufe zurückgeben.
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
              <span className="animate-pulse">Denkt nach… (kann 30–120s dauern)</span>
            </div>
          )}
          <div ref={bottomRef} />
        </div>

        {pendingPlan && (
          <div className="flex flex-col gap-2 rounded-card border border-accent/30 bg-accent/10 px-3 py-2">
            <div className="flex items-center justify-between gap-3">
              <span className="text-caption text-text-secondary">
                Die KI hat einen Vorschlag mit {pendingPlan.runs.length} {pendingPlan.runs.length === 1 ? 'Lauf' : 'Läufen'}{' '}
                erstellt — er ersetzt die aktuellen Läufe.
              </span>
              <Button variant="primary" size="sm" disabled={applying || pendingPlan.runs.length === 0} onClick={applyPlan}>
                {applying ? 'Übernehme…' : 'Vorschlag übernehmen'}
              </Button>
            </div>
            {pendingPlan.runs.length > 0 && (
              <ul className="flex flex-col gap-1">
                {pendingPlan.runs.map((r, i) => (
                  <li key={i} className="flex items-center gap-2 text-caption text-text-tertiary">
                    <span className="min-w-0 flex-1 truncate text-text-secondary">{r.name}</span>
                    <span className="shrink-0">
                      {r.axiomIds.length} {r.axiomIds.length === 1 ? 'Axiom' : 'Axiome'}
                    </span>
                    <span className="shrink-0">· {scheduleSummary(r.schedule)}</span>
                  </li>
                ))}
              </ul>
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
            placeholder="Frag mich zu Axiomen, Regeln, Läufen oder ToDos… (Enter zum Senden, Shift+Enter für Zeilenumbruch)"
            className="dl-scroll flex-1 resize-none rounded-md border border-separator bg-surface px-3 py-2 text-footnote text-text-primary outline-none focus:border-accent/50"
          />
          <Button variant="primary" size="sm" disabled={busy || !input.trim()} onClick={send}>
            <SendIcon className="h-4 w-4" /> Senden
          </Button>
        </div>
      </div>
    </aside>
  );
}

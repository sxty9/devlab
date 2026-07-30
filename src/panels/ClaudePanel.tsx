import { useEffect, useMemo, useRef, useState } from 'react';
import { useWorkspace } from '@/state/workspace';
import { getDataSource } from '@/data';
import { PanelHeader } from './PanelHeader';
import { IconButton } from '@/ui/Button';
import { ClaudeIcon, PlusIcon, SendIcon } from '@/ui/icons';
import { renderMarkdown } from '@/lib/markdown';
import { basename } from '@/lib/lang';
import { cn } from '@/lib/cn';
import { aigenticUrl } from '@/lib/constants';
import type { AiAsk, AiMessage, AiModelCatalog } from '@/types';
import { NATIVE_EFFORTS } from '@/ui/RunTuning';
import {
  AGENT_ENGINE,
  AGENT_MODES,
  ENGINES,
  answerMark,
  capTranscript,
  changeNote,
  modelLabel,
  modelsForEngine,
} from './aiChat';

// AskOptions renders a structured question the AI posed — a header, the question, and clickable
// options — so the user answers by clicking inside the chat rather than typing (à la Claude Code).
// A single-select single question submits on click; multi-select or batched questions collect a
// selection then Send. An "answer in your own words" escape always sits below, since the model was
// told the options are a suggestion, not a cage.
function AskOptions({ ask, onAnswer, disabled }: { ask: AiAsk; onAnswer: (t: string) => void; disabled?: boolean }) {
  const [sel, setSel] = useState<number[][]>(() => ask.questions.map(() => []));
  const [own, setOwn] = useState('');
  const [typing, setTyping] = useState(false);
  const single = ask.questions.length === 1 && !ask.questions[0]?.multiSelect;

  const answer = ask.questions
    .map((q, qi) => {
      const labels = (sel[qi] ?? []).map((oi) => q.options[oi]?.label).filter(Boolean);
      if (!labels.length) return '';
      const value = labels.join(', ');
      return ask.questions.length > 1 ? `${q.header || q.question}: ${value}` : value;
    })
    .filter(Boolean)
    .join('\n');
  const canSend = answer.trim().length > 0;

  const pick = (qi: number, oi: number) => {
    if (disabled) return;
    if (single) {
      onAnswer(ask.questions[qi].options[oi]?.label ?? '');
      return;
    }
    setSel((prev) => {
      const next = prev.map((a) => a.slice());
      if (ask.questions[qi].multiSelect) {
        const at = next[qi].indexOf(oi);
        if (at >= 0) next[qi].splice(at, 1);
        else next[qi].push(oi);
      } else {
        next[qi] = next[qi][0] === oi ? [] : [oi];
      }
      return next;
    });
  };

  const submitOwn = () => {
    const t = own.trim();
    if (t) onAnswer(t);
  };

  return (
    <div className="space-y-2">
      {ask.questions.map((q, qi) => (
        <div key={qi} className="space-y-1.5">
          <div className="flex flex-wrap items-center gap-1.5">
            {q.header && <span className="rounded-full bg-accent/15 px-2 py-0.5 text-[11px] font-medium text-accent">{q.header}</span>}
            <span className="text-footnote font-medium text-text-primary">{q.question}</span>
          </div>
          <div className="flex flex-col gap-1" role={q.multiSelect ? 'group' : 'radiogroup'}>
            {q.options.map((o, oi) => {
              const selected = sel[qi]?.includes(oi) ?? false;
              return (
                <button
                  key={oi}
                  type="button"
                  disabled={disabled}
                  aria-pressed={selected}
                  onClick={() => pick(qi, oi)}
                  className={cn(
                    'flex w-full items-start gap-2 rounded-md border px-2.5 py-1.5 text-left text-footnote transition',
                    selected ? 'border-accent bg-accent/10' : 'border-separator bg-fill/[0.03]',
                    disabled ? 'cursor-default opacity-70' : 'hover:border-accent/60 hover:bg-fill/10',
                  )}
                >
                  <span className={cn('mt-px flex h-3.5 w-3.5 shrink-0 items-center justify-center rounded-full border text-[9px] leading-none', selected ? 'border-accent bg-accent text-accent-fg' : 'border-separator text-transparent')}>
                    ✓
                  </span>
                  <span className="min-w-0">
                    <span className="block text-text-primary">{o.label}</span>
                    {o.description && <span className="block text-caption text-text-tertiary">{o.description}</span>}
                  </span>
                </button>
              );
            })}
          </div>
        </div>
      ))}
      {!disabled && (
        <div className="flex flex-wrap items-center gap-2 pt-0.5">
          {!single && (
            <button
              type="button"
              disabled={!canSend}
              onClick={() => canSend && onAnswer(answer)}
              className={cn('rounded-md px-2.5 py-1 text-caption font-medium transition', canSend ? 'bg-accent text-accent-fg hover:bg-accent-hover' : 'bg-fill/10 text-text-tertiary')}
            >
              Send
            </button>
          )}
          {typing ? (
            <div className="flex flex-1 items-center gap-1.5">
              <input
                autoFocus
                value={own}
                onChange={(e) => setOwn(e.target.value)}
                onKeyDown={(e) => {
                  if (e.key === 'Enter') {
                    e.preventDefault();
                    submitOwn();
                  }
                }}
                placeholder="Type your answer…"
                className="min-w-0 flex-1 rounded-md border border-separator bg-fill/5 px-2 py-1 text-footnote text-text-primary placeholder:text-text-tertiary focus:border-accent/50 focus:outline-none"
              />
              <button
                type="button"
                disabled={!own.trim()}
                onClick={submitOwn}
                className={cn('rounded-md px-2 py-1 text-caption font-medium transition', own.trim() ? 'bg-accent/15 text-accent hover:bg-accent/25' : 'bg-fill/10 text-text-tertiary')}
              >
                Send
              </button>
            </div>
          ) : (
            <button type="button" onClick={() => setTyping(true)} className="text-caption text-text-tertiary underline-offset-2 hover:text-text-secondary hover:underline">
              Answer in your own words
            </button>
          )}
        </div>
      )}
    </div>
  );
}

// Bubble renders one turn. An assistant turn carries the mark of the model that produced it
// (labeling duty, D 26); a turn whose model is unknown — a transcript read back from the store —
// carries no mark rather than a guessed one.
function Bubble({
  msg,
  mark,
  onAnswer,
  disabled,
}: {
  msg: AiMessage;
  mark?: string;
  onAnswer?: (t: string) => void;
  disabled?: boolean;
}) {
  if (msg.role === 'user') {
    return (
      <div className="flex justify-end">
        <div className="max-w-[85%] whitespace-pre-wrap rounded-md rounded-br-sm bg-accent/15 px-3 py-2 text-footnote text-text-primary">{msg.content}</div>
      </div>
    );
  }
  return (
    <div className="flex gap-2">
      <span className="mt-0.5 flex h-6 w-6 shrink-0 items-center justify-center rounded-md bg-gpu/15 text-gpu shadow-elev-1">
        <ClaudeIcon className="h-3.5 w-3.5" />
      </span>
      <div className="min-w-0 max-w-[85%] space-y-1">
        {msg.content && (
          <div className="dl-markdown rounded-md rounded-tl-sm bg-fill/[0.07] px-3 py-2 text-footnote leading-relaxed text-text-primary" dangerouslySetInnerHTML={{ __html: renderMarkdown(msg.content) }} />
        )}
        {mark && <span className="block px-1 font-mono text-[10px] text-text-tertiary">{mark}</span>}
        {msg.ask && msg.ask.questions.length > 0 && onAnswer && <AskOptions ask={msg.ask} onAnswer={onAnswer} disabled={disabled} />}
      </div>
    </div>
  );
}

/** The AI assistant panel: a general chat whose context is the open repo, powered by aigentic. */
export function ClaudePanel() {
  const { activeRepo, openTabs, activeTabId, reloadRepo } = useWorkspace();
  const source = useMemo(() => getDataSource(), []);
  const [messages, setMessages] = useState<AiMessage[]>([]);
  const [draft, setDraft] = useState('');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [includeFile, setIncludeFile] = useState(true);
  const [effort, setEffort] = useState('medium');
  const [engine, setEngine] = useState('choose');
  const [model, setModel] = useState('');
  const [mode, setMode] = useState('auto'); // agent permission mode (plan | auto | full)
  const [sessionId, setSessionId] = useState(''); // agent session, for multi-turn --resume
  const [catalog, setCatalog] = useState<AiModelCatalog | null>(null);
  // The model mark of each answer, keyed by the turn's timestamp (stable across a transcript trim,
  // unlike its position). Filled from the reply of a live turn; a transcript loaded from the store
  // carries no marks, because the store does not keep them — and an unmarked answer shows no mark
  // rather than a borrowed one.
  const [marks, setMarks] = useState<Record<string, string>>({});
  const scrollRef = useRef<HTMLDivElement>(null);
  const isAgent = engine === AGENT_ENGINE;

  useEffect(() => {
    let cancelled = false;
    source
      .assistantModels()
      .then((c) => !cancelled && setCatalog(c))
      .catch(() => {});
    return () => {
      cancelled = true;
    };
  }, [source]);

  // Model options depend on the chosen engine; Auto/router leaves the model to aigentic.
  const models = useMemo(() => modelsForEngine(engine, catalog), [catalog, engine]);

  const activeFile = useMemo(() => {
    const t = openTabs.find((x) => x.id === activeTabId);
    return t && t.kind === 'code' && t.path ? t.path : null;
  }, [openTabs, activeTabId]);

  useEffect(() => {
    let cancelled = false;
    setSessionId(''); // a new repo starts a fresh agent session
    setMarks({}); // the store keeps no model marks, so none are claimed for a loaded transcript
    source
      .getAssistantHistory(activeRepo.id)
      .then((h) => !cancelled && setMessages(h))
      .catch(() => {});
    return () => {
      cancelled = true;
    };
  }, [source, activeRepo.id]);

  useEffect(() => {
    scrollRef.current?.scrollTo({ top: scrollRef.current.scrollHeight });
  }, [messages, busy]);

  // send(answer?) posts a turn. With no argument it sends the composer's draft; with an argument it
  // sends a picked answer from a structured question (leaving the composer untouched).
  const send = async (answer?: string) => {
    const fromComposer = answer === undefined;
    const text = (answer ?? draft).trim();
    if (!text || busy) return;
    if (fromComposer) setDraft('');
    setError(null);
    const history = messages;
    const next: AiMessage[] = [...history, { role: 'user', content: text, ts: new Date().toISOString() }];
    setMessages(next);
    setBusy(true);
    try {
      let output: string;
      let ask: AiAsk | undefined;
      let mark: string;
      if (isAgent) {
        // Full agentic run: claude CLI in the workspace, as the user — it can edit the tree.
        const reply = await source.askAgent(activeRepo.id, { prompt: text, model, effort, mode, resume: sessionId });
        if (reply.sessionId) setSessionId(reply.sessionId);
        const n = reply.changes.length;
        output = reply.output + changeNote(n);
        // The agent reply carries no model of its own, so the answer is marked with the model that
        // was asked for; with none chosen the engine that ran is the honest mark.
        mark = answerMark({ model, engine });
        if (n) void reloadRepo(); // surface the edits in the tree + VCS panel
      } else {
        const contextPaths = includeFile && activeFile ? [activeFile] : [];
        const reply = await source.askAssistant(activeRepo.id, { prompt: text, contextPaths, history, kind: engine, model, effort });
        output = reply.output;
        ask = reply.ask; // a structured question the model posed → rendered as clickable options
        mark = answerMark(reply); // the model that actually answered
      }
      const answeredAt = new Date().toISOString();
      const withReply: AiMessage[] = capTranscript([
        ...next,
        { role: 'assistant', content: output, ts: answeredAt, ask },
      ]);
      setMessages(withReply);
      setMarks((m) => ({ ...m, [answeredAt]: mark }));
      source.saveAssistantHistory(activeRepo.id, withReply).catch(() => {});
    } catch (e) {
      setError(String((e as Error)?.message ?? e));
      setMessages(next); // keep the user's message visible; the error card explains what happened
    } finally {
      setBusy(false);
    }
  };

  const clear = () => {
    setMessages([]);
    setMarks({});
    setError(null);
    setSessionId(''); // drop the agent session so the next turn starts fresh
    source.saveAssistantHistory(activeRepo.id, []).catch(() => {});
  };

  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <PanelHeader
        title="KI"
        actions={
          <IconButton label="Clear conversation" onClick={clear}>
            <PlusIcon className="h-4 w-4" />
          </IconButton>
        }
      />

      {/* Context + engine controls */}
      <div className="dl-no-select flex flex-col gap-1.5 border-b border-separator px-3 py-1.5 text-caption text-text-tertiary">
        <div className="flex flex-wrap items-center gap-1.5">
          {isAgent ? (
            <span>
              Agent in <span className="text-text-secondary">{activeRepo.name}</span> — runs as you, edits files
            </span>
          ) : (
            <span>
              Context: <span className="text-text-secondary">{activeRepo.name}</span> (whole repo)
            </span>
          )}
          {!isAgent && activeFile && (
            <button
              type="button"
              aria-pressed={includeFile}
              onClick={() => setIncludeFile((v) => !v)}
              className={cn('truncate rounded-sm px-1.5 py-0.5 font-mono text-[11px] transition', includeFile ? 'bg-accent/15 text-accent' : 'bg-fill/10 text-text-tertiary line-through')}
            >
              +{basename(activeFile)}
            </button>
          )}
        </div>
        <div className="flex flex-wrap items-center gap-1.5">
          <select
            value={engine}
            onChange={(e) => {
              setEngine(e.target.value);
              setModel('');
            }}
            className="rounded-sm bg-fill/10 px-1 py-0.5 text-[11px] text-text-secondary focus:outline-none"
            aria-label="Engine"
          >
            {ENGINES.map((x) => (
              <option key={x.id} value={x.id}>{x.label}</option>
            ))}
          </select>
          {models.length > 0 && (
            <select
              value={model}
              onChange={(e) => setModel(e.target.value)}
              className="max-w-[10rem] rounded-sm bg-fill/10 px-1 py-0.5 text-[11px] text-text-secondary focus:outline-none"
              aria-label="Model"
            >
              <option value="">Model: auto</option>
              {models.map((m) => (
                <option key={m.id} value={m.id}>{modelLabel(m)}</option>
              ))}
            </select>
          )}
          {isAgent && (
            <select
              value={mode}
              onChange={(e) => setMode(e.target.value)}
              className="rounded-sm bg-fill/10 px-1 py-0.5 text-[11px] text-text-secondary focus:outline-none"
              aria-label="Agent mode"
            >
              {AGENT_MODES.map((x) => (
                <option key={x.id} value={x.id}>{x.label}</option>
              ))}
            </select>
          )}
          <select
            value={effort}
            onChange={(e) => setEffort(e.target.value)}
            className="ml-auto rounded-sm bg-fill/10 px-1 py-0.5 text-[11px] text-text-secondary focus:outline-none"
            aria-label="Reasoning effort"
          >
            {NATIVE_EFFORTS.map((x) => (
              <option key={x} value={x}>{x}</option>
            ))}
          </select>
        </div>
      </div>

      <div ref={scrollRef} className="dl-scroll min-h-0 flex-1 space-y-3 overflow-y-auto px-3 py-2">
        {messages.length === 0 && !busy && (
          <p className="px-1 py-4 text-center text-caption text-text-tertiary">Ask about {activeRepo.name} — the assistant uses the repo (and the open file) as context.</p>
        )}
        {messages.map((m, i) => (
          <Bubble key={i} msg={m} mark={marks[m.ts]} onAnswer={(t) => void send(t)} disabled={busy || i !== messages.length - 1} />
        ))}
        {busy && (
          <div className="flex gap-2">
            <span className="mt-0.5 flex h-6 w-6 shrink-0 items-center justify-center rounded-md bg-gpu/15 text-gpu">
              <ClaudeIcon className="h-3.5 w-3.5" />
            </span>
            <div className="rounded-md bg-fill/[0.07] px-3 py-2 text-footnote text-text-tertiary">Thinking…</div>
          </div>
        )}
        {error && (
          <div className="rounded-md border border-danger/30 bg-danger/10 px-3 py-2 text-caption text-text-secondary">
            {error}
            <div className="mt-1">
              <a href={aigenticUrl()} target="_blank" rel="noreferrer" className="text-accent hover:underline">
                Open aigentic ↗
              </a>
            </div>
          </div>
        )}
      </div>

      <div className="border-t border-separator p-2">
        <div className="flex items-end gap-1.5 rounded-md bg-fill/10 p-1.5 focus-within:ring-2 focus-within:ring-accent/40">
          <textarea
            value={draft}
            onChange={(e) => setDraft(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === 'Enter' && !e.shiftKey) {
                e.preventDefault();
                void send();
              }
            }}
            rows={1}
            placeholder={`Ask about ${activeRepo.name}…  (Shift+Enter for newline)`}
            aria-label={`Ask the assistant about ${activeRepo.name}`}
            className="dl-scroll max-h-28 min-h-[24px] w-full resize-none bg-transparent px-1.5 py-1 text-footnote text-text-primary placeholder:text-text-tertiary focus:outline-none"
          />
          <button
            type="button"
            onClick={() => void send()}
            disabled={!draft.trim() || busy}
            aria-label="Send message"
            className={cn('flex h-7 w-7 shrink-0 items-center justify-center rounded-md transition duration-fast ease-out', draft.trim() && !busy ? 'bg-accent text-accent-fg hover:bg-accent-hover' : 'bg-fill/10 text-text-tertiary')}
          >
            <SendIcon className="h-4 w-4" />
          </button>
        </div>
      </div>
    </div>
  );
}

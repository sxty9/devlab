import { useEffect, useMemo, useRef, useState } from 'react';
import { useWorkspace } from '@/state/workspace';
import { getDataSource } from '@/data';
import { PanelHeader } from './PanelHeader';
import { IconButton } from '@/ui/Button';
import { ClaudeIcon, PlusIcon, SendIcon } from '@/ui/icons';
import { renderMarkdown } from '@/lib/markdown';
import { basename } from '@/lib/lang';
import { cn } from '@/lib/cn';
import type { AiMessage, AiModelCatalog } from '@/types';

const ENGINES = [
  { id: 'choose', label: 'Auto (Router)' },
  { id: 'claude-agent', label: 'Claude Agent (Repo)' },
  { id: 'claude-cli', label: 'Claude Code' },
  { id: 'claude-api', label: 'Claude API' },
  { id: 'ollama', label: 'Ollama (lokal)' },
];

// The agentic engine runs the FULL claude CLI in the workspace, as the user — it can edit files.
const AGENT_MODES = [
  { id: 'plan', label: 'Plan (nur lesen)' },
  { id: 'auto', label: 'Auto (bearbeiten)' },
  { id: 'full', label: 'Full (autonom)' },
];

/** Leak the model + version into one option label: the catalog's friendly name plus the version
 *  encoded in the model id (claude-opus-4-8 → "4.8", claude-fable-5 → "5"), so the picker shows
 *  WHICH version it selects instead of a bare family name. Targets the current family-first id
 *  scheme; leaves the label untouched when it already carries the version or the id has none
 *  (e.g. an Ollama tag). */
function modelLabel(m: { id: string; label: string }): string {
  const parts = m.id.replace(/^claude-/, '').split('-');
  const ver: string[] = [];
  for (let i = 1; i < parts.length && /^\d{1,2}$/.test(parts[i]); i++) ver.push(parts[i]);
  const v = ver.join('.');
  return v && !m.label.includes(v) ? `${m.label} ${v}` : m.label;
}

/** Resolve the Holistic dashboard origin so we can deep-link to the aigentic settings. */
function aigenticUrl(): string {
  if (typeof window === 'undefined') return '#';
  const { protocol, hostname } = window.location;
  const parts = hostname.split('.');
  const origin = parts.length >= 2 ? `${protocol}//holistic.${parts.slice(1).join('.')}` : window.location.origin;
  return `${origin}/app/aigentic`;
}

function Bubble({ msg }: { msg: AiMessage }) {
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
      <div className="dl-markdown max-w-[85%] rounded-md rounded-tl-sm bg-fill/[0.07] px-3 py-2 text-footnote leading-relaxed text-text-primary" dangerouslySetInnerHTML={{ __html: renderMarkdown(msg.content) }} />
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
  const scrollRef = useRef<HTMLDivElement>(null);
  const isAgent = engine === 'claude-agent';

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
  const models = useMemo<{ id: string; label: string }[]>(() => {
    if (!catalog) return [];
    if (engine === 'ollama') return catalog.ollama.map((m) => ({ id: m, label: m }));
    if (engine === 'claude-cli' || engine === 'claude-api' || engine === 'claude-agent') return catalog.claude;
    return [];
  }, [catalog, engine]);

  const activeFile = useMemo(() => {
    const t = openTabs.find((x) => x.id === activeTabId);
    return t && t.kind === 'code' && t.path ? t.path : null;
  }, [openTabs, activeTabId]);

  useEffect(() => {
    let cancelled = false;
    setSessionId(''); // a new repo starts a fresh agent session
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

  const send = async () => {
    const text = draft.trim();
    if (!text || busy) return;
    setDraft('');
    setError(null);
    const history = messages;
    const next: AiMessage[] = [...history, { role: 'user', content: text, ts: new Date().toISOString() }];
    setMessages(next);
    setBusy(true);
    try {
      let output: string;
      if (isAgent) {
        // Full agentic run: claude CLI in the workspace, as the user — it can edit the tree.
        const reply = await source.askAgent(activeRepo.id, { prompt: text, model, effort, mode, resume: sessionId });
        if (reply.sessionId) setSessionId(reply.sessionId);
        const n = reply.changes.length;
        output = reply.output + (n ? `\n\n_(${n} ${n === 1 ? 'file' : 'files'} changed — open Source Control to review)_` : '');
        if (n) void reloadRepo(); // surface the edits in the tree + VCS panel
      } else {
        const contextPaths = includeFile && activeFile ? [activeFile] : [];
        const reply = await source.askAssistant(activeRepo.id, { prompt: text, contextPaths, history, kind: engine, model, effort });
        output = reply.output;
      }
      const withReply: AiMessage[] = [...next, { role: 'assistant', content: output, ts: new Date().toISOString() }];
      setMessages(withReply);
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
    setError(null);
    setSessionId(''); // drop the agent session so the next turn starts fresh
    source.saveAssistantHistory(activeRepo.id, []).catch(() => {});
  };

  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <PanelHeader
        title="KI"
        actions={
          <IconButton label="Clear conversation" title="Clear conversation" onClick={clear}>
            <PlusIcon className="h-4 w-4" />
          </IconButton>
        }
      />

      {/* Context + engine controls */}
      <div className="dl-no-select flex flex-col gap-1.5 border-b border-separator px-3 py-1.5 text-caption text-text-tertiary">
        <div className="flex flex-wrap items-center gap-1.5">
          {isAgent ? (
            <span>Agent im Workspace <span className="text-text-secondary">{activeRepo.name}</span> — läuft als du, kann Dateien ändern</span>
          ) : (
            <span>Kontext: <span className="text-text-secondary">{activeRepo.name}</span> (ganzes Repo)</span>
          )}
          {!isAgent && activeFile && (
            <button
              type="button"
              onClick={() => setIncludeFile((v) => !v)}
              className={cn('truncate rounded-sm px-1.5 py-0.5 font-mono text-[11px] transition', includeFile ? 'bg-accent/15 text-accent' : 'bg-fill/10 text-text-tertiary line-through')}
              title={includeFile ? 'Offene Datei priorisiert' : 'Offene Datei nicht priorisiert'}
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
            title="Engine"
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
              title="Modell"
            >
              <option value="">Modell: Auto</option>
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
              title="Agent-Modus"
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
            title="Reasoning effort"
          >
            {['low', 'medium', 'high', 'xhigh', 'max'].map((x) => (
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
          <Bubble key={i} msg={m} />
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
                aigentic öffnen ↗
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

import { useEffect, useRef, useState } from 'react';
import { useWorkspace } from '@/state/workspace';
import type { ClaudeMsg } from '@/types';
import { PanelHeader } from './PanelHeader';
import { IconButton } from '@/ui/Button';
import { ClaudeIcon, PlusIcon, SendIcon } from '@/ui/icons';
import { cn } from '@/lib/cn';

function Message({ msg }: { msg: ClaudeMsg }) {
  if (msg.role === 'tool') {
    return (
      <div className="flex items-center gap-2 px-1 py-0.5">
        <span className="rounded-sm bg-fill/10 px-1.5 py-0.5 font-mono text-[11px] text-text-secondary">{msg.tool}</span>
        <span className="truncate font-mono text-[11px] text-text-tertiary">{msg.text}</span>
      </div>
    );
  }
  if (msg.role === 'user') {
    return (
      <div className="flex justify-end">
        <div className="max-w-[85%] rounded-md rounded-br-sm bg-accent/15 px-3 py-2 text-footnote text-text-primary">{msg.text}</div>
      </div>
    );
  }
  return (
    <div className="flex gap-2">
      <span className="mt-0.5 flex h-6 w-6 shrink-0 items-center justify-center rounded-md bg-gpu/15 text-gpu shadow-elev-1">
        <ClaudeIcon className="h-3.5 w-3.5" />
      </span>
      <div className="max-w-[85%] rounded-md rounded-tl-sm bg-fill/[0.07] px-3 py-2 text-footnote leading-relaxed text-text-primary">
        {msg.text}
      </div>
    </div>
  );
}

export function ClaudePanel() {
  const { data, activeRepo } = useWorkspace();
  // One transcript per repo, kept in state so switching repos preserves history.
  const [sessions, setSessions] = useState<Record<string, ClaudeMsg[]>>(() => ({ [activeRepo.id]: data.claude }));
  const [draft, setDraft] = useState('');
  const scrollRef = useRef<HTMLDivElement>(null);

  const msgs = sessions[activeRepo.id] ?? data.claude;

  // Seed a repo's transcript from its mock the first time it's opened.
  useEffect(() => {
    setSessions((s) => (s[activeRepo.id] ? s : { ...s, [activeRepo.id]: data.claude }));
  }, [activeRepo.id, data.claude]);

  useEffect(() => {
    scrollRef.current?.scrollTo({ top: scrollRef.current.scrollHeight });
  }, [msgs]);

  const send = () => {
    const text = draft.trim();
    if (!text) return;
    setDraft('');
    setSessions((s) => {
      const cur = s[activeRepo.id] ?? data.claude;
      return {
        ...s,
        [activeRepo.id]: [
          ...cur,
          { id: `u-${cur.length}`, role: 'user', text, ts: 'now' },
          {
            id: `a-${cur.length + 1}`,
            role: 'assistant',
            text: `(preview) I'd work on “${text}” in ${activeRepo.name} — but the Claude backend isn't wired yet. This panel is a mock of the session view.`,
            ts: 'now',
          },
        ],
      };
    });
  };

  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <PanelHeader
        title="Claude"
        actions={
          <IconButton label="New session">
            <PlusIcon className="h-4 w-4" />
          </IconButton>
        }
      />

      <div ref={scrollRef} className="dl-scroll min-h-0 flex-1 space-y-3 overflow-y-auto px-3 py-2">
        {msgs.map((m) => (
          <Message key={m.id} msg={m} />
        ))}
      </div>

      <div className="border-t border-separator p-2">
        <div className="flex items-end gap-1.5 rounded-md bg-fill/10 p-1.5 focus-within:ring-2 focus-within:ring-accent/40">
          <textarea
            value={draft}
            onChange={(e) => setDraft(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === 'Enter' && !e.shiftKey) {
                e.preventDefault();
                send();
              }
            }}
            rows={1}
            placeholder={`Ask Claude about ${activeRepo.name}…  (Shift+Enter for newline)`}
            aria-label={`Message Claude about ${activeRepo.name}`}
            className="dl-scroll max-h-28 min-h-[24px] w-full resize-none bg-transparent px-1.5 py-1 text-footnote text-text-primary placeholder:text-text-tertiary focus:outline-none"
          />
          <button
            type="button"
            onClick={send}
            disabled={!draft.trim()}
            aria-label="Send message"
            className={cn(
              'flex h-7 w-7 shrink-0 items-center justify-center rounded-md transition duration-fast ease-out',
              draft.trim() ? 'bg-accent text-accent-fg hover:bg-accent-hover' : 'bg-fill/10 text-text-tertiary',
            )}
          >
            <SendIcon className="h-4 w-4" />
          </button>
        </div>
      </div>
    </div>
  );
}

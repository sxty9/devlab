import { useEffect, useRef, useState } from 'react';
import { useWorkspace } from '@/state/workspace';
import type { TermLine } from '@/types';
import { PanelHeader } from './PanelHeader';
import { IconButton } from '@/ui/Button';
import { PlusIcon, XIcon } from '@/ui/icons';
import { cn } from '@/lib/cn';

const lineCls: Record<TermLine['kind'], string> = {
  cmd: 'text-text-primary',
  stdout: 'text-text-secondary',
  stderr: 'text-danger',
  system: 'text-success',
};

export function TerminalPanel() {
  const { data, activeRepo, activeBranch } = useWorkspace();
  const [lines, setLines] = useState<TermLine[]>(data.terminal);
  const [input, setInput] = useState('');
  const scrollRef = useRef<HTMLDivElement>(null);

  useEffect(() => setLines(data.terminal), [data.terminal]);
  useEffect(() => {
    scrollRef.current?.scrollTo({ top: scrollRef.current.scrollHeight });
  }, [lines]);

  const prompt = `~/${activeRepo.name} ${activeBranch.name} $`;

  const run = () => {
    const cmd = input.trim();
    if (!cmd) return;
    setInput('');
    const reply: TermLine =
      cmd === 'clear'
        ? { id: `c-${lines.length}`, kind: 'system', text: '' }
        : { id: `o-${lines.length + 1}`, kind: 'stdout', text: `${cmd.split(' ')[0]}: (preview) terminal backend not wired yet` };
    if (cmd === 'clear') {
      setLines([]);
      return;
    }
    setLines((l) => [...l, { id: `c-${l.length}`, kind: 'cmd', text: cmd }, reply]);
  };

  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <PanelHeader
        title="Terminal"
        actions={
          <>
            <IconButton label="New terminal">
              <PlusIcon className="h-4 w-4" />
            </IconButton>
            <IconButton label="Clear" onClick={() => setLines([])}>
              <XIcon className="h-4 w-4" />
            </IconButton>
          </>
        }
      />

      <div
        ref={scrollRef}
        className="dl-scroll min-h-0 flex-1 overflow-y-auto px-3 py-2 font-mono text-[12.5px] leading-relaxed"
        onClick={(e) => {
          // focus the input when clicking anywhere in the scrollback
          (e.currentTarget.parentElement?.querySelector('input') as HTMLInputElement | null)?.focus();
        }}
      >
        {lines.map((l) => (
          <div key={l.id} className={cn('whitespace-pre-wrap break-words', lineCls[l.kind])}>
            {l.kind === 'cmd' && <span className="mr-1.5 text-accent">$</span>}
            {l.text}
          </div>
        ))}
      </div>

      <div className="flex items-center gap-1.5 border-t border-separator px-3 py-1.5 font-mono text-[12.5px]">
        <span className="shrink-0 text-success">{prompt}</span>
        <input
          value={input}
          onChange={(e) => setInput(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === 'Enter') run();
          }}
          spellCheck={false}
          autoComplete="off"
          aria-label="Terminal input"
          className="w-full bg-transparent text-text-primary caret-accent focus:outline-none"
        />
      </div>
    </div>
  );
}

import { useState } from 'react';
import { useWorkspace } from '@/state/workspace';
import type { Change } from '@/types';
import { PanelHeader } from './PanelHeader';
import { Button, IconButton } from '@/ui/Button';
import { GitCommitIcon, RefreshIcon } from '@/ui/icons';
import { gitStatusMeta } from '@/ui/git';
import { basename, dirname, guessLang } from '@/lib/lang';
import { cn } from '@/lib/cn';

function ChangeRow({ change, onOpen }: { change: Change; onOpen: (c: Change) => void }) {
  const meta = gitStatusMeta[change.status];
  return (
    <button
      type="button"
      onClick={() => onOpen(change)}
      className="group flex h-[28px] w-full items-center gap-2 rounded-sm px-3 text-footnote text-text-secondary transition-colors hover:bg-fill/10"
    >
      <span className={cn('w-3 shrink-0 text-center font-mono text-caption', meta.cls)} title={meta.label}>
        {meta.letter}
      </span>
      <span className="truncate text-text-primary">{basename(change.path)}</span>
      <span className="truncate text-caption text-text-tertiary">{dirname(change.path)}</span>
      <span className="ml-auto shrink-0 font-mono text-caption">
        {change.additions > 0 && <span className="text-success">+{change.additions}</span>}
        {change.additions > 0 && change.deletions > 0 && ' '}
        {change.deletions > 0 && <span className="text-danger">−{change.deletions}</span>}
      </span>
    </button>
  );
}

export function VersionControlPanel() {
  const { data, activeBranch, openFile } = useWorkspace();
  const [message, setMessage] = useState('');

  const staged = data.changes.filter((c) => c.staged);
  const unstaged = data.changes.filter((c) => !c.staged);

  const open = (c: Change) => openFile({ id: c.path, name: basename(c.path), lang: guessLang(c.path) });

  const canCommit = staged.length > 0 && message.trim().length > 0;

  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <PanelHeader
        title="Version Control"
        actions={
          <IconButton label="Refresh status">
            <RefreshIcon className="h-4 w-4" />
          </IconButton>
        }
      />

      <div className="space-y-2 px-2 pb-2">
        <textarea
          value={message}
          onChange={(e) => setMessage(e.target.value)}
          rows={2}
          placeholder={`Commit message (on ${activeBranch.name})`}
          className="dl-scroll w-full resize-none rounded-md bg-fill/10 px-2.5 py-2 text-footnote text-text-primary placeholder:text-text-tertiary focus:outline-none focus:ring-2 focus:ring-accent/40"
        />
        <Button variant="primary" size="sm" disabled={!canCommit} className="w-full">
          <GitCommitIcon className="h-3.5 w-3.5" />
          Commit {staged.length > 0 ? `${staged.length} file${staged.length > 1 ? 's' : ''}` : ''}
        </Button>
      </div>

      <div className="dl-scroll min-h-0 flex-1 overflow-y-auto pb-3">
        {staged.length > 0 && (
          <Section title="Staged Changes" count={staged.length}>
            {staged.map((c) => (
              <ChangeRow key={c.path} change={c} onOpen={open} />
            ))}
          </Section>
        )}
        <Section title="Changes" count={unstaged.length}>
          {unstaged.length ? (
            unstaged.map((c) => <ChangeRow key={c.path} change={c} onOpen={open} />)
          ) : (
            <p className="px-3 py-2 text-caption text-text-tertiary">Nothing to commit.</p>
          )}
        </Section>
      </div>
    </div>
  );
}

function Section({ title, count, children }: { title: string; count: number; children: React.ReactNode }) {
  return (
    <div className="pb-2">
      <div className="dl-no-select flex items-center gap-2 px-3 py-1 text-caption font-semibold uppercase tracking-wide text-text-tertiary">
        {title}
        <span className="rounded-full bg-fill/10 px-1.5 text-[10px] font-semibold text-text-tertiary">{count}</span>
      </div>
      {children}
    </div>
  );
}

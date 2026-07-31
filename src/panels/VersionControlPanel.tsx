import { useCallback, useState } from 'react';
import { useWorkspace } from '@/state/workspace';
import type { Change } from '@/types';
import { PanelHeader } from './PanelHeader';
import { ContextMenu } from './ContextMenu';
import { changeMenu } from './treeOps';
import { Button, IconButton } from '@/ui/Button';
import { GitCommitIcon, RefreshIcon } from '@/ui/icons';
import { useToast } from '@/ui/Toast';
import { gitStatusMeta } from '@/ui/git';
import { basename, dirname } from '@/lib/lang';
import { cn } from '@/lib/cn';

function ChangeRow({
  change,
  selected,
  onOpen,
  onSelect,
  onMenu,
  action,
  onAction,
  busy,
}: {
  change: Change;
  selected: boolean;
  onOpen: (c: Change) => void;
  onSelect: (c: Change) => void;
  onMenu: (c: Change, x: number, y: number) => void;
  action: '+' | '−';
  onAction: (c: Change) => void;
  busy: boolean;
}) {
  const meta = gitStatusMeta[change.status];
  return (
    <div
      role="option"
      aria-selected={selected}
      data-change-path={change.path}
      tabIndex={selected ? 0 : -1}
      onContextMenu={(e) => {
        e.preventDefault();
        onSelect(change);
        onMenu(change, e.clientX, e.clientY);
      }}
      className={cn(
        'group flex h-[28px] w-full items-center gap-2 rounded-sm px-3 text-footnote text-text-secondary transition-colors hover:bg-fill/10',
        'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-accent/40',
        selected && 'bg-fill/[0.08]',
      )}
    >
      <button
        type="button"
        onClick={() => {
          onSelect(change);
          onOpen(change);
        }}
        className="flex min-w-0 flex-1 items-center gap-2 text-left"
      >
        <span className={cn('w-3 shrink-0 text-center font-mono text-caption', meta.cls)} aria-label={meta.label}>
          {meta.letter}
        </span>
        <span className="truncate text-text-primary">{basename(change.path)}</span>
        <span className="truncate text-caption text-text-tertiary">{dirname(change.path)}</span>
      </button>
      <span className="ml-auto flex shrink-0 items-center gap-2">
        <span className="font-mono text-caption">
          {change.additions > 0 && <span className="text-success">+{change.additions}</span>}
          {change.additions > 0 && change.deletions > 0 && ' '}
          {change.deletions > 0 && <span className="text-danger">−{change.deletions}</span>}
        </span>
        <button
          type="button"
          disabled={busy}
          onClick={() => onAction(change)}
          aria-label={action === '+' ? `Stage ${change.path}` : `Unstage ${change.path}`}
          className="flex h-5 w-5 items-center justify-center rounded-sm font-mono text-caption text-text-tertiary opacity-0 transition hover:bg-fill/15 hover:text-text-primary group-hover:opacity-100 disabled:opacity-30 focus-visible:opacity-100"
        >
          {action}
        </button>
      </span>
    </div>
  );
}

export function VersionControlPanel() {
  const { data, activeBranch, openDiff, canWrite, stageChange, unstageChange, commitStaged, reloadRepo } = useWorkspace();
  const { toast } = useToast();
  const [message, setMessage] = useState('');
  const [busy, setBusy] = useState(false);
  const [selected, setSelected] = useState<string | null>(null);
  const [menu, setMenu] = useState<{ x: number; y: number; change: Change } | null>(null);

  const staged = data.changes.filter((c) => c.staged);
  const unstaged = data.changes.filter((c) => !c.staged);

  const open = (c: Change) => openDiff(c.path);

  const canCommit = canWrite && staged.length > 0 && message.trim().length > 0 && !busy;

  const run = async (fn: () => Promise<unknown>, fail: string) => {
    setBusy(true);
    try {
      await fn();
    } catch (e) {
      toast({ title: fail, description: String((e as Error)?.message ?? e), variant: 'danger' });
    } finally {
      setBusy(false);
    }
  };

  const doCommit = () =>
    run(async () => {
      const res = await commitStaged(message.trim());
      setMessage('');
      toast({ title: 'Committed', description: `${res.hash} on ${res.branch}`, variant: 'success' });
    }, 'Commit failed');

  // Arrow keys walk the change list (staged rows first, then unstaged — the order on screen), and
  // the context-menu key opens the row's menu (D 17).
  const ordered = [...staged, ...unstaged];
  const focusRow = useCallback((path: string) => {
    setSelected(path);
    requestAnimationFrame(() => {
      document.querySelector<HTMLElement>(`[data-change-path="${CSS.escape(path)}"]`)?.focus();
    });
  }, []);

  const onKeyDown = (e: React.KeyboardEvent) => {
    if (ordered.length === 0) return;
    const at = ordered.findIndex((c) => c.path === selected);
    if (e.key === 'ArrowDown' || e.key === 'ArrowUp') {
      e.preventDefault();
      const next = e.key === 'ArrowDown' ? Math.min(at + 1, ordered.length - 1) : Math.max(at <= 0 ? 0 : at - 1, 0);
      focusRow(ordered[next].path);
      return;
    }
    if (at < 0) return;
    const change = ordered[at];
    if (e.key === 'Enter') {
      e.preventDefault();
      if (e.metaKey || e.ctrlKey) {
        const r = document.querySelector<HTMLElement>(`[data-change-path="${CSS.escape(change.path)}"]`)?.getBoundingClientRect();
        setMenu({ x: r ? r.left + 16 : 16, y: r ? r.bottom : 16, change });
      } else {
        open(change);
      }
      return;
    }
    if (e.key === 'ContextMenu') {
      e.preventDefault();
      const r = document.querySelector<HTMLElement>(`[data-change-path="${CSS.escape(change.path)}"]`)?.getBoundingClientRect();
      setMenu({ x: r ? r.left + 16 : 16, y: r ? r.bottom : 16, change });
    }
  };

  const chooseMenu = (id: string) => {
    const change = menu?.change;
    if (!change) return;
    switch (id) {
      case 'diff':
        open(change);
        return;
      case 'copyPath':
        navigator.clipboard?.writeText(change.path).then(
          () => toast({ title: 'Path copied', description: change.path }),
          () => toast({ title: 'Could not copy path', variant: 'danger' }),
        );
        return;
      case 'stage':
        void run(() => stageChange(change.path), 'Stage failed');
        return;
      case 'unstage':
        void run(() => unstageChange(change.path), 'Unstage failed');
        return;
    }
  };

  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <PanelHeader
        title="Version Control"
        actions={
          <IconButton label="Refresh status" onClick={() => void reloadRepo()}>
            <RefreshIcon className="h-4 w-4" />
          </IconButton>
        }
      />

      <div className="space-y-2 px-2 pb-2">
        <textarea
          value={message}
          onChange={(e) => setMessage(e.target.value)}
          rows={2}
          disabled={!canWrite}
          placeholder={canWrite ? `Commit message (on ${activeBranch.name})` : 'Read-only repository'}
          className="dl-scroll w-full resize-none rounded-md bg-fill/10 px-2.5 py-2 text-footnote text-text-primary placeholder:text-text-tertiary focus:outline-none focus:ring-2 focus:ring-accent/40 disabled:opacity-50"
        />
        <Button variant="primary" size="sm" disabled={!canCommit} className="w-full" onClick={doCommit}>
          <GitCommitIcon className="h-3.5 w-3.5" />
          Commit {staged.length > 0 ? `${staged.length} file${staged.length > 1 ? 's' : ''}` : ''}
        </Button>
      </div>

      <div className="dl-scroll min-h-0 flex-1 overflow-y-auto pb-3" role="listbox" aria-label="Changes" onKeyDown={onKeyDown}>
        {staged.length > 0 && (
          <Section title="Staged Changes" count={staged.length}>
            {staged.map((c) => (
              <ChangeRow
                key={`s:${c.path}`}
                change={c}
                selected={selected === c.path}
                onOpen={open}
                onSelect={(x) => setSelected(x.path)}
                onMenu={(x, mx, my) => setMenu({ x: mx, y: my, change: x })}
                action="−"
                onAction={(x) => run(() => unstageChange(x.path), 'Unstage failed')}
                busy={busy || !canWrite}
              />
            ))}
          </Section>
        )}
        <Section title="Changes" count={unstaged.length}>
          {unstaged.length ? (
            unstaged.map((c) => (
              <ChangeRow
                key={`u:${c.path}`}
                change={c}
                selected={selected === c.path}
                onOpen={open}
                onSelect={(x) => setSelected(x.path)}
                onMenu={(x, mx, my) => setMenu({ x: mx, y: my, change: x })}
                action="+"
                onAction={(x) => run(() => stageChange(x.path), 'Stage failed')}
                busy={busy || !canWrite}
              />
            ))
          ) : (
            <p className="px-3 py-2 text-caption text-text-tertiary">Nothing to commit.</p>
          )}
        </Section>
      </div>

      {menu && (
        <ContextMenu
          x={menu.x}
          y={menu.y}
          entries={changeMenu(menu.change.staged, canWrite)}
          onChoose={chooseMenu}
          onClose={() => setMenu(null)}
        />
      )}
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

import { useMemo, useState } from 'react';
import { useWorkspace } from '@/state/workspace';
import { getDataSource } from '@/data';
import type { FileNode } from '@/types';
import { PanelHeader } from './PanelHeader';
import { TreeNode } from './TreeNode';
import { IconButton } from '@/ui/Button';
import { useToast } from '@/ui/Toast';
import { FileTextIcon, PlusIcon, RefreshIcon, SearchIcon } from '@/ui/icons';
import { gitStatusMeta } from '@/ui/git';
import { basename, guessLang } from '@/lib/lang';
import { cn } from '@/lib/cn';

/** Flatten every file (not dir) for the search view. */
function flattenFiles(nodes: FileNode[], acc: FileNode[] = []): FileNode[] {
  for (const n of nodes) {
    if (n.kind === 'file') acc.push(n);
    if (n.children) flattenFiles(n.children, acc);
  }
  return acc;
}

export function ProjectPanel() {
  const { data, activeRepo, activeTabId, openFile, reloadRepo, canWrite } = useWorkspace();
  const source = useMemo(() => getDataSource(), []);
  const { toast } = useToast();
  const [query, setQuery] = useState('');
  const q = query.trim().toLowerCase();

  const matches = useMemo(() => {
    if (!q) return [];
    return flattenFiles(data.tree).filter((n) => n.id.toLowerCase().includes(q));
  }, [q, data.tree]);

  const newFile = async () => {
    const path = window.prompt('New file — repo-relative path:')?.trim();
    if (!path) return;
    try {
      await source.saveFile(activeRepo.id, path, '');
      await reloadRepo();
      openFile({ id: path, name: basename(path), lang: guessLang(path) });
      toast({ title: 'File created', description: path, variant: 'success' });
    } catch (e) {
      toast({ title: 'Create failed', description: String((e as Error)?.message ?? e), variant: 'danger' });
    }
  };

  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <PanelHeader
        title="Project"
        actions={
          <>
            <IconButton label="New file" title={canWrite ? 'New file' : 'Read-only repository'} disabled={!canWrite} onClick={() => void newFile()}>
              <PlusIcon className="h-4 w-4" />
            </IconButton>
            <IconButton label="Refresh" title="Refresh file tree" onClick={() => void reloadRepo()}>
              <RefreshIcon className="h-4 w-4" />
            </IconButton>
          </>
        }
      />

      <div className="px-2 pb-2">
        <label className="flex items-center gap-2 rounded-md bg-fill/10 px-2 py-1.5 focus-within:ring-2 focus-within:ring-accent/40">
          <SearchIcon className="h-3.5 w-3.5 shrink-0 text-text-tertiary" />
          <input
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            placeholder="Search files"
            className="w-full bg-transparent text-footnote text-text-primary placeholder:text-text-tertiary focus:outline-none"
          />
        </label>
      </div>

      <div className="dl-scroll min-h-0 flex-1 overflow-y-auto pb-3">
        <div className="dl-no-select flex items-center gap-1.5 px-3 pb-1 text-caption font-semibold uppercase tracking-wide text-text-tertiary">
          {activeRepo.name}
        </div>

        {q ? (
          matches.length ? (
            <ul>
              {matches.map((n) => {
                const status = n.status ? gitStatusMeta[n.status] : null;
                const active = activeTabId === n.id;
                return (
                  <li key={n.id}>
                    <button
                      type="button"
                      onClick={() => openFile(n)}
                      className={cn(
                        'group flex h-[26px] w-full items-center gap-1.5 rounded-sm px-3 text-footnote transition-colors',
                        active ? 'bg-accent/15 text-text-primary' : 'text-text-secondary hover:bg-fill/10',
                      )}
                    >
                      <FileTextIcon className="h-4 w-4 shrink-0 text-text-tertiary" />
                      <span className="truncate">{n.name}</span>
                      <span className="ml-auto truncate text-caption text-text-tertiary">{n.id}</span>
                      {status && <span className={cn('shrink-0 font-mono text-caption', status.cls)}>{status.letter}</span>}
                    </button>
                  </li>
                );
              })}
            </ul>
          ) : (
            <p role="status" className="px-3 py-4 text-footnote text-text-tertiary">
              No files match “{query}”.
            </p>
          )
        ) : (
          <ul>
            {data.tree.map((node) => (
              <TreeNode key={node.id} node={node} depth={0} activeId={activeTabId} onOpen={openFile} />
            ))}
          </ul>
        )}
      </div>
    </div>
  );
}

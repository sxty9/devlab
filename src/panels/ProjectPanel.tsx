import { useCallback, useMemo, useState } from 'react';
import { useWorkspace } from '@/state/workspace';
import { getDataSource } from '@/data';
import type { FileNode } from '@/types';
import { PanelHeader } from './PanelHeader';
import { TreeNode } from './TreeNode';
import { ContextMenu } from './ContextMenu';
import {
  dragHasFiles,
  dropFolder,
  dropPath,
  parentIndex,
  rowIndex,
  treeKeyAction,
  treeMenu,
  visibleRows,
} from './treeOps';
import { IconButton } from '@/ui/Button';
import { useToast } from '@/ui/Toast';
import { FileTextIcon, PlusIcon, RefreshIcon, SearchIcon } from '@/ui/icons';
import { gitStatusMeta } from '@/ui/git';
import { basename, guessLang } from '@/lib/lang';
import { filesFromClipboard } from '@/lib/file';
import { usePasteFiles } from '@/lib/usePasteFiles';
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
  const { data, activeRepo, activeTabId, openFile, openDiff, reloadRepo, stageChange, canWrite } = useWorkspace();
  const source = useMemo(() => getDataSource(), []);
  const { toast } = useToast();
  const [query, setQuery] = useState('');
  const [expanded, setExpanded] = useState<Record<string, boolean>>({});
  const [focusedId, setFocusedId] = useState<string | null>(null);
  const [menu, setMenu] = useState<{ x: number; y: number; node: FileNode } | null>(null);
  const [dropId, setDropId] = useState<string | null>(null);
  const q = query.trim().toLowerCase();

  const matches = useMemo(() => {
    if (!q) return [];
    return flattenFiles(data.tree).filter((n) => n.id.toLowerCase().includes(q));
  }, [q, data.tree]);

  // Folders are open by default down to depth 2 (as before), and every explicit toggle wins.
  const defaultOpen = useMemo(() => {
    const open: Record<string, boolean> = {};
    const walk = (nodes: FileNode[], depth: number) => {
      for (const n of nodes) {
        if (n.kind !== 'dir') continue;
        if (depth < 2) open[n.id] = true;
        if (n.children) walk(n.children, depth + 1);
      }
    };
    walk(data.tree, 0);
    return open;
  }, [data.tree]);

  const isOpen = useCallback((id: string) => expanded[id] ?? defaultOpen[id] ?? false, [expanded, defaultOpen]);
  const toggle = useCallback((id: string) => setExpanded((e) => ({ ...e, [id]: !(e[id] ?? defaultOpen[id] ?? false) })), [defaultOpen]);
  const setOpen = useCallback((id: string, open: boolean) => setExpanded((e) => ({ ...e, [id]: open })), []);

  const rows = useMemo(() => visibleRows(data.tree, isOpen), [data.tree, isOpen]);

  const copyPath = useCallback(
    (path: string) => {
      navigator.clipboard?.writeText(path).then(
        () => toast({ title: 'Path copied', description: path }),
        () => toast({ title: 'Could not copy path', variant: 'danger' }),
      );
    },
    [toast],
  );

  const newFile = useCallback(
    async (folder = '') => {
      const asked = window.prompt('New file — repo-relative path:', folder ? `${folder}/` : '')?.trim();
      if (!asked) return;
      try {
        await source.saveFile(activeRepo.id, asked, '');
        await reloadRepo();
        openFile({ id: asked, name: basename(asked), lang: guessLang(asked) });
        toast({ title: 'File created', description: asked, variant: 'success' });
      } catch (e) {
        toast({ title: 'Create failed', description: String((e as Error)?.message ?? e), variant: 'danger' });
      }
    },
    [source, activeRepo.id, reloadRepo, openFile, toast],
  );

  // ── dropped / pasted files land in the tree ────────────────────────────────
  // The tree represents a collection of files, so it takes a browser drag & drop (D 12); the
  // clipboard is the equal second way into the same path (D 16). Both go through one writer.
  const addFiles = useCallback(
    async (files: File[], folder = '') => {
      if (!canWrite || files.length === 0) return;
      const written: string[] = [];
      for (const file of files) {
        const path = dropPath(folder, file.name);
        try {
          const text = await file.text();
          // The repo write path carries text; a binary file belongs in the Vision catalog, which
          // uploads bytes. Saying so is better than writing a mangled file.
          if (text.includes('\u0000')) throw new Error('binary files belong in the Vision catalog');
          await source.saveFile(activeRepo.id, path, text);
          written.push(path);
        } catch (e) {
          toast({ title: 'Could not add file', description: `${file.name}: ${String((e as Error)?.message ?? e)}`, variant: 'danger' });
        }
      }
      if (written.length) {
        await reloadRepo();
        toast({
          title: written.length === 1 ? 'File added' : `${written.length} files added`,
          description: written.join(', '),
          variant: 'success',
        });
      }
    },
    [canWrite, source, activeRepo.id, reloadRepo, toast],
  );

  usePasteFiles((files) => void addFiles(files), canWrite);

  // A row handles its own hover and drop and stops there, so the surrounding root drop zone does
  // not overwrite the target (and a dropped file is never written twice).
  const onDragOverNode = useCallback((node: FileNode, e: React.DragEvent) => {
    if (!dragHasFiles(e.dataTransfer)) return;
    e.preventDefault();
    e.stopPropagation();
    e.dataTransfer.dropEffect = 'copy';
    setDropId(node.id);
  }, []);

  const onDropOn = useCallback(
    (node: FileNode | null, e: React.DragEvent) => {
      if (!dragHasFiles(e.dataTransfer)) return;
      e.preventDefault();
      if (node) e.stopPropagation();
      setDropId(null);
      void addFiles(filesFromClipboard({ clipboardData: e.dataTransfer }), dropFolder(node));
    },
    [addFiles],
  );

  // ── keyboard navigation over the rows ──────────────────────────────────────
  const focusRow = useCallback((index: number) => {
    const row = rows[index];
    if (!row) return;
    setFocusedId(row.node.id);
    // Move the DOM focus with the selection so the next key press lands on the same row.
    requestAnimationFrame(() => {
      document.querySelector<HTMLElement>(`[data-row-id="${CSS.escape(row.node.id)}"]`)?.focus();
    });
  }, [rows]);

  const onKeyDown = useCallback(
    (e: React.KeyboardEvent) => {
      const index = rowIndex(rows, focusedId);
      const action = treeKeyAction(e, index, rows, isOpen);
      if (action.kind === 'none') return;
      e.preventDefault();
      const node = rows[index]?.node;
      switch (action.kind) {
        case 'move':
          focusRow(action.index);
          return;
        case 'open':
          if (!node) return;
          if (node.kind === 'dir') toggle(node.id);
          else openFile(node);
          return;
        case 'expand':
          if (node) setOpen(node.id, true);
          return;
        case 'collapse':
          if (node) setOpen(node.id, false);
          return;
        case 'parent': {
          const up = parentIndex(rows, index);
          if (up >= 0) focusRow(up);
          return;
        }
        case 'copyPath':
          if (node) copyPath(node.id);
          return;
        case 'menu': {
          if (!node) return;
          const el = document.querySelector<HTMLElement>(`[data-row-id="${CSS.escape(node.id)}"]`);
          const r = el?.getBoundingClientRect();
          setMenu({ x: r ? r.left + 16 : 16, y: r ? r.bottom : 16, node });
          return;
        }
      }
    },
    [rows, focusedId, isOpen, focusRow, toggle, openFile, setOpen, copyPath],
  );

  // The search results are a list too, so they answer the same keys (D 17).
  const onSearchKeyDown = useCallback(
    (e: React.KeyboardEvent) => {
      if (matches.length === 0) return;
      const at = matches.findIndex((n) => n.id === (e.target as HTMLElement).getAttribute?.('data-search-id'));
      if (e.key === 'ArrowDown' || e.key === 'ArrowUp') {
        e.preventDefault();
        const next = e.key === 'ArrowDown' ? Math.min(at + 1, matches.length - 1) : Math.max(at <= 0 ? 0 : at - 1, 0);
        document.querySelector<HTMLElement>(`[data-search-id="${CSS.escape(matches[next].id)}"]`)?.focus();
        return;
      }
      if (at >= 0 && (e.metaKey || e.ctrlKey) && (e.key === 'c' || e.key === 'C')) {
        e.preventDefault();
        copyPath(matches[at].id);
      }
    },
    [matches, copyPath],
  );

  const chooseMenu = useCallback(
    (id: string) => {
      const node = menu?.node;
      if (!node) return;
      switch (id) {
        case 'open':
          openFile(node);
          return;
        case 'diff':
          openDiff(node.id);
          return;
        case 'toggle':
          toggle(node.id);
          return;
        case 'copyPath':
          copyPath(node.id);
          return;
        case 'newFile':
          void newFile(dropFolder(node));
          return;
        case 'stage':
          void stageChange(node.id).catch((e: unknown) =>
            toast({ title: 'Stage failed', description: String((e as Error)?.message ?? e), variant: 'danger' }),
          );
          return;
      }
    },
    [menu, openFile, openDiff, toggle, copyPath, newFile, stageChange, toast],
  );

  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <PanelHeader
        title="Project"
        actions={
          <>
            <IconButton label="New file" disabled={!canWrite} onClick={() => void newFile()}>
              <PlusIcon className="h-4 w-4" />
            </IconButton>
            <IconButton label="Refresh file tree" onClick={() => void reloadRepo()}>
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

      <div
        className={cn('dl-scroll min-h-0 flex-1 overflow-y-auto pb-3', dropId === '' && 'ring-1 ring-inset ring-accent/40')}
        onDragOver={(e) => {
          if (!dragHasFiles(e.dataTransfer)) return;
          e.preventDefault();
          e.dataTransfer.dropEffect = 'copy';
          setDropId('');
        }}
        onDragLeave={() => setDropId(null)}
        onDrop={(e) => onDropOn(null, e)}
      >
        <div className="dl-no-select flex items-center gap-1.5 px-3 pb-1 text-caption font-semibold uppercase tracking-wide text-text-tertiary">
          {activeRepo.name}
        </div>

        {q ? (
          matches.length ? (
            <ul role="listbox" aria-label="Search results" onKeyDown={onSearchKeyDown}>
              {matches.map((n) => {
                const status = n.status ? gitStatusMeta[n.status] : null;
                const active = activeTabId === n.id;
                return (
                  <li key={n.id}>
                    <button
                      type="button"
                      role="option"
                      aria-selected={active}
                      data-search-id={n.id}
                      onClick={() => openFile(n)}
                      onContextMenu={(e) => {
                        e.preventDefault();
                        setMenu({ x: e.clientX, y: e.clientY, node: n });
                      }}
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
          <ul role="tree" aria-label={`${activeRepo.name} files`} onKeyDown={onKeyDown}>
            {data.tree.map((node) => (
              <TreeNode
                key={node.id}
                node={node}
                depth={0}
                focusedId={focusedId}
                activeId={activeTabId}
                isOpen={isOpen}
                onToggle={toggle}
                onOpen={openFile}
                onFocus={(n) => setFocusedId(n.id)}
                onMenu={(n, x, y) => setMenu({ x, y, node: n })}
                dropId={dropId}
                onDropOn={onDropOn}
                onDragOverNode={onDragOverNode}
                onDragLeaveNode={() => setDropId(null)}
              />
            ))}
          </ul>
        )}
      </div>

      {menu && (
        <ContextMenu
          x={menu.x}
          y={menu.y}
          entries={treeMenu(menu.node, canWrite)}
          onChoose={chooseMenu}
          onClose={() => setMenu(null)}
        />
      )}
    </div>
  );
}

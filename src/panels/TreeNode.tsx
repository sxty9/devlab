import { useState } from 'react';
import type { FileNode } from '@/types';
import { ChevronRightIcon, FileTextIcon, FolderIcon, FolderOpenIcon } from '@/ui/icons';
import { gitStatusMeta } from '@/ui/git';
import { tintText } from '@/ui/tint';
import { cn } from '@/lib/cn';
import type { Repo } from '@/types';

const ROW = 'group flex h-[26px] w-full items-center gap-1.5 rounded-sm pr-2 text-footnote transition-colors duration-fast';

// A loose lang → accent-color map so file icons read at a glance (purely cosmetic).
const langTint: Record<string, Repo['tint']> = {
  typescript: 'accent',
  javascript: 'warning',
  go: 'ssd',
  python: 'success',
  shell: 'net',
  json: 'warning',
  css: 'gpu',
  yaml: 'net',
  markdown: 'accent',
};

export function TreeNode({
  node,
  depth,
  activeId,
  onOpen,
}: {
  node: FileNode;
  depth: number;
  activeId: string | null;
  onOpen: (n: FileNode) => void;
}) {
  const [open, setOpen] = useState(depth < 2);
  const pad = { paddingLeft: depth * 12 + 8 };

  if (node.kind === 'dir') {
    return (
      <li>
        <button
          type="button"
          onClick={() => setOpen((o) => !o)}
          onKeyDown={(e) => {
            if (e.key === 'ArrowRight' && !open) {
              e.preventDefault();
              setOpen(true);
            } else if (e.key === 'ArrowLeft' && open) {
              e.preventDefault();
              setOpen(false);
            }
          }}
          className={cn(ROW, 'text-text-secondary hover:bg-fill/10 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-accent/40')}
          style={pad}
          aria-expanded={open}
        >
          <ChevronRightIcon className={cn('h-3.5 w-3.5 shrink-0 text-text-tertiary transition-transform', open && 'rotate-90')} />
          {open ? (
            <FolderOpenIcon className="h-4 w-4 shrink-0 text-text-tertiary" />
          ) : (
            <FolderIcon className="h-4 w-4 shrink-0 text-text-tertiary" />
          )}
          <span className="truncate">{node.name}</span>
        </button>
        {open && node.children && (
          <ul>
            {node.children.map((child) => (
              <TreeNode key={child.id} node={child} depth={depth + 1} activeId={activeId} onOpen={onOpen} />
            ))}
          </ul>
        )}
      </li>
    );
  }

  const active = activeId === node.id;
  const status = node.status ? gitStatusMeta[node.status] : null;
  const iconTint = tintText[langTint[node.lang ?? ''] ?? 'accent'];

  return (
    <li>
      <button
        type="button"
        onClick={() => onOpen(node)}
        aria-current={active}
        className={cn(
          ROW,
          'relative focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-accent/40',
          active ? 'bg-accent/15 text-text-primary' : 'text-text-secondary hover:bg-fill/10',
        )}
        style={pad}
      >
        {active && <span className="absolute inset-y-1 left-0 w-0.5 rounded-r bg-accent" />}
        <span className="w-3.5 shrink-0" />
        <FileTextIcon className={cn('h-4 w-4 shrink-0', active ? 'text-accent' : iconTint, 'opacity-90')} />
        <span className={cn('truncate', status && status.cls)}>{node.name}</span>
        {status && <span className={cn('ml-auto shrink-0 font-mono text-caption', status.cls)} title={status.label}>{status.letter}</span>}
      </button>
    </li>
  );
}

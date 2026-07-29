import type { FileNode, Repo } from '@/types';
import { ChevronRightIcon, FileTextIcon, FolderIcon, FolderOpenIcon } from '@/ui/icons';
import { gitStatusMeta } from '@/ui/git';
import { tintText } from '@/ui/tint';
import { cn } from '@/lib/cn';
import { PATH_MIME } from './treeOps';

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

export interface TreeNodeProps {
  node: FileNode;
  depth: number;
  /** The focused row (keyboard + menu subject); the tree owns the selection. */
  focusedId: string | null;
  /** The open editor tab, marked as current. */
  activeId: string | null;
  isOpen: (id: string) => boolean;
  onToggle: (id: string) => void;
  onOpen: (n: FileNode) => void;
  onFocus: (n: FileNode) => void;
  onMenu: (n: FileNode, x: number, y: number) => void;
  /** The folder a file drop would land in, while a drag hovers it. */
  dropId: string | null;
  onDropOn: (n: FileNode, e: React.DragEvent) => void;
  onDragOverNode: (n: FileNode, e: React.DragEvent) => void;
  onDragLeaveNode: () => void;
}

/** One row of the file tree — a folder or a file. Rows are focusable targets of the tree's
 *  keyboard navigation, carry the context menu, can be dragged out by path and accept dropped
 *  files (cross-cutting 11). */
export function TreeNode(props: TreeNodeProps) {
  const { node, depth, focusedId, activeId, isOpen, onToggle, onOpen, onFocus, onMenu, dropId, onDropOn, onDragOverNode, onDragLeaveNode } = props;
  const open = isOpen(node.id);
  const pad = { paddingLeft: depth * 12 + 8 };
  const focused = focusedId === node.id;
  const dropTarget = dropId === node.id;

  // Every row is a drag source (its repo path) and a drop target (files land in its folder).
  const dnd = {
    draggable: true,
    onDragStart: (e: React.DragEvent) => {
      e.dataTransfer.setData(PATH_MIME, node.id);
      e.dataTransfer.setData('text/plain', node.id);
      e.dataTransfer.effectAllowed = 'copy';
    },
    onDragOver: (e: React.DragEvent) => onDragOverNode(node, e),
    onDragLeave: onDragLeaveNode,
    onDrop: (e: React.DragEvent) => onDropOn(node, e),
    onContextMenu: (e: React.MouseEvent) => {
      e.preventDefault();
      onFocus(node);
      onMenu(node, e.clientX, e.clientY);
    },
  };

  if (node.kind === 'dir') {
    return (
      <li>
        <div
          {...dnd}
          role="treeitem"
          aria-expanded={open}
          aria-selected={focused}
          tabIndex={focused ? 0 : -1}
          data-row-id={node.id}
          onClick={() => {
            onFocus(node);
            onToggle(node.id);
          }}
          className={cn(
            ROW,
            'cursor-pointer text-text-secondary hover:bg-fill/10 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-accent/40',
            focused && 'bg-fill/[0.08]',
            dropTarget && 'ring-1 ring-inset ring-accent/60',
          )}
          style={pad}
        >
          <ChevronRightIcon className={cn('h-3.5 w-3.5 shrink-0 text-text-tertiary transition-transform', open && 'rotate-90')} />
          {open ? (
            <FolderOpenIcon className="h-4 w-4 shrink-0 text-text-tertiary" />
          ) : (
            <FolderIcon className="h-4 w-4 shrink-0 text-text-tertiary" />
          )}
          <span className="truncate">{node.name}</span>
        </div>
        {open && node.children && (
          <ul role="group">
            {node.children.map((child) => (
              <TreeNode key={child.id} {...props} node={child} depth={depth + 1} />
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
      <div
        {...dnd}
        role="treeitem"
        aria-selected={focused}
        aria-current={active}
        tabIndex={focused ? 0 : -1}
        data-row-id={node.id}
        onClick={() => {
          onFocus(node);
          onOpen(node);
        }}
        className={cn(
          ROW,
          'relative cursor-pointer focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-accent/40',
          active ? 'bg-accent/15 text-text-primary' : 'text-text-secondary hover:bg-fill/10',
          focused && !active && 'bg-fill/[0.08]',
          dropTarget && 'ring-1 ring-inset ring-accent/60',
        )}
        style={pad}
      >
        {active && <span className="absolute inset-y-1 left-0 w-0.5 rounded-r bg-accent" />}
        <span className="w-3.5 shrink-0" />
        <FileTextIcon className={cn('h-4 w-4 shrink-0', active ? 'text-accent' : iconTint, 'opacity-90')} />
        <span className={cn('truncate', status && status.cls)}>{node.name}</span>
        {status && (
          <span className={cn('ml-auto shrink-0 font-mono text-caption', status.cls)} aria-label={status.label}>
            {status.letter}
          </span>
        )}
      </div>
    </li>
  );
}

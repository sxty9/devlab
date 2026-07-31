import { useState } from 'react';
import { useWorkspace } from '@/state/workspace';
import type { Tab } from '@/types';
import { ContextMenu } from '@/panels/ContextMenu';
import { tabMenu } from '@/panels/treeOps';
import { useToast } from '@/ui/Toast';
import { DiffIcon, SitemapIcon, FileTextIcon, LightbulbIcon, XIcon } from '@/ui/icons';
import { cn } from '@/lib/cn';

const tabIcon = { structure: SitemapIcon, diff: DiffIcon, code: FileTextIcon, vision: LightbulbIcon } as const;

function TabButton({
  tab,
  active,
  onMenu,
  onArrow,
}: {
  tab: Tab;
  active: boolean;
  onMenu: (tab: Tab, x: number, y: number) => void;
  onArrow: (tab: Tab, dir: -1 | 1) => void;
}) {
  const { setActiveTab, closeTab } = useWorkspace();
  const Icon = tabIcon[tab.kind];

  const openMenuAt = (el: HTMLElement) => {
    const r = el.getBoundingClientRect();
    onMenu(tab, r.left + 16, r.bottom);
  };

  return (
    <div
      role="tab"
      aria-selected={active}
      tabIndex={active ? 0 : -1}
      data-tab-id={tab.id}
      onClick={() => setActiveTab(tab.id)}
      onContextMenu={(e) => {
        e.preventDefault();
        setActiveTab(tab.id);
        onMenu(tab, e.clientX, e.clientY);
      }}
      onKeyDown={(e) => {
        const cmd = e.metaKey || e.ctrlKey;
        if (e.key === 'Enter' && cmd) {
          e.preventDefault();
          openMenuAt(e.currentTarget);
        } else if (e.key === 'Enter' || e.key === ' ') {
          e.preventDefault();
          setActiveTab(tab.id);
        } else if (e.key === 'ArrowRight' || e.key === 'ArrowLeft') {
          e.preventDefault();
          onArrow(tab, e.key === 'ArrowRight' ? 1 : -1);
        } else if (cmd && (e.key === 'w' || e.key === 'W')) {
          e.preventDefault();
          closeTab(tab.id);
        } else if (e.key === 'ContextMenu') {
          e.preventDefault();
          openMenuAt(e.currentTarget);
        }
      }}
      onMouseDown={(e) => {
        if (e.button === 1) {
          e.preventDefault();
          closeTab(tab.id);
        }
      }}
      className={cn(
        'group/tab relative flex h-9 max-w-[15rem] shrink-0 cursor-pointer items-center gap-2 border-r border-separator pl-3 pr-2 text-footnote',
        'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-accent/50',
        active ? 'bg-surface-raised text-text-primary' : 'text-text-tertiary hover:bg-fill/5 hover:text-text-secondary',
      )}
    >
      {active && <span className="absolute inset-x-0 top-0 h-0.5 bg-accent" />}
      <Icon className={cn('h-4 w-4 shrink-0', active ? 'text-accent' : 'text-text-tertiary')} />
      <span className="truncate">{tab.title}</span>
      <span className="relative ml-1 flex h-4 w-4 shrink-0 items-center justify-center">
        {tab.dirty && (
          <span className="absolute h-2 w-2 rounded-full bg-text-secondary group-hover/tab:opacity-0" aria-label="Unsaved" />
        )}
        <button
          type="button"
          aria-label={`Close ${tab.title}`}
          onClick={(e) => {
            e.stopPropagation();
            closeTab(tab.id);
          }}
          className="flex h-4 w-4 items-center justify-center rounded-sm text-text-tertiary opacity-0 transition hover:bg-fill/15 hover:text-text-primary group-hover/tab:opacity-100 focus-visible:opacity-100"
        >
          <XIcon className="h-3.5 w-3.5" />
        </button>
      </span>
    </div>
  );
}

export function TabStrip() {
  const { openTabs, activeTabId, setActiveTab, closeTab } = useWorkspace();
  const { toast } = useToast();
  const [menu, setMenu] = useState<{ x: number; y: number; tab: Tab } | null>(null);

  // Arrow keys walk the strip, as on every other list-like surface (D 17).
  const onArrow = (tab: Tab, dir: -1 | 1) => {
    const at = openTabs.findIndex((t) => t.id === tab.id);
    const next = openTabs[Math.min(Math.max(at + dir, 0), openTabs.length - 1)];
    if (!next) return;
    setActiveTab(next.id);
    requestAnimationFrame(() => {
      document.querySelector<HTMLElement>(`[data-tab-id="${CSS.escape(next.id)}"]`)?.focus();
    });
  };

  const chooseMenu = (id: string) => {
    const tab = menu?.tab;
    if (!tab) return;
    switch (id) {
      case 'close':
        closeTab(tab.id);
        return;
      case 'closeOthers':
        for (const t of openTabs) if (t.id !== tab.id) closeTab(t.id);
        return;
      case 'closeAll':
        for (const t of openTabs) closeTab(t.id);
        return;
      case 'copyPath': {
        const path = tab.path ?? tab.id;
        navigator.clipboard?.writeText(path).then(
          () => toast({ title: 'Path copied', description: path }),
          () => toast({ title: 'Could not copy path', variant: 'danger' }),
        );
        return;
      }
    }
  };

  return (
    <>
      <div role="tablist" aria-label="Open editors" className="dl-scroll flex h-9 shrink-0 items-stretch overflow-x-auto border-b border-separator bg-surface-sidebar">
        {openTabs.map((t) => (
          <TabButton
            key={t.id}
            tab={t}
            active={t.id === activeTabId}
            onMenu={(tab, x, y) => setMenu({ x, y, tab })}
            onArrow={onArrow}
          />
        ))}
      </div>
      {menu && (
        <ContextMenu
          x={menu.x}
          y={menu.y}
          entries={tabMenu(openTabs.length)}
          onChoose={chooseMenu}
          onClose={() => setMenu(null)}
        />
      )}
    </>
  );
}

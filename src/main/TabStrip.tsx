import { useWorkspace } from '@/state/workspace';
import type { Tab } from '@/types';
import { SitemapIcon, FileTextIcon, XIcon } from '@/ui/icons';
import { cn } from '@/lib/cn';

function TabButton({ tab, active }: { tab: Tab; active: boolean }) {
  const { setActiveTab, closeTab } = useWorkspace();
  const Icon = tab.kind === 'structure' ? SitemapIcon : FileTextIcon;

  return (
    <div
      role="tab"
      aria-selected={active}
      tabIndex={0}
      onClick={() => setActiveTab(tab.id)}
      onKeyDown={(e) => {
        if (e.key === 'Enter' || e.key === ' ') {
          e.preventDefault();
          setActiveTab(tab.id);
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
  const { openTabs, activeTabId } = useWorkspace();
  return (
    <div role="tablist" aria-label="Open editors" className="dl-scroll flex h-9 shrink-0 items-stretch overflow-x-auto border-b border-separator bg-surface-sidebar">
      {openTabs.map((t) => (
        <TabButton key={t.id} tab={t} active={t.id === activeTabId} />
      ))}
    </div>
  );
}

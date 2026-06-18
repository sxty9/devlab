import { useWorkspace } from '@/state/workspace';
import type { StageState, VisionKind } from '@/types';
import { PanelHeader } from './PanelHeader';
import { IconButton } from '@/ui/Button';
import { FileTextIcon, LightbulbIcon, PlusIcon, RocketIcon, SitemapIcon } from '@/ui/icons';
import { cn } from '@/lib/cn';

const kindIcon: Record<VisionKind, (p: { className?: string }) => JSX.Element> = {
  spec: FileTextIcon,
  mindmap: SitemapIcon,
  jet: RocketIcon,
  note: LightbulbIcon,
};
const kindLabel: Record<VisionKind, string> = { spec: 'Spec', mindmap: 'Mindmap', jet: 'Jet', note: 'Note' };
const stateDot: Record<StageState, string> = { done: 'bg-success', active: 'bg-accent', pending: 'bg-fill/30' };

/** "Vision Deposit" — the front of the pipeline: specs, mindmaps, jets and notes per repo. */
export function VisionPanel() {
  const { data, activeRepo } = useWorkspace();

  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <PanelHeader
        title="Vision"
        actions={
          <IconButton label="New vision" title="Capture a new idea (phase 2)">
            <PlusIcon className="h-4 w-4" />
          </IconButton>
        }
      />

      <div className="dl-no-select flex items-center gap-1.5 px-3 pb-1 pt-1 text-caption text-text-tertiary">
        {data.vision.length} ideas in {activeRepo.name}
      </div>

      <div className="dl-scroll min-h-0 flex-1 space-y-1.5 overflow-y-auto px-2 pb-3">
        {data.vision.map((doc) => {
          const Icon = kindIcon[doc.kind];
          return (
            <button
              key={doc.id}
              type="button"
              className="group flex w-full items-start gap-2.5 rounded-md border border-transparent bg-fill/[0.04] p-2.5 text-left transition hover:border-separator hover:bg-fill/[0.08] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent/40"
            >
              <span className="mt-0.5 flex h-7 w-7 shrink-0 items-center justify-center rounded-md bg-fill/10 text-text-secondary">
                <Icon className="h-4 w-4" />
              </span>
              <span className="min-w-0 flex-1">
                <span className="flex items-center gap-1.5">
                  <span className={cn('h-1.5 w-1.5 shrink-0 rounded-full', stateDot[doc.state])} />
                  <span className="truncate text-footnote font-medium text-text-primary">{doc.title}</span>
                </span>
                <span className="mt-0.5 line-clamp-2 block text-caption text-text-secondary">{doc.summary}</span>
                <span className="mt-1 flex items-center gap-1.5 text-[11px] text-text-tertiary">
                  <span className="rounded-sm bg-fill/10 px-1.5 py-0.5">{kindLabel[doc.kind]}</span>
                  <span>· {doc.updated}</span>
                </span>
              </span>
            </button>
          );
        })}
      </div>
    </div>
  );
}

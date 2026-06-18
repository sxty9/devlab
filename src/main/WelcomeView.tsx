import { useWorkspace } from '@/state/workspace';
import { CodeIcon, FilesIcon, GitBranchIcon, RocketIcon } from '@/ui/icons';

/** Shown when no editor tab is open. */
export function WelcomeView() {
  const { activeRepo, openStructure, setPanel } = useWorkspace();

  const actions = [
    { icon: FilesIcon, label: 'Browse project files', onClick: () => setPanel('project') },
    { icon: CodeIcon, label: `Open ${activeRepo.name} structure`, onClick: openStructure },
    { icon: GitBranchIcon, label: 'Review version control', onClick: () => setPanel('vcs') },
  ];

  return (
    <div className="flex min-h-0 flex-1 flex-col items-center justify-center bg-bg-base px-6 text-center">
      <span className="flex h-16 w-16 items-center justify-center rounded-2xl bg-surface-raised shadow-elev-2 ring-1 ring-separator">
        <CodeIcon className="h-8 w-8 text-accent" />
      </span>
      <h1 className="mt-5 text-title2 font-semibold tracking-tight text-text-primary">DevLab</h1>
      <p className="mt-1 max-w-sm text-subhead text-text-secondary">
        Develop, maintain and ship Holistic services — from vision to preview to prod, in one place.
      </p>

      <div className="mt-7 flex w-full max-w-sm flex-col gap-1.5">
        {actions.map(({ icon: Icon, label, onClick }) => (
          <button
            key={label}
            type="button"
            onClick={onClick}
            className="flex items-center gap-3 rounded-md border border-separator bg-surface px-3.5 py-2.5 text-left text-footnote text-text-secondary shadow-elev-1 transition hover:border-accent/40 hover:text-text-primary"
          >
            <Icon className="h-4 w-4 text-accent" />
            {label}
          </button>
        ))}
      </div>

      <p className="mt-7 flex items-center gap-1.5 text-caption text-text-tertiary">
        <RocketIcon className="h-3.5 w-3.5" />
        Preview-deployed via sxgate · phase 1 (UI shell, mock data)
      </p>
    </div>
  );
}

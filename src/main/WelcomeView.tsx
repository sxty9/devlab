import { useWorkspace } from '@/state/workspace';
import { ClaudeIcon, CodeIcon, FilesIcon, GitBranchIcon, LightbulbIcon, SitemapIcon, TerminalIcon } from '@/ui/icons';

/** Shown when no editor tab is open — the landing surface of the open repository. It offers ways
 *  IN and states nothing about the repository's condition: a fixed row of pipeline stages would
 *  assert a state nobody measured (B 1.4), so the honest overview lives in the structure view,
 *  which renders only what the server could derive. */
export function WelcomeView() {
  const { activeRepo, openStructure, setPanel } = useWorkspace();

  const actions = [
    { icon: SitemapIcon, label: `Open ${activeRepo.name} structure`, onClick: openStructure },
    { icon: FilesIcon, label: 'Browse project files', onClick: () => setPanel('project') },
    { icon: GitBranchIcon, label: 'Review changes', onClick: () => setPanel('vcs') },
    { icon: LightbulbIcon, label: 'Open the vision catalog', onClick: () => setPanel('vision') },
    { icon: TerminalIcon, label: 'Open a terminal', onClick: () => setPanel('terminal') },
    { icon: ClaudeIcon, label: 'Ask AI about this repository', onClick: () => setPanel('claude') },
  ];

  return (
    <div className="dl-scroll flex min-h-0 flex-1 flex-col items-center justify-center overflow-y-auto bg-bg-base px-6 py-10 text-center">
      <div className="relative">
        <div className="absolute inset-0 -z-10 scale-150 rounded-full bg-accent/20 blur-3xl" aria-hidden />
        <span className="flex h-16 w-16 items-center justify-center rounded-2xl bg-surface-raised shadow-elev-3 ring-1 ring-separator">
          <CodeIcon className="h-8 w-8 text-accent" />
        </span>
      </div>
      <h1 className="mt-5 text-title1 font-semibold tracking-tight text-text-primary">DevLab</h1>
      <p className="mt-1.5 max-w-md text-subhead text-text-secondary">
        Develop, maintain and ship Holistic services — in one place.
      </p>

      {/* Quick actions */}
      <div className="mt-8 flex w-full max-w-sm flex-col gap-1.5">
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

    </div>
  );
}

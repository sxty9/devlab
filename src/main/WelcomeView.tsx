import { useWorkspace } from '@/state/workspace';
import { CodeIcon, FilesIcon, GitCommitIcon, LightbulbIcon, RocketIcon, SitemapIcon, TerminalIcon } from '@/ui/icons';

const STAGES: { icon: (p: { className?: string }) => JSX.Element; label: string; desc: string }[] = [
  { icon: LightbulbIcon, label: 'Vision', desc: 'Specs · mindmaps · jets' },
  { icon: CodeIcon, label: 'Code', desc: 'Edit with Claude & CLI' },
  { icon: RocketIcon, label: 'Preview', desc: 'Branch sandbox · sxgate' },
  { icon: TerminalIcon, label: 'Delivery', desc: 'Prod test' },
  { icon: GitCommitIcon, label: 'main', desc: 'Merge & ship' },
];

/** Shown when no editor tab is open — the DevLab landing & pipeline overview. */
export function WelcomeView() {
  const { activeRepo, openStructure, setPanel } = useWorkspace();

  const actions = [
    { icon: FilesIcon, label: 'Browse project files', onClick: () => setPanel('project') },
    { icon: SitemapIcon, label: `Open ${activeRepo.name} structure`, onClick: openStructure },
    { icon: LightbulbIcon, label: 'Review the vision deposit', onClick: () => setPanel('vision') },
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
        Develop, maintain and ship Holistic services — from <span className="text-text-primary">vision</span> to{' '}
        <span className="text-text-primary">preview</span> to <span className="text-text-primary">prod</span>, in one place.
      </p>

      {/* Pipeline */}
      <div className="mt-8 grid w-full max-w-2xl grid-cols-2 gap-2 sm:grid-cols-5">
        {STAGES.map(({ icon: Icon, label, desc }, i) => (
          <div
            key={label}
            className="flex flex-col items-center gap-1.5 rounded-card border border-separator bg-surface px-2 py-3 shadow-elev-1"
          >
            <span className="flex h-9 w-9 items-center justify-center rounded-md bg-fill/10 text-accent">
              <Icon className="h-5 w-5" />
            </span>
            <span className="text-footnote font-medium text-text-primary">{label}</span>
            <span className="text-[11px] leading-tight text-text-tertiary">{desc}</span>
            <span className="text-[10px] font-semibold text-text-tertiary">{i + 1}</span>
          </div>
        ))}
      </div>

      {/* Quick actions */}
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

import type { ReactNode } from 'react';
import { useSession } from '@/state/session';
import { CAPABILITIES, type Capability } from '@/views/capabilities';
import { IconCard } from '@/ui/IconCard';
import { repoGlyph } from '@/ui/repoIcon';
import { Button } from '@/ui/Button';
import { CodeIcon } from '@/ui/icons';

function Group({ title, children }: { title: string; children: ReactNode }) {
  return (
    <section className="w-full">
      <h2 className="mb-2.5 text-caption font-semibold uppercase tracking-wide text-text-tertiary">{title}</h2>
      {children}
    </section>
  );
}

const grid = 'grid grid-cols-2 gap-3 sm:grid-cols-3 md:grid-cols-4 lg:grid-cols-6';

/** DevLab's landing surface: the Holistic repositories, and what DevLab can do with them. */
export function Dashboard() {
  const { repos, reposError, reloadRepos, user, lastRepo, openRepo, openCapability } = useSession();

  // A card that cannot be used SAYS WHY. The IDE needs a repository; without one it is dead, and a
  // silently greyed-out tile leaves the viewer guessing what is wrong with it — which is exactly
  // what happened when the repository read failed: the tile went dark with no reason at all
  // (browser inspection, 2026-07-31). Returns the reason, or null when the card is usable.
  const ideBlocked = (id: string): string | null => {
    if (id !== 'ide' || repos.length > 0) return null;
    return reposError ? 'needs the repository list, which could not be read' : 'needs a repository';
  };

  const openCapabilityCard = (c: Capability) => {
    if (c.id !== 'ide') return openCapability(c.id);
    // The IDE card resumes where you were; from a cold start it opens the first repo.
    const target = repos.some((r) => r.id === lastRepo) ? lastRepo : repos[0]?.id;
    if (target) openRepo(target);
  };

  const visible = CAPABILITIES.filter((c) => !c.visible || c.visible(user, repos)).sort((a, b) => a.order - b.order);

  return (
    <div className="dl-scroll min-h-0 flex-1 overflow-y-auto bg-bg-base">
      <div className="mx-auto flex w-full max-w-5xl flex-col items-center gap-9 px-6 py-12">
        <div className="flex flex-col items-center">
          <div className="relative">
            <div className="absolute inset-0 -z-10 scale-150 rounded-full bg-accent/20 blur-3xl" aria-hidden />
            <span className="flex h-14 w-14 items-center justify-center rounded-2xl bg-surface-raised shadow-elev-3 ring-1 ring-separator">
              <CodeIcon className="h-7 w-7 text-accent" />
            </span>
          </div>
          <h1 className="mt-4 text-title2 font-semibold tracking-tight text-text-primary">DevLab</h1>
        </div>

        <Group title="Repositories">
          {repos.length > 0 ? (
            <div className={grid}>
              {repos.map((r) => (
                <IconCard
                  key={r.id}
                  icon={repoGlyph(r.icon)}
                  tint={r.tint}
                  title={r.name}
                  subtitle={r.permission === 'pull' ? `${r.language} · read-only` : r.language}
                  onClick={() => openRepo(r.id)}
                />
              ))}
            </div>
          ) : (
            <div className="flex flex-col items-start gap-3 rounded-card border border-separator bg-surface px-4 py-4 shadow-elev-1">
              <p className="max-w-lg text-footnote text-text-secondary">
                {/* The reason is the one that was MEASURED. Naming GitHub here regardless is how the
                    dashboard blamed GitHub while our own server was the one answering 502. */}
                {reposError
                  ? `The repository list could not be read: ${reposError}`
                  : 'No repositories visible. DevLab shows the Holistic repositories your linked GitHub account has access to.'}
              </p>
              <Button variant="secondary" size="sm" onClick={reloadRepos}>
                Check again
              </Button>
            </div>
          )}
        </Group>

        <Group title="Capabilities">
          <div className={grid}>
            {visible.map((c) => (
              <IconCard
                key={c.id}
                icon={c.icon}
                tint={c.tint}
                title={c.displayName}
                subtitle={ideBlocked(c.id) ?? c.role}
                disabled={c.id === 'ide' && repos.length === 0}
                onClick={() => openCapabilityCard(c)}
              />
            ))}
          </div>
        </Group>
      </div>
    </div>
  );
}

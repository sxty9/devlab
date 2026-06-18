import { useWorkspace } from '@/state/workspace';
import { Dropdown, DropdownItem, DropdownLabel, DropdownSeparator } from '@/ui/Dropdown';
import { PlusIcon } from '@/ui/icons';
import { tintBg } from '@/ui/tint';
import { cn } from '@/lib/cn';

const KIND_LABEL: Record<string, string> = { service: 'Service', repo: 'Repo', library: 'Library' };

/** Top-bar repository/service picker — the IntelliJ-style "which repo am I in" dropdown. */
export function RepoDropdown() {
  const { repos, activeRepo, setRepo } = useWorkspace();

  return (
    <Dropdown
      ariaLabel="Select repository"
      trigger={
        <span className="flex min-w-0 items-center gap-2">
          <span className={cn('h-2 w-2 shrink-0 rounded-full', tintBg[activeRepo.tint])} />
          <span className="truncate font-medium">{activeRepo.name}</span>
          <span className="hidden shrink-0 rounded-sm bg-fill/10 px-1.5 py-0.5 text-caption text-text-tertiary sm:inline">
            {KIND_LABEL[activeRepo.kind]}
          </span>
        </span>
      }
      menuClassName="min-w-[20rem]"
    >
      {(close) => (
        <>
          <DropdownLabel>Repositories</DropdownLabel>
          {repos.map((r) => (
            <DropdownItem
              key={r.id}
              selected={r.id === activeRepo.id}
              leading={<span className={cn('h-2.5 w-2.5 rounded-full', tintBg[r.tint])} />}
              title={
                <span className="flex items-center gap-2">
                  <span className="truncate">{r.name}</span>
                  <span className="shrink-0 text-caption text-text-tertiary">{r.language}</span>
                </span>
              }
              hint={r.description}
              onClick={() => {
                setRepo(r.id);
                close();
              }}
            />
          ))}
          <DropdownSeparator />
          <DropdownItem
            leading={<PlusIcon className="h-4 w-4 text-text-secondary" />}
            title="Clone repository…"
            onClick={close}
          />
        </>
      )}
    </Dropdown>
  );
}

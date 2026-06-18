import { useWorkspace } from '@/state/workspace';
import { Dropdown, DropdownItem, DropdownLabel, DropdownSeparator } from '@/ui/Dropdown';
import { GitBranchIcon, PlusIcon } from '@/ui/icons';

/** Branch / working-session picker ("Session | Branch" in the sketch). */
export function BranchDropdown() {
  const { branches, activeBranch, setBranch } = useWorkspace();

  return (
    <Dropdown
      ariaLabel="Select branch"
      trigger={
        <span className="flex min-w-0 items-center gap-1.5">
          <GitBranchIcon className="h-3.5 w-3.5 shrink-0 text-text-secondary" />
          <span className="truncate text-text-secondary">{activeBranch.name}</span>
        </span>
      }
    >
      {(close) => (
        <>
          <DropdownLabel>Branches</DropdownLabel>
          {branches.map((b) => (
            <DropdownItem
              key={b.name}
              selected={b.name === activeBranch.name}
              leading={<GitBranchIcon className="h-4 w-4 text-text-secondary" />}
              title={
                <span className="flex items-center gap-2">
                  <span className="truncate">{b.name}</span>
                  {b.isDefault && <span className="shrink-0 rounded-sm bg-fill/10 px-1.5 text-caption text-text-tertiary">default</span>}
                </span>
              }
              hint={`updated ${b.updated}`}
              trailing={
                (b.ahead > 0 || b.behind > 0) && (
                  <span className="mr-1 shrink-0 font-mono text-caption text-text-tertiary">
                    {b.ahead > 0 && <span className="text-success">↑{b.ahead}</span>}
                    {b.ahead > 0 && b.behind > 0 && ' '}
                    {b.behind > 0 && <span className="text-warning">↓{b.behind}</span>}
                  </span>
                )
              }
              onClick={() => {
                setBranch(b.name);
                close();
              }}
            />
          ))}
          <DropdownSeparator />
          <DropdownItem leading={<PlusIcon className="h-4 w-4 text-text-secondary" />} title="New branch / session…" onClick={close} />
        </>
      )}
    </Dropdown>
  );
}

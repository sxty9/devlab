import type { GitStatus } from '@/types';

/** Single-letter badge + token color for a git status (VS Code-style decorations). */
export const gitStatusMeta: Record<GitStatus, { letter: string; cls: string; label: string }> = {
  modified: { letter: 'M', cls: 'text-warning', label: 'Modified' },
  added: { letter: 'A', cls: 'text-success', label: 'Added' },
  deleted: { letter: 'D', cls: 'text-danger', label: 'Deleted' },
  untracked: { letter: 'U', cls: 'text-success', label: 'Untracked' },
  renamed: { letter: 'R', cls: 'text-accent', label: 'Renamed' },
  conflict: { letter: 'C', cls: 'text-danger', label: 'Conflict' },
};

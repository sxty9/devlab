// Shared derivations over RepoData — "given a repo's data, tell me X" — so no caller re-derives them.
import type { RepoData } from '@/types';

/** The repo's default branch name: the one marked default, else the first, else "main". */
export function defaultBranchName(d: RepoData): string {
  return d.branches.find((b) => b.isDefault)?.name ?? d.branches[0]?.name ?? 'main';
}

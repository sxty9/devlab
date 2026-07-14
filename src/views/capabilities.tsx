import type { Repo, User } from '@/types';
import { AtlasIcon, CodeIcon, MercuryIcon } from '@/ui/icons';

export type CapabilityId = 'mercury' | 'ide' | 'atlas';

/** What DevLab can do, as opposed to what it can do it *to* (the repositories).
 *
 *  The field names mirror @holistic/ui's ServicePlugin (id · displayName · icon · order · visible)
 *  one for one, so a capability that later grows into a real Holistic service — or DevLab itself
 *  folding into the Holistic shell — is a rename rather than a rewrite. It stays a compile-time
 *  const rather than a served manifest: the ids bind to bundled React components, so it can never be
 *  more dynamic than the bundle, and serving it would duplicate a build-time truth into a runtime one. */
export interface Capability {
  id: CapabilityId;
  displayName: string;
  /** What it is, in one line. The card's only secondary text. */
  role: string;
  icon: (p: { className?: string }) => JSX.Element;
  tint: Repo['tint'];
  order: number;
  /** Mirrors serviceVisibleByDefault(): a capability you cannot use never clutters the surface. */
  visible?: (user: User, repos: Repo[]) => boolean;
}

export const CAPABILITIES: Capability[] = [
  { id: 'mercury', displayName: 'Mercury', role: 'Holistic axioms', icon: MercuryIcon, tint: 'warning', order: 10 },
  { id: 'ide', displayName: 'DevLab IDE', role: 'Build & ship', icon: CodeIcon, tint: 'accent', order: 20 },
  { id: 'atlas', displayName: 'Atlas', role: 'Service interactions', icon: AtlasIcon, tint: 'net', order: 30 },
];

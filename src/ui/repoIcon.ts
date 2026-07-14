import type { RepoIcon } from '@/types';
import {
  BracesIcon,
  CogIcon,
  FolderIcon,
  GoIcon,
  LayersIcon,
  PythonIcon,
  SitemapIcon,
  TerminalIcon,
} from './icons';

type Glyph = (p: { className?: string }) => JSX.Element;

/** Repo card marks, keyed by the glyph name discover.icon() derives from language + kind. */
const GLYPHS: Record<RepoIcon, Glyph> = {
  go: GoIcon,
  ts: BracesIcon,
  rust: CogIcon,
  python: PythonIcon,
  shell: TerminalIcon,
  service: SitemapIcon,
  repo: FolderIcon,
  library: LayersIcon,
};

/** The glyph for a repo. Falls back to the service mark for a name this build doesn't know. */
export function repoGlyph(icon: RepoIcon): Glyph {
  return GLYPHS[icon] ?? SitemapIcon;
}

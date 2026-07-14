import type { SVGProps } from 'react';

// Line icons on a 24px grid, 1.6 stroke, round joins — tuned to sit next to SF/Inter text.
// All accept className (color via currentColor) so they inherit text-* token utilities.

type IconProps = { className?: string } & SVGProps<SVGSVGElement>;

function Svg({ className, children, ...rest }: IconProps & { children: React.ReactNode }) {
  return (
    <svg
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth={1.6}
      strokeLinecap="round"
      strokeLinejoin="round"
      className={className}
      aria-hidden="true"
      {...rest}
    >
      {children}
    </svg>
  );
}

export const FilesIcon = (p: IconProps) => (
  <Svg {...p}>
    <path d="M14 3H7a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h10a2 2 0 0 0 2-2V8z" />
    <path d="M14 3v5h5" />
  </Svg>
);

export const FolderIcon = (p: IconProps) => (
  <Svg {...p}>
    <path d="M3 7a2 2 0 0 1 2-2h4l2 2.5h8a2 2 0 0 1 2 2V17a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2z" />
  </Svg>
);

export const FolderOpenIcon = (p: IconProps) => (
  <Svg {...p}>
    <path d="M3 7a2 2 0 0 1 2-2h4l2 2.5h8a2 2 0 0 1 2 2V10H3z" />
    <path d="M3 10h17.5a1 1 0 0 1 .96 1.27l-1.4 5A2 2 0 0 1 18.13 18H5.5a2 2 0 0 1-1.94-1.52L3 10z" />
  </Svg>
);

export const FileIcon = (p: IconProps) => (
  <Svg {...p}>
    <path d="M14 3H7a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h10a2 2 0 0 0 2-2V8z" />
    <path d="M14 3v5h5" />
  </Svg>
);

export const FileTextIcon = (p: IconProps) => (
  <Svg {...p}>
    <path d="M14 3H7a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h10a2 2 0 0 0 2-2V8z" />
    <path d="M14 3v5h5M9 13h6M9 17h4" />
  </Svg>
);

export const GitBranchIcon = (p: IconProps) => (
  <Svg {...p}>
    <circle cx="6" cy="6" r="2.4" />
    <circle cx="6" cy="18" r="2.4" />
    <circle cx="18" cy="8" r="2.4" />
    <path d="M6 8.4v7.2M18 10.4c0 4.2-3.4 4.6-6 5.2" />
  </Svg>
);

export const GitCommitIcon = (p: IconProps) => (
  <Svg {...p}>
    <circle cx="12" cy="12" r="3.2" />
    <path d="M3 12h5.8M15.2 12H21" />
  </Svg>
);

export const TerminalIcon = (p: IconProps) => (
  <Svg {...p}>
    <rect x="3" y="4" width="18" height="16" rx="2.5" />
    <path d="M7 9l3 3-3 3M13 15h4" />
  </Svg>
);

// Anthropic-style radial sunburst — DevLab's Claude mark (deliberately NOT the 4-point
// Gemini diamond). Rays of two lengths burst from the centre.
export const ClaudeIcon = ({ className, ...rest }: IconProps) => {
  const cx = 12;
  const cy = 12;
  const rays = Array.from({ length: 12 }, (_, i) => {
    const a = (i * Math.PI) / 6 - Math.PI / 2;
    const inner = 2.4;
    const outer = i % 2 === 0 ? 9 : 6.4;
    return {
      x1: +(cx + inner * Math.cos(a)).toFixed(2),
      y1: +(cy + inner * Math.sin(a)).toFixed(2),
      x2: +(cx + outer * Math.cos(a)).toFixed(2),
      y2: +(cy + outer * Math.sin(a)).toFixed(2),
    };
  });
  return (
    <Svg className={className} strokeWidth={1.7} {...rest}>
      {rays.map((r, i) => (
        <line key={i} x1={r.x1} y1={r.y1} x2={r.x2} y2={r.y2} />
      ))}
    </Svg>
  );
};

export const ChevronRightIcon = (p: IconProps) => (
  <Svg {...p}>
    <path d="M9 6l6 6-6 6" />
  </Svg>
);

export const ChevronDownIcon = (p: IconProps) => (
  <Svg {...p}>
    <path d="M6 9l6 6 6-6" />
  </Svg>
);

export const ChevronUpDownIcon = (p: IconProps) => (
  <Svg {...p}>
    <path d="M8 9l4-4 4 4M8 15l4 4 4-4" />
  </Svg>
);

export const XIcon = (p: IconProps) => (
  <Svg {...p}>
    <path d="M6 6l12 12M18 6L6 18" />
  </Svg>
);

export const SearchIcon = (p: IconProps) => (
  <Svg {...p}>
    <circle cx="11" cy="11" r="6.5" />
    <path d="M20 20l-4-4" />
  </Svg>
);

export const PlusIcon = (p: IconProps) => (
  <Svg {...p}>
    <path d="M12 5v14M5 12h14" />
  </Svg>
);

export const CheckIcon = (p: IconProps) => (
  <Svg {...p}>
    <path d="M5 12.5l4.5 4.5L19 7" />
  </Svg>
);

export const PlayIcon = (p: IconProps) => (
  <Svg {...p} fill="currentColor" stroke="none">
    <path d="M8 5.5v13a1 1 0 0 0 1.53.85l10-6.5a1 1 0 0 0 0-1.7l-10-6.5A1 1 0 0 0 8 5.5z" />
  </Svg>
);

export const RocketIcon = (p: IconProps) => (
  <Svg {...p}>
    <path d="M5 15c-1.5 1.2-2 4.5-2 4.5s3.3-.5 4.5-2c.7-.9.6-2.2-.2-3-.8-.8-2.1-.8-3 .5z" />
    <path d="M9 15l-3-3c1-5 4-8 9-9 0 5-2 9-6 12z" />
    <circle cx="14.5" cy="9.5" r="1.4" />
  </Svg>
);

export const SitemapIcon = (p: IconProps) => (
  <Svg {...p}>
    <rect x="9" y="3" width="6" height="4" rx="1" />
    <rect x="3" y="16" width="6" height="4" rx="1" />
    <rect x="15" y="16" width="6" height="4" rx="1" />
    <path d="M12 7v4M6 16v-2.5a1 1 0 0 1 1-1h10a1 1 0 0 1 1 1V16" />
  </Svg>
);

export const CodeIcon = (p: IconProps) => (
  <Svg {...p}>
    <path d="M9 8l-4 4 4 4M15 8l4 4-4 4" />
  </Svg>
);

export const RefreshIcon = (p: IconProps) => (
  <Svg {...p}>
    <path d="M4 9a8 8 0 0 1 13.5-3L20 8M20 5v3h-3" />
    <path d="M20 15a8 8 0 0 1-13.5 3L4 16M4 19v-3h3" />
  </Svg>
);

export const SettingsIcon = (p: IconProps) => (
  <Svg {...p}>
    <circle cx="12" cy="12" r="3" />
    <path d="M12 3v2.5M12 18.5V21M21 12h-2.5M5.5 12H3M18.4 5.6l-1.8 1.8M7.4 16.6l-1.8 1.8M18.4 18.4l-1.8-1.8M7.4 7.4 5.6 5.6" />
  </Svg>
);

export const SunIcon = (p: IconProps) => (
  <Svg {...p}>
    <circle cx="12" cy="12" r="4" />
    <path d="M12 2v2M12 20v2M4 12H2M22 12h-2M5.6 5.6 4.2 4.2M19.8 19.8l-1.4-1.4M18.4 5.6l1.4-1.4M4.2 19.8l1.4-1.4" />
  </Svg>
);

export const MoonIcon = (p: IconProps) => (
  <Svg {...p}>
    <path d="M20 13.5A8 8 0 1 1 10.5 4a6.5 6.5 0 0 0 9.5 9.5z" />
  </Svg>
);

export const DotIcon = (p: IconProps) => (
  <Svg {...p} fill="currentColor" stroke="none">
    <circle cx="12" cy="12" r="4" />
  </Svg>
);

export const SendIcon = (p: IconProps) => (
  <Svg {...p}>
    <path d="M5 12h13M13 6l6 6-6 6" />
  </Svg>
);

export const LightbulbIcon = (p: IconProps) => (
  <Svg {...p}>
    <path d="M9 18h6M10 21h4" />
    <path d="M12 3a6 6 0 0 0-3.6 10.8c.5.4.9 1 1 1.7l.1.5h5l.1-.5c.1-.7.5-1.3 1-1.7A6 6 0 0 0 12 3z" />
  </Svg>
);

export const HelpIcon = (p: IconProps) => (
  <Svg {...p}>
    <circle cx="12" cy="12" r="9" />
    <path d="M9.5 9.5a2.5 2.5 0 0 1 4.5 1.5c0 1.5-2 1.8-2 3" />
    <path d="M12 17h.01" />
  </Svg>
);

export const SplitIcon = (p: IconProps) => (
  <Svg {...p}>
    <rect x="3" y="4" width="18" height="16" rx="2" />
    <path d="M12 4v16" />
  </Svg>
);

export const CopyIcon = (p: IconProps) => (
  <Svg {...p}>
    <rect x="9" y="9" width="11" height="11" rx="2" />
    <path d="M5 15V5a2 2 0 0 1 2-2h8" />
  </Svg>
);

// A commit graph: a lane with nodes and a branch — the Git/Worktree tool.
export const GitGraphIcon = (p: IconProps) => (
  <Svg {...p}>
    <circle cx="6" cy="6" r="2.2" />
    <circle cx="6" cy="18" r="2.2" />
    <circle cx="17" cy="12" r="2.2" />
    <path d="M6 8.2v7.6M6 12h2.5a3 3 0 0 1 3 3 M14.8 12H8.5" />
  </Svg>
);

// Git compare / diff — two nodes with directional arrows.
export const DiffIcon = (p: IconProps) => (
  <Svg {...p}>
    <circle cx="6" cy="6" r="2.2" />
    <circle cx="18" cy="18" r="2.2" />
    <path d="M6 8.2V14a3 3 0 0 0 3 3h6M14 14l3 3-3 3M18 15.8V10a3 3 0 0 0-3-3H9M10 10 7 7l3-3" />
  </Svg>
);

// ── Repo card marks (discover.icon(): language first, kind as the fallback) ───────────────────
// Shell → TerminalIcon, service → SitemapIcon and repo → FolderIcon are reused above; these are
// the marks the set was missing. src/ui/repoIcon.ts binds each name to its glyph.

// Go — a forward double-chevron.
export const GoIcon = (p: IconProps) => (
  <Svg {...p}>
    <path d="M4.5 7.5 9 12l-4.5 4.5M12.5 7.5 17 12l-4.5 4.5" />
  </Svg>
);

// TypeScript / JavaScript — braces.
export const BracesIcon = (p: IconProps) => (
  <Svg {...p}>
    <path d="M9 4c-1.7 0-2.5.9-2.5 2.6v2.6c0 1.5-.8 2.4-2.2 2.8 1.4.4 2.2 1.3 2.2 2.8v2.6C6.5 19.1 7.3 20 9 20" />
    <path d="M15 4c1.7 0 2.5.9 2.5 2.6v2.6c0 1.5.8 2.4 2.2 2.8-1.4.4-2.2 1.3-2.2 2.8v2.6c0 1.7-.8 2.6-2.5 2.6" />
  </Svg>
);

// Rust — a cog.
export const CogIcon = (p: IconProps) => (
  <Svg {...p}>
    <circle cx="12" cy="12" r="3.2" />
    <path d="M12 2.8v2.6M12 18.6v2.6M21.2 12h-2.6M5.4 12H2.8M18.5 5.5l-1.8 1.8M7.3 16.7l-1.8 1.8M18.5 18.5l-1.8-1.8M7.3 7.3 5.5 5.5" />
  </Svg>
);

// Python — two interlocking curves.
export const PythonIcon = (p: IconProps) => (
  <Svg {...p}>
    <path d="M12 3.5c-3 0-4.5.9-4.5 2.8V9h9v1H5.8C4 10 3 11.4 3 14s1 4 2.8 4h1.7v-2.8c0-1.9 1.5-2.8 4.5-2.8" />
    <path d="M12 20.5c3 0 4.5-.9 4.5-2.8V15h-9v-1h10.7c1.8 0 2.8-1.4 2.8-4s-1-4-2.8-4h-1.7v2.8c0 1.9-1.5 2.8-4.5 2.8" />
  </Svg>
);

// Library / template — a stack of layers.
export const LayersIcon = (p: IconProps) => (
  <Svg {...p}>
    <path d="m12 3 8.5 4.5L12 12 3.5 7.5z" />
    <path d="m3.5 12 8.5 4.5 8.5-4.5M3.5 16.5 12 21l8.5-4.5" />
  </Svg>
);

// ── Capability marks ─────────────────────────────────────────────────────────────────────────

// Mercury — the alchemical mercury sign (☿): horns, orb, cross. The axiom centre.
export const MercuryIcon = (p: IconProps) => (
  <Svg {...p}>
    <path d="M8.5 2.8a4.5 4.5 0 0 0 7 0" />
    <circle cx="12" cy="10.5" r="3.7" />
    <path d="M12 14.2V21M9 18h6" />
  </Svg>
);

// Atlas — an interaction graph: nodes joined by edges.
export const AtlasIcon = (p: IconProps) => (
  <Svg {...p}>
    <circle cx="12" cy="4.8" r="2.2" />
    <circle cx="4.8" cy="17.5" r="2.2" />
    <circle cx="19.2" cy="17.5" r="2.2" />
    <path d="m10.9 6.7-4.8 8.6M13.1 6.7l4.8 8.6M7 17.5h10" />
  </Svg>
);

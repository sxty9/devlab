import type { Branch, Change, Commit, FileContent, FileNode, Repo, RepoData, Stage, Tab, TermLine, Worktree } from '@/types';

// ─────────────────────────────────────────────────────────────────────────────
// Phase-1 mock workspace. Realistic but entirely local — selecting a repo/branch
// swaps these structures. The backend will later replace `REPO_DATA[id]` with live
// git + filesystem + Claude/terminal streams.
// ─────────────────────────────────────────────────────────────────────────────

export const REPOS: Repo[] = [
  { id: 'devlab', name: 'devlab', kind: 'service', description: 'Developer collaboration & in-browser IDE', language: 'TypeScript', tint: 'gpu' },
  { id: 'holistic', name: 'holistic', kind: 'service', description: 'Self-hosting control plane & dashboard', language: 'TS · Python', tint: 'accent' },
  { id: 'sxgate', name: 'sxgate', kind: 'repo', description: 'Cloudflare-tunnel subdomain & preview router', language: 'Shell', tint: 'net' },
  { id: 'holistic-service-template', name: 'holistic-service-template', kind: 'library', description: 'Scaffold for new Holistic services', language: 'Go · TSX', tint: 'ssd' },
  { id: 'bwl-manager', name: 'bwl-manager', kind: 'service', description: 'Business-admin & invoicing workspace', language: 'TypeScript', tint: 'success' },
  { id: 'hostek', name: 'hostek', kind: 'service', description: 'Host metrics & hardware telemetry', language: 'Go', tint: 'ram' },
];

// ── tiny tree/file builders ──────────────────────────────────────────────────
const dir = (id: string, name: string, children: FileNode[]): FileNode => ({ id, name, kind: 'dir', children });
const f = (id: string, name: string, lang: string, status?: FileNode['status']): FileNode => ({
  id,
  name,
  kind: 'file',
  lang,
  ...(status ? { status } : {}),
});
const file = (path: string, lang: string, code: string): FileContent => ({ path, lang, code });
const tab = (path: string, title: string, lang: string, dirty = false): Tab => ({ id: path, title, kind: 'code', path, lang, dirty });
const structureTab = (id: string, title: string): Tab => ({ id, title, kind: 'structure' });

const COMMON_BRANCHES = (feature: string): Branch[] => [
  { name: 'main', isDefault: true, ahead: 0, behind: 0, updated: '3h ago' },
  { name: feature, isDefault: false, ahead: 4, behind: 1, updated: 'just now' },
  { name: 'fix/regression', isDefault: false, ahead: 1, behind: 0, updated: 'yesterday' },
];

// A single-lane (linear) commit log.
function linearLog(rows: { hash: string; message: string; author: string; time: string; refs?: string[] }[]): Commit[] {
  return rows.map((r) => ({ ...r, dotLane: 0, lines: [{ from: 0, to: 0, lane: 0 }] }));
}

const through0 = { from: 0, to: 0, lane: 0 };

// devlab's log: a feature branch (lane 1) that forks from and merges back into main (lane 0).
const devlabCommits: Commit[] = [
  { hash: 'af2e813', message: "Merge branch 'feature/ui-shell'", author: 'nanu', time: '40m ago', refs: ['main', 'HEAD', 'preview'], dotLane: 0, lines: [through0, { from: 1, to: 0, lane: 1 }] },
  { hash: '6b1c9af', message: 'refine pass: Vision tool, modals, pipeline polish', author: 'Claude', time: '1h ago', dotLane: 1, lines: [through0, { from: 1, to: 1, lane: 1 }] },
  { hash: '8309b95', message: 'fix must-fix review findings (state, a11y, tokens)', author: 'Claude', time: '2h ago', refs: ['feature/ui-shell'], dotLane: 1, lines: [through0, { from: 1, to: 1, lane: 1 }] },
  { hash: '5f7e012', message: 'wire Monaco editor + tabs', author: 'nanu', time: '2h ago', dotLane: 1, lines: [through0, { from: 1, to: 1, lane: 1 }] },
  { hash: 'c41a8d3', message: 'scaffold IDE panels & icon rail', author: 'Claude', time: '3h ago', dotLane: 0, lines: [through0, { from: 1, to: 0, lane: 1 }] },
  { hash: 'b90e771', message: 'vendor Holistic Apple-dark design tokens', author: 'nanu', time: '3h ago', dotLane: 0, lines: [through0] },
  { hash: '2ad5e60', message: 'add .sxgate/preview.conf (static mode)', author: 'nanu', time: '4h ago', dotLane: 0, lines: [through0] },
  { hash: '739b036', message: 'DevLab phase 1: initial Vite + React scaffold', author: 'nanu', time: '4h ago', dotLane: 0, lines: [through0] },
];

const devlabWorktrees: Worktree[] = [
  { branch: 'main', note: 'this window · /home/nanu/devlab', current: true },
  { branch: 'preview', note: 'static preview · sxgate', url: 'https://preview-devlab.henrysoase.org' },
];

// ─────────────────────────────────────────────────────────────────────────────
// devlab (self-referential — the repo you are looking at)
// ─────────────────────────────────────────────────────────────────────────────
const devlabTree: FileNode[] = [
  dir('src', 'src', [
    dir('src/theme', 'theme', [
      f('src/theme/tokens.css', 'tokens.css', 'css'),
      f('src/theme/tailwind-preset.ts', 'tailwind-preset.ts', 'typescript'),
    ]),
    dir('src/shell', 'shell', [
      f('src/shell/TopBar.tsx', 'TopBar.tsx', 'typescript', 'modified'),
      f('src/shell/IconRail.tsx', 'IconRail.tsx', 'typescript'),
      f('src/shell/PanelHost.tsx', 'PanelHost.tsx', 'typescript'),
      f('src/shell/StatusBar.tsx', 'StatusBar.tsx', 'typescript'),
    ]),
    dir('src/panels', 'panels', [
      f('src/panels/ProjectPanel.tsx', 'ProjectPanel.tsx', 'typescript'),
      f('src/panels/VersionControlPanel.tsx', 'VersionControlPanel.tsx', 'typescript'),
      f('src/panels/ClaudePanel.tsx', 'ClaudePanel.tsx', 'typescript'),
      f('src/panels/TerminalPanel.tsx', 'TerminalPanel.tsx', 'typescript'),
    ]),
    dir('src/main', 'main', [
      f('src/main/MainArea.tsx', 'MainArea.tsx', 'typescript'),
      f('src/main/EditorView.tsx', 'EditorView.tsx', 'typescript', 'added'),
    ]),
    f('src/App.tsx', 'App.tsx', 'typescript', 'modified'),
    f('src/types.ts', 'types.ts', 'typescript'),
    f('src/main.tsx', 'main.tsx', 'typescript'),
  ]),
  dir('.sxgate', '.sxgate', [f('.sxgate/preview.conf', 'preview.conf', 'shell', 'added')]),
  f('package.json', 'package.json', 'json'),
  f('vite.config.ts', 'vite.config.ts', 'typescript'),
  f('README.md', 'README.md', 'markdown'),
];

const devlabFiles: Record<string, FileContent> = {
  'src/App.tsx': file(
    'src/App.tsx',
    'typescript',
    `import { WorkspaceProvider } from '@/state/workspace';
import { TopBar } from '@/shell/TopBar';
import { IconRail } from '@/shell/IconRail';
import { PanelHost } from '@/shell/PanelHost';
import { MainArea } from '@/main/MainArea';
import { StatusBar } from '@/shell/StatusBar';

/** The DevLab IDE shell: top bar, left rail + panel column, editor, pipeline bar. */
export default function App() {
  return (
    <WorkspaceProvider>
      <div className="flex h-full flex-col bg-bg-base text-text-primary">
        <TopBar />
        <div className="flex min-h-0 flex-1">
          <IconRail />
          <PanelHost />
          <MainArea />
        </div>
        <StatusBar />
      </div>
    </WorkspaceProvider>
  );
}
`,
  ),
  'src/shell/TopBar.tsx': file(
    'src/shell/TopBar.tsx',
    'typescript',
    `import { useWorkspace } from '@/state/workspace';
import { RepoDropdown } from './RepoDropdown';
import { BranchDropdown } from './BranchDropdown';

/** Brand lockup + repository picker + branch/session picker + right-side actions. */
export function TopBar() {
  const { activeRepo } = useWorkspace();
  return (
    <header className="flex h-12 items-center gap-3 border-b border-separator bg-material-regular px-3
      [backdrop-filter:var(--material-blur)]">
      <Brand />
      <RepoDropdown />
      <span className="text-text-tertiary">/</span>
      <BranchDropdown />
      <div className="ml-auto flex items-center gap-1.5" aria-label={activeRepo.name} />
    </header>
  );
}
`,
  ),
  'src/types.ts': file(
    'src/types.ts',
    'typescript',
    `export type PanelId = 'project' | 'vcs' | 'claude' | 'terminal';

export interface Repo {
  id: string;
  name: string;
  kind: 'service' | 'repo' | 'library';
  description: string;
  language: string;
}
// …shared domain types for the DevLab workspace (see full file).
`,
  ),
  '.sxgate/preview.conf': file(
    '.sxgate/preview.conf',
    'shell',
    `# sxgate preview manifest for DevLab — UI-only static SPA (no backend yet).
SERVICE="devlab"
BUILD="cd {worktree} && CI=true npm install --silent && npm run build"
MODE="static"
ROOT="dist"
`,
  ),
  'README.md': file(
    'README.md',
    'markdown',
    `# DevLab

Developer collaboration & in-browser IDE for the Holistic ecosystem — develop,
maintain, and ship (preview → prod) Holistic services from one place.

## Status
Phase 1 — UI shell (mock data, no backend). Preview-deployed via sxgate.
`,
  ),
  'package.json': file(
    'package.json',
    'json',
    `{
  "name": "@devlab/app",
  "private": true,
  "type": "module",
  "scripts": { "dev": "vite", "build": "tsc -p tsconfig.json --noEmit && vite build" },
  "dependencies": {
    "@monaco-editor/react": "^4.7.0",
    "react": "^18.3.1",
    "react-dom": "^18.3.1"
  }
}
`,
  ),
};

const devlabData: RepoData = {
  branches: COMMON_BRANCHES('feature/ui-shell'),
  tree: devlabTree,
  files: devlabFiles,
  changes: [
    { path: 'src/App.tsx', status: 'modified', additions: 18, deletions: 4, staged: true },
    { path: 'src/main/EditorView.tsx', status: 'added', additions: 96, deletions: 0, staged: true },
    { path: '.sxgate/preview.conf', status: 'added', additions: 6, deletions: 0, staged: false },
    { path: 'src/shell/TopBar.tsx', status: 'modified', additions: 7, deletions: 2, staged: false },
  ],
  commits: devlabCommits,
  worktrees: devlabWorktrees,
  diffBefore: {
    'src/App.tsx': `import { WorkspaceProvider } from '@/state/workspace';
import { IconRail } from '@/shell/IconRail';
import { PanelHost } from '@/shell/PanelHost';
import { MainArea } from '@/main/MainArea';

export default function App() {
  return (
    <WorkspaceProvider>
      <div className="flex h-full">
        <IconRail />
        <PanelHost />
        <MainArea />
      </div>
    </WorkspaceProvider>
  );
}
`,
  },
  vision: [
    { id: 'v1', title: 'DevLab vision & sketches', kind: 'spec', summary: 'IntelliJ/VS-Code-style in-browser IDE: repo dropdown, left tools, editor, delivery pipeline.', state: 'done', updated: '2h ago' },
    { id: 'v2', title: 'Pipeline: Vision → Code → Preview → Delivery → main', kind: 'mindmap', summary: 'How a service flows from idea to prod through sxgate previews.', state: 'active', updated: '1h ago' },
    { id: 'v3', title: 'Repo dropdown like IntelliJ', kind: 'jet', summary: 'One-click switch of the active repo/service from the top bar.', state: 'done', updated: '3h ago' },
    { id: 'v4', title: 'Claude + terminal as first-class tools', kind: 'jet', summary: 'Drive each repo with a Claude session and a real shell, side by side.', state: 'active', updated: '40m ago' },
    { id: 'v5', title: 'Multi-developer sessions on a branch', kind: 'note', summary: 'Later: collaborate on the same branch/session in real time.', state: 'pending', updated: 'yesterday' },
  ],
  claude: [
    { id: 'c1', role: 'user', text: 'Scaffold the IDE shell and vendor the Holistic dark tokens.', ts: '10:24' },
    { id: 'c2', role: 'assistant', text: 'On it. I vendored tokens.css + the Tailwind preset into src/theme and wired a VS-Code-style layout: top bar with repo/branch pickers, a 56px icon rail, a resizable panel column and a Monaco editor.', ts: '10:24' },
    { id: 'c3', role: 'tool', tool: 'Write', text: 'src/App.tsx · src/shell/TopBar.tsx · 11 more files', ts: '10:25' },
    { id: 'c4', role: 'assistant', text: 'Build is green and dist/ is produced. Ready to preview-deploy with `sxgate preview up main`. Want the pipeline bar wired to live sxgate status next?', ts: '10:31' },
  ],
  terminal: [
    { id: 't1', kind: 'cmd', text: 'npm run build' },
    { id: 't2', kind: 'stdout', text: 'vite v5.4.9 building for production...' },
    { id: 't3', kind: 'stdout', text: '✓ 214 modules transformed.' },
    { id: 't4', kind: 'stdout', text: 'dist/index.html                  0.71 kB' },
    { id: 't5', kind: 'stdout', text: 'dist/assets/index-8f2a1c.css    18.4 kB │ gzip:  4.6 kB' },
    { id: 't6', kind: 'stdout', text: 'dist/assets/index-b91e07.js    241.7 kB │ gzip: 78.2 kB' },
    { id: 't7', kind: 'stdout', text: '✓ built in 1.42s' },
    { id: 't8', kind: 'cmd', text: 'sudo sxgate preview up main' },
    { id: 't9', kind: 'system', text: '→ https://main-devlab.henrysoase.org' },
  ],
  stages: [
    { id: 'vision', label: 'Vision', state: 'done', hint: 'Sketches & spec captured' },
    { id: 'code', label: 'Code', state: 'active', hint: 'UI shell in progress' },
    { id: 'preview', label: 'Preview', state: 'active', hint: 'main-devlab.henrysoase.org' },
    { id: 'delivery', label: 'Delivery', state: 'pending', hint: 'Prod test pending' },
    { id: 'merge', label: 'main', state: 'pending', hint: 'Awaiting merge' },
  ],
  defaultTabs: [tab('src/App.tsx', 'App.tsx', 'typescript', true), structureTab('structure:devlab', 'devlab — structure')],
  activeTabId: 'src/App.tsx',
  structure: [
    {
      title: 'Frontend (Vite SPA)',
      hint: 'React 18 · TypeScript · Tailwind (vendored Holistic tokens)',
      entries: [
        { name: 'src/shell', kind: 'dir', note: 'Top bar, icon rail, panel host, status/pipeline bar' },
        { name: 'src/panels', kind: 'dir', note: 'Project · Version Control · Claude · Terminal' },
        { name: 'src/main', kind: 'dir', note: 'Editor tabs + Monaco + structure view' },
        { name: 'src/theme', kind: 'dir', note: 'Vendored Apple-dark tokens & Tailwind preset' },
      ],
    },
    {
      title: 'Delivery',
      hint: 'Preview-deployed via sxgate; backend lands in phase 2',
      entries: [
        { name: '.sxgate/preview.conf', kind: 'file', note: 'MODE="static" → main-devlab.henrysoase.org' },
        { name: 'backend/ (planned)', kind: 'dir', note: 'Go API: repos, git, Claude & terminal sessions' },
      ],
    },
  ],
};

// ─────────────────────────────────────────────────────────────────────────────
// holistic
// ─────────────────────────────────────────────────────────────────────────────
const holisticTree: FileNode[] = [
  dir('frontend', 'frontend', [
    dir('frontend/app', 'app', [
      f('frontend/app/src/main.tsx', 'main.tsx', 'typescript'),
      f('frontend/app/vite.config.ts', 'vite.config.ts', 'typescript'),
    ]),
    dir('frontend/packages', 'packages', [
      dir('frontend/packages/ui', 'ui', [
        f('frontend/packages/ui/src/tokens.css', 'tokens.css', 'css'),
        f('frontend/packages/ui/src/index.ts', 'index.ts', 'typescript'),
      ]),
    ]),
  ]),
  dir('services', 'services', [
    dir('services/dashboard', 'dashboard', [f('services/dashboard/main.py', 'main.py', 'python', 'modified')]),
    dir('services/samba', 'samba', [f('services/samba/main.py', 'main.py', 'python')]),
  ]),
  dir('.sxgate', '.sxgate', [f('.sxgate/preview.conf', 'preview.conf', 'shell')]),
  f('README.md', 'README.md', 'markdown'),
];

const holisticData: RepoData = {
  branches: COMMON_BRANCHES('feat/profile-settings'),
  tree: holisticTree,
  files: {
    'services/dashboard/main.py': file(
      'services/dashboard/main.py',
      'python',
      `from fastapi import FastAPI, Depends
from holistic_api.auth import current_user

app = FastAPI(title="Holistic Dashboard API")


@app.get("/api/health")
def health() -> dict[str, str]:
    return {"status": "ok"}


@app.get("/api/me")
def me(user=Depends(current_user)) -> dict:
    return {"id": user.id, "name": user.name, "admin": user.is_admin}
`,
    ),
    '.sxgate/preview.conf': file(
      '.sxgate/preview.conf',
      'shell',
      `SERVICE="holistic"
BUILD="cd {worktree}/frontend && CI=true pnpm install --silent && pnpm --filter @holistic/app build"
MODE="static_proxy"
ROOT="frontend/app/dist"
API_PREFIX="/api"
RUN="{worktree}/.venv/bin/uvicorn holistic_api.main:app --host 127.0.0.1 --port {port}"
HEALTHCHECK="/api/health"
`,
    ),
    'README.md': file('README.md', 'markdown', `# Holistic\n\nSelf-hosting control plane: dashboard, auth, services.\n`),
  },
  changes: [
    { path: 'services/dashboard/main.py', status: 'modified', additions: 24, deletions: 6, staged: false },
    { path: 'frontend/packages/ui/src/tokens.css', status: 'modified', additions: 3, deletions: 1, staged: false },
  ],
  commits: linearLog([
    { hash: 'd1a7c40', message: 'feat(dashboard): add /api/me endpoint', author: 'nanu', time: '1h ago', refs: ['feat/profile-settings', 'HEAD'] },
    { hash: 'a02f6b8', message: 'ui: tweak surface tokens for profile cards', author: 'Claude', time: '2h ago' },
    { hash: '77c0e51', message: 'auth: share current_user across services', author: 'nanu', time: 'yesterday', refs: ['main'] },
    { hash: '3e9b14d', message: 'samba: file-share management service', author: 'nanu', time: '2d ago' },
    { hash: 'f5102aa', message: 'frontend: scaffold @holistic/ui design system', author: 'nanu', time: '4d ago' },
  ]),
  worktrees: [{ branch: 'main', note: 'this window', current: true }, { branch: 'feat/profile-settings', note: 'sandbox · sxgate', url: 'https://feat-profile-holistic.henrysoase.org' }],
  vision: [
    { id: 'v1', title: 'Profile & settings page', kind: 'spec', summary: 'Self-serve profile, avatar, and notification settings in the dashboard.', state: 'active', updated: '1h ago' },
    { id: 'v2', title: 'Auth & service map', kind: 'mindmap', summary: 'How dashboard, samba and manifest services share one auth surface.', state: 'done', updated: 'yesterday' },
    { id: 'v3', title: 'Per-service health badges', kind: 'jet', summary: 'Surface /api/health for each mounted service on the home screen.', state: 'pending', updated: '2d ago' },
  ],
  claude: [
    { id: 'c1', role: 'user', text: 'Add a /api/me endpoint gated by current_user.', ts: '09:02' },
    { id: 'c2', role: 'assistant', text: 'Added it to services/dashboard/main.py with a Depends(current_user) guard and a matching health check. Tests pass.', ts: '09:03' },
  ],
  terminal: [
    { id: 't1', kind: 'cmd', text: 'pnpm --filter @holistic/app build' },
    { id: 't2', kind: 'stdout', text: '✓ built in 2.1s' },
    { id: 't3', kind: 'cmd', text: 'pytest services/dashboard -q' },
    { id: 't4', kind: 'stdout', text: '14 passed in 0.83s' },
  ],
  stages: [
    { id: 'vision', label: 'Vision', state: 'done', hint: 'Profile settings spec' },
    { id: 'code', label: 'Code', state: 'done', hint: 'Implemented' },
    { id: 'preview', label: 'Preview', state: 'active', hint: 'feat-profile-settings-holistic…' },
    { id: 'delivery', label: 'Delivery', state: 'pending', hint: 'Prod test pending' },
    { id: 'merge', label: 'main', state: 'pending', hint: 'Awaiting merge' },
  ],
  defaultTabs: [tab('services/dashboard/main.py', 'main.py', 'python'), structureTab('structure:holistic', 'holistic — structure')],
  activeTabId: 'services/dashboard/main.py',
  structure: [
    {
      title: 'frontend/ (pnpm monorepo)',
      hint: 'Vite SPA + shared @holistic/ui design system',
      entries: [
        { name: 'frontend/app', kind: 'dir', note: 'The Holistic single-page app' },
        { name: 'frontend/packages/ui', kind: 'dir', note: 'Apple-dark design system & components' },
      ],
    },
    {
      title: 'services/ (FastAPI)',
      hint: 'Per-service Python backends mounted under /api',
      entries: [
        { name: 'services/dashboard', kind: 'dir', note: 'Auth, profile, control plane' },
        { name: 'services/samba', kind: 'dir', note: 'File-share management' },
      ],
    },
  ],
};

// ─────────────────────────────────────────────────────────────────────────────
// sxgate
// ─────────────────────────────────────────────────────────────────────────────
const sxgateTree: FileNode[] = [
  f('sxgate', 'sxgate', 'shell', 'modified'),
  dir('lib', 'lib', [f('lib/preview.sh', 'preview.sh', 'shell', 'modified')]),
  dir('cloudflared', 'cloudflared', [f('cloudflared/config.yml.example', 'config.yml.example', 'yaml')]),
  dir('docs', 'docs', [f('docs/cli.md', 'cli.md', 'markdown'), f('docs/architecture.md', 'architecture.md', 'markdown')]),
  dir('tests', 'tests', [f('tests/run.sh', 'run.sh', 'shell')]),
  f('README.md', 'README.md', 'markdown'),
];

const sxgateData: RepoData = {
  branches: COMMON_BRANCHES('feat/preview-static-mode'),
  tree: sxgateTree,
  files: {
    'lib/preview.sh': file(
      'lib/preview.sh',
      'shell',
      `#!/usr/bin/env bash
# sxgate preview engine — per-branch sandboxes behind a single wildcard ingress.
set -euo pipefail

preview_up() {
  local branch="$1" slug
  slug="$(slugify "$branch")"
  source_manifest "$PWD/.sxgate/preview.conf"
  build_worktree "$slug"
  case "$MODE" in
    static)        serve_static "$slug" "$ROOT" ;;
    static_proxy)  serve_static_proxy "$slug" "$ROOT" "$API_PREFIX" ;;
    proxy)         serve_proxy "$slug" ;;
  esac
  echo "→ https://\${slug}-\${SERVICE}.\${MANAGED_ZONE}"
}
`,
    ),
    'README.md': file('README.md', 'markdown', `# sxgate\n\nSubdomain↔service routing for a Cloudflare tunnel, plus per-branch preview sandboxes.\n`),
  },
  changes: [
    { path: 'sxgate', status: 'modified', additions: 12, deletions: 3, staged: false },
    { path: 'lib/preview.sh', status: 'modified', additions: 31, deletions: 8, staged: true },
  ],
  commits: linearLog([
    { hash: 'e7b3019', message: 'feat(preview): add MODE="static" path', author: 'nanu', time: '20m ago', refs: ['feat/preview-static-mode', 'HEAD'] },
    { hash: '9c4f2ab', message: 'preview: per-branch teardown safety', author: 'nanu', time: '3d ago', refs: ['main'] },
    { hash: '4d80e15', message: 'route: validate hostnames against managed zone', author: 'nanu', time: '5d ago' },
    { hash: 'b22a7f9', message: 'init: cloudflared tunnel scaffold', author: 'nanu', time: '1w ago' },
  ]),
  worktrees: [{ branch: 'main', note: 'this window', current: true }],
  vision: [
    { id: 'v1', title: 'static preview mode (no backend)', kind: 'spec', summary: 'Serve a pure dist/ SPA with SPA fallback — what DevLab uses.', state: 'active', updated: '30m ago' },
    { id: 'v2', title: 'Wildcard ingress → dispatcher', kind: 'mindmap', summary: 'One *.<zone> ingress to a local Caddy that fans out per-branch.', state: 'done', updated: '3d ago' },
    { id: 'v3', title: 'Per-branch teardown safety', kind: 'note', summary: 'down removes worktree + route + instance atomically.', state: 'done', updated: '3d ago' },
  ],
  claude: [
    { id: 'c1', role: 'user', text: 'Make sure MODE="static" works without a backend instance.', ts: '14:11' },
    { id: 'c2', role: 'assistant', text: 'Added a serve_static path that skips the systemd backend unit entirely and just points the dispatcher vhost at dist/ with SPA fallback. DevLab can use it as-is.', ts: '14:12' },
  ],
  terminal: [
    { id: 't1', kind: 'cmd', text: 'sudo ./sxgate preview ls' },
    { id: 't2', kind: 'stdout', text: 'SLUG                       SERVICE   MODE          STATE' },
    { id: 't3', kind: 'stdout', text: 'main-devlab                devlab    static        up' },
    { id: 't4', kind: 'stdout', text: 'feat-profile-holistic      holistic  static_proxy  up' },
  ],
  stages: [
    { id: 'vision', label: 'Vision', state: 'done', hint: 'static mode RFC' },
    { id: 'code', label: 'Code', state: 'active', hint: 'serve_static path' },
    { id: 'preview', label: 'Preview', state: 'pending', hint: 'n/a (CLI tool)' },
    { id: 'delivery', label: 'Delivery', state: 'pending', hint: 'tests/run.sh' },
    { id: 'merge', label: 'main', state: 'pending', hint: 'Awaiting merge' },
  ],
  defaultTabs: [tab('lib/preview.sh', 'preview.sh', 'shell', true), structureTab('structure:sxgate', 'sxgate — structure')],
  activeTabId: 'lib/preview.sh',
  structure: [
    {
      title: 'CLI',
      hint: 'A single bash entrypoint + library',
      entries: [
        { name: 'sxgate', kind: 'file', note: 'Routing CLI: service/route/zone/preview' },
        { name: 'lib/preview.sh', kind: 'file', note: 'Per-branch preview sandbox engine' },
      ],
    },
    {
      title: 'Docs',
      hint: 'Mental model & runbooks',
      entries: [
        { name: 'docs/cli.md', kind: 'file', note: 'Command reference' },
        { name: 'docs/architecture.md', kind: 'file', note: 'Tunnel + dispatcher design' },
      ],
    },
  ],
};

// ── minimal data for the remaining repos (still feels real) ───────────────────
function minimalRepo(opts: {
  feature: string;
  tree: FileNode[];
  files: Record<string, FileContent>;
  activePath: string;
  activeLang: string;
  activeTitle: string;
  changes: Change[];
  claudeAsk: string;
  claudeReply: string;
  term: TermLine[];
  structureTitle: string;
}): RepoData {
  const stages: Stage[] = [
    { id: 'vision', label: 'Vision', state: 'done', hint: 'Captured' },
    { id: 'code', label: 'Code', state: 'active', hint: 'In progress' },
    { id: 'preview', label: 'Preview', state: 'pending', hint: 'Not deployed' },
    { id: 'delivery', label: 'Delivery', state: 'pending', hint: 'Pending' },
    { id: 'merge', label: 'main', state: 'pending', hint: 'Awaiting merge' },
  ];
  return {
    branches: COMMON_BRANCHES(opts.feature),
    tree: opts.tree,
    files: opts.files,
    changes: opts.changes,
    commits: linearLog([
      { hash: 'aa11bb2', message: opts.claudeAsk.replace(/\.$/, ''), author: 'nanu', time: '1h ago', refs: [opts.feature, 'HEAD'] },
      { hash: 'cc33dd4', message: 'wire the feature into the app shell', author: 'Claude', time: '3h ago' },
      { hash: 'ee55ff6', message: 'initial scaffold', author: 'nanu', time: '2d ago', refs: ['main'] },
    ]),
    worktrees: [{ branch: 'main', note: 'this window', current: true }],
    vision: [
      { id: 'v1', title: opts.claudeAsk.replace(/\.$/, ''), kind: 'spec', summary: opts.claudeReply, state: 'active', updated: '1h ago' },
      { id: 'v2', title: `${opts.feature.split('/').pop()} plan`, kind: 'note', summary: 'Scoped from the feature branch; details captured here.', state: 'pending', updated: 'yesterday' },
    ],
    claude: [
      { id: 'c1', role: 'user', text: opts.claudeAsk, ts: '11:40' },
      { id: 'c2', role: 'assistant', text: opts.claudeReply, ts: '11:41' },
      { id: 'c3', role: 'tool', tool: 'Edit', text: opts.activePath, ts: '11:41' },
      { id: 'c4', role: 'assistant', text: 'Done — build is green. Want me to open a branch preview?', ts: '11:42' },
    ],
    terminal: opts.term,
    stages,
    defaultTabs: [tab(opts.activePath, opts.activeTitle, opts.activeLang), structureTab(`structure:${opts.feature}`, opts.structureTitle)],
    activeTabId: opts.activePath,
    structure: [
      {
        title: 'Overview',
        hint: 'Pick a file in the Project panel to start editing',
        entries: opts.tree
          .filter((n) => n.kind === 'dir')
          .map((n) => ({ name: n.name, kind: 'dir' as const, note: 'module' })),
      },
    ],
  };
}

const templateData = minimalRepo({
  feature: 'feat/add-metrics-endpoint',
  tree: [
    dir('backend', 'backend', [
      dir('backend/cmd', 'cmd', [f('backend/cmd/main.go', 'main.go', 'go')]),
      dir('backend/internal', 'internal', [f('backend/internal/server.go', 'server.go', 'go', 'modified')]),
      f('backend/go.mod', 'go.mod', 'go'),
    ]),
    dir('ui', 'ui', [f('ui/Dashboard.tsx', 'Dashboard.tsx', 'typescript'), f('ui/index.tsx', 'index.tsx', 'typescript')]),
    dir('permissions', 'permissions', [f('permissions/myservice.json', 'myservice.json', 'json')]),
    f('CLAUDE.md', 'CLAUDE.md', 'markdown'),
  ],
  files: {
    'backend/internal/server.go': file(
      'backend/internal/server.go',
      'go',
      `package internal

import "net/http"

// NewServer returns the service's HTTP handler. Mount under /api by the host shell.
func NewServer() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/health", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(` + '`{"status":"ok"}`' + `))
	})
	return mux
}
`,
    ),
  },
  activePath: 'backend/internal/server.go',
  activeLang: 'go',
  activeTitle: 'server.go',
  changes: [{ path: 'backend/internal/server.go', status: 'modified', additions: 9, deletions: 0, staged: false }],
  claudeAsk: 'Add a /api/metrics endpoint to the template.',
  claudeReply: 'Added a metrics handler next to /api/health in backend/internal/server.go and a matching permission entry.',
  term: [
    { id: 't1', kind: 'cmd', text: 'go test ./...' },
    { id: 't2', kind: 'stdout', text: 'ok  	holistic-service-template/backend/internal	0.01s' },
  ],
  structureTitle: 'template — structure',
});

const bwlData = minimalRepo({
  feature: 'feat/invoice-pdf',
  tree: [
    dir('src', 'src', [
      f('src/App.tsx', 'App.tsx', 'typescript', 'modified'),
      dir('src/invoices', 'invoices', [f('src/invoices/InvoiceList.tsx', 'InvoiceList.tsx', 'typescript')]),
    ]),
    f('package.json', 'package.json', 'json'),
  ],
  files: {
    'src/App.tsx': file(
      'src/App.tsx',
      'typescript',
      `import { InvoiceList } from './invoices/InvoiceList';

export default function App() {
  return (
    <main className="mx-auto max-w-5xl p-6">
      <h1 className="text-title2">BWL Manager</h1>
      <InvoiceList />
    </main>
  );
}
`,
    ),
  },
  activePath: 'src/App.tsx',
  activeLang: 'typescript',
  activeTitle: 'App.tsx',
  changes: [{ path: 'src/App.tsx', status: 'modified', additions: 14, deletions: 2, staged: true }],
  claudeAsk: 'Wire the invoice list into the app shell.',
  claudeReply: 'Mounted <InvoiceList/> in src/App.tsx inside a centered container.',
  term: [
    { id: 't1', kind: 'cmd', text: 'pnpm build' },
    { id: 't2', kind: 'stdout', text: '✓ built in 1.9s' },
  ],
  structureTitle: 'bwl-manager — structure',
});

const hostekData = minimalRepo({
  feature: 'feat/gpu-temps',
  tree: [
    dir('cmd', 'cmd', [f('cmd/hostek/main.go', 'main.go', 'go')]),
    dir('internal', 'internal', [f('internal/collector.go', 'collector.go', 'go', 'modified')]),
    f('go.mod', 'go.mod', 'go'),
  ],
  files: {
    'internal/collector.go': file(
      'internal/collector.go',
      'go',
      `package internal

// Sample collects one snapshot of host hardware metrics.
type Sample struct {
	CPU float64 ` + '`json:"cpu"`' + `
	RAM float64 ` + '`json:"ram"`' + `
	GPU float64 ` + '`json:"gpu"`' + `
}

func Collect() (Sample, error) {
	return Sample{CPU: cpuPercent(), RAM: ramPercent(), GPU: gpuPercent()}, nil
}
`,
    ),
  },
  activePath: 'internal/collector.go',
  activeLang: 'go',
  activeTitle: 'collector.go',
  changes: [{ path: 'internal/collector.go', status: 'modified', additions: 22, deletions: 5, staged: false }],
  claudeAsk: 'Add GPU temperature to the metrics sample.',
  claudeReply: 'Added a GPU field to Sample and wired gpuPercent() into Collect().',
  term: [
    { id: 't1', kind: 'cmd', text: 'go run ./cmd/hostek --once' },
    { id: 't2', kind: 'stdout', text: 'cpu=12.4%  ram=38.1%  gpu=7.0%' },
  ],
  structureTitle: 'hostek — structure',
});

export const REPO_DATA: Record<string, RepoData> = {
  devlab: devlabData,
  holistic: holisticData,
  sxgate: sxgateData,
  'holistic-service-template': templateData,
  'bwl-manager': bwlData,
  hostek: hostekData,
};

export const DEFAULT_REPO_ID = 'devlab';

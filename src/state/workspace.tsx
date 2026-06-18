import { createContext, useCallback, useContext, useEffect, useMemo, useRef, useState, type ReactNode } from 'react';
import type { Branch, EditorSettings, FileContent, FileNode, Overlay, PanelId, Repo, RepoData, Tab } from '@/types';
import { DEFAULT_REPO_ID, REPOS, REPO_DATA } from '@/mock/workspace';
import { guessLang } from '@/lib/lang';

/** Fabricate a believable "before" for a modified file with no explicit diff content. */
function synthBefore(after: string): string {
  const lines = after.split('\n');
  if (lines.length <= 5) return lines.slice(0, Math.max(1, lines.length - 1)).join('\n');
  const out = lines.slice();
  const mid = Math.floor(out.length / 2);
  out.splice(mid, 2); // these two lines read as additions in the "after"
  const tweak = Math.min(3, out.length - 1);
  out[tweak] = `${out[tweak]}  // (previous)`; // one modified line
  return out.join('\n');
}

/** Last-resort branch so an empty `branches` array (future backend) never crashes the shell. */
const FALLBACK_BRANCH: Branch = { name: 'main', isDefault: true, ahead: 0, behind: 0, updated: '' };

const defaultBranchName = (d: RepoData) => d.branches.find((b) => b.isDefault)?.name ?? d.branches[0]?.name ?? FALLBACK_BRANCH.name;

/** Pick the activeTabId for a repo, guarding against a default that isn't actually in its tabs. */
const initialTabId = (d: RepoData) =>
  d.defaultTabs.some((t) => t.id === d.activeTabId) ? d.activeTabId : (d.defaultTabs[0]?.id ?? null);

const DEFAULT_SETTINGS: EditorSettings = { fontSize: 13, tabSize: 2 };

function readSettings(): EditorSettings {
  try {
    const raw = localStorage.getItem('dl.settings');
    if (raw) return { ...DEFAULT_SETTINGS, ...(JSON.parse(raw) as Partial<EditorSettings>) };
  } catch {
    /* ignore */
  }
  return DEFAULT_SETTINGS;
}

interface WorkspaceContextValue {
  repos: Repo[];
  activeRepo: Repo;
  data: RepoData;
  setRepo: (id: string) => void;

  branches: Branch[];
  activeBranch: Branch;
  setBranch: (name: string) => void;

  /** null = the panel column is collapsed. */
  activePanel: PanelId | null;
  togglePanel: (id: PanelId) => void;
  setPanel: (id: PanelId) => void;
  /** Collapse/expand the panel column, remembering the last tool (⌘/Ctrl+B). */
  toggleColumn: () => void;

  openTabs: Tab[];
  activeTabId: string | null;
  openFile: (node: Pick<FileNode, 'id' | 'name' | 'lang'>) => void;
  openStructure: () => void;
  /** Open a side-by-side diff for a changed file. */
  openDiff: (path: string) => void;
  setActiveTab: (id: string) => void;
  closeTab: (id: string) => void;

  fileContent: (path: string) => FileContent;
  /** Before/after content + language for a changed file's diff view. */
  fileDiff: (path: string) => { before: string; after: string; lang: string };

  settings: EditorSettings;
  updateSettings: (patch: Partial<EditorSettings>) => void;

  overlay: Overlay;
  setOverlay: (o: Overlay) => void;
}

const WorkspaceContext = createContext<WorkspaceContextValue | null>(null);

/** Flatten a file tree into a path→node map for quick lookups. */
function indexTree(nodes: FileNode[], acc: Record<string, FileNode> = {}): Record<string, FileNode> {
  for (const n of nodes) {
    acc[n.id] = n;
    if (n.children) indexTree(n.children, acc);
  }
  return acc;
}

/** A believable placeholder for tree files that have no explicit mock contents. */
function stubContent(path: string, lang: string): FileContent {
  const name = path.split('/').pop() ?? path;
  const code = `// ${path}\n// (preview) — open in DevLab.\n//\n// Phase 1 ships the IDE shell with mock data; file contents arrive with the backend.\n// "${name}" is a ${lang || 'text'} file in this repo's working tree.\n`;
  return { path, lang: lang || 'plaintext', code };
}

export function WorkspaceProvider({ children }: { children: ReactNode }) {
  const [activeRepoId, setActiveRepoId] = useState(DEFAULT_REPO_ID);
  const data = REPO_DATA[activeRepoId];

  const [activeBranchName, setActiveBranchName] = useState(() => defaultBranchName(data));
  const [activePanel, setActivePanel] = useState<PanelId | null>('project');
  const [openTabs, setOpenTabs] = useState<Tab[]>(() => data.defaultTabs);
  const [activeTabId, setActiveTabId] = useState<string | null>(() => initialTabId(data));

  const fileIndex = useMemo(() => indexTree(data.tree), [data.tree]);

  // Remember the last-used branch per repo so switching away and back doesn't snap to default.
  const branchMemory = useRef<Record<string, string>>({ [DEFAULT_REPO_ID]: activeBranchName });

  const setRepo = useCallback((id: string) => {
    const next = REPO_DATA[id];
    if (!next) return;
    const remembered = branchMemory.current[id];
    const branch = remembered && next.branches.some((b) => b.name === remembered) ? remembered : defaultBranchName(next);
    setActiveRepoId(id);
    setActiveBranchName(branch);
    setOpenTabs(next.defaultTabs);
    setActiveTabId(initialTabId(next));
  }, []);

  const setBranch = useCallback(
    (name: string) => {
      branchMemory.current[activeRepoId] = name;
      setActiveBranchName(name);
    },
    [activeRepoId],
  );

  const lastPanelRef = useRef<PanelId>('project');
  const togglePanel = useCallback(
    (id: PanelId) =>
      setActivePanel((cur) => {
        if (cur === id) return null;
        lastPanelRef.current = id;
        return id;
      }),
    [],
  );
  const setPanel = useCallback((id: PanelId) => {
    lastPanelRef.current = id;
    setActivePanel(id);
  }, []);
  const toggleColumn = useCallback(() => setActivePanel((cur) => (cur ? null : lastPanelRef.current)), []);

  const [settings, setSettings] = useState<EditorSettings>(readSettings);
  const updateSettings = useCallback((patch: Partial<EditorSettings>) => {
    setSettings((s) => {
      const next = { ...s, ...patch };
      try {
        localStorage.setItem('dl.settings', JSON.stringify(next));
      } catch {
        /* ignore */
      }
      return next;
    });
  }, []);

  const [overlay, setOverlay] = useState<Overlay>(null);

  // Global shortcuts: ⌘/Ctrl+B toggles the panel column; ? opens the shortcuts help.
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      const mod = e.metaKey || e.ctrlKey;
      if (mod && (e.key === 'b' || e.key === 'B')) {
        e.preventDefault();
        toggleColumn();
      } else if (e.key === '?') {
        const t = e.target as HTMLElement | null;
        if (t && (t.tagName === 'INPUT' || t.tagName === 'TEXTAREA' || t.isContentEditable)) return;
        e.preventDefault();
        setOverlay('help');
      }
    };
    window.addEventListener('keydown', onKey);
    return () => window.removeEventListener('keydown', onKey);
  }, [toggleColumn]);

  const openFile = useCallback<WorkspaceContextValue['openFile']>((node) => {
    setOpenTabs((tabs) => {
      if (tabs.some((t) => t.id === node.id)) return tabs;
      const newTab: Tab = { id: node.id, title: node.name, kind: 'code', path: node.id, lang: node.lang ?? 'plaintext' };
      return [...tabs, newTab];
    });
    setActiveTabId(node.id);
  }, []);

  const openStructure = useCallback(() => {
    const id = `structure:${activeRepoId}`;
    setOpenTabs((tabs) => {
      if (tabs.some((t) => t.id === id)) return tabs;
      return [...tabs, { id, title: `${activeRepoId} — structure`, kind: 'structure' }];
    });
    setActiveTabId(id);
  }, [activeRepoId]);

  const openDiff = useCallback(
    (path: string) => {
      const id = `diff:${path}`;
      const name = path.split('/').pop() ?? path;
      setOpenTabs((tabs) =>
        tabs.some((t) => t.id === id)
          ? tabs
          : [...tabs, { id, title: name, kind: 'diff', path, lang: fileIndex[path]?.lang ?? guessLang(path) }],
      );
      setActiveTabId(id);
    },
    [fileIndex],
  );

  const setActiveTab = useCallback((id: string) => setActiveTabId(id), []);

  const closeTab = useCallback((id: string) => {
    setOpenTabs((tabs) => {
      const idx = tabs.findIndex((t) => t.id === id);
      if (idx === -1) return tabs;
      const next = tabs.filter((t) => t.id !== id);
      setActiveTabId((cur) => {
        if (cur !== id) return cur;
        if (next.length === 0) return null;
        return next[Math.min(idx, next.length - 1)].id;
      });
      return next;
    });
  }, []);

  const fileContent = useCallback(
    (path: string): FileContent => {
      const explicit = data.files[path];
      if (explicit) return explicit;
      const node = fileIndex[path];
      return stubContent(path, node?.lang ?? 'plaintext');
    },
    [data.files, fileIndex],
  );

  const fileDiff = useCallback(
    (path: string) => {
      const after = fileContent(path);
      const change = data.changes.find((c) => c.path === path);
      const status = change?.status;
      if (status === 'added' || status === 'untracked') return { before: '', after: after.code, lang: after.lang };
      if (status === 'deleted') return { before: after.code, after: '', lang: after.lang };
      const before = data.diffBefore?.[path] ?? synthBefore(after.code);
      return { before, after: after.code, lang: after.lang };
    },
    [data.changes, data.diffBefore, fileContent],
  );

  const value: WorkspaceContextValue = {
    repos: REPOS,
    activeRepo: REPOS.find((r) => r.id === activeRepoId)!,
    data,
    setRepo,
    branches: data.branches,
    activeBranch: data.branches.find((b) => b.name === activeBranchName) ?? data.branches[0] ?? FALLBACK_BRANCH,
    setBranch,
    activePanel,
    togglePanel,
    setPanel,
    toggleColumn,
    openTabs,
    activeTabId,
    openFile,
    openStructure,
    openDiff,
    setActiveTab,
    closeTab,
    fileContent,
    fileDiff,
    settings,
    updateSettings,
    overlay,
    setOverlay,
  };

  return <WorkspaceContext.Provider value={value}>{children}</WorkspaceContext.Provider>;
}

export function useWorkspace(): WorkspaceContextValue {
  const ctx = useContext(WorkspaceContext);
  if (!ctx) throw new Error('useWorkspace must be used within <WorkspaceProvider>');
  return ctx;
}

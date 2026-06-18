import { createContext, useCallback, useContext, useMemo, useState, type ReactNode } from 'react';
import type { Branch, FileContent, FileNode, PanelId, Repo, RepoData, Tab } from '@/types';
import { DEFAULT_REPO_ID, REPOS, REPO_DATA } from '@/mock/workspace';

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

  openTabs: Tab[];
  activeTabId: string | null;
  openFile: (node: Pick<FileNode, 'id' | 'name' | 'lang'>) => void;
  openStructure: () => void;
  setActiveTab: (id: string) => void;
  closeTab: (id: string) => void;

  fileContent: (path: string) => FileContent;
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

  const [activeBranchName, setActiveBranchName] = useState(
    () => data.branches.find((b) => b.isDefault)?.name ?? data.branches[0].name,
  );
  const [activePanel, setActivePanel] = useState<PanelId | null>('project');
  const [openTabs, setOpenTabs] = useState<Tab[]>(() => data.defaultTabs);
  const [activeTabId, setActiveTabId] = useState<string | null>(() => data.activeTabId);

  const fileIndex = useMemo(() => indexTree(data.tree), [data.tree]);

  const setRepo = useCallback((id: string) => {
    if (!REPO_DATA[id]) return;
    const next = REPO_DATA[id];
    setActiveRepoId(id);
    setActiveBranchName(next.branches.find((b) => b.isDefault)?.name ?? next.branches[0].name);
    setOpenTabs(next.defaultTabs);
    setActiveTabId(next.activeTabId);
  }, []);

  const setBranch = useCallback((name: string) => setActiveBranchName(name), []);

  const togglePanel = useCallback((id: PanelId) => setActivePanel((cur) => (cur === id ? null : id)), []);
  const setPanel = useCallback((id: PanelId) => setActivePanel(id), []);

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

  const value: WorkspaceContextValue = {
    repos: REPOS,
    activeRepo: REPOS.find((r) => r.id === activeRepoId)!,
    data,
    setRepo,
    branches: data.branches,
    activeBranch: data.branches.find((b) => b.name === activeBranchName) ?? data.branches[0],
    setBranch,
    activePanel,
    togglePanel,
    setPanel,
    openTabs,
    activeTabId,
    openFile,
    openStructure,
    setActiveTab,
    closeTab,
    fileContent,
  };

  return <WorkspaceContext.Provider value={value}>{children}</WorkspaceContext.Provider>;
}

export function useWorkspace(): WorkspaceContextValue {
  const ctx = useContext(WorkspaceContext);
  if (!ctx) throw new Error('useWorkspace must be used within <WorkspaceProvider>');
  return ctx;
}

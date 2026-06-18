import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useRef,
  useState,
  type ReactNode,
} from 'react';
import type { Branch, EditorSettings, FileContent, FileNode, Overlay, PanelId, Repo, RepoData, Tab } from '@/types';
import { getDataSource, type DiffPayload } from '@/data';
import { guessLang } from '@/lib/lang';
import { CodeIcon } from '@/ui/icons';
import { LoginGate } from '@/shell/LoginGate';

const FALLBACK_BRANCH: Branch = { name: 'main', isDefault: true, ahead: 0, behind: 0, updated: '' };
const FALLBACK_REPO: Repo = { id: '', name: '…', kind: 'repo', description: '', language: '', tint: 'accent' };

const defaultBranchName = (d: RepoData) => d.branches.find((b) => b.isDefault)?.name ?? d.branches[0]?.name ?? FALLBACK_BRANCH.name;

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

  activePanel: PanelId | null;
  togglePanel: (id: PanelId) => void;
  setPanel: (id: PanelId) => void;
  toggleColumn: () => void;

  openTabs: Tab[];
  activeTabId: string | null;
  openFile: (node: Pick<FileNode, 'id' | 'name' | 'lang'>) => void;
  openStructure: () => void;
  openDiff: (path: string) => void;
  setActiveTab: (id: string) => void;
  closeTab: (id: string) => void;

  fileContent: (path: string) => FileContent;
  fileDiff: (path: string) => DiffPayload;

  settings: EditorSettings;
  updateSettings: (patch: Partial<EditorSettings>) => void;

  overlay: Overlay;
  setOverlay: (o: Overlay) => void;
}

const WorkspaceContext = createContext<WorkspaceContextValue | null>(null);

function indexTree(nodes: FileNode[], acc: Record<string, FileNode> = {}): Record<string, FileNode> {
  for (const n of nodes) {
    acc[n.id] = n;
    if (n.children) indexTree(n.children, acc);
  }
  return acc;
}

const loadingFile = (path: string): FileContent => ({ path, lang: guessLang(path), code: '// loading…\n' });
const loadingDiff = (path: string): DiffPayload => ({ before: '', after: '// loading…\n', lang: guessLang(path) });

type Phase = 'boot' | 'login' | 'ready';

export function WorkspaceProvider({ children }: { children: ReactNode }) {
  const source = useMemo(() => getDataSource(), []);

  const [phase, setPhase] = useState<Phase>('boot');
  const [repos, setRepos] = useState<Repo[]>([]);
  const [activeRepoId, setActiveRepoId] = useState('');
  const [data, setData] = useState<RepoData | null>(null);
  const [activeBranchName, setActiveBranchName] = useState('');

  const [activePanel, setActivePanel] = useState<PanelId | null>('project');
  const [openTabs, setOpenTabs] = useState<Tab[]>([]);
  const [activeTabId, setActiveTabId] = useState<string | null>(null);
  const [fileCache, setFileCache] = useState<Record<string, FileContent>>({});
  const [diffCache, setDiffCache] = useState<Record<string, DiffPayload>>({});

  const branchMemory = useRef<Record<string, string>>({});
  const lastPanelRef = useRef<PanelId>('project');

  const fileIndex = useMemo(() => (data ? indexTree(data.tree) : {}), [data]);

  // Apply a freshly-loaded RepoData: set state + reset the editor tabs.
  const applyData = useCallback((id: string, d: RepoData, keepTabs = false) => {
    setData(d);
    const remembered = branchMemory.current[id];
    setActiveBranchName(remembered && d.branches.some((b) => b.name === remembered) ? remembered : defaultBranchName(d));
    if (!keepTabs) {
      setOpenTabs(d.defaultTabs);
      setActiveTabId(initialTabId(d));
    }
  }, []);

  // ── boot: detect api/mock + auth, then load the first repo ──────────────────
  const bootstrap = useCallback(async () => {
    const rs = await source.repos();
    setRepos(rs);
    const first = rs[0]?.id ?? '';
    setActiveRepoId(first);
    if (first) {
      const d = await source.repoData(first);
      applyData(first, d);
    }
    setPhase('ready');
  }, [source, applyData]);

  useEffect(() => {
    let cancelled = false;
    source
      .init()
      .then((res) => {
        if (cancelled) return;
        if (res.gated && !res.authed) {
          setPhase('login');
          return;
        }
        return bootstrap();
      })
      .catch(() => setPhase('login'));
    return () => {
      cancelled = true;
    };
  }, [source, bootstrap]);

  const login = useCallback(
    async (password: string) => {
      const ok = await source.login(password);
      if (ok) {
        setPhase('boot');
        await bootstrap();
      }
      return ok;
    },
    [source, bootstrap],
  );

  // ── repo switching ──────────────────────────────────────────────────────────
  const setRepo = useCallback(
    (id: string) => {
      if (id === activeRepoId || !repos.some((r) => r.id === id)) return;
      setActiveRepoId(id);
      setOpenTabs([]); // optimistic; applyData sets the real defaults
      setActiveTabId(null);
      source.repoData(id, branchMemory.current[id]).then((d) => applyData(id, d));
    },
    [activeRepoId, repos, source, applyData],
  );

  const setBranch = useCallback(
    (name: string) => {
      branchMemory.current[activeRepoId] = name;
      setActiveBranchName(name);
      source.repoData(activeRepoId, name).then((d) => applyData(activeRepoId, d, true));
    },
    [activeRepoId, source, applyData],
  );

  // ── panels ──────────────────────────────────────────────────────────────────
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

  // ── tabs + lazy file/diff fetching ──────────────────────────────────────────
  const fetchFile = useCallback(
    (path: string) => {
      const key = `${activeRepoId}:${path}`;
      if (fileCache[key] || data?.files[path]) return;
      source.fileContent(activeRepoId, path).then((fc) => setFileCache((c) => ({ ...c, [key]: fc })));
    },
    [activeRepoId, fileCache, data, source],
  );

  const fetchDiff = useCallback(
    (path: string) => {
      const key = `${activeRepoId}:${path}`;
      if (diffCache[key]) return;
      source.fileDiff(activeRepoId, path).then((d) => setDiffCache((c) => ({ ...c, [key]: d })));
    },
    [activeRepoId, diffCache, source],
  );

  const openFile = useCallback<WorkspaceContextValue['openFile']>(
    (node) => {
      setOpenTabs((tabs) =>
        tabs.some((t) => t.id === node.id)
          ? tabs
          : [...tabs, { id: node.id, title: node.name, kind: 'code', path: node.id, lang: node.lang ?? guessLang(node.id) }],
      );
      setActiveTabId(node.id);
      fetchFile(node.id);
    },
    [fetchFile],
  );

  const openStructure = useCallback(() => {
    const id = `structure:${activeRepoId}`;
    setOpenTabs((tabs) => (tabs.some((t) => t.id === id) ? tabs : [...tabs, { id, title: `${activeRepoId} — structure`, kind: 'structure' }]));
    setActiveTabId(id);
  }, [activeRepoId]);

  const openDiff = useCallback(
    (path: string) => {
      const id = `diff:${path}`;
      const name = path.split('/').pop() ?? path;
      setOpenTabs((tabs) => (tabs.some((t) => t.id === id) ? tabs : [...tabs, { id, title: name, kind: 'diff', path, lang: fileIndex[path]?.lang ?? guessLang(path) }]));
      setActiveTabId(id);
      fetchDiff(path);
    },
    [fileIndex, fetchDiff],
  );

  const setActiveTab = useCallback((id: string) => setActiveTabId(id), []);

  const closeTab = useCallback((id: string) => {
    setOpenTabs((tabs) => {
      const idx = tabs.findIndex((t) => t.id === id);
      if (idx === -1) return tabs;
      const next = tabs.filter((t) => t.id !== id);
      setActiveTabId((cur) => (cur !== id ? cur : next.length === 0 ? null : next[Math.min(idx, next.length - 1)].id));
      return next;
    });
  }, []);

  const fileContent = useCallback(
    (path: string): FileContent => fileCache[`${activeRepoId}:${path}`] ?? data?.files[path] ?? loadingFile(path),
    [fileCache, activeRepoId, data],
  );
  const fileDiff = useCallback(
    (path: string): DiffPayload => diffCache[`${activeRepoId}:${path}`] ?? loadingDiff(path),
    [diffCache, activeRepoId],
  );

  // ── settings + overlay + shortcuts ──────────────────────────────────────────
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

  if (phase === 'login') return <LoginGate onSubmit={login} />;
  if (phase === 'boot' || !data) return <Splash />;

  const value: WorkspaceContextValue = {
    repos,
    activeRepo: repos.find((r) => r.id === activeRepoId) ?? FALLBACK_REPO,
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

function Splash() {
  return (
    <div className="flex h-full flex-col items-center justify-center gap-4 bg-bg-base text-text-primary">
      <span className="flex h-12 w-12 animate-pulse items-center justify-center rounded-2xl bg-surface-raised shadow-elev-2 ring-1 ring-separator">
        <CodeIcon className="h-6 w-6 text-accent" />
      </span>
      <p className="text-footnote text-text-tertiary">Loading workspace…</p>
    </div>
  );
}

export function useWorkspace(): WorkspaceContextValue {
  const ctx = useContext(WorkspaceContext);
  if (!ctx) throw new Error('useWorkspace must be used within <WorkspaceProvider>');
  return ctx;
}

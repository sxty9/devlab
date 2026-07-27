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
import type { Branch, EditorSettings, FileContent, FileNode, Overlay, PanelId, PullRequestResult, Repo, RepoData, Tab, User } from '@/types';
import { getDataSource, type CommitResult, type DiffPayload, type PushResult } from '@/data';
import { basename, guessLang } from '@/lib/lang';
import { defaultBranchName } from '@/lib/repo';
import { useSession } from '@/state/session';
import { useToast } from '@/ui/Toast';
import { Splash } from '@/shell/Splash';

const FALLBACK_BRANCH: Branch = { name: 'main', isDefault: true, ahead: 0, behind: 0, updated: '' };
const FALLBACK_REPO: Repo = { id: '', name: '…', fullName: '', kind: 'repo', description: '', language: '', icon: 'service', tint: 'accent', permission: 'pull' };

const initialTabId = (d: RepoData) =>
  d.defaultTabs.some((t) => t.id === d.activeTabId) ? d.activeTabId : (d.defaultTabs[0]?.id ?? null);

interface WorkspaceContextValue {
  user: User;
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
  openVision: (path: string) => void;
  setActiveTab: (id: string) => void;
  closeTab: (id: string) => void;

  fileContent: (path: string) => FileContent;
  fileDiff: (path: string) => DiffPayload;

  // ── editing + write loop ────────────────────────────────────────────────────
  /** Editor buffer for a path (unsaved draft if edited, else disk content). */
  editorValue: (path: string) => string;
  /** Record an editor edit (marks the tab dirty when it diverges from disk). */
  onEdit: (path: string, value: string) => void;
  /** Whether a code path has unsaved edits. */
  isDirty: (path: string) => boolean;
  /** Whether the active repo can be written (GitHub push/admin). */
  canWrite: boolean;
  saveFile: (path: string) => Promise<void>;
  stageChange: (path: string) => Promise<void>;
  unstageChange: (path: string) => Promise<void>;
  commitStaged: (message: string) => Promise<CommitResult>;
  push: () => Promise<PushResult>;
  pull: () => Promise<PushResult>;
  /** Push the current branch and open (or focus) a GitHub PR into the default branch. */
  openPR: () => Promise<PullRequestResult>;
  createBranch: (name: string, from?: string) => Promise<void>;
  reloadRepo: () => Promise<void>;

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

/** Drop every entry whose key starts with prefix (used to evict one repo's buffers). */
function stripPrefix<T>(obj: Record<string, T>, prefix: string): Record<string, T> {
  const out: Record<string, T> = {};
  for (const k in obj) if (!k.startsWith(prefix)) out[k] = obj[k];
  return out;
}

/** The IDE's repo-scoped state: one repo's tree, tabs, editor buffers and git write loop.
 *
 *  Mounted only by the IDE view, and bound to `repoId` — identity, the repo set, settings and the
 *  overlay live a level up in SessionProvider and are re-exported here, so every consumer below
 *  keeps reading them from a single `useWorkspace()` as before. */
export function WorkspaceProvider({ repoId, children }: { repoId: string; children: ReactNode }) {
  const source = useMemo(() => getDataSource(), []);
  const { user, repos, view, openRepo, closeRepo, settings, updateSettings, overlay, setOverlay } = useSession();
  const { toast } = useToast();

  const [data, setData] = useState<RepoData | null>(null);
  const [activeBranchName, setActiveBranchName] = useState('');

  const [activePanel, setActivePanel] = useState<PanelId | null>('project');
  const [openTabs, setOpenTabs] = useState<Tab[]>([]);
  const [activeTabId, setActiveTabId] = useState<string | null>(null);
  const [fileCache, setFileCache] = useState<Record<string, FileContent>>({});
  const [diffCache, setDiffCache] = useState<Record<string, DiffPayload>>({});
  const [drafts, setDrafts] = useState<Record<string, string>>({}); // key `${repo}:${path}` → unsaved buffer

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

  // ── the bound repo ──────────────────────────────────────────────────────────
  // Load whenever the repo this provider is bound to changes (the first mount included). The
  // previous repo's data is deliberately left in place until the new data lands, so switching repos
  // does not flash the whole shell back to its splash — only the very first open does. The
  // file/diff/draft caches are keyed by `${repo}:${path}` and survive a switch too, so an unsaved
  // draft in one repo is still there when you come back to it.
  useEffect(() => {
    let cancelled = false;
    setOpenTabs([]); // optimistic; applyData sets the real defaults
    setActiveTabId(null);
    source
      .repoData(repoId, branchMemory.current[repoId])
      .then((d) => {
        if (!cancelled) applyData(repoId, d);
      })
      .catch(() => {
        if (cancelled) return;
        // Without this the provider would sit on its splash forever, taking the whole shell with it.
        toast({ title: `${repoId} konnte nicht geladen werden`, variant: 'danger' });
        closeRepo();
      });
    return () => {
      cancelled = true;
    };
  }, [repoId, source, applyData, toast, closeRepo]);

  // Switching repos is a navigation: the session owns which repo the IDE is bound to.
  const setRepo = useCallback(
    (id: string) => {
      if (id === repoId || !repos.some((r) => r.id === id)) return;
      openRepo(id);
    },
    [repoId, repos, openRepo],
  );

  // Evict a repo's cached file/diff/draft buffers (keyed by repo:path, branch-agnostic). Called on
  // branch switch so an open file cannot keep the previous branch's content — otherwise a save
  // would write the old branch's bytes onto the new branch. Also clears tab dirty flags.
  const clearRepoBuffers = useCallback((id: string) => {
    const prefix = `${id}:`;
    setFileCache((c) => stripPrefix(c, prefix));
    setDiffCache((c) => stripPrefix(c, prefix));
    setDrafts((d) => stripPrefix(d, prefix));
    setOpenTabs((tabs) => tabs.map((t) => (t.dirty ? { ...t, dirty: false } : t)));
  }, []);

  const setBranch = useCallback(
    (name: string) => {
      if (name === activeBranchName) return;
      // In-browser drafts are built on the current branch's content and are never written to disk
      // before a checkout (so the backend can't refuse). Confirm before discarding them.
      const prefix = `${repoId}:`;
      if (Object.keys(drafts).some((k) => k.startsWith(prefix)) && !window.confirm('Discard unsaved changes and switch branch?')) {
        return;
      }
      const prev = activeBranchName;
      branchMemory.current[repoId] = name;
      setActiveBranchName(name);
      // Real checkout in the workspace, then load that branch's view, evicting the previous
      // branch's cached content. On refusal (e.g. a dirty tree) revert the selection.
      source
        .checkout(repoId, name)
        .then(() => source.repoData(repoId, name))
        .then((d) => {
          clearRepoBuffers(repoId);
          applyData(repoId, d, true);
        })
        .catch(() => {
          branchMemory.current[repoId] = prev;
          setActiveBranchName(prev);
        });
    },
    [repoId, activeBranchName, drafts, source, applyData, clearRepoBuffers],
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
      const key = `${repoId}:${path}`;
      if (fileCache[key] || data?.files[path]) return;
      source.fileContent(repoId, path).then((fc) => setFileCache((c) => ({ ...c, [key]: fc })));
    },
    [repoId, fileCache, data, source],
  );

  const fetchDiff = useCallback(
    (path: string) => {
      const key = `${repoId}:${path}`;
      if (diffCache[key]) return;
      source.fileDiff(repoId, path).then((d) => setDiffCache((c) => ({ ...c, [key]: d })));
    },
    [repoId, diffCache, source],
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
    const id = `structure:${repoId}`;
    setOpenTabs((tabs) => (tabs.some((t) => t.id === id) ? tabs : [...tabs, { id, title: `${repoId} — structure`, kind: 'structure' }]));
    setActiveTabId(id);
  }, [repoId]);

  const openDiff = useCallback(
    (path: string) => {
      const id = `diff:${path}`;
      const name = basename(path);
      setOpenTabs((tabs) => (tabs.some((t) => t.id === id) ? tabs : [...tabs, { id, title: name, kind: 'diff', path, lang: fileIndex[path]?.lang ?? guessLang(path) }]));
      setActiveTabId(id);
      fetchDiff(path);
    },
    [fileIndex, fetchDiff],
  );

  const openVision = useCallback((path: string) => {
    const id = `vision:${path}`;
    const name = basename(path);
    setOpenTabs((tabs) => (tabs.some((t) => t.id === id) ? tabs : [...tabs, { id, title: name, kind: 'vision', path }]));
    setActiveTabId(id);
  }, []);

  const setActiveTab = useCallback((id: string) => setActiveTabId(id), []);

  const closeTab = useCallback(
    (id: string) => {
      const idx = openTabs.findIndex((t) => t.id === id);
      if (idx === -1) return;
      const tab = openTabs[idx];
      if (tab.dirty && !window.confirm(`Discard unsaved changes to ${tab.title}?`)) return;
      if (tab.path) {
        const key = `${repoId}:${tab.path}`;
        setDrafts((d) => {
          if (!(key in d)) return d;
          const next = { ...d };
          delete next[key];
          return next;
        });
      }
      const next = openTabs.filter((t) => t.id !== id);
      setOpenTabs(next);
      setActiveTabId((cur) => (cur !== id ? cur : next.length === 0 ? null : next[Math.min(idx, next.length - 1)].id));
    },
    [openTabs, repoId],
  );

  const fileContent = useCallback(
    (path: string): FileContent => fileCache[`${repoId}:${path}`] ?? data?.files[path] ?? loadingFile(path),
    [fileCache, repoId, data],
  );
  const fileDiff = useCallback(
    (path: string): DiffPayload => diffCache[`${repoId}:${path}`] ?? loadingDiff(path),
    [diffCache, repoId],
  );

  // Keep the active tab's content loaded for the current branch. After a branch switch (which
  // evicts the repo's file+diff caches) this refetches the open file/diff from the newly
  // checked-out branch, so a code editor or diff view never shows another branch's content or
  // gets stuck on the loading placeholder. fetchFile/fetchDiff short-circuit when cached.
  useEffect(() => {
    const tab = openTabs.find((t) => t.id === activeTabId);
    if (!tab || !tab.path) return;
    if (tab.kind === 'code') fetchFile(tab.path);
    else if (tab.kind === 'diff') fetchDiff(tab.path);
  }, [activeTabId, activeBranchName, openTabs, fetchFile, fetchDiff]);

  // ── editing + write loop ─────────────────────────────────────────────────────
  const diskContent = useCallback(
    (path: string): string => fileCache[`${repoId}:${path}`]?.code ?? data?.files[path]?.code ?? '',
    [fileCache, repoId, data],
  );

  const editorValue = useCallback(
    (path: string): string => {
      const key = `${repoId}:${path}`;
      return key in drafts ? drafts[key] : diskContent(path);
    },
    [drafts, repoId, diskContent],
  );

  const isDirty = useCallback((path: string) => `${repoId}:${path}` in drafts, [drafts, repoId]);

  const setTabDirty = useCallback((id: string, dirty: boolean) => {
    setOpenTabs((tabs) => tabs.map((t) => (t.id === id ? { ...t, dirty } : t)));
  }, []);

  const onEdit = useCallback(
    (path: string, value: string) => {
      const key = `${repoId}:${path}`;
      const clean = value === diskContent(path);
      setDrafts((d) => {
        const next = { ...d };
        if (clean) delete next[key];
        else next[key] = value;
        return next;
      });
      setTabDirty(path, !clean);
    },
    [repoId, diskContent, setTabDirty],
  );

  const reloadRepo = useCallback(async () => {
    const d = await source.repoData(repoId, branchMemory.current[repoId]);
    applyData(repoId, d, true); // keep open tabs (and their unsaved drafts)
  }, [source, repoId, applyData]);

  const saveFile = useCallback(
    async (path: string) => {
      const key = `${repoId}:${path}`;
      const content = key in drafts ? drafts[key] : diskContent(path);
      await source.saveFile(repoId, path, content);
      setFileCache((c) => ({ ...c, [key]: { path, lang: guessLang(path), code: content } }));
      setDrafts((d) => {
        if (!(key in d)) return d;
        const next = { ...d };
        delete next[key];
        return next;
      });
      setTabDirty(path, false);
      await reloadRepo();
    },
    [repoId, drafts, diskContent, source, setTabDirty, reloadRepo],
  );

  const stageChange = useCallback(
    async (path: string) => {
      await source.stage(repoId, path);
      await reloadRepo();
    },
    [source, repoId, reloadRepo],
  );
  const unstageChange = useCallback(
    async (path: string) => {
      await source.unstage(repoId, path);
      await reloadRepo();
    },
    [source, repoId, reloadRepo],
  );
  const commitStaged = useCallback(
    async (message: string) => {
      const res = await source.commit(repoId, message);
      await reloadRepo();
      return res;
    },
    [source, repoId, reloadRepo],
  );
  const push = useCallback(async () => {
    const res = await source.push(repoId);
    await reloadRepo();
    return res;
  }, [source, repoId, reloadRepo]);
  const pull = useCallback(async () => {
    const res = await source.pull(repoId);
    await reloadRepo();
    return res;
  }, [source, repoId, reloadRepo]);
  const openPR = useCallback(async () => {
    const res = await source.openPR(repoId);
    await reloadRepo(); // the push may change ahead/behind
    return res;
  }, [source, repoId, reloadRepo]);
  const createBranch = useCallback(
    async (name: string, from?: string) => {
      await source.createBranch(repoId, name, from);
      branchMemory.current[repoId] = name;
      setActiveBranchName(name);
      await reloadRepo();
    },
    [source, repoId, reloadRepo],
  );

  // ── shortcuts ───────────────────────────────────────────────────────────────
  // The IDE stays mounted behind the dashboard, so its shortcuts must not fire from another view.
  const isActive = view.kind === 'ide';

  // Cmd/Ctrl-S saves the active code tab from anywhere. A ref keeps the key handler stable while
  // still calling the latest saveFile/tab state.
  const saveActiveRef = useRef<() => void>(() => {});
  saveActiveRef.current = () => {
    const tab = openTabs.find((t) => t.id === activeTabId);
    if (tab && tab.kind === 'code' && tab.path && tab.dirty) void saveFile(tab.path);
  };

  useEffect(() => {
    if (!isActive) return;
    const onKey = (e: KeyboardEvent) => {
      const mod = e.metaKey || e.ctrlKey;
      if (mod && (e.key === 'b' || e.key === 'B')) {
        e.preventDefault();
        toggleColumn();
      } else if (mod && (e.key === 's' || e.key === 'S')) {
        e.preventDefault();
        saveActiveRef.current();
      }
    };
    window.addEventListener('keydown', onKey);
    return () => window.removeEventListener('keydown', onKey);
  }, [isActive, toggleColumn]);

  if (!data) return <Splash />;

  const activeRepo = repos.find((r) => r.id === repoId) ?? FALLBACK_REPO;
  const canWrite = activeRepo.permission === 'push' || activeRepo.permission === 'admin';

  const value: WorkspaceContextValue = {
    user,
    repos,
    activeRepo,
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
    openVision,
    setActiveTab,
    closeTab,
    fileContent,
    fileDiff,
    editorValue,
    onEdit,
    isDirty,
    canWrite,
    saveFile,
    stageChange,
    unstageChange,
    commitStaged,
    push,
    pull,
    openPR,
    createBranch,
    reloadRepo,
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

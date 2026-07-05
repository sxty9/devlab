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
import { guessLang } from '@/lib/lang';
import { CodeIcon } from '@/ui/icons';
import { SignInGate, AccessDenied, GateShell } from '@/shell/LoginGate';
import { GitHubLinkGate } from '@/shell/GitHubLinkGate';
import { Button } from '@/ui/Button';

const FALLBACK_BRANCH: Branch = { name: 'main', isDefault: true, ahead: 0, behind: 0, updated: '' };
const FALLBACK_REPO: Repo = { id: '', name: '…', fullName: '', kind: 'repo', description: '', language: '', tint: 'accent', permission: 'pull' };

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

type Phase = 'boot' | 'login' | 'denied' | 'github-link' | 'ready';

const FALLBACK_USER: User = { username: '', displayName: '', isAdmin: false, canUseDevlab: false, githubLinked: false };

export function WorkspaceProvider({ children }: { children: ReactNode }) {
  const source = useMemo(() => getDataSource(), []);

  const [phase, setPhase] = useState<Phase>('boot');
  const [user, setUser] = useState<User>(FALLBACK_USER);
  const [repos, setRepos] = useState<Repo[]>([]);
  const [activeRepoId, setActiveRepoId] = useState('');
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

  // ── boot: detect api/mock + auth, then load identity + the first repo ───────
  const bootstrap = useCallback(async () => {
    const [u, rs] = await Promise.all([source.getUser(), source.repos()]);
    setUser(u);
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
        if (!res.signedIn) {
          setPhase('login');
          return;
        }
        if (!res.canUseDevlab) {
          // Name the user on the access-denied screen, best-effort.
          source.getUser().then((u) => !cancelled && setUser(u)).catch(() => {});
          setPhase('denied');
          return;
        }
        if (!res.githubLinked) {
          // Mandatory GitHub link before the workspace loads; name the user best-effort.
          source.getUser().then((u) => !cancelled && setUser(u)).catch(() => {});
          setPhase('github-link');
          return;
        }
        return bootstrap();
      })
      .catch(() => setPhase('login'));
    return () => {
      cancelled = true;
    };
  }, [source, bootstrap]);

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

  // Evict a repo's cached file/diff/draft buffers (keyed by repo:path, branch-agnostic). Called on
  // branch switch so an open file cannot keep the previous branch's content — otherwise a save
  // would write the old branch's bytes onto the new branch. Also clears tab dirty flags.
  const clearRepoBuffers = useCallback((repoId: string) => {
    const prefix = `${repoId}:`;
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
      const prefix = `${activeRepoId}:`;
      if (Object.keys(drafts).some((k) => k.startsWith(prefix)) && !window.confirm('Discard unsaved changes and switch branch?')) {
        return;
      }
      const prev = activeBranchName;
      branchMemory.current[activeRepoId] = name;
      setActiveBranchName(name);
      // Real checkout in the workspace, then load that branch's view, evicting the previous
      // branch's cached content. On refusal (e.g. a dirty tree) revert the selection.
      source
        .checkout(activeRepoId, name)
        .then(() => source.repoData(activeRepoId, name))
        .then((d) => {
          clearRepoBuffers(activeRepoId);
          applyData(activeRepoId, d, true);
        })
        .catch(() => {
          branchMemory.current[activeRepoId] = prev;
          setActiveBranchName(prev);
        });
    },
    [activeRepoId, activeBranchName, drafts, source, applyData, clearRepoBuffers],
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

  const openVision = useCallback((path: string) => {
    const id = `vision:${path}`;
    const name = path.split('/').pop() ?? path;
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
        const key = `${activeRepoId}:${tab.path}`;
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
    [openTabs, activeRepoId],
  );

  const fileContent = useCallback(
    (path: string): FileContent => fileCache[`${activeRepoId}:${path}`] ?? data?.files[path] ?? loadingFile(path),
    [fileCache, activeRepoId, data],
  );
  const fileDiff = useCallback(
    (path: string): DiffPayload => diffCache[`${activeRepoId}:${path}`] ?? loadingDiff(path),
    [diffCache, activeRepoId],
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
    (path: string): string => fileCache[`${activeRepoId}:${path}`]?.code ?? data?.files[path]?.code ?? '',
    [fileCache, activeRepoId, data],
  );

  const editorValue = useCallback(
    (path: string): string => {
      const key = `${activeRepoId}:${path}`;
      return key in drafts ? drafts[key] : diskContent(path);
    },
    [drafts, activeRepoId, diskContent],
  );

  const isDirty = useCallback((path: string) => `${activeRepoId}:${path}` in drafts, [drafts, activeRepoId]);

  const setTabDirty = useCallback((id: string, dirty: boolean) => {
    setOpenTabs((tabs) => tabs.map((t) => (t.id === id ? { ...t, dirty } : t)));
  }, []);

  const onEdit = useCallback(
    (path: string, value: string) => {
      const key = `${activeRepoId}:${path}`;
      const clean = value === diskContent(path);
      setDrafts((d) => {
        const next = { ...d };
        if (clean) delete next[key];
        else next[key] = value;
        return next;
      });
      setTabDirty(path, !clean);
    },
    [activeRepoId, diskContent, setTabDirty],
  );

  const reloadRepo = useCallback(async () => {
    const d = await source.repoData(activeRepoId, branchMemory.current[activeRepoId]);
    applyData(activeRepoId, d, true); // keep open tabs (and their unsaved drafts)
  }, [source, activeRepoId, applyData]);

  const saveFile = useCallback(
    async (path: string) => {
      const key = `${activeRepoId}:${path}`;
      const content = key in drafts ? drafts[key] : diskContent(path);
      await source.saveFile(activeRepoId, path, content);
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
    [activeRepoId, drafts, diskContent, source, setTabDirty, reloadRepo],
  );

  const stageChange = useCallback(
    async (path: string) => {
      await source.stage(activeRepoId, path);
      await reloadRepo();
    },
    [source, activeRepoId, reloadRepo],
  );
  const unstageChange = useCallback(
    async (path: string) => {
      await source.unstage(activeRepoId, path);
      await reloadRepo();
    },
    [source, activeRepoId, reloadRepo],
  );
  const commitStaged = useCallback(
    async (message: string) => {
      const res = await source.commit(activeRepoId, message);
      await reloadRepo();
      return res;
    },
    [source, activeRepoId, reloadRepo],
  );
  const push = useCallback(async () => {
    const res = await source.push(activeRepoId);
    await reloadRepo();
    return res;
  }, [source, activeRepoId, reloadRepo]);
  const pull = useCallback(async () => {
    const res = await source.pull(activeRepoId);
    await reloadRepo();
    return res;
  }, [source, activeRepoId, reloadRepo]);
  const openPR = useCallback(async () => {
    const res = await source.openPR(activeRepoId);
    await reloadRepo(); // the push may change ahead/behind
    return res;
  }, [source, activeRepoId, reloadRepo]);
  const createBranch = useCallback(
    async (name: string, from?: string) => {
      await source.createBranch(activeRepoId, name, from);
      branchMemory.current[activeRepoId] = name;
      setActiveBranchName(name);
      await reloadRepo();
    },
    [source, activeRepoId, reloadRepo],
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

  // Cmd/Ctrl-S saves the active code tab from anywhere. A ref keeps the key handler stable while
  // still calling the latest saveFile/tab state.
  const saveActiveRef = useRef<() => void>(() => {});
  saveActiveRef.current = () => {
    const tab = openTabs.find((t) => t.id === activeTabId);
    if (tab && tab.kind === 'code' && tab.path && tab.dirty) void saveFile(tab.path);
  };

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      const mod = e.metaKey || e.ctrlKey;
      if (mod && (e.key === 'b' || e.key === 'B')) {
        e.preventDefault();
        toggleColumn();
      } else if (mod && (e.key === 's' || e.key === 'S')) {
        e.preventDefault();
        saveActiveRef.current();
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

  if (phase === 'login') return <SignInGate />;
  if (phase === 'denied') return <AccessDenied user={user} />;
  if (phase === 'github-link') return <GitHubLinkGate user={user} />;
  if (phase === 'boot') return <Splash />;
  // Ready with no accessible repos: show an empty state rather than blocking on `!data` forever.
  if (repos.length === 0) return <NoRepos />;
  if (!data) return <Splash />;

  const activeRepo = repos.find((r) => r.id === activeRepoId) ?? FALLBACK_REPO;
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

/** Shown when the user is authorized and GitHub-linked but has no repositories in the holistic
 *  set visible to their GitHub account (so there is nothing to open). */
function NoRepos() {
  return (
    <GateShell>
      <p className="mt-1 max-w-sm text-footnote text-text-secondary">
        Keine Repositories sichtbar. DevLab zeigt die Holistic-Repos, auf die dein verknüpftes
        GitHub-Konto Zugriff hat — aktuell sind das keine. Sobald dir Zugriff gewährt wird,
        erscheinen sie hier.
      </p>
      <div className="mt-6 flex w-full max-w-xs flex-col gap-2">
        <Button variant="secondary" size="md" className="w-full" onClick={() => window.location.reload()}>
          Erneut prüfen
        </Button>
      </div>
    </GateShell>
  );
}

export function useWorkspace(): WorkspaceContextValue {
  const ctx = useContext(WorkspaceContext);
  if (!ctx) throw new Error('useWorkspace must be used within <WorkspaceProvider>');
  return ctx;
}

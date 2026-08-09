import { createContext, useCallback, useContext, useEffect, useMemo, useState, type ReactNode } from 'react';
import type { EditorSettings, Overlay, Repo, User } from '@/types';
import { getDataSource } from '@/data';
import { currentView, parseHash, toHash, type View } from './route';
import { SignInGate, AccessDenied } from '@/shell/LoginGate';
import { GitHubLinkGate } from '@/shell/GitHubLinkGate';

/** Uniform error-to-string — the same shape the rest of the surface uses. */
const errMsg = (e: unknown): string => String((e as Error)?.message ?? e);
import { Splash } from '@/shell/Splash';

type Phase = 'boot' | 'login' | 'denied' | 'github-link' | 'ready';

const DEFAULT_SETTINGS: EditorSettings = { fontSize: 13, tabSize: 2 };
const FALLBACK_USER: User = {
  username: '',
  displayName: '',
  isAdmin: false,
  canUseDevlab: false,
  canWatchSession: false,
  canSpeakSession: false,
  githubLinked: false,
};
const LAST_REPO_KEY = 'dl.lastRepo';

function readSettings(): EditorSettings {
  try {
    const raw = localStorage.getItem('dl.settings');
    if (raw) return { ...DEFAULT_SETTINGS, ...(JSON.parse(raw) as Partial<EditorSettings>) };
  } catch {
    /* ignore */
  }
  return DEFAULT_SETTINGS;
}

function readLastRepo(): string {
  try {
    return localStorage.getItem(LAST_REPO_KEY) ?? '';
  } catch {
    return '';
  }
}

/** Session-scoped state: who you are, what you can see, and which view you are in. Everything here
 *  outlives a repo, so it sits above the IDE rather than inside it. */
interface SessionContextValue {
  user: User;
  repos: Repo[];
  /** The repo listing failed (GitHub unreachable). The dashboard offers a retry. */
  /** Why the repository list could not be read — the MEASURED reason, null when it could. */
  reposError: string | null;
  reloadRepos: () => void;

  view: View;
  /** Open the IDE on a repo. */
  openRepo: (id: string) => void;
  openCapability: (id: 'mercury' | 'atlas') => void;
  goHome: () => void;
  /** Unbind the IDE and go back to the dashboard. */
  closeRepo: () => void;
  /** The repo the IDE is bound to. The IDE stays mounted on it across a trip to the dashboard, so
   *  unsaved drafts, the terminal socket and a running agent survive. Null until a repo is opened. */
  openedRepo: string | null;
  /** The last repo opened in this browser — what the "DevLab IDE" capability card resumes. */
  lastRepo: string;

  settings: EditorSettings;
  updateSettings: (patch: Partial<EditorSettings>) => void;
  overlay: Overlay;
  setOverlay: (o: Overlay) => void;
}

const SessionContext = createContext<SessionContextValue | null>(null);

export function SessionProvider({ children }: { children: ReactNode }) {
  const source = useMemo(() => getDataSource(), []);

  const [phase, setPhase] = useState<Phase>('boot');
  const [user, setUser] = useState<User>(FALLBACK_USER);
  const [repos, setRepos] = useState<Repo[]>([]);
  const [reposError, setReposError] = useState<string | null>(null);

  const [view, setView] = useState<View>(currentView);
  const [openedRepo, setOpenedRepo] = useState<string | null>(null);
  const [lastRepo, setLastRepo] = useState<string>(readLastRepo);

  const [settings, setSettings] = useState<EditorSettings>(readSettings);
  const [overlay, setOverlay] = useState<Overlay>(null);

  // ── the URL is the source of truth for `view` ───────────────────────────────
  const navigate = useCallback((next: View) => {
    const hash = toHash(next);
    // Writing the hash fires `hashchange`, which sets the state. When it is already the current
    // hash no event fires, so set it directly.
    if (window.location.hash === hash) setView(next);
    else window.location.hash = hash;
  }, []);

  useEffect(() => {
    const onHashChange = () => setView(parseHash(window.location.hash));
    window.addEventListener('hashchange', onHashChange);
    return () => window.removeEventListener('hashchange', onHashChange);
  }, []);

  const loadRepos = useCallback(async () => {
    try {
      const rs = await source.repos();
      setRepos(rs);
      setReposError(null);
      return rs;
    } catch (e) {
      // A failed read must not look like a signed-out session — it is surfaced on the dashboard.
      // The REASON is kept: throwing it away is what made the dashboard blame GitHub for a fault it
      // had never measured (the browser inspection saw our OWN server answer 502 while the surface
      // said "GitHub is unreachable").
      setReposError(errMsg(e));
      setRepos([]);
      return [];
    }
  }, [source]);

  const reloadRepos = useCallback(() => void loadRepos(), [loadRepos]);

  // ── boot: identity + the repo set. No repo is opened until you pick one. ────
  useEffect(() => {
    let cancelled = false;
    source
      .init()
      .then(async (res) => {
        if (cancelled) return;
        if (!res.signedIn) return setPhase('login');
        if (!res.canUseDevlab) {
          source.getUser().then((u) => !cancelled && setUser(u)).catch(() => {});
          return setPhase('denied');
        }
        if (!res.githubLinked) {
          source.getUser().then((u) => !cancelled && setUser(u)).catch(() => {});
          return setPhase('github-link');
        }
        const [u, rs] = await Promise.all([source.getUser(), loadRepos()]);
        if (cancelled) return;
        setUser(u);
        // Honour an `#/ide/<repo>` deep link, but only for a repo the user can actually see.
        const initial = currentView();
        if (initial.kind === 'ide' && rs.some((r) => r.id === initial.repo)) setOpenedRepo(initial.repo);
        setPhase('ready');
      })
      .catch(() => !cancelled && setPhase('login'));
    return () => {
      cancelled = true;
    };
  }, [source, loadRepos]);

  // The URL owns which repo the IDE is bound to, so back/forward between two `#/ide/<repo>` entries
  // rebinds it. A link to a repo that isn't in the set (revoked access, typo, renamed) must not
  // strand the user on a blank IDE — send them to the dashboard instead.
  useEffect(() => {
    if (phase !== 'ready' || view.kind !== 'ide') return;
    if (!repos.some((r) => r.id === view.repo)) {
      navigate({ kind: 'dashboard' });
      return;
    }
    setOpenedRepo((cur) => (cur === view.repo ? cur : view.repo));
  }, [phase, view, repos, navigate]);

  // ── navigation ──────────────────────────────────────────────────────────────
  const openRepo = useCallback(
    (id: string) => {
      setOpenedRepo(id);
      setLastRepo(id);
      try {
        localStorage.setItem(LAST_REPO_KEY, id);
      } catch {
        /* ignore quota/availability errors */
      }
      navigate({ kind: 'ide', repo: id });
    },
    [navigate],
  );

  const openCapability = useCallback((id: 'mercury' | 'atlas') => navigate({ kind: id }), [navigate]);
  const goHome = useCallback(() => navigate({ kind: 'dashboard' }), [navigate]);

  /** Unbind the IDE and return to the dashboard. The escape hatch when a repo's data won't load —
   *  without it the IDE would sit on its splash forever, taking the whole shell with it. */
  const closeRepo = useCallback(() => {
    setOpenedRepo(null);
    navigate({ kind: 'dashboard' });
  }, [navigate]);

  // ── settings + shortcuts ────────────────────────────────────────────────────
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

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key !== '?') return;
      const t = e.target as HTMLElement | null;
      if (t && (t.tagName === 'INPUT' || t.tagName === 'TEXTAREA' || t.isContentEditable)) return;
      e.preventDefault();
      setOverlay('help');
    };
    window.addEventListener('keydown', onKey);
    return () => window.removeEventListener('keydown', onKey);
  }, []);

  if (phase === 'login') return <SignInGate />;
  if (phase === 'denied') return <AccessDenied user={user} />;
  if (phase === 'github-link') return <GitHubLinkGate user={user} />;
  if (phase === 'boot') return <Splash />;

  const value: SessionContextValue = {
    user,
    repos,
    reposError,
    reloadRepos,
    view,
    openRepo,
    openCapability,
    goHome,
    closeRepo,
    openedRepo,
    lastRepo,
    settings,
    updateSettings,
    overlay,
    setOverlay,
  };

  return <SessionContext.Provider value={value}>{children}</SessionContext.Provider>;
}

export function useSession(): SessionContextValue {
  const ctx = useContext(SessionContext);
  if (!ctx) throw new Error('useSession must be used within <SessionProvider>');
  return ctx;
}

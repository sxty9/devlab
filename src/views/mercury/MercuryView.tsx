// MercuryView — ONLY the shell + section routing (B-31; the old monolith is dissolved:
// reference `git show ae5eed5:src/views/MercuryView.tsx`). Sections own their data; every
// view subscribes to the one live stream (W5). The Mercury-wide AI chat docks at the right
// edge and applies its proposals through the same DataSource the sections use (B-37).
//
// The shell carries ONE piece of information of its own: how many DISTURBANCES still demand
// attention. A disturbance is the service reporting a fault about itself — a delivery that never
// happened, an exhausted request budget — and an alarm that only shows once the notices section
// happens to be opened does not reach anybody. So the count sits on the section, visible from every
// other one. Routine operational notes (a restart, an assignment) are recorded but do not nag, so the
// badge stays a real "something is stuck" signal rather than lighting up on ordinary lifecycle chatter.
import { useCallback, useEffect, useMemo, useState } from 'react';
import { getDataSource } from '@/data';
import { LiveProvider, useLiveTopic } from '@/state/live';
import { AxiomTree } from './tree/AxiomTree';
import { CoverageView } from './tree/CoverageView';
import { RunsView } from './tasks/RunsView';
import { TodosView } from './tasks/TodosView';
import { ExecutionsView } from './exec/ExecutionsView';
import { ActiveList } from './exec/ActiveList';
import { DeliveriesView } from './deliveries/DeliveriesView';
import { NoticesPanel } from './NoticesPanel';
import { attentionCount } from './notices';
import GlobalCalendarView from '@/views/GlobalCalendarView';
import MercuryChat from './MercuryChat';

type Section =
  | 'axioms'
  | 'coverage'
  | 'runs'
  | 'todos'
  | 'active'
  | 'executions'
  | 'deliveries'
  | 'calendar'
  | 'notices';

const SECTIONS: { id: Section; label: string }[] = [
  { id: 'axioms', label: 'Constitution' },
  { id: 'coverage', label: 'Coverage' },
  { id: 'runs', label: 'Runs' },
  { id: 'todos', label: 'Todos' },
  { id: 'active', label: 'Active' },
  { id: 'executions', label: 'History' },
  { id: 'deliveries', label: 'Deliveries' },
  { id: 'calendar', label: 'Calendar' },
  { id: 'notices', label: 'Notices' },
];

// The chosen section survives a browser reload (Zustandserhalt): same tab, same view.
const SECTION_KEY = 'mercury.section';

function initialSection(): Section {
  try {
    const stored = window.localStorage.getItem(SECTION_KEY);
    if (SECTIONS.some((s) => s.id === stored)) return stored as Section;
  } catch {
    /* storage unavailable (private mode) — fall through */
  }
  return 'axioms';
}

/** MercuryView mounts the ONE live stream of this surface; everything that subscribes to it must
 *  therefore render BELOW it (a subscription outside the provider is inert by design). */
export function MercuryView() {
  return (
    <LiveProvider>
      <MercurySections />
    </LiveProvider>
  );
}

function MercurySections() {
  const [section, setSectionState] = useState<Section>(initialSection);
  const [chatOpen, setChatOpen] = useState(false);
  // Bumped when the chat applied an action: remounts the active section so it refetches even
  // before the live stream is wired (a tick would trigger the same refetch).
  const [refreshKey, setRefreshKey] = useState(0);
  const attention = useAttentionNotices();

  const setSection = (s: Section) => {
    setSectionState(s);
    try {
      window.localStorage.setItem(SECTION_KEY, s);
    } catch {
      /* best-effort persistence */
    }
  };

  return (
    <div className="flex h-full min-h-0">
      <div className="flex h-full min-h-0 min-w-0 flex-1 flex-col">
        <nav className="flex shrink-0 gap-1 overflow-x-auto border-b border-separator px-3 py-2">
          {SECTIONS.map((s) => (
            <button
              key={s.id}
              type="button"
              onClick={() => setSection(s.id)}
              className={
                'flex items-center gap-1.5 rounded px-2.5 py-1 text-footnote ' +
                (section === s.id ? 'bg-bg-raised text-text-primary' : 'text-text-secondary hover:text-text-primary')
              }
            >
              {s.label}
              {/* What still demands attention is stated where it can be seen, in the tone of an
                  alarm — no number is shown once every disturbance has been read. */}
              {s.id === 'notices' && attention > 0 && (
                <span className="rounded-full bg-danger/15 px-1.5 text-caption font-semibold text-danger">{attention}</span>
              )}
            </button>
          ))}
        </nav>
        <div key={refreshKey} className="min-h-0 flex-1 overflow-auto">
          {section === 'axioms' && <AxiomTree />}
          {section === 'coverage' && <CoverageView />}
          {section === 'runs' && <RunsView />}
          {section === 'todos' && <TodosView />}
          {section === 'active' && <ActiveList />}
          {section === 'executions' && <ExecutionsView />}
          {section === 'deliveries' && <DeliveriesView />}
          {section === 'calendar' && <GlobalCalendarView />}
          {section === 'notices' && <NoticesPanel />}
        </div>
      </div>
      {/* Kept mounted so the conversation survives collapsing; collapsed it is a slim tab. */}
      <MercuryChat open={chatOpen} onOpenChange={setChatOpen} onApplied={() => setRefreshKey((k) => k + 1)} />
    </div>
  );
}

/** How many disturbances still demand attention — read through the ONE notices access point and
 *  counted by the ONE rule the notices surface itself applies (notices.ts), so the badge and the list
 *  can never disagree. It refreshes on the notices topic and asks nothing while nothing happens
 *  (REQ-034); a failed read leaves the last known count instead of inventing a quiet zero. */
function useAttentionNotices(): number {
  const source = useMemo(() => getDataSource(), []);
  const [attention, setAttention] = useState(0);

  const load = useCallback(async () => {
    try {
      const { notices } = await source.mercuryRunNotices();
      setAttention(attentionCount(notices));
    } catch {
      /* keep the last known count — a transient read never clears an alarm */
    }
  }, [source]);

  useEffect(() => {
    void load();
  }, [load]);
  useLiveTopic('notices', () => void load());

  return attention;
}

// MercuryView — ONLY the shell + section routing (B-31; the old monolith is dissolved:
// reference `git show ae5eed5:src/views/MercuryView.tsx`). Sections own their data; every
// view subscribes to the one live stream (W5). B6 fills the shell's real navigation.
import { useState } from 'react';
import { LiveProvider } from '@/state/live';
import { AxiomTree } from './tree/AxiomTree';
import { CoverageView } from './tree/CoverageView';
import { RunsView } from './tasks/RunsView';
import { TodosView } from './tasks/TodosView';
import { ExecutionsView } from './exec/ExecutionsView';
import { ActiveList } from './exec/ActiveList';
import { DeliveriesView } from './deliveries/DeliveriesView';
import { NoticesPanel } from './NoticesPanel';
import GlobalCalendarView from '@/views/GlobalCalendarView';

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

export function MercuryView() {
  const [section, setSection] = useState<Section>('axioms');
  return (
    <LiveProvider>
      <div className="flex h-full min-h-0 flex-col">
        <nav className="flex shrink-0 gap-1 overflow-x-auto border-b border-border-default px-3 py-2">
          {SECTIONS.map((s) => (
            <button
              key={s.id}
              type="button"
              onClick={() => setSection(s.id)}
              className={
                'rounded px-2.5 py-1 text-footnote ' +
                (section === s.id
                  ? 'bg-bg-raised text-text-primary'
                  : 'text-text-secondary hover:text-text-primary')
              }
            >
              {s.label}
            </button>
          ))}
        </nav>
        <div className="min-h-0 flex-1 overflow-auto">
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
    </LiveProvider>
  );
}

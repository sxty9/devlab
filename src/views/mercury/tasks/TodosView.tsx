// TodosView — the concrete-todos surface: the shared TaskSurface (one machinery, REQ-005)
// instantiated for kind=todo. Everything todo-specific — targets incl. new repos (REQ-006),
// media with dialog+clipboard as one access point (REQ-007), the "now"-preselected execution
// moment (REQ-008) and the open-list/history split (REQ-011.2) — lives in the shared building
// blocks, so this surface adds nothing but its labels (no structural twin, B-33).
import { TaskSurface } from './Surface';

export function TodosView() {
  return (
    <TaskSurface
      kind="todo"
      newLabel="New todo"
      buckets={['todo']}
      emptyText="No new todos. Create one for an ad-hoc fix or a new service — running, waiting and stuck todos move to Active, Pending and Blocked."
    />
  );
}

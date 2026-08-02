// PendingView — the Pending surface (WHAT-4): the todos whose work is DONE and delivered and only
// wait for the automated tail — the pull request's merge window, or the production step after the
// merge. It is the shared TaskSurface (one machinery, REQ-005) narrowed to the 'pending' lifecycle
// bucket, so a task waiting here appears in no other tab. Nothing is being implemented and nothing is
// blocked; each row says exactly what it waits on and, where known, until when.
import { TaskSurface } from './Surface';

export function PendingView() {
  return (
    <TaskSurface
      kind="todo"
      newLabel="New todo"
      buckets={['pending']}
      emptyText="Nothing is pending. A delivered todo waits here for its merge window, then for its production step, and moves to History once it runs in production."
    />
  );
}

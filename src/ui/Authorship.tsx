import { cn } from '@/lib/cn';
import { Person } from '@/ui/Person';

/** The created-by / last-edited-by line, composed from the single Person component. "Created" and
 *  "last edited" are kept apart so the original creator stays visible after someone else edits; the
 *  editor is shown only when it actually differs (no redundant "edited by == creator"). An empty
 *  author renders through Person as an explicit "unknown", never a guessed person. */
export function Authorship({
  createdBy,
  updatedBy,
  size = 'sm',
  className,
}: {
  createdBy?: string;
  updatedBy?: string;
  size?: 'sm' | 'md';
  className?: string;
}) {
  const edited = (updatedBy ?? '') !== '' && updatedBy !== createdBy;
  return (
    <div className={cn('flex flex-wrap items-center gap-x-2 gap-y-1 text-caption text-text-tertiary', className)}>
      <span className="inline-flex items-center gap-1.5">
        Created by
        <Person username={createdBy} size={size} />
      </span>
      {edited && (
        <span className="inline-flex items-center gap-1.5">
          · Last edited by
          <Person username={updatedBy} size={size} />
        </span>
      )}
    </div>
  );
}

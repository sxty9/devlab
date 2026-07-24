import { ChevronRightIcon } from './icons';
import { cn } from '@/lib/cn';

/** A path rendered as chevron-separated breadcrumb segments, the last one emphasized. Shared by the
 *  editor and diff headers. `interactive` adds the editor's hover affordance on the leading segments;
 *  the caller owns the surrounding scroll container and any trailing controls. */
export function PathBreadcrumb({ path, interactive = false }: { path: string; interactive?: boolean }) {
  const segments = path.split('/');
  return (
    <>
      {segments.map((seg, i) => {
        const last = i === segments.length - 1;
        return (
          <span key={i} className="flex shrink-0 items-center gap-0.5">
            {i > 0 && <ChevronRightIcon className="h-3 w-3 text-text-tertiary" />}
            <span
              className={cn(
                interactive && 'rounded-sm px-1 py-0.5 transition-colors',
                last ? 'font-medium text-text-secondary' : interactive && 'hover:bg-fill/10 hover:text-text-secondary',
              )}
            >
              {seg}
            </span>
          </span>
        );
      })}
    </>
  );
}

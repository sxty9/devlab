import { type ReactNode } from 'react';

/** Consistent panel title row across all side panels. */
export function PanelHeader({ title, actions }: { title: string; actions?: ReactNode }) {
  return (
    <div className="dl-no-select flex h-9 shrink-0 items-center gap-2 px-3">
      <span className="text-caption font-semibold uppercase tracking-wide text-text-tertiary">{title}</span>
      {actions && <div className="ml-auto flex items-center gap-0.5">{actions}</div>}
    </div>
  );
}

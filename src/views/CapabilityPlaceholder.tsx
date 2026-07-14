/** The holding surface for a capability whose substance is still being consolidated. Temporary: each
 *  capability replaces it with its own view. */
export function CapabilityPlaceholder({
  icon: Icon,
  title,
  note,
}: {
  icon: (p: { className?: string }) => JSX.Element;
  title: string;
  note: string;
}) {
  return (
    <div className="flex min-h-0 flex-1 flex-col items-center justify-center gap-3 bg-bg-base">
      <span className="flex h-14 w-14 items-center justify-center rounded-2xl bg-surface-raised shadow-elev-2 ring-1 ring-separator">
        <Icon className="h-7 w-7 text-accent" />
      </span>
      <h1 className="text-title3 font-semibold tracking-tight text-text-primary">{title}</h1>
      <p className="text-footnote text-text-tertiary">{note}</p>
    </div>
  );
}

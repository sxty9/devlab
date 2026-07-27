import { cn } from '@/lib/cn';
import { describePerson, PERSON_TONES, type Identity, type PersonView } from './person';

type Size = 'sm' | 'md';

const dims: Record<Size, string> = {
  sm: 'h-5 w-5',
  md: 'h-6 w-6',
};

const glyphText: Record<Size, string> = {
  sm: 'text-[9px]',
  md: 'text-[10px]',
};

export interface PersonProps extends Identity {
  size?: Size;
  /** Render only the avatar; the name is exposed as an accessible label for a caller-supplied tooltip. */
  avatarOnly?: boolean;
  /** The label used for the autonomous kind, e.g. "Autonomous run". */
  autonomousLabel?: string;
  /** The label used for the unknown kind, e.g. "Unknown". */
  unknownLabel?: string;
  className?: string;
}

/** The one way to show a person anywhere in DevLab: a derived avatar + name, from a single component —
 *  so a human, an autonomous process, and an author-less record can never diverge into three UIs.
 *  It is labelling only: every viewer sees the same authorship, never a permission gate. */
export function Person({ size = 'md', avatarOnly, autonomousLabel, unknownLabel, className, ...id }: PersonProps) {
  const v = describePerson(id, { autonomousLabel, unknownLabel });
  const avatar = <Avatar view={v} size={size} avatarUrl={id.avatarUrl} />;
  if (avatarOnly) {
    // No text and no own tooltip — the caller supplies the visible label (e.g. TopBar's account tooltip).
    return (
      <span className={cn('inline-flex', className)} aria-label={v.title}>
        {avatar}
      </span>
    );
  }
  return (
    <span className={cn('inline-flex min-w-0 items-center gap-1.5', className)} title={v.title}>
      {avatar}
      <span className={cn('truncate text-footnote', v.kind === 'unknown' ? 'italic text-text-tertiary' : 'text-text-primary')}>
        {v.label}
      </span>
    </span>
  );
}

function Avatar({ view, size, avatarUrl }: { view: PersonView; size: Size; avatarUrl?: string }) {
  const base = cn('flex shrink-0 items-center justify-center font-semibold', dims[size], glyphText[size]);
  if (view.kind === 'autonomous') {
    // A distinct SHAPE (rounded square, not a circle) plus a bot glyph: unmistakably not a person.
    return (
      <span className={cn(base, 'rounded-md bg-fill/15 text-text-secondary')} aria-hidden>
        <BotGlyph />
      </span>
    );
  }
  if (view.kind === 'unknown') {
    return (
      <span className={cn(base, 'rounded-full border border-dashed border-separator bg-fill/5 text-text-tertiary')} aria-hidden>
        ?
      </span>
    );
  }
  if (avatarUrl) {
    return <img src={avatarUrl} alt="" className={cn('shrink-0 rounded-full object-cover', dims[size])} />;
  }
  return (
    <span className={cn(base, 'rounded-full', PERSON_TONES[view.tone])} aria-hidden>
      {view.initials}
    </span>
  );
}

/** A minimal robot-head mark for the autonomous kind (antenna + two eyes). */
function BotGlyph() {
  return (
    <svg viewBox="0 0 16 16" className="h-3 w-3" fill="none" stroke="currentColor" strokeWidth="1.4" strokeLinecap="round" strokeLinejoin="round">
      <rect x="3.5" y="6" width="9" height="6.5" rx="1.5" />
      <line x1="8" y1="6" x2="8" y2="3.6" />
      <circle cx="8" cy="3" r="0.9" fill="currentColor" stroke="none" />
      <circle cx="6.3" cy="9.3" r="0.85" fill="currentColor" stroke="none" />
      <circle cx="9.7" cy="9.3" r="0.85" fill="currentColor" stroke="none" />
    </svg>
  );
}

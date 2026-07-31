// The one way DevLab names a person. Identity is the central Holistic identity: a Linux username,
// optionally a human display name (the OS gecos field) and — when the landscape ever exposes one — an
// avatar image. DevLab keeps NONE of its own: it only renders what the landscape already knows. There
// is no avatar service in the landscape today, so the picture is DERIVED deterministically from the
// identity (initials on a stable hue) — a rendering of the identity, never a stored or invented image.
//
// Three kinds of actor share this single shape, so no view invents its own way to show a person:
//   person      — a human (username, maybe a display name)
//   autonomous  — a non-human process (a scheduled run, the runner); never shown AS a person
//   unknown     — a record with no recorded author; shown explicitly as unknown, never guessed
//
// The whole decision lives in describePerson() — pure, so it is unit-tested directly (person.test.ts).

export type PersonKind = 'person' | 'autonomous' | 'unknown';

/** An actor to display. An absent/blank username with no display name is unknown (unless autonomous). */
export interface Identity {
  username?: string;
  displayName?: string;
  /** Set when the landscape exposes a real avatar image; absent ⇒ a derived initials avatar. */
  avatarUrl?: string;
  /** A non-human process (a scheduled run / the runner). Rendered distinctly, never as a person. */
  autonomous?: boolean;
}

/** The resolved, render-ready view of an identity — the single source both the component and the
 *  tests read. */
export interface PersonView {
  kind: PersonKind;
  /** The visible name. */
  label: string;
  /** Two-char avatar initials (for the derived avatar); '' for the autonomous kind. */
  initials: string;
  /** Index into the identity-hue palette — stable per username, so a person keeps one colour. */
  tone: number;
  /** The full hover title (e.g. "Alice Ng (@alice)"). */
  title: string;
  avatarUrl?: string;
}

/** Two-letter initials from a display name (or username) — the single implementation, replacing the
 *  copies that had drifted into TopBar and CommentsThread. */
export function initials(name: string): string {
  const parts = name.trim().split(/\s+/).filter(Boolean);
  if (parts.length === 0) return '?';
  if (parts.length === 1) return parts[0].slice(0, 2).toUpperCase();
  return (parts[0][0] + parts[parts.length - 1][0]).toUpperCase();
}

/** Neutral per-identity hues (the hostek metric colours). Semantic colours (danger/success/warning)
 *  are deliberately excluded — a person's avatar must never read as an error or a status. Full class
 *  strings, so Tailwind's JIT emits them (a `bg-${x}` template would not compile). */
export const PERSON_TONES = [
  'bg-accent/20 text-accent',
  'bg-gpu/20 text-gpu',
  'bg-net/20 text-net',
  'bg-ssd/20 text-ssd',
  'bg-ram/20 text-ram',
  'bg-cpu/20 text-cpu',
] as const;

/** A tiny stable string hash → a palette index, so the same username always gets the same hue. */
function toneOf(seed: string): number {
  let h = 0;
  for (let i = 0; i < seed.length; i++) h = (h * 31 + seed.charCodeAt(i)) | 0;
  return Math.abs(h) % PERSON_TONES.length;
}

/** Resolve an identity into a render-ready view. Pure and unit-tested, because it encodes the three
 *  non-negotiable rules: an autonomous actor is never a person; an author-less record is unknown, not
 *  guessed; and a known person is shown by name with a stable, deterministic avatar. */
export function describePerson(
  id: Identity,
  opts?: { autonomousLabel?: string; unknownLabel?: string },
): PersonView {
  if (id.autonomous) {
    const label = opts?.autonomousLabel ?? 'Autonomous';
    return { kind: 'autonomous', label, initials: '', tone: 0, title: `${label} — no person` };
  }
  const username = (id.username ?? '').trim();
  const displayName = (id.displayName ?? '').trim();
  if (!username && !displayName) {
    const label = opts?.unknownLabel ?? 'Unknown';
    return { kind: 'unknown', label, initials: '?', tone: 0, title: `${label} — no author recorded` };
  }
  const label = displayName || username;
  const title =
    displayName && username && displayName !== username
      ? `${displayName} (@${username})`
      : username
        ? `@${username}`
        : displayName;
  return { kind: 'person', label, initials: initials(label), tone: toneOf(username || label), avatarUrl: id.avatarUrl, title };
}

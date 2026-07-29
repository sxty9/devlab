import type { Repo } from '@/types';

// Static class maps so Tailwind's JIT scanner sees every utility literally.
export const tintBg: Record<Repo['tint'], string> = {
  accent: 'bg-accent',
  success: 'bg-success',
  warning: 'bg-warning',
  gpu: 'bg-gpu',
  net: 'bg-net',
  ssd: 'bg-ssd',
  ram: 'bg-ram',
};

export const tintText: Record<Repo['tint'], string> = {
  accent: 'text-accent',
  success: 'text-success',
  warning: 'text-warning',
  gpu: 'text-gpu',
  net: 'text-net',
  ssd: 'text-ssd',
  ram: 'text-ram',
};

/** Soft tinted background (channel-based colors support the /opacity modifier). */
export const tintSoftBg: Record<Repo['tint'], string> = {
  accent: 'bg-accent/15',
  success: 'bg-success/15',
  warning: 'bg-warning/15',
  gpu: 'bg-gpu/15',
  net: 'bg-net/15',
  ssd: 'bg-ssd/15',
  ram: 'bg-ram/15',
};

/** A semantic status tone. Distinct from the Repo channel tints above (a hardware-style palette):
 *  these are the meanings a status/stage chip carries anywhere in the app — a stage's outcome, a
 *  delivery's lifecycle, a pass/fail marker. */
export type BadgeTone = 'success' | 'accent' | 'warning' | 'danger' | 'neutral';

/** Tone → soft background + matching text for every status pill. ONE definition, so a stage badge,
 *  a delivery badge and a pass/fail pill can never drift into three palettes (B-42). */
export const badgeTone: Record<BadgeTone, string> = {
  success: 'bg-success/15 text-success',
  accent: 'bg-accent/15 text-accent',
  warning: 'bg-warning/15 text-warning',
  danger: 'bg-danger/15 text-danger',
  neutral: 'bg-fill/15 text-text-tertiary',
};

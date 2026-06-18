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

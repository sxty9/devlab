// deliveries.ts — PURE presentation logic of the deliveries surface (B9 fills; testable
// without React): sorting incl. missing completion time, stage badge mapping, reversal
// pairing. No client-side stage derivation — the server array is the truth (B-17).
import type { Delivery } from '@/types';

/** Sort deliveries newest first; records without a completion time keep their creation
 *  order — never dropped, never NaN-sorted (the old black-screen trap). */
export function sortDeliveries(list: Delivery[]): Delivery[] {
  return [...list].sort((a, b) => (b.createdAt || '').localeCompare(a.createdAt || ''));
}

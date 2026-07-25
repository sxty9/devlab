/** sxgate preview hostnames for this Holistic instance.
 *
 *  The instance's base domain is never hardcoded (see the "no instance specifics" axiom): it is
 *  resolved from runtime config — an explicit `VITE_PREVIEW_BASE_DOMAIN`, else derived from where
 *  the app is served (`devlab.<domain>` ⇒ `<domain>`), else a neutral placeholder for non-host
 *  contexts (e.g. dev on localhost). */
function instanceBaseDomain(): string {
  const configured = import.meta.env.VITE_PREVIEW_BASE_DOMAIN as string | undefined;
  if (configured) return configured;
  const host = typeof window !== 'undefined' ? window.location.hostname : '';
  const labels = host.split('.');
  if (labels.length >= 3) return labels.slice(1).join('.'); // strip the service label
  return 'holistic.local';
}

/** Hostname of a repo's sxgate preview: `preview-<repo>.<instance-domain>`. */
export const previewHost = (repo: string): string => `preview-${repo}.${instanceBaseDomain()}`;

/** The live sxgate preview deployment of this very app (DevLab). Phase-1 "Preview"/"Deploy" actions
 *  open it so the buttons are real, not dead. */
export const PREVIEW_URL = `https://${previewHost('devlab')}`;

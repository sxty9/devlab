// The ONE derivation of this Holistic instance's own addresses (B-41). Every surface that has to
// name a sibling — the dashboard the session comes from, the AI service's settings, the running dev
// deployment of a repository — reads it from here, so no view derives the instance host a second
// time and no instance is baked into the code ("no instance specifics").
//
// Everything below is derived from where the app is actually served, at runtime: `devlab-dev.<zone>`
// tells us the zone (`<zone>`) AND the environment (`-dev`), and both are carried to the siblings so
// a jump stays inside its own environment: `devlab-dev.<zone>` reaches `holistic-dev.<zone>`, never
// the production `holistic.<zone>`. An explicit build-time override wins where the deployment does
// not follow that shape, and a host that carries no zone at all (localhost, a dev port) degrades to
// same-origin paths instead of inventing a domain.

/** The zone all services of this instance live under: `devlab-dev.<zone>` ⇒ `<zone>`. Only the
 *  first label (this service, together with any environment marker on it) is dropped; the rest is
 *  the zone, shared by every environment.
 *
 *  `VITE_INSTANCE_BASE_DOMAIN` overrides it for deployments whose host does not carry the service
 *  as its first label. "" means the current host carries no zone (a bare hostname such as
 *  `localhost`); callers then stay on the current origin rather than guessing one. */
export function instanceBaseDomain(): string {
  // `?.` because this module is also exercised outside a bundler, where there is no injected env.
  const configured = import.meta.env?.VITE_INSTANCE_BASE_DOMAIN as string | undefined;
  if (configured) return configured.trim();
  const host = typeof window === 'undefined' ? '' : window.location.hostname;
  const labels = host.split('.').filter(Boolean);
  return labels.length >= 3 ? labels.slice(1).join('.') : '';
}

/** The environment marker that decorates every sibling host of THIS deployment — `-dev` on a dev
 *  instance served from `devlab-dev.<zone>`, empty on production served from `devlab.<zone>`. It is
 *  what keeps a jump inside its own environment instead of landing in production.
 *
 *  WHERE IT COMES FROM. `VITE_INSTANCE_ENV` names it explicitly (`prod`/"" ⇒ none, `dev` ⇒ `-dev`),
 *  which is the honest source: it states the environment rather than guessing it. Where that config
 *  is absent we fall back to the host's own first label — the environment travels there as a suffix
 *  after the service id (`devlab-dev` ⇒ `-dev`), and we KEEP whatever the host carries. This is a
 *  convention, not a law, so it can misread a service id that itself contains a hyphen — but it can
 *  never do the one thing that matters: it never turns a decorated dev host into a bare production
 *  one. An unrecognised marker (`devlab-staging`) stays named (`-staging`); it does not collapse to
 *  production. The only truly unknown host — one with no zone — is handled by the callers below,
 *  which stay same-origin rather than address any sibling at all. */
export function instanceEnvSuffix(): string {
  const configured = import.meta.env?.VITE_INSTANCE_ENV as string | undefined;
  if (configured !== undefined) {
    const env = configured.trim().replace(/^-+/, '');
    return env === '' || env === 'prod' ? '' : `-${env}`;
  }
  const host = typeof window === 'undefined' ? '' : window.location.hostname;
  const firstLabel = host.split('.').filter(Boolean)[0] ?? '';
  const hyphen = firstLabel.indexOf('-');
  return hyphen === -1 ? '' : firstLabel.slice(hyphen);
}

/** The current protocol, defaulting to https where there is no document (SSR, tests). */
function scheme(): string {
  return typeof window === 'undefined' ? 'https:' : window.location.protocol;
}

/** The origin of the Holistic dashboard, in THIS deployment's environment: `holistic-dev.<zone>`
 *  from a dev instance, `holistic.<zone>` from production — never crossing from one to the other.
 *  Without a zone the dashboard is addressed on the current origin (single-host dev), and with no
 *  window at all as a bare path — a relative link is always safe, a guessed host is not. */
export function holisticOrigin(): string {
  if (typeof window === 'undefined') return '/';
  const zone = instanceBaseDomain();
  return zone ? `${scheme()}//holistic${instanceEnvSuffix()}.${zone}` : window.location.origin;
}

/** The dashboard tab of one Holistic service — the ONE way this app deep-links a sibling service
 *  (its UI lives in the dashboard, never on a host of its own). */
export function serviceTabUrl(serviceId: string): string {
  const origin = holisticOrigin();
  return `${origin === '/' ? '' : origin}/app/${encodeURIComponent(serviceId)}`;
}

/** The AI service's own surface, where its models and budgets are configured. */
export const aigenticUrl = (): string => serviceTabUrl('aigentic');

/** Where the running deployment of one repository is used: a service delivered by DevLab is
 *  operated as its own tab in this instance's dashboard (that is what the delivery installs and
 *  routes). This is the STATIC access — it names WHERE the service lives, and claims nothing about
 *  it being up: what attests a deployment is the delivery ledger and the execution stages. */
export const devServiceUrl = (repo: string): string => serviceTabUrl(repo);

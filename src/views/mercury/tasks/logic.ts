// Pure task/run logic shared by the RunsView and TodosView surfaces (one machinery, REQ-005)
// and by ui/RunTuning. No React, no DOM, no data source — everything here is unit-tested with
// node --test (logic.test.ts). Time-dependent helpers take the clock as an argument.
import type { Repo, Run, RunSchedule, RunTarget, RunTuning } from '../../../types';

/** Uniform error-to-string, mirroring the rest of the Mercury surface. */
export const errMsg = (e: unknown): string => String((e as Error)?.message ?? e);

// ── Schedule ──────────────────────────────────────────────────────────────────

// Weekdays are stored in the Go convention (0=Sun … 6=Sat) but always DISPLAYED Mon→Sun.
export const WEEKDAYS: { num: number; label: string }[] = [
  { num: 1, label: 'Mon' },
  { num: 2, label: 'Tue' },
  { num: 3, label: 'Wed' },
  { num: 4, label: 'Thu' },
  { num: 5, label: 'Fri' },
  { num: 6, label: 'Sat' },
  { num: 0, label: 'Sun' },
];

/** A human schedule line: `daily 03:00` or `weekly Mon, Thu · 03:00`. */
export function scheduleSummary(s: RunSchedule | undefined | null): string {
  if (!s) return '—';
  if (s.kind === 'daily') return `daily ${s.timeOfDay}`;
  const days = WEEKDAYS.filter((w) => s.weekdays?.includes(w.num)).map((w) => w.label);
  if (days.length === 0) return `weekly · ${s.timeOfDay}`;
  return `weekly ${days.join(', ')} · ${s.timeOfDay}`;
}

/** The category of an axiom, derived from its scheme path (drop the namespace and the file). */
export function categoryLabel(path: string): string {
  const segs = path.split('/').filter(Boolean);
  return segs.slice(1, -1).join(' / ');
}

// ── Due date (REQ-008) ────────────────────────────────────────────────────────

const pad = (n: number) => String(n).padStart(2, '0');

/** ISO → the `YYYY-MM-DDTHH:mm` a datetime-local input wants, in the viewer's timezone. */
export function isoToLocalInput(iso?: string): string {
  if (!iso) return '';
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return '';
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}`;
}

/** A datetime-local value (local time, no offset) → ISO; '' → null, i.e. "on demand only". */
export function localInputToIso(v: string): string | null {
  if (!v) return null;
  const d = new Date(v);
  if (Number.isNaN(d.getTime())) return null;
  return d.toISOString();
}

/** The earliest moment the picker allows: the given clock's current minute. A todo runs once at
 *  its due moment, so a moment already in the past would never fire — the form forbids it. */
export function nowLocalInput(now: Date = new Date()): string {
  return isoToLocalInput(now.toISOString());
}

/** A sensible default due moment for a scheduled todo: the next full hour — always comfortably
 *  in the future of `now` (REQ-008.2). */
export function defaultDueLocalInput(now: Date = new Date()): string {
  const d = new Date(now);
  d.setHours(d.getHours() + 1, 0, 0, 0);
  return isoToLocalInput(d.toISOString());
}

/** How a todo runs. `now` executes it the instant it is saved (REQ-008.1 — the preselected
 *  option of a FRESH todo), `scheduled` fires it once at a chosen moment, `ondemand` only ever
 *  runs via "Run now". */
export type ExecutionMode = 'now' | 'scheduled' | 'ondemand';

export const EXECUTION_MODES: { id: ExecutionMode; label: string }[] = [
  { id: 'now', label: 'Now' },
  { id: 'scheduled', label: 'Scheduled' },
  { id: 'ondemand', label: 'On demand' },
];

/** The mode a todo editor opens in: a fresh todo preselects "now" (REQ-008.1); editing reflects
 *  the stored plan — a due date is "scheduled", none is "ondemand" ("now" is a save-time action,
 *  never a stored state). */
export function initialExecutionMode(base: Pick<Run, 'dueAt'> | null): ExecutionMode {
  if (!base) return 'now';
  return base.dueAt ? 'scheduled' : 'ondemand';
}

/** Whether the chosen schedule is invalid: only "scheduled" needs a moment, and it must not lie
 *  before `min`. Both strings are zero-padded `YYYY-MM-DDTHH:mm`, so a lexicographic compare is
 *  a chronological one (REQ-008.2: past moments are not selectable). */
export function scheduleInvalid(mode: ExecutionMode, due: string, min: string): boolean {
  if (mode !== 'scheduled') return false;
  return due === '' || due < min;
}

// ── Targets (REQ-006) ─────────────────────────────────────────────────────────

/** A new repo becomes a GitHub repo and an install-allowlist key; the backend bounds it the
 *  same way. */
export const NEW_REPO_RE = /^[A-Za-z0-9._-]{1,100}$/;

/** One editable target row: an existing repo, or a new one to create. */
export interface TargetRow {
  kind: 'existing' | 'new';
  repo: string;
  newRepo: string;
}

/** The target rows a todo opens with: its stored targets, else one empty existing-repo row. */
export function initialTargetRows(base: Pick<Run, 'targets'> | null): TargetRow[] {
  const targets = base?.targets ?? [];
  if (targets.length === 0) return [{ kind: 'existing', repo: '', newRepo: '' }];
  return targets.map((t) =>
    t.create ? { kind: 'new', repo: '', newRepo: t.repo } : { kind: 'existing', repo: t.repo, newRepo: '' },
  );
}

export const targetRowValid = (r: TargetRow): boolean =>
  r.kind === 'existing' ? r.repo.trim().length > 0 : NEW_REPO_RE.test(r.newRepo.trim());

/** Rows → the wire targets. Every row maps; the backend re-validates and collapses duplicates. */
export function toRunTargets(rows: TargetRow[]): RunTarget[] {
  return rows.map((r) =>
    r.kind === 'existing' ? { repo: r.repo.trim() } : { repo: r.newRepo.trim(), create: true },
  );
}

/** The repo choices a target picker offers: EVERY repo of the instance, sorted for scanning —
 *  none is ever dropped (REQ-006.3). */
export function repoOptions(repos: Repo[]): Repo[] {
  return [...repos].sort((a, b) => a.name.localeCompare(b.name));
}

/** One line naming a todo's destinations ("—" when none). */
export function targetLabel(run: Pick<Run, 'targets'>, repos: Repo[]): string {
  const ts = run.targets ?? [];
  if (ts.length === 0) return '—';
  return ts
    .map((t) => (t.create ? `new: ${t.repo}` : (repos.find((r) => r.id === t.repo)?.name ?? t.repo)))
    .join(', ');
}

// ── Attachments (REQ-007) — the ONE ingest pipeline for dialog AND clipboard ──

/** A single-file cap that matches the backend (25 MiB). */
export const MAX_ATTACHMENT_BYTES = 25 * 1024 * 1024;
/** A todo is a focused task, not a media library — matches the backend ceiling. */
export const MAX_ATTACHMENTS = 20;

export interface IngestCandidate {
  name: string;
  size: number;
}

export type IngestRejection =
  | { name: string; reason: 'too-many' }
  | { name: string; reason: 'too-large' }
  | { name: string; reason: 'duplicate-name' };

export interface IngestPlan<T extends IngestCandidate> {
  accepted: T[];
  rejected: IngestRejection[];
}

/** Decide which picked/pasted files may attach — the SAME pipeline whether the files came from
 *  the file dialog or the clipboard (REQ-007.2: one access point, two input paths). Applies the
 *  count cap, the per-file size cap and case-insensitive name dedup against what is already
 *  attached; content-level SHA dedup is the backend's half of the contract. */
export function planAttachmentIngest<T extends IngestCandidate>(takenNames: string[], files: T[]): IngestPlan<T> {
  const taken = new Set(takenNames.map((n) => n.toLowerCase()));
  let count = takenNames.length;
  const plan: IngestPlan<T> = { accepted: [], rejected: [] };
  for (const f of files) {
    if (count >= MAX_ATTACHMENTS) {
      plan.rejected.push({ name: f.name, reason: 'too-many' });
      continue;
    }
    if (f.size > MAX_ATTACHMENT_BYTES) {
      plan.rejected.push({ name: f.name, reason: 'too-large' });
      continue;
    }
    if (taken.has(f.name.toLowerCase())) {
      plan.rejected.push({ name: f.name, reason: 'duplicate-name' });
      continue;
    }
    taken.add(f.name.toLowerCase());
    count += 1;
    plan.accepted.push(f);
  }
  return plan;
}

/** True for a MIME the browser can show inline as an image thumbnail. */
export const isImageMime = (m?: string): boolean => !!m && m.startsWith('image/');

// ── Time budget (REQ-010) — the third tunable's presentation values ───────────

/** Parse a Go duration string ("3h", "1h30m", "3h0m0s") into milliseconds; null when it is not
 *  one. Mirrors Go's unit set; alternation order keeps "ms" from reading as minutes+stray. */
export function parseGoDuration(v: string): number | null {
  let s = v.trim();
  if (s === '') return null;
  let sign = 1;
  if (s.startsWith('+')) s = s.slice(1);
  else if (s.startsWith('-')) {
    sign = -1;
    s = s.slice(1);
  }
  if (s === '0') return 0;
  const re = /(\d+(?:\.\d+)?)(ns|us|µs|ms|s|m|h)/y;
  let ms = 0;
  let idx = 0;
  while (idx < s.length) {
    re.lastIndex = idx;
    const m = re.exec(s);
    if (!m) return null;
    const n = parseFloat(m[1]);
    const unit = m[2];
    ms +=
      unit === 'h' ? n * 3_600_000
      : unit === 'm' ? n * 60_000
      : unit === 's' ? n * 1000
      : unit === 'ms' ? n
      : unit === 'us' || unit === 'µs' ? n / 1000
      : n / 1e6;
    idx = re.lastIndex;
  }
  return sign * ms;
}

/** Render a duration in ms as a compact human label ("3h", "1h 30m", "45m", "no budget" for 0). */
export function budgetLabel(ms: number): string {
  if (ms <= 0) return 'no budget';
  const h = Math.floor(ms / 3_600_000);
  const m = Math.round((ms % 3_600_000) / 60_000);
  if (h > 0 && m > 0) return `${h}h ${m}m`;
  if (h > 0) return `${h}h`;
  if (m > 0) return `${m}m`;
  return `${Math.round(ms / 1000)}s`;
}

/** The label for a STORED budget value: '' refers to the service default (named when known,
 *  REQ-010 — the surface names the governing budget), "0s" is explicitly no budget. */
export function storedBudgetLabel(v: string | undefined, defaultBudget?: string): string {
  if (!v) {
    const d = defaultBudget ? parseGoDuration(defaultBudget) : null;
    return d !== null && d > 0 ? `Default (${budgetLabel(d)})` : 'Default';
  }
  const ms = parseGoDuration(v);
  return ms === null ? v : budgetLabel(ms);
}

/** The budget ladder a run/todo may pick: default reference, common caps, and "no budget"
 *  (REQ-010.3). Values are Go duration strings — exactly what the wire carries. */
export const BUDGET_CHOICES: { value: string; label: string }[] = [
  { value: '30m', label: '30m' },
  { value: '1h', label: '1h' },
  { value: '2h', label: '2h' },
  { value: '3h', label: '3h' },
  { value: '6h', label: '6h' },
  { value: '12h', label: '12h' },
  { value: '0s', label: 'No budget' },
];

/** The compact chips summarizing a run's EXPLICIT tuning (empty fields refer to the defaults
 *  and are not shown — an untouched run shows nothing, a choice is recognizable, REQ-010.2). */
export function tuningChips(t: RunTuning | undefined): string[] {
  if (!t) return [];
  const chips: string[] = [];
  if (t.model) chips.push(t.modelVersion ? `${t.model} · ${t.modelVersion}` : t.model);
  if (t.effort) chips.push(t.effort);
  if (t.timeBudget !== undefined && t.timeBudget !== '') chips.push(storedBudgetLabel(t.timeBudget));
  return chips;
}

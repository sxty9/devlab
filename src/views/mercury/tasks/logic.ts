// Pure task/run logic shared by the RunsView and TodosView surfaces (one machinery, REQ-005)
// and by ui/RunTuning. No React, no DOM, no data source — everything here is unit-tested with
// node --test (logic.test.ts). Time-dependent helpers take the clock as an argument.
import type {
  Repo,
  Run,
  RunPlan,
  RunProposal,
  RunProposalAction,
  RunProposalKind,
  RunSchedule,
  RunTarget,
  RunTuning,
} from '../../../types';
import type { BadgeTone } from '../../../ui/tint';
import type { OpenTodoState } from './select';

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

/** The one axiom rule of a recurring run, as the server states it (mercury.RunLacksRequiredAxioms):
 *  a run that will be ACTIVE must carry at least one axiom — it would otherwise execute the
 *  constitution against nothing — while an INACTIVE one is a definition that never fires and is
 *  therefore storable without any. A form stricter than the server refuses to save what the server
 *  accepts, and that is how a run imported without axioms becomes uneditable: axioms reach it only
 *  through the assignment, so run and assignment would wait for each other forever. */
export function runLacksRequiredAxioms(active: boolean, axiomIds: string[]): boolean {
  return active && axiomIds.length === 0;
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

/** The pill an open todo shows so its state reads at a glance, without a tooltip (the "intuitive by
 *  design" axiom): a moving amber for the run in flight, a calm blue for the timed wait on a merge,
 *  a still amber for the delivery held for a release, red for the failed one that needs a person,
 *  grey for the untouched. Running pulses; nothing else does — so the moving one is never confused
 *  with the held one. */
export function openTodoStatePill(state: OpenTodoState): { label: string; tone: BadgeTone; pulse?: boolean } {
  switch (state.kind) {
    case 'not-run':
      return { label: 'not run yet', tone: 'neutral' };
    case 'running':
      return { label: 'running', tone: 'warning', pulse: true };
    case 'awaiting-merge':
      return { label: 'awaiting merge', tone: 'accent' };
    case 'awaiting-prod':
      // The work merged and is waiting for (or retrying) its production step — a calm blue, because
      // it clears itself; not amber, because nothing is blocked and the user is asked nothing.
      return { label: state.retrying ? 'production retrying' : 'awaiting production', tone: 'accent' };
    case 'blocked':
      return { label: 'blocked', tone: 'warning' };
    case 'failed':
      return { label: 'failed', tone: 'danger' };
  }
}

/** The extra line an open-todo state adds under its row when it has something specific to say: the
 *  deadline the awaiting state waits on (formatted by the caller's clock), or the reason a delivery
 *  is blocked. Empty for the states whose pill already says everything. `at` formats a timestamp. */
export function openTodoStateNote(state: OpenTodoState, at: (iso: string) => string): string {
  if (state.kind === 'awaiting-merge') {
    return state.mergeBy ? `Merges automatically after ${at(state.mergeBy)}` : 'Merges automatically once its window elapses';
  }
  if (state.kind === 'awaiting-prod') {
    if (state.retrying) {
      return state.reason
        ? `Merged — the production delivery is retrying: ${state.reason}`
        : 'Merged — the production delivery is retrying by itself';
    }
    return 'Merged — waiting to run in production (the last step of the chain)';
  }
  if (state.kind === 'blocked') {
    return state.reason ? `Blocked: ${state.reason}` : 'Blocked — waits for a release';
  }
  return '';
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

// ── AI proposals (S6): what the surface may say about an analysis it did not wait for ────────

/** The proposals the surface currently knows about, one per kind. */
export type ProposalStates = Record<RunProposalKind, RunProposal | null>;

/** The subset of the data seam this logic needs — the two access points of the proposals. */
export interface ProposalAccess {
  mercuryRunAiFill(action?: RunProposalAction): Promise<RunProposal>;
  mercuryRunAiFinetune(action?: RunProposalAction): Promise<RunProposal>;
}

/** What the surface may show for one analysis. Deliberately three separate answers, because the
 *  three claims must never blur into each other: work is under way (and can be abandoned), a
 *  proposal is really there to review, or it failed FOR A NAMED REASON. */
export interface ProposalPresentation {
  /** Work is under way — say so, offer to abandon it, and claim nothing else. */
  busy: boolean;
  /** The plan to review — set ONLY when a result really is there. */
  ready: RunPlan | null;
  /** The named reason of a failure; never set while work is under way. */
  failure: string | null;
}

const NO_PROPOSAL: ProposalPresentation = { busy: false, ready: null, failure: null };

/** Reads one analysis into what the surface may claim about it. An analysis that says `completed`
 *  without carrying a plan is NOT reported as ready — "finished" without a result is exactly the
 *  claim that must never be made. */
export function presentProposal(p: RunProposal | null | undefined): ProposalPresentation {
  if (!p) return NO_PROPOSAL;
  switch (p.state) {
    case 'running':
      return { busy: true, ready: null, failure: null };
    case 'completed':
      return p.proposal ?
          { busy: false, ready: p.proposal, failure: null }
        : { busy: false, ready: null, failure: 'The analysis ended without a proposal.' };
    case 'failed':
      return { busy: false, ready: null, failure: p.reason?.trim() || 'The AI analysis ended without a stated reason.' };
    default:
      return NO_PROPOSAL;
  }
}

/** Whether ANY analysis is under way (both buttons wait for the one model call in flight). */
export function anyProposalBusy(states: ProposalStates): boolean {
  return presentProposal(states.fill).busy || presentProposal(states.finetune).busy;
}

/** The one access of one kind. The kind→operation dispatch lives HERE, once, so no surface has to
 *  spell it out again for each of the three things one may do with a proposal. */
export function accessProposal(
  source: ProposalAccess,
  kind: RunProposalKind,
  action: RunProposalAction,
): Promise<RunProposal> {
  return kind === 'fill' ? source.mercuryRunAiFill(action) : source.mercuryRunAiFinetune(action);
}

/** Reads both analyses WITHOUT starting one. This is what a surface does when it mounts — after a
 *  reload, after coming back from another view, and on every live tick: it must show what is
 *  running or waiting for review, and must never set a model call off by itself. */
export async function readProposals(source: ProposalAccess): Promise<ProposalStates> {
  const [fill, finetune] = await Promise.all([
    accessProposal(source, 'fill', 'read'),
    accessProposal(source, 'finetune', 'read'),
  ]);
  return { fill, finetune };
}

/** Ask for an analysis of this kind. The access point never restarts by itself: a request that
 *  meets a failed analysis reports THAT failure, so a caller which can only ask can never set off
 *  model pass after model pass while being told "running" every time. Starting over is therefore a
 *  deliberate act of two steps — put the failure aside, then ask — and pressing the button while a
 *  failure is on screen IS that act. `current` is what the surface last knew about this kind. */
export async function requestProposal(
  source: ProposalAccess,
  kind: RunProposalKind,
  current: RunProposal | null,
): Promise<RunProposal> {
  if (presentProposal(current).failure !== null) await accessProposal(source, kind, 'cancel');
  return accessProposal(source, kind, 'request');
}

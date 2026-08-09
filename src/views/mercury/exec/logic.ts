// Pure presentation logic of the execution & delivery surface (S13) — no React, no DOM, no data
// source, so every rule below is unit-tested with node --test (logic.test.ts).
//
// The ONE law of this surface: the server's typed stage array IS the truth (B-17/B-35). Nothing
// here derives a stage, guesses a step name or reconstructs a pipeline shape — it maps
// server-stated values onto labels, tones and orders. That is why no chain stage name appears as
// a literal in this module: a stage's label is its own server-given name, so archive executions
// with historical stage names render exactly as they were recorded.
import type {
  Backoff,
  ExecutionView,
  PauseView,
  RepoPipeline,
  RunResult,
  SlotOverview,
  StageView,
  StartOutcome,
  StepState,
  TaskState,
} from '@/types';
import type { BadgeTone } from '@/ui/tint';

/** Uniform error-to-string, mirroring the rest of the Mercury surface. */
export const errMsg = (e: unknown): string => String((e as Error)?.message ?? e);

// ── Step states (REQ-030.5) ───────────────────────────────────────────────────

/** How one step state is shown. `dot` is the visual mark that makes the four terminal states
 *  distinguishable WITHOUT reading the label (REQ-030.5): filled green, filled red, a hollow ring
 *  and a dim fill; the two transient states pulse or outline. `pulse` marks the live state. */
export interface StepBadge {
  label: string;
  dot: string;
  pill: string;
  pulse?: boolean;
}

/** The step-state map — total over the wire enum, so no state can render as a blank chip. */
export const STEP_BADGE: Record<StepState, StepBadge> = {
  running: { label: 'running', dot: 'bg-warning', pill: 'border-warning/40 bg-warning/10 text-warning', pulse: true },
  pending: { label: 'pending', dot: 'bg-transparent ring-1 ring-inset ring-warning/60', pill: 'border-warning/25 bg-warning/[0.06] text-warning/90' },
  executed: { label: 'executed', dot: 'bg-success', pill: 'border-success/30 bg-success/10 text-success' },
  failed: { label: 'failed', dot: 'bg-danger', pill: 'border-danger/30 bg-danger/10 text-danger' },
  'not-applicable': {
    label: 'not applicable',
    dot: 'bg-transparent ring-1 ring-inset ring-text-tertiary',
    pill: 'border-separator bg-surface text-text-tertiary',
  },
  // The dim mark uses the neutral FILL channel, not the text token: only channel-based colours
  // carry Tailwind's /opacity modifier — on a var()-valued token the declaration is dropped and
  // the mark would silently disappear.
  'not-executed': { label: 'not executed', dot: 'bg-fill/40', pill: 'border-separator bg-fill/[0.04] text-text-tertiary' },
};

/** The badge of one recorded stage. An unknown state (a record from a future or foreign writer)
 *  still yields a DEFINED chip — a history entry never renders a black screen (REQ-037.5). */
export function stageBadge(sv: Pick<StageView, 'state'>): StepBadge {
  return STEP_BADGE[sv.state] ?? { label: String(sv.state || 'unknown'), dot: 'bg-fill/40', pill: 'border-separator bg-surface text-text-tertiary' };
}

/** The four states a step ENDS in — the legend of the surface. */
export const TERMINAL_STATES: StepState[] = ['executed', 'failed', 'not-applicable', 'not-executed'];

/** A stage's visible name: the server's own stage string, only made readable. Legacy archive
 *  stages carry their historical names and therefore keep them here (no name coupling, B-35). */
export function stageLabel(stage: string): string {
  const s = String(stage ?? '').trim();
  if (s === '') return 'stage';
  return s.replace(/[-_]+/g, ' ');
}

/** Whether this stage still carries something to read: its report/transcript, its short skip
 *  reason, its evidence or a link. Drives whether the badge is inspectable at all. */
export function stageHasDetail(sv: StageView): boolean {
  return !!(sv.log || sv.reason || sv.evidence || sv.link);
}

// ── Task states (REQ-020.4) ───────────────────────────────────────────────────

/** The preflight observation, named. `implemented-undelivered` gets its OWN wording — it is the
 *  state that earns the one-grip delivery (REQ-020.4), never a silent "failed". */
export const TASK_STATE: Record<TaskState, { label: string; tone: BadgeTone }> = {
  'not-implemented': { label: 'not implemented', tone: 'neutral' },
  'implemented-undelivered': { label: 'implemented, not delivered', tone: 'warning' },
  delivered: { label: 'delivered', tone: 'success' },
  unknown: { label: 'state unknown', tone: 'neutral' },
};

/** True exactly for the state whose remaining work is the delivery alone (REQ-020.4). */
export const needsDelivery = (t?: TaskState): boolean => t === 'implemented-undelivered';

// ── Repo pipelines ────────────────────────────────────────────────────────────

/** One repo row's overall marker — read off the SERVER's done/succeeded plus its own block; a
 *  repo that is neither done nor blocked is still in flight. */
export function repoOutcome(rp: RepoPipeline): { label: string; tone: BadgeTone; pulse: boolean } {
  if (rp.block) return { label: 'blocked', tone: 'danger', pulse: false };
  if (!rp.done) return { label: 'running', tone: 'warning', pulse: true };
  return rp.succeeded ? { label: 'succeeded', tone: 'success', pulse: false } : { label: 'failed', tone: 'danger', pulse: false };
}

/** The stages of a repo, always a list. A QUEUED execution carries no stage yet and an archived
 *  record may carry none either; the wire has sent `null` for both. Reading through this one helper
 *  keeps a missing list meaning "no stage yet" instead of throwing — one such record used to take
 *  the entire active view down with it (REQ-037.5, which had only been pinned for the history). */
export function stagesOf(rp: RepoPipeline | null | undefined): StageView[] {
  return Array.isArray(rp?.stages) ? rp.stages : [];
}

/** The stage a repo is working right now (the first running one), or null. */
export function runningStage(rp: RepoPipeline): StageView | null {
  return stagesOf(rp).find((s) => s.state === 'running') ?? null;
}

/** A blocked repo's honest line: reason, attempt count and when the next try is due
 *  (REQ-032.4 — repository, reason, attempts, resumable from there). */
export function blockSummary(b: Backoff): string {
  const parts = [b.reason || b.class || 'blocked'];
  if (b.attempts > 0) parts.push(`${b.attempts} ${b.attempts === 1 ? 'attempt' : 'attempts'}`);
  return parts.join(' · ');
}

// ── Pauses (REQ-016.2) ────────────────────────────────────────────────────────

/** A paused execution states its REASON and its resume attempts — never a bare "paused"
 *  (REQ-016.2). The usage-limit pause is collective, the deferral is the user's own. */
export function pauseSummary(p: PauseView): string {
  const reason = p.reason === 'usage-limit' ? 'usage limit reached — all executions pause together' : 'deferred to free the slot';
  const parts = [p.message || reason];
  if (p.resumeAttempts > 0) parts.push(`${p.resumeAttempts} ${p.resumeAttempts === 1 ? 'resume attempt' : 'resume attempts'}`);
  return parts.join(' · ');
}

// ── Execution phase & outcome ─────────────────────────────────────────────────

/** A live execution's phase, named. `interrupted` is the state a process death leaves behind: it
 *  is shown as interrupted and continued — never as an endless "running" (REQ-039.1). */
const PHASE_BADGE: Record<ExecutionView['phase'], { label: string; tone: BadgeTone; pulse?: boolean }> = {
  created: { label: 'created', tone: 'neutral' },
  queued: { label: 'queued', tone: 'accent' },
  running: { label: 'running', tone: 'warning', pulse: true },
  paused: { label: 'paused', tone: 'accent' },
  blocked: { label: 'blocked', tone: 'danger' },
  interrupted: { label: 'interrupted', tone: 'danger' },
  completed: { label: 'completed', tone: 'success' },
  failed: { label: 'failed', tone: 'danger' },
  discarded: { label: 'discarded', tone: 'neutral' },
};

export function phaseBadge(phase: ExecutionView['phase']): { label: string; tone: BadgeTone; pulse?: boolean } {
  return PHASE_BADGE[phase] ?? { label: String(phase || 'unknown'), tone: 'neutral' };
}

/** Whether a record carries no step detail at all: it names repositories, but every one of them has
 *  an EMPTY stage array. Such a record cannot be judged — there is nothing that ran, failed or was
 *  skipped to read. */
export function withoutStageDetail(res: RunResult): boolean {
  return res.repos.length > 0 && res.repos.every((rp) => stagesOf(rp).length === 0);
}

/** A recorded execution's outcome, derived ONLY from server-stated fields: still running while it
 *  has no end stamp; otherwise succeeded ⇔ every repo is done and succeeded. An execution without
 *  repositories is honestly "nothing to do", never a green success.
 *
 *  A record without any step detail gets its OWN state instead of the derived "incomplete": that
 *  label claims the work stopped half-way, which is a statement about steps nobody recorded. An
 *  imported record whose source knew a single outcome for the whole run is the everyday case, and
 *  the honest answer is to name the gap rather than to paint it red. */
export function executionOutcome(res: RunResult): { label: string; tone: BadgeTone; pulse: boolean } {
  if (!res.endedAt) return { label: 'running', tone: 'warning', pulse: true };
  if (res.repos.length === 0) return { label: 'no repositories', tone: 'neutral', pulse: false };
  if (res.repos.some((rp) => rp.block)) return { label: 'blocked', tone: 'danger', pulse: false };
  const done = res.repos.every((rp) => rp.done);
  if (!done) {
    return withoutStageDetail(res)
      ? { label: 'no step detail', tone: 'neutral', pulse: false }
      : { label: 'incomplete', tone: 'danger', pulse: false };
  }
  return res.repos.every((rp) => rp.succeeded)
    ? { label: 'succeeded', tone: 'success', pulse: false }
    : { label: 'failed', tone: 'danger', pulse: false };
}

/** Whether a recorded execution can be triggered again from the history — no corpses (REQ-037.2),
 *  and no control into the void either (REQ-040.3). Two conditions, both necessary:
 *
 *   1. it ended without every repository succeeding — a running one is not re-triggered from the
 *      history, a fully succeeded one has nothing to repeat; and
 *   2. its RUN still exists. Results outlive the run they came from by contract, so a deleted run —
 *      and an imported record that never had a definition — leaves a result with nothing left to
 *      start. The caller states which run ids the pool still holds; without that answer nothing is
 *      offered, because a missing answer is never taken for a yes. */
export function retriable(res: RunResult, existingRunIds: ReadonlySet<string>): boolean {
  if (!res.endedAt) return false;
  if (!existingRunIds.has(res.runId)) return false;
  return res.repos.length === 0 || !res.repos.every((rp) => rp.done && rp.succeeded);
}

/** The provenance chips of a stored execution — archive and reconciliation are stated, never
 *  disguised as an ordinary run. */
export function provenanceChips(res: RunResult): string[] {
  const out: string[] = [];
  if (res.legacy) out.push('archive');
  if (res.synthetic) out.push('reconciled');
  return out;
}

// ── History ordering & membership ─────────────────────────────────────────────

/** The instant a stored execution is ordered by: its end, else its start (REQ-037.3 — a record
 *  without a completion time falls back to its beginning). NaN can never leak: an unparseable or
 *  missing pair yields 0, so such a record sorts last instead of vanishing. */
export function executionAt(res: RunResult): number {
  for (const iso of [res.endedAt, res.startedAt]) {
    if (!iso) continue;
    const t = Date.parse(iso);
    if (!Number.isNaN(t)) return t;
  }
  return 0;
}

/** Stored executions, newest first — stable, so records that share an instant (or carry no usable
 *  time at all) keep the server's order instead of shuffling per render. */
export function sortExecutions(list: RunResult[]): RunResult[] {
  return [...list]
    .map((res, i) => ({ res, i, at: executionAt(res) }))
    .sort((a, b) => (b.at !== a.at ? b.at - a.at : a.i - b.i))
    .map((x) => x.res);
}

// The MEMBERSHIP rule of the history — an execution enters it only once it ended AND every
// delivery of it is settled (merged, rolled back, or closed with a reason; B-8, REQ-037.1) — is
// deliberately NOT restated in this module. ExecutionsView applies the very selector the task
// surfaces apply (tasks/select.ts: openDeliveryExecutionIds + executionCompleted) and orders the
// result with sortExecutions above, so the open list and the history can never disagree.

// ── Start outcomes (REQ-015/018/020) ─────────────────────────────────────────

/** What actually happened on a start — every branch of the wire outcome, named. `queued` is
 *  checked BEFORE the refusal text because the restart gate answers with BOTH (queued behind a
 *  pending restart, with the reason as its explanation): a queued start is a start, not a
 *  refusal. `contended` is the refusal a slot decision can still resolve (a defer suggestion or
 *  a full floor) — the only branch that opens the start dialog. */
export type StartClass = 'started' | 'resumed' | 'queued' | 'contended' | 'refused';

export interface StartVerdict {
  kind: StartClass;
  title: string;
  detail?: string;
}

export function classifyStartOutcome(o: StartOutcome): StartVerdict {
  if (o.resumed) {
    return { kind: 'resumed', title: o.started ? 'Execution resumed' : 'Resumed — queued for a free slot' };
  }
  if (o.started) return { kind: 'started', title: o.fresh ? 'Fresh execution started' : 'Execution started' };
  if (o.queued) {
    return {
      kind: 'queued',
      title: 'Queued',
      detail: o.notStarted || (o.restartPending ? 'A restart is pending — the start fires afterwards.' : 'Waiting for a free slot.'),
    };
  }
  if (o.suggestion) {
    return { kind: 'contended', title: 'Slots are contended', detail: o.notStarted || 'All slots are occupied.' };
  }
  return {
    kind: 'refused',
    title: 'Not started',
    detail: withEvidence(o.notStarted || 'The start was refused without a reason.', o.taskEvidence),
  };
}

/** Appends the per-repo observation a refusal rests on, so an "already delivered" answer names the
 *  delivered stand the user can check against the todo's current text — a bare "already delivered"
 *  is not verifiable. Empty evidence changes nothing. */
function withEvidence(detail: string, evidence?: Record<string, string[]>): string {
  const repos = Object.keys(evidence ?? {});
  if (!evidence || repos.length === 0) return detail;
  const lines = repos
    .flatMap((repo) => (evidence[repo] ?? []).map((e) => `${repo}: ${e}`))
    .filter(Boolean);
  return lines.length === 0 ? detail : `${detail}\n\n${lines.join('\n')}`;
}

/** The slot placements a contended start may choose, given the server's own slot projection.
 *  `queue` is always possible. `defer` needs something running to set aside. `overload` is the
 *  server's cap-block branch only: one temporary extra slot at most, so it is offered while no
 *  overload lives and the floor is genuinely full — it never crosses repository exclusivity, and
 *  a refusal on that ground comes back named. */
export function placementOptions(ov: SlotOverview | null): { queue: true; defer: boolean; overload: boolean } {
  const running = ov?.active.filter((a) => a.phase === 'running') ?? [];
  return {
    queue: true,
    defer: running.length > 0,
    overload: !!ov && !ov.overloadActive && ov.occupied >= ov.capacity,
  };
}

// ── Durations & consumption ───────────────────────────────────────────────────

/** A coarse human duration ("8 s", "3 min", "1 h 12 min"). */
export function fmtDuration(ms: number): string {
  const s = Math.max(0, Math.floor(ms / 1000));
  const h = Math.floor(s / 3600);
  const m = Math.floor((s % 3600) / 60);
  if (h > 0) return `${h} h ${m} min`;
  if (m > 0) return `${m} min`;
  return `${s} s`;
}

/** How long ago an instant was; '—' when there is no usable one. */
export function fmtSince(iso: string | undefined, now: number = Date.now()): string {
  const t = iso ? Date.parse(iso) : NaN;
  return Number.isNaN(t) ? '—' : fmtDuration(now - t);
}

/** How far ahead an instant lies; 'now' once it has passed. */
export function fmtUntil(iso: string | undefined, now: number = Date.now()): string {
  const t = iso ? Date.parse(iso) : NaN;
  if (Number.isNaN(t)) return '—';
  return t - now <= 0 ? 'now' : `in ${fmtDuration(t - now)}`;
}

/** Repos finished / total of one live execution — from the server-stated done flags. */
export function repoProgress(v: ExecutionView): string {
  const total = v.repos.length;
  const done = v.repos.filter((rp) => rp.done).length;
  return total > 0 ? `${done}/${total} repos` : 'no repositories yet';
}

/** The consumption line: input, output and the monetary equivalent — informative, NEVER a cap and
 *  never a remaining budget (REQ-017). Empty when nothing has been consumed yet. */
export function usageParts(u: { inputTokens: number; outputTokens: number; costUsd: number } | undefined): {
  input: number;
  output: number;
  costUsd: number;
  any: boolean;
} {
  const input = u?.inputTokens ?? 0;
  const output = u?.outputTokens ?? 0;
  const costUsd = u?.costUsd ?? 0;
  return { input, output, costUsd, any: input > 0 || output > 0 || costUsd > 0 };
}

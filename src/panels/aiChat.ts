// The KI panel's decisions, separated from its rendering so they can be tested without a DOM:
// which engines and agent modes exist, how a model is named in the picker, how an answer is marked
// with the model that produced it (labeling duty, D 26), and the transcript cap.

/** The engines the panel offers. `claude-agent` is DevLab's own agentic route (the user's CLI in
 *  their workspace); every other value is an aigentic router kind. */
export const ENGINES: { id: string; label: string }[] = [
  { id: 'choose', label: 'Auto (router)' },
  { id: 'claude-agent', label: 'Claude agent (repo)' },
  { id: 'claude-cli', label: 'Claude Code' },
  { id: 'claude-api', label: 'Claude API' },
  { id: 'ollama', label: 'Ollama (local)' },
];

/** The agentic engine runs the FULL claude CLI in the workspace, as the user — it edits files. */
export const AGENT_MODES: { id: string; label: string }[] = [
  { id: 'plan', label: 'Plan (read only)' },
  { id: 'auto', label: 'Auto (edit files)' },
  { id: 'full', label: 'Full (autonomous)' },
];

/** The engine id of the agentic route. */
export const AGENT_ENGINE = 'claude-agent';

/** How many turns a transcript keeps — the same cap the store enforces, so the panel never sends
 *  more than can be kept (one number, one meaning). */
export const TRANSCRIPT_CAP = 500;

/** Trim a transcript to its newest turns at the cap. */
export function capTranscript<T>(msgs: readonly T[], cap: number = TRANSCRIPT_CAP): T[] {
  return msgs.length > cap ? msgs.slice(msgs.length - cap) : [...msgs];
}

/** Fold the model's version into its option label: the catalog's friendly name plus the version
 *  encoded in the model id (claude-opus-4-8 → "4.8", claude-fable-5 → "5"), so the picker shows
 *  WHICH version it selects instead of a bare family name. Labels that already carry the version,
 *  and ids that have none (e.g. an Ollama tag), are returned untouched. */
export function modelLabel(m: { id: string; label: string }): string {
  const parts = m.id.replace(/^claude-/, '').split('-');
  const ver: string[] = [];
  for (let i = 1; i < parts.length && /^\d{1,2}$/.test(parts[i]); i++) ver.push(parts[i]);
  const v = ver.join('.');
  return v && !m.label.includes(v) ? `${m.label} ${v}` : m.label;
}

/** The mark an answer carries: the model that produced it, else the engine that answered, else
 *  nothing. A model name is never invented — an answer nobody labelled stays unmarked, which is
 *  visible as the absence of a mark rather than as a guess. */
export function answerMark(reply: { model?: string; engine?: string }): string {
  const model = (reply.model ?? '').trim();
  if (model) return model;
  return (reply.engine ?? '').trim();
}

/** Which model options an engine offers. Auto/router leaves the model to aigentic, so it offers
 *  none; ollama offers its local tags; the claude engines offer the claude catalog. */
export function modelsForEngine(
  engine: string,
  catalog: { claude: { id: string; label: string }[]; ollama: string[] } | null,
): { id: string; label: string }[] {
  if (!catalog) return [];
  if (engine === 'ollama') return catalog.ollama.map((m) => ({ id: m, label: m }));
  if (engine === 'claude-cli' || engine === 'claude-api' || engine === AGENT_ENGINE) return catalog.claude;
  return [];
}

/** The note appended to an agentic answer when the run changed files, so the edits are never
 *  silent. Returns '' when nothing changed. */
export function changeNote(changed: number): string {
  if (changed <= 0) return '';
  return `\n\n_(${changed} ${changed === 1 ? 'file' : 'files'} changed — open Version Control to review)_`;
}

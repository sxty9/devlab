// stubSource — DEFINED EMPTY STATES for offline development (B-24). Deliberately NOT a
// behavior clone of the backend: reads return empty-but-valid shapes, writes reject with a
// named error, events() is null (the live provider then falls back to a gentle poll).
import type { DataSource, InitResult } from './source';

const offline = () => Promise.reject(new Error('Offline stub: writes are not available'));

export const stubSource: DataSource = {
  async init(): Promise<InitResult> {
    return { mode: 'stub', signedIn: true, canUseDevlab: true, githubLinked: true };
  },
  // Offline there is no session to re-mint — "not refreshed" is the honest answer, never a
  // pretended success that would make a caller retry forever.
  async refreshSession() {
    return false;
  },
  async getUser() {
    return { username: 'dev', displayName: 'Dev (offline)', isAdmin: false, canUseDevlab: true, githubLinked: true };
  },
  async repos() {
    return [];
  },
  async repoData() {
    return {
      branches: [], tree: [], files: {}, changes: [], commits: [], worktrees: [], vision: [],
      claude: [], terminal: [], stages: [], defaultTabs: [], activeTabId: '', structure: [],
    };
  },
  async fileContent(_id, path) {
    return { path, lang: 'plaintext', code: '' };
  },
  async fileDiff() {
    return { before: '', after: '', lang: 'plaintext' };
  },
  githubAuthorizeUrl: () => '/api/github/authorize',
  unlinkGitHub: offline,
  ensureRepo: offline,
  saveFile: offline,
  stage: offline,
  unstage: offline,
  commit: offline,
  push: offline,
  pull: offline,
  createBranch: offline,
  checkout: offline,
  openPR: offline,
  async vision() {
    return [];
  },
  rawUrl: () => '',
  uploadVision: offline,
  deleteVision: offline,
  async listComments() {
    return [];
  },
  addComment: offline,
  deleteComment: offline,
  askAssistant: offline,
  askAgent: offline,
  async getAssistantHistory() {
    return [];
  },
  saveAssistantHistory: offline,
  async assistantModels() {
    return { claude: [], ollama: [] };
  },
  terminalUrl: () => null,
  async atlas() {
    return { nodes: [], findings: [], scannedAt: '' };
  },
  async atlasPorts() {
    return [];
  },
  async mercuryTree() {
    return { axiome: [], regeln: [], laeufe: [], meta: [] };
  },
  mercuryItem: offline,
  mercuryAddAxiom: offline,
  mercuryOptimize: offline,
  mercuryConform: offline,
  mercuryEditAxiom: offline,
  mercuryMoveAxiom: offline,
  mercuryDeleteAxiom: offline,
  mercuryMoveCategory: offline,
  mercuryReorder: offline,
  async mercuryRuns() {
    return { runs: [], axioms: {} };
  },
  mercuryRun: offline,
  async mercuryRunPrompt() {
    return { prompt: '' };
  },
  async mercuryRunCoverage() {
    return { covered: {}, index: {}, axioms: {} };
  },
  async mercuryRunNotices() {
    return { notices: [] };
  },
  mercuryDismissRunNotice: offline,
  mercuryClearRunNotices: offline,
  mercuryCreateRun: offline,
  mercuryUpdateRun: offline,
  mercuryDeleteRun: offline,
  mercuryRunAssign: offline,
  mercuryRunAiFill: offline,
  mercuryRunAiFinetune: offline,
  mercuryApplyRunProposal: offline,
  async mercuryRunHistory() {
    return { snapshots: [] };
  },
  mercuryRestoreRunHistory: offline,
  async mercuryRunResults() {
    return { results: [] };
  },
  mercuryRunResult: offline,
  async mercuryRunCalendar() {
    const now = new Date().toISOString();
    return { from: now, to: now, occurrences: [] };
  },
  async mercuryRunExecutions() {
    return { executions: [] };
  },
  async mercuryReportStatus() {
    return { records: [] };
  },
  mercuryResumeReportDelivery: offline,
  mercuryChat: offline,
  mercuryRunNow: offline,
  async mercuryRunActive() {
    return {
      active: [],
      restart: { pending: false, requestedBy: { user: '' }, requestedAt: '', deadline: '' },
    };
  },
  async mercurySlots() {
    return {
      capacity: 0, occupied: 0, overloadActive: false, restartPending: false,
      active: [], deferred: [], queuedStarts: [],
    };
  },
  mercuryCancelRun: offline,
  mercuryDeferRun: offline,
  mercuryResumeRun: offline,
  async mercuryDeliveries() {
    return [];
  },
  mercuryRollbackDelivery: offline,
  mercuryResumeDelivery: offline,
  mercuryRepoReset: offline,
  mercuryUploadAttachment: offline,
  mercuryDeleteAttachment: offline,
  mercuryAttachmentRawUrl: () => '',
  events: () => null,
  async serviceConfig() {
    return { maxConcurrency: 0, defaultTimeBudget: '0s', automergeWindow: '0s' };
  },
  async serviceStorage() {
    return { pools: [], totalBytes: 0 };
  },
  async serviceTelemetry() {
    return { cpuPercent: 0, rssBytes: 0, goroutines: 0 };
  },
  async serviceAiUsage() {
    return { windowHours: 0, samples: 0, totals: { inputTokens: 0, outputTokens: 0, costUsd: 0 }, bySource: {} };
  },
};

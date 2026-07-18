import type { DataSource } from './source';
import { mockSource } from './mockSource';
import { httpSource } from './httpSource';

export type {
  DataSource,
  DiffPayload,
  InitResult,
  WriteResult,
  CommitResult,
  PushResult,
  BranchResult,
} from './source';
export { AuthRequiredError } from './source';

// VITE_DATA_SOURCE=mock forces offline mock; =api forces the backend; unset = try the backend
// and transparently fall back to mock when it's unreachable (so `vite dev` works offline).
const forced = import.meta.env.VITE_DATA_SOURCE as 'api' | 'mock' | undefined;

let impl: DataSource = forced === 'mock' ? mockSource : httpSource;

/** The active data source. `init()` resolves api-vs-mock and caches the choice. */
export const dataSource: DataSource = {
  async init() {
    if (forced === 'mock') return mockSource.init();
    const res = await httpSource.init();
    impl = res.mode === 'mock' && forced !== 'api' ? mockSource : httpSource;
    return res;
  },
  getUser: () => impl.getUser(),
  repos: () => impl.repos(),
  repoData: (id, b) => impl.repoData(id, b),
  fileContent: (id, p) => impl.fileContent(id, p),
  fileDiff: (id, p) => impl.fileDiff(id, p),
  githubAuthorizeUrl: () => impl.githubAuthorizeUrl(),
  unlinkGitHub: () => impl.unlinkGitHub(),
  ensureRepo: (id) => impl.ensureRepo(id),
  saveFile: (id, p, c) => impl.saveFile(id, p, c),
  stage: (id, p) => impl.stage(id, p),
  unstage: (id, p) => impl.unstage(id, p),
  commit: (id, m) => impl.commit(id, m),
  push: (id) => impl.push(id),
  pull: (id) => impl.pull(id),
  createBranch: (id, n, f) => impl.createBranch(id, n, f),
  checkout: (id, n) => impl.checkout(id, n),
  openPR: (id, title, body) => impl.openPR(id, title, body),
  vision: (id) => impl.vision(id),
  rawUrl: (id, p) => impl.rawUrl(id, p),
  uploadVision: (id, p, c) => impl.uploadVision(id, p, c),
  deleteVision: (id, p) => impl.deleteVision(id, p),
  listComments: (id, p) => impl.listComments(id, p),
  addComment: (id, p, b, parent) => impl.addComment(id, p, b, parent),
  deleteComment: (id, cid) => impl.deleteComment(id, cid),
  askAssistant: (id, ask) => impl.askAssistant(id, ask),
  askAgent: (id, ask) => impl.askAgent(id, ask),
  getAssistantHistory: (id) => impl.getAssistantHistory(id),
  saveAssistantHistory: (id, msgs) => impl.saveAssistantHistory(id, msgs),
  assistantModels: () => impl.assistantModels(),
  terminalUrl: (id) => impl.terminalUrl(id),
  atlas: () => impl.atlas(),
  mercuryTree: () => impl.mercuryTree(),
  mercuryItem: (path) => impl.mercuryItem(path),
  mercuryAddAxiom: (titel, body, section, force) => impl.mercuryAddAxiom(titel, body, section, force),
  mercuryOptimize: (titel, body, section) => impl.mercuryOptimize(titel, body, section),
  mercuryConform: (titel, body) => impl.mercuryConform(titel, body),
  mercuryEditAxiom: (path, titel, body) => impl.mercuryEditAxiom(path, titel, body),
  mercuryMoveAxiom: (from, to) => impl.mercuryMoveAxiom(from, to),
  mercuryDeleteAxiom: (path) => impl.mercuryDeleteAxiom(path),
  mercuryMoveCategory: (from, to) => impl.mercuryMoveCategory(from, to),
  mercuryReorder: (category, order) => impl.mercuryReorder(category, order),
  mercuryRuns: () => impl.mercuryRuns(),
  mercuryRun: (id) => impl.mercuryRun(id),
  mercuryRunPrompt: (id) => impl.mercuryRunPrompt(id),
  mercuryRunCoverage: () => impl.mercuryRunCoverage(),
  mercuryCreateRun: (body) => impl.mercuryCreateRun(body),
  mercuryUpdateRun: (id, body) => impl.mercuryUpdateRun(id, body),
  mercuryDeleteRun: (id) => impl.mercuryDeleteRun(id),
  mercuryRecomposeRun: (id) => impl.mercuryRecomposeRun(id),
  mercuryRunAiFill: () => impl.mercuryRunAiFill(),
  mercuryRunAiFinetune: () => impl.mercuryRunAiFinetune(),
  mercuryApplyRunProposal: (mode, plan) => impl.mercuryApplyRunProposal(mode, plan),
  mercuryRunHistory: () => impl.mercuryRunHistory(),
  mercuryRestoreRunHistory: (ts) => impl.mercuryRestoreRunHistory(ts),
  mercuryRunResults: (id) => impl.mercuryRunResults(id),
  mercuryRunResult: (id, resultId) => impl.mercuryRunResult(id, resultId),
  mercuryRunCalendar: (days, type) => impl.mercuryRunCalendar(days, type),
  mercuryRunExecutions: () => impl.mercuryRunExecutions(),
  mercuryChat: (messages) => impl.mercuryChat(messages),
  mercuryRunNow: (id) => impl.mercuryRunNow(id),
  mercuryCancelRun: () => impl.mercuryCancelRun(),
};

export function getDataSource(): DataSource {
  return dataSource;
}

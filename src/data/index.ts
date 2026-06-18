import type { DataSource } from './source';
import { mockSource } from './mockSource';
import { httpSource } from './httpSource';

export type { DataSource, DiffPayload, InitResult } from './source';
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
  login: (pw) => impl.login(pw),
  repos: () => impl.repos(),
  repoData: (id, b) => impl.repoData(id, b),
  fileContent: (id, p) => impl.fileContent(id, p),
  fileDiff: (id, p) => impl.fileDiff(id, p),
};

export function getDataSource(): DataSource {
  return dataSource;
}

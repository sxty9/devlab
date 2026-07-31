// The data-source umbrella: httpSource is the real seam; stubSource serves defined empty
// states offline (B-24 — no behavior clone). VITE_DATA_SOURCE=stub forces the stub; =api
// forces the backend; unset probes the backend once and falls back to the stub when it is
// unreachable so `vite dev` works offline.
import type { DataSource } from './source';
import { stubSource } from './stubSource';
import { httpSource } from './httpSource';

export type {
  DataSource,
  DiffPayload,
  InitResult,
  WriteResult,
  CommitResult,
  PushResult,
  BranchResult,
  StartPlacement,
} from './source';
export { AuthRequiredError } from './source';

const forced = import.meta.env.VITE_DATA_SOURCE as 'api' | 'stub' | undefined;

let impl: DataSource = forced === 'stub' ? stubSource : httpSource;

/** The active data source. `init()` resolves api-vs-stub and caches the choice. */
export const dataSource: DataSource = new Proxy({} as DataSource, {
  get(_t, prop: keyof DataSource) {
    if (prop === 'init') {
      return async () => {
        if (forced === 'stub') return stubSource.init();
        const res = await httpSource.init();
        impl = res.mode === 'stub' && forced !== 'api' ? stubSource : httpSource;
        return res;
      };
    }
    return (impl as unknown as Record<string, unknown>)[prop];
  },
});

export function getDataSource(): DataSource {
  return dataSource;
}

import type { FileContent, Repo, RepoData, User } from '@/types';

export interface DiffPayload {
  before: string;
  after: string;
  lang: string;
}

export interface InitResult {
  /** 'api' when the Go backend is reachable; 'mock' when offline (dev fallback). */
  mode: 'api' | 'mock';
  /** true when a valid Holistic session is present on this origin (the SSO cookie). */
  signedIn: boolean;
  /** true when the signed-in user holds hp_devlab_access (or is admin). */
  canUseDevlab: boolean;
}

/** The single seam between the UI and its data. mockSource serves bundled mock data;
 *  httpSource talks to the devlabd /api. The UI components are agnostic to which is active. */
export interface DataSource {
  init(): Promise<InitResult>;
  getUser(): Promise<User>;
  repos(): Promise<Repo[]>;
  repoData(id: string, branch?: string): Promise<RepoData>;
  fileContent(id: string, path: string): Promise<FileContent>;
  fileDiff(id: string, path: string): Promise<DiffPayload>;
}

/** Thrown by httpSource when the backend returns 401 (login required / expired). */
export class AuthRequiredError extends Error {
  constructor() {
    super('Authentication required');
    this.name = 'AuthRequiredError';
  }
}

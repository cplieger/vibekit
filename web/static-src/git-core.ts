// ---------------------------------------------------------------------------
// Git core: shared interface between git.ts and git-render.ts to break
// the circular import. git.ts registers the implementations at init time;
// git-render.ts calls through these references.
// ---------------------------------------------------------------------------

import type { GitStatusData, GitPostResult } from "./git-types.js";

export interface GitCoreFns {
  gitPost: (endpoint: string, body: Record<string, unknown>) => Promise<GitPostResult>;
  selectedRepoKey: () => string;
  getStatusData: () => GitStatusData | null;
  refreshGitStatus: () => void;
}

const fns: GitCoreFns = {
  gitPost: () => Promise.resolve({ error: "git-core not initialized" }),
  selectedRepoKey: () => "",
  getStatusData: () => null,
  refreshGitStatus: () => {},
};

export function registerGitCore(impl: GitCoreFns): void {
  fns.gitPost = impl.gitPost;
  fns.selectedRepoKey = impl.selectedRepoKey;
  fns.getStatusData = impl.getStatusData;
  fns.refreshGitStatus = impl.refreshGitStatus;
}

export function gitPost(endpoint: string, body: Record<string, unknown>): Promise<GitPostResult> {
  return fns.gitPost(endpoint, body);
}

export function selectedRepoKey(): string {
  return fns.selectedRepoKey();
}

export function getStatusData(): GitStatusData | null {
  return fns.getStatusData();
}

export function refreshGitStatus(): void {
  fns.refreshGitStatus();
}

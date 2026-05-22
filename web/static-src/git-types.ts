// ---------------------------------------------------------------------------
// Git panel shared types. Extracted from git.ts so that git-render.ts can
// import types without creating a circular dependency back to git.ts.
// ---------------------------------------------------------------------------

export interface GitFileEntry {
  path: string;
  status: string;
  staged: boolean;
  display: string;
}

export interface GitStatusData {
  is_repo: boolean;
  branch: string;
  ahead: number;
  behind: number;
  files: GitFileEntry[];
  remote: string;
  has_gh: boolean;
  stashes: number;
  has_dirty: boolean;
}

/** Discriminated union representing the git panel's view state.
 *  Transitions: no-repo → loading → ready (or not-git). */
export type GitViewState =
  | { status: "no-repo" }
  | { status: "not-git"; repoKey: string }
  | { status: "loading"; repoKey: string }
  | { status: "ready"; repoKey: string; data: GitStatusData };

export interface GitPostResult {
  output?: string;
  error?: string;
}

/** Unified git error classification table. Each rule maps a substring
 *  match to a user-friendly message. Rules with `triggerAuth: true`
 *  additionally signal that the clone flow should prompt for gh auth. */
export interface GitErrorRule {
  match: string;
  message: string;
  /** When true, the match triggers the gh-auth flow instead of just showing text. */
  triggerAuth?: boolean;
}

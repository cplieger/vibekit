// ---------------------------------------------------------------------------
// Shared git wire types — single source of truth for interfaces that
// appear in the /api/git/* and /api/forges/* JSON responses.
// ---------------------------------------------------------------------------

import type { ForgeKind } from "./forge-types.js";

/** A single file entry from git status. */
export interface GitFileEntry {
  path: string;
  status: string;
  staged: boolean;
  display: string;
}

/** Per-repo status from /api/git/status-all. */
export interface GitRepoStatus {
  repo: string;
  is_repo: boolean;
  branch: string;
  remote: string;
  ahead: number;
  behind: number;
  files: GitFileEntry[];
  has_dirty: boolean;
  stashes: number;
}

/** Subset of GitRepoStatus used by the badge (no files array needed). */
export type GitRepoStatusBadge = Pick<
  GitRepoStatus,
  "repo" | "is_repo" | "branch" | "ahead" | "behind" | "has_dirty"
>;

/** A pull request from the forge API. */
export interface GitPR {
  number: number;
  title: string;
  state: string;
  draft?: boolean;
  mergeable?: boolean;
  source_branch: string;
  target_branch: string;
  url?: string;
  author?: string;
  created_at?: number;
  updated_at?: number;
}

/** A group of PRs for a single repo (used in the PRs tab). */
export interface GitRepoGroup {
  forge_id: string;
  forge_kind: ForgeKind;
  forge_host: string;
  owner: string;
  name: string;
  full_name: string;
  prs: GitPR[];
  error?: string;
}

// --- Status label utilities (single source of truth) ---

export const GIT_STATUS_LABELS: Readonly<Record<string, string>> = {
  M: "Modified",
  A: "Added",
  D: "Deleted",
  R: "Renamed",
  "?": "Untracked",
  U: "Unmerged",
};

export function statusLetter(s: string): string {
  if (s.length >= 1) {
    return s.charAt(0);
  }
  return "?";
}

export function describeStatus(s: string): string {
  return GIT_STATUS_LABELS[s.charAt(0)] ?? s;
}
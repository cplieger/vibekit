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

/** A pull request from the forge API.
 *
 *  `check_status` and `merge_blocked` are plain strings rather than
 *  unions on purpose: the server's vocabulary can grow, and a union here
 *  would make the unknown-value fallbacks in git-pr-status.ts read as
 *  dead code to the type checker while the wire still produces them. The
 *  canonical values are listed on each field. */
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
  /** Head commit of the source branch; the merge pins itself to this. */
  head_sha?: string;
  /** "" (forge reported none) | "pending" | "passing" | "failing". */
  check_status?: string;
  /** "" | "draft" | "conflicts" | "checks_failing" | "checks_running"
   *  | "behind" | "blocked" | "unknown". */
  merge_blocked?: string;
  checks_total?: number;
  checks_failing?: number;
  /** The forge will merge this itself once its requirements are met. */
  auto_merge_armed?: boolean;
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

const GIT_STATUS_LABELS: Readonly<Record<string, string>> = {
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

/** How many of these entries `git stash push` would actually take.
 *
 *  It is NOT `files.length`. The server runs `stash push` with no `-u`
 *  (internal/git/handlers_sync.go), so an untracked file is not stashed, while
 *  the status parse runs `-uall` and therefore DOES report untracked entries
 *  (status `?`). A tree whose only changes are new files is `has_dirty: true`
 *  and yet git answers "No local changes to save" — so the two questions
 *  genuinely have different answers and only this one gates the Stash control.
 *
 *  Here rather than at the call site because the rule is about git's status
 *  vocabulary, which this module owns, not about how the git panel lays out. */
export function stashableCount(files: readonly GitFileEntry[]): number {
  return files.filter((f) => statusLetter(f.status) !== "?").length;
}

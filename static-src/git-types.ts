// ---------------------------------------------------------------------------
// Shared git wire types — single source of truth for interfaces that
// appear in the /api/git/* and /api/forges/* JSON responses.
// ---------------------------------------------------------------------------

import type { ForgeKind } from "./forge-types.js";

/** A single file entry from git status.
 *
 *  ONE PATH CAN PRODUCE TWO ENTRIES. A file staged and then modified
 *  again in the worktree arrives once per side of the index (server:
 *  internal/git/parse.go appendStatusEntries), so stage, unstage and
 *  discard can each act on one side. Anything counting CHANGED FILES
 *  therefore counts distinct paths — `changedPathCount`, never
 *  `files.length`.
 *
 *  `orig_path` rides only a rename or copy entry, carrying the path the
 *  content came from. Absent on every other entry, and absent on the
 *  worktree half of a staged rename, which describes an ordinary edit at
 *  the new path rather than the move. */
export interface GitFileEntry {
  path: string;
  status: string;
  staged: boolean;
  display: string;
  orig_path?: string;
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

/** Every status character `git status --porcelain=v1` emits, mirroring
 *  the server's own table (internal/git/parse.go statusLabels) so a
 *  tooltip never disagrees with the label beside it.
 *
 *  'C' and 'T' were both missing, so `describeStatus` handed back the
 *  bare letter for a copy and for a typechange — the two statuses whose
 *  letter is least guessable. 'T' is a regular file replaced by a
 *  symlink or the reverse, which git reports as ` T`/`T `. */
const GIT_STATUS_LABELS: Readonly<Record<string, string>> = {
  M: "Modified",
  T: "Typechange",
  A: "Added",
  D: "Deleted",
  R: "Renamed",
  C: "Copied",
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

// --- Path-level counting -------------------------------------------------
//
// Entries are per SIDE OF THE INDEX; a person counts FILES. The two differ
// on exactly one input, a path staged and then edited again, and the count
// that got it wrong was one that SPANNED both sides: the old repo-level
// "Discard all (N)" read `files.length` over every entry, so one file
// edited twice offered to discard "2 uncommitted changes" and sent that
// path twice.
//
// Within ONE side the server already emits at most one entry per path, so
// these two are a guard on that invariant rather than a live dedup —
// measured by mutation, replacing either with `.length` at a group-scoped
// call site changes no output. They are worth the lines anyway: the
// invariant belongs to another module's parse (internal/git/parse.go), and
// a count here should not go wrong if that shape ever changes.

/** The distinct paths of these entries, in first-seen order. Also the
 *  shape a bulk mutation payload wants, so the server's own log line
 *  ("files=2") cannot disagree with the tree. */
export function distinctPaths(files: readonly GitFileEntry[]): string[] {
  const seen = new Set<string>();
  const out: string[] = [];
  for (const f of files) {
    if (!seen.has(f.path)) {
      seen.add(f.path);
      out.push(f.path);
    }
  }
  return out;
}

/** How many FILES these entries describe. */
export function changedPathCount(files: readonly GitFileEntry[]): number {
  return new Set(files.map((f) => f.path)).size;
}

/** The paths present on BOTH sides of the index: staged, then changed
 *  again in the worktree.
 *
 *  Such a path renders as two rows, and with the list grouped by staged
 *  state those rows sit in different groups — correct git semantics that
 *  reads as a duplicate to anyone who does not know porcelain's XY pair.
 *  The panel marks both rows from this set rather than leaving the reader
 *  to work it out. */
export function partiallyStagedPaths(files: readonly GitFileEntry[]): ReadonlySet<string> {
  const staged = new Set<string>();
  const unstaged = new Set<string>();
  for (const f of files) {
    (f.staged ? staged : unstaged).add(f.path);
  }
  const both = new Set<string>();
  for (const p of staged) {
    if (unstaged.has(p)) {
      both.add(p);
    }
  }
  return both;
}

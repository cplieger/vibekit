// ---------------------------------------------------------------------------
// One shared owner of /api/git/status-all, with a per-path lookup.
//
// Before this, two modules fetched that endpoint independently and neither kept
// anything a third could read: git-badge.ts reduced the response to two counters
// and threw the repos away, and git-changes-tab.ts kept its own copy private for
// its own rendering. So "what is this file's git status" had no answer outside
// the git view — which is why the file browser and the docs page could not
// decorate a row.
//
// IT HOLDS NO TIMER. It polled every 15 s and fired an extra FULL scan on every
// `turn_ended`, and one scan is 270 git subprocesses across 54 worktrees, so the
// steady-state cost of an idle page was a scan every 15 seconds for a tree
// nothing had touched. A turn ending is a GUESS that the tree changed; the client
// already holds the fact — `handlers/messages.ts` sees each repo-mutating tool
// call complete — and was throwing it away. So the only automatic refresh is that
// fact arriving, through `markGitDirty`.
//
// A watcher was considered and rejected, and the reason is checkable in the tree:
// vibekit IS the writer, so it holds a more precise fact than inotify does — it
// can name the repos — while a recursive watch over 54 worktrees would need tens
// of thousands of inotify watches against a host `fs.inotify.max_user_watches`
// the container cannot raise, and a `.git/index`-only watch would miss the
// commonest case, an unstaged agent write. No new SSE event either: the frames
// that carry the fact already reach the client.
//
// Scope note: git-changes-tab.ts deliberately keeps its own fetch. It needs the
// `?fetch=1` forced-refresh variant (a real `git fetch`, the only way to learn
// remote state) and re-reads on user gestures, which is a different lifecycle
// from this one.
// ---------------------------------------------------------------------------

import { apiAction, defineAction } from "./actions/index.js";
import { signal, subscribe } from "@cplieger/reactive";
import { absPath, onWorkspaceRoot, workspaceRoot } from "./workspace.js";
import type { GitRepoStatus } from "./git-types.js";
import { statusLetter } from "./git-types.js";

interface StatusAllResponse {
  repos?: GitRepoStatus[] | null;
}

// eslint-disable-next-line @typescript-eslint/no-invalid-void-type -- void as a generic argument for an action taking no args
const fetchStatusAll = apiAction<void, StatusAllResponse>({
  name: "git-status.all",
  request: () => ({ method: "GET", path: "/api/git/status-all" }),
  error: false,
  success: false,
});

// eslint-disable-next-line @typescript-eslint/no-invalid-void-type -- void as a generic argument for an action taking no args
const refreshAction = defineAction<void, StatusAllResponse>({
  name: "git-status.refresh",
  dedupe: true,
  run: async () => (await fetchStatusAll.dispatch(undefined)) ?? { repos: [] },
  error: false,
  success: false,
});

/** The current repos array. A signal so consumers re-render on every poll
 *  without each holding its own copy. */
const repos = signal<readonly GitRepoStatus[]>([]);

/** Lookup index: "<repo>\u0000<repo-relative path>" → status letter. Rebuilt on
 *  every poll. A map rather than a scan per row because the docs page asks ~200
 *  times per paint. */
let index = new Map<string, string>();

/** Absolute-path index: the file's absolute path → status letter.
 *
 *  The file browser has absolute paths and no idea which repo a path belongs to,
 *  and making it resolve that itself would put a second copy of the repo-split
 *  rule beside the docs page's. One more map off the SAME poll costs nothing.
 *
 *  The keys are genuinely absolute now. They used to be composed as
 *  "<repoName>/<relPath>" — which is workspace-RELATIVE, because `repo` is a bare
 *  directory name under the workspace (or "." for the root), not a path — while
 *  every lookup passed a real absolute path. No key could ever match, so the file
 *  browser's status letters and every directory rollup were silently empty for
 *  every file. The join goes through workspace.ts's absPath, the one owner of the
 *  relative→absolute rule. */
let absIndex = new Map<string, string>();

/** Ancestor rollup: absolute directory path → the worst letter beneath it.
 *
 *  A directory row has to say something, or a change three levels down is
 *  invisible until you walk into it — which is the whole reason to decorate the
 *  browser rather than only the git view. */
let dirIndex = new Map<string, string>();

/** Rollup precedence, worst first. A conflict is the most urgent thing a
 *  directory can contain and an untracked file the least, so a folder holding
 *  both reports the conflict.
 *
 *  It must list EVERY letter git emits, or the two the table missed fall to
 *  the unknown-letter tail below and a directory holding only a copy or a
 *  typechange reports whatever else it contains instead. 'T' sits with the
 *  structural changes (a file replaced by a symlink), 'C' beside 'R' since
 *  both describe content arriving from somewhere else. */
const ROLLUP_ORDER: readonly string[] = ["U", "D", "T", "M", "R", "C", "A", "?"];

function worse(a: string, b: string): string {
  if (a === "") {
    return b;
  }
  if (b === "") {
    return a;
  }
  const ia = ROLLUP_ORDER.indexOf(a);
  const ib = ROLLUP_ORDER.indexOf(b);
  // An unknown letter sorts last rather than winning by accident.
  return (ia === -1 ? ROLLUP_ORDER.length : ia) <= (ib === -1 ? ROLLUP_ORDER.length : ib) ? a : b;
}

let started = false;

function rebuildIndex(list: readonly GitRepoStatus[]): void {
  const next = new Map<string, string>();
  const nextAbs = new Map<string, string>();
  const nextDir = new Map<string, string>();
  // The repo-relative index needs no root; the two absolute ones cannot be built
  // without it. Declining to build them is the honest answer, and it is why the
  // lookups return "" before the handshake instead of returning a letter derived
  // from a guessed root.
  const rooted = workspaceRoot() !== "";
  for (const r of list) {
    if (!r.is_repo) {
      continue;
    }
    // `r.repo` is a directory NAME under the workspace (discoverRepos), and "."
    // means the workspace root IS the repo — so the absolute form of the repo's
    // own directory is the root itself in that case, and the root joined with the
    // name otherwise.
    const repoAbs = r.repo === "." ? workspaceRoot() : absPath(r.repo);
    for (const f of r.files) {
      const key = `${r.repo}\u0000${f.path}`;
      const letter = statusLetter(f.status);
      // Worst-status-wins when a path appears twice (staged + unstaged): the
      // first non-empty letter is kept, matching the git view's rollup.
      if (!next.has(key)) {
        next.set(key, letter);
      }
      if (letter === "" || !rooted) {
        continue;
      }
      const abs = `${repoAbs}/${f.path}`;
      if (!nextAbs.has(abs)) {
        nextAbs.set(abs, letter);
      }
      // Mark every ancestor up to and including the repo root.
      let cut = abs.lastIndexOf("/");
      while (cut > 0) {
        const dir = abs.slice(0, cut);
        nextDir.set(dir, worse(nextDir.get(dir) ?? "", letter));
        if (dir === repoAbs) {
          break;
        }
        cut = dir.lastIndexOf("/");
      }
    }
  }
  index = next;
  absIndex = nextAbs;
  dirIndex = nextDir;
}

// The handshake that states the workspace root and the first status poll race,
// with no ordering between them: pollAction fires its first tick synchronously
// when the store starts, while the root arrives on an SSE frame. A poll that wins
// that race built an index with no absolute keys at all, and the browser's status
// letters would have stayed blank until the next poll 15s later. Rebuilding when
// the root lands removes the ordering question; republishing is what repaints the
// rows that were painted letter-less in the meantime.
onWorkspaceRoot(() => {
  const list = repos.peek();
  rebuildIndex(list);
  // A new array identity, because the INDEX changed while the data did not, and
  // `repos` is the only thing consumers watch.
  repos.value = [...list];
});

/** Read the tree once, so a surface that just opened has something to show.
 *
 *  Idempotent — safe to call from several init paths, and it is the ONLY read
 *  this module makes on its own. Everything after it is `refreshGitStatus`,
 *  driven by the client learning that a repo changed. */
export function initGitStatusStore(): void {
  if (started) {
    return;
  }
  started = true;
  void refreshGitStatus();
}

/** Re-read the tree. Deduped with any read already in flight.
 *
 *  Exported because the trigger is no longer this module's: the fact that a repo
 *  moved arrives at `handlers/messages.ts` as a repo-mutating tool call
 *  completing, and travels here through `git.ts`'s markGitDirty. A user gesture in
 *  the Changes tab has its own forced-refresh path.
 *
 *  DEFERRED, and it is the one half of this that is not client-side: the request
 *  is unscoped, so a single-file edit still costs a full 54-repo scan. Scoping it
 *  needs a `?paths=` form on `/api/git/status-all`, which is Phase A's file
 *  (`internal/git/` is A's entirely) and did not land with A2's snapshot cache.
 *  What HAS landed is the expensive half: the scan now happens when the tree
 *  actually moved rather than every 15 seconds and on every turn end. */
export async function refreshGitStatus(): Promise<void> {
  const d = await refreshAction.dispatch(undefined);
  const list = d?.repos ?? [];
  rebuildIndex(list);
  repos.value = list;
}

/** The git status letter for one repo-relative path, or "" when the path is
 *  clean, untracked-but-ignored, or the repo is unknown.
 *
 *  Letters are git-types.ts's vocabulary (M/A/D/R/?/U) — the same ones the git
 *  view and the file browser show, so a user learns one alphabet. */
export function statusFor(repo: string, relPath: string): string {
  return index.get(`${repo}\u0000${relPath}`) ?? "";
}

/** The status letter for an ABSOLUTE path, or "" when clean/unknown. For
 *  consumers that hold real filesystem paths rather than a repo-relative pair. */
export function statusForPath(absPath: string): string {
  return absIndex.get(normalizeAbs(absPath)) ?? "";
}

/** The worst status letter anywhere BENEATH an absolute directory path, or "".
 *  What lets a collapsed folder admit that something inside it changed. */
export function statusUnder(absDirPath: string): string {
  return dirIndex.get(normalizeAbs(absDirPath)) ?? "";
}

/** Trailing slashes and a bare "/" would miss every key. */
function normalizeAbs(p: string): string {
  return p.length > 1 && p.endsWith("/") ? p.slice(0, -1) : p;
}

/** Subscribe to poll results. Fires immediately with the current value. */
export function onGitStatusChange(fn: () => void): () => void {
  return subscribe(repos, fn);
}

/** The current repos array, for consumers deriving aggregates (the badge). */
export function currentRepos(): readonly GitRepoStatus[] {
  return repos.value;
}

/** @internal Test seam: inject a repos array without a fetch. */
export function _setReposForTest(list: readonly GitRepoStatus[]): void {
  rebuildIndex(list);
  repos.value = list;
}

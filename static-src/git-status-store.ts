// ---------------------------------------------------------------------------
// One shared owner of the /api/git/status-all poll, with a per-path lookup.
//
// Before this, two modules fetched that endpoint independently and neither kept
// anything a third could read: git-badge.ts reduced the response to two counters
// and threw the repos away, and git-changes-tab.ts kept its own copy private for
// its own rendering. So "what is this file's git status" had no answer outside
// the git view — which is why the file browser and the docs page could not
// decorate a row.
//
// This store owns the timer and the data; consumers subscribe. It adds NO new
// server call and NO second timer: the badge now derives its counters from here
// instead of issuing its own status fetch.
//
// Scope note: git-changes-tab.ts deliberately keeps its own fetch. It needs the
// `?fetch=1` forced-refresh variant and re-reads on user gestures, which is a
// different lifecycle from a background poll — folding it in would mean the poll
// serving a forced refresh, or the tab waiting up to 15s for one.
// ---------------------------------------------------------------------------

import { apiAction, defineAction, pollAction } from "./actions/index.js";
import { onSSE } from "./bus.js";
import { signal, subscribe } from "@cplieger/reactive";
import { absPath, onWorkspaceRoot, workspaceRoot } from "./workspace.js";
import type { GitRepoStatus } from "./git-types.js";
import { statusLetter } from "./git-types.js";

/** Poll cadence, unchanged from the badge's own (pollAction pauses while the
 *  document is hidden and refreshes on focus, so this is a ceiling not a floor). */
const POLL_INTERVAL_MS = 15_000;

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
 *  both reports the conflict. */
const ROLLUP_ORDER: readonly string[] = ["U", "D", "M", "R", "A", "?"];

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

/** Start the poll. Idempotent — safe to call from several init paths. */
export function initGitStatusStore(): void {
  if (started) {
    return;
  }
  started = true;
  const apply = (d: StatusAllResponse | null): void => {
    const list = d?.repos ?? [];
    rebuildIndex(list);
    repos.value = list;
  };
  // A finished turn is the most likely moment for the tree to have changed.
  onSSE("turn_ended", () => {
    void refresh();
  });
  pollAction(refreshAction, undefined, {
    interval: POLL_INTERVAL_MS,
    onSuccess: apply,
  });
}

/** Force a refresh now. Deduped with any in-flight poll. Internal: consumers
 *  subscribe rather than pull, and the poll plus the turn_ended nudge below
 *  cover every moment the tree plausibly changed. */
async function refresh(): Promise<void> {
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

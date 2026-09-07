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
// already holds the fact and was throwing it away. So every automatic refresh is a
// fact arriving, through `markGitDirty`, and each one NAMES the paths it knows
// about so only the owning repositories are rescanned.
//
// The facts, and between them they cover every writer:
//
//   the agent      a repo-mutating tool call completing   handlers/messages.ts
//   the editor     a save landing                         editor-core.ts
//   the shell      the panel closing                      shell.ts
//   anything else  the tab becoming visible again          below
//
// The first two NAME their paths and cost the owning repositories only. The last
// two cannot: a terminal command writes wherever it likes, and the catch-all is by
// definition about a writer this client cannot see — a command run by another tool,
// a second window on the same workspace, the file browser's own create and delete.
// Neither is a clock. The shell one fires on a gesture, and the catch-all fires
// when a stale badge would be READ rather than on a schedule, so a page nobody is
// looking at costs nothing; the server answers from its snapshot and rescans behind
// the answer, so a burst costs one scan.
//
// A watcher was considered and rejected, and the reason is checkable in the tree:
// vibekit IS the writer for the agent's half, so it holds a more precise fact than
// inotify does — it can name the repos — while a recursive watch over 54 worktrees
// would need tens of thousands of inotify watches against a host
// `fs.inotify.max_user_watches` the container cannot raise, and a `.git/index`-only
// watch would miss the commonest case, an unstaged agent write. No new SSE event
// either: the frames that carry the fact already reach the client.
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

/** How many paths one scoped read may name. The server caps it too, and its cap is
 *  the one that binds; this keeps a turn that touched hundreds of files from
 *  building a URL only to have most of it dropped. */
const SCOPE_PATHS_MAX = 64;

/** A scoped read's `?paths=`, or "" for a full one.
 *
 *  The paths are workspace-RELATIVE, which is the language `ownerOf` speaks, and
 *  the split from path to repository stays server-side: the one time this module
 *  composed repo keys itself it got the rule wrong and every status letter was
 *  silently empty. */
function scopeQuery(paths: readonly string[] | undefined): string {
  if (paths === undefined || paths.length === 0) {
    return "";
  }
  const wanted = [...new Set(paths.filter((p) => p !== ""))].slice(0, SCOPE_PATHS_MAX);
  if (wanted.length === 0) {
    return "";
  }
  return `?paths=${encodeURIComponent(wanted.join(","))}`;
}

interface ScopeArgs {
  paths?: readonly string[];
}

const fetchStatusAll = apiAction<ScopeArgs, StatusAllResponse>({
  name: "git-status.all",
  request: ({ paths }) => ({ method: "GET", path: `/api/git/status-all${scopeQuery(paths)}` }),
  error: false,
  success: false,
});

const refreshAction = defineAction<ScopeArgs, StatusAllResponse>({
  name: "git-status.refresh",
  // Keyed on the SCOPE, not blanket-true. Two reads naming the same paths are one
  // read; two naming different repositories are two, and collapsing them would
  // leave the second repository's rows stale — which is the whole defect scoping
  // is here to fix, reintroduced on the client side.
  dedupe: (args) => `git-status.refresh${scopeQuery(args.paths)}`,
  run: async (args) => (await fetchStatusAll.dispatch(args)) ?? { repos: [] },
  error: false,
  success: false,
});

/** The current repos array. A signal so consumers re-render on every read
 *  without each holding its own copy. */
const repos = signal<readonly GitRepoStatus[]>([]);

/** Lookup index: "<repo>\u0000<repo-relative path>" → status letter. Rebuilt on
 *  every read. A map rather than a scan per row because the docs page asks ~200
 *  times per paint. */
let index = new Map<string, string>();

/** Absolute-path index: the file's absolute path → status letter.
 *
 *  The file browser has absolute paths and no idea which repo a path belongs to,
 *  and making it resolve that itself would put a second copy of the repo-split
 *  rule beside the docs page's. One more map off the SAME read costs nothing.
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

// The handshake that states the workspace root and the store's first read race,
// with no ordering between them: the read is fired by whichever surface starts
// the store, while the root arrives on an SSE frame. A read that wins that race
// builds an index with no absolute keys at all, and the browser's status letters
// would stay blank — indefinitely now that no timer follows. Rebuilding when the
// root lands removes the ordering question; republishing is what repaints the
// rows that were painted letter-less in the meantime.
onWorkspaceRoot(() => {
  const list = repos.peek();
  rebuildIndex(list);
  // A new array identity, because the INDEX changed while the data did not, and
  // `repos` is the only thing consumers watch.
  repos.value = [...list];
});

// The tab coming back is the catch-all for every writer this client cannot name:
// a `git commit` typed in the shell, an edit made by a command, a change from
// another window onto the same workspace. It is not a poll — a page nobody looks
// at fires nothing, and coming back is exactly the moment a stale badge would be
// READ — and it is unscoped because there is nothing to scope it to.
//
// Guarded on the store having started: registering this at module load and firing
// it for a page whose file browser and git view were never opened would put a scan
// of every worktree back on a surface with no subscriber, which is the cost
// `startOnFirstSubscriber` exists to avoid.
document.addEventListener("visibilitychange", () => {
  if (started && !document.hidden) {
    void refreshGitStatus();
  }
});

/** Read the tree once, on the FIRST SUBSCRIBER, and never for nobody.
 *
 *  There is no init call: one belonged to whichever module happened to construct
 *  first, which was `initFileBrowser` at boot, so a scan of every worktree ran for
 *  a browser nobody had opened. This is the module's ONLY unprompted read —
 *  everything after it is `refreshGitStatus`, driven by a repo actually moving. */
function startOnFirstSubscriber(): void {
  if (started) {
    return;
  }
  started = true;
  void refreshGitStatus();
}

/** Re-read the tree, or only the repositories owning `paths`. Deduped per scope
 *  with any read already in flight.
 *
 *  Exported because the trigger is not this module's: the facts that the tree moved
 *  arrive at their own sites — a repo-mutating tool call completing
 *  (`handlers/messages.ts`), an editor save, a file-browser action — and travel
 *  here through `git.ts`'s markGitDirty. A user gesture in the Changes tab has its
 *  own forced-refresh path.
 *
 *  A scoped read costs the named repositories' two subprocesses each instead of the
 *  whole tree's ~110, which is what makes a per-edit trigger affordable at all. The
 *  answer is still the WHOLE repos array: the server merges a scoped scan into its
 *  snapshot, so this never publishes a partial list and every index below is built
 *  over the same complete set as before. */
export async function refreshGitStatus(paths?: readonly string[]): Promise<void> {
  const d = await refreshAction.dispatch(paths === undefined ? {} : { paths });
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

/** Subscribe to the repos array. Fires immediately with the current value, and
 *  the FIRST subscriber is what starts the store's one read — so a surface that
 *  just opened gets data by watching for it, with no separate init call to forget
 *  or to fire too early. */
export function onGitStatusChange(fn: () => void): () => void {
  const off = subscribe(repos, fn);
  startOnFirstSubscriber();
  return off;
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

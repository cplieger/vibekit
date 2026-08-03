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

let started = false;

function rebuildIndex(list: readonly GitRepoStatus[]): void {
  const next = new Map<string, string>();
  for (const r of list) {
    if (!r.is_repo) {
      continue;
    }
    for (const f of r.files) {
      const key = `${r.repo}\u0000${f.path}`;
      const letter = statusLetter(f.status);
      // Worst-status-wins when a path appears twice (staged + unstaged): the
      // first non-empty letter is kept, matching the git view's rollup.
      if (!next.has(key)) {
        next.set(key, letter);
      }
    }
  }
  index = next;
}

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

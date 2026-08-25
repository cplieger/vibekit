// ---------------------------------------------------------------------------
// Git Changes tab: per-repo collapsible sections rendered into
// #git-changes-mount. Each section shows branch + ahead/behind, the
// list of file changes (staged + unstaged interleaved with their
// status letters), an action bar (pull / stash / pop / stage all /
// discard all), and a commit message + Commit button.
//
// Single fetch of /api/git/status-all produces the data; the section
// rendering is purely data-driven so re-renders are cheap. Re-render
// triggers: filter input, after a successful action, on tab activate.
// ---------------------------------------------------------------------------

import { apiGet } from "./api-client.js";
import { onSSE } from "./bus.js";
import { ICON_REFRESH, ICON_REPO_EMPTY, ICON_CLEAN, ICON_FILTER } from "./icons.js";
import { withAsyncFeedback } from "./async-button.js";
import { confirm as confirmDialog } from "./confirm.js";
import { preserveGitScroll } from "./git-scroll.js";
import { stage, discard, pull, push, stash, stashPop, unstage } from "./actions/git-changes.js";
import { bindLoadingState, registerCleanup } from "./actions/index.js";
import { reconcile } from "./reconcile.js";
import { el } from "@cplieger/reactive";
import { chevronEl } from "./chevron.js";
import { createSearchPopup } from "./search-popup.js";
import type { SearchPopup } from "./search-popup.js";
import { createDisclosure } from "@cplieger/ui-primitives/disclosure";
import { escAttr as escapeHTML } from "./strings.js";
import { renderRecentCommits, renderCommitArea, type CommitDeps } from "./git-changes-commit.js";

// --- Helpers for withAsyncFeedback ---

/** Throw if an action dispatch returned null or undefined (failure already toasted). */
function assertOk<T>(result: T): asserts result is NonNullable<T> {
  if (result === null || result === undefined) {
    throw new Error("action failed");
  }
}

/** Sentinel thrown when user cancels a confirm dialog. withAsyncFeedback
 *  treats it like any error (shows ✗); no toast fires because no action
 *  was dispatched. */
class CancelledError extends Error {
  constructor() {
    super("cancelled");
  }
}

// --- Wire types ---

import type { GitFileEntry as FileEntry, GitRepoStatus as RepoStatus } from "./git-types.js";
import { statusLetter, describeStatus, stashableCount } from "./git-types.js";

interface StatusAllResponse {
  repos: RepoStatus[];
}

// --- State ---

let inited = false;
let lastStatusAll: RepoStatus[] = [];
let filterText = "";
let refreshGeneration = 0;
let refreshAbort: AbortController | null = null;
/** Long-lived abort controller for diff/log fetches — only aborted on
 *  tab teardown, NOT on every repaint. Individual diff fetches create
 *  their own per-fetch controllers with a timeout. */
const diffAbortCtrl = new AbortController();
registerCleanup(() => {
  refreshAbort?.abort();
});
registerCleanup(() => {
  diffAbortCtrl.abort();
});

/** Repos that recently received a successful push. Used to surface a
 *  contextual "Open PR" hint in their section header for a few
 *  re-renders after the push. */
const recentlyPushed = new Set<string>();
const RECENTLY_PUSHED_TTL_MS = 60_000;

// Bug 1: Preserve commit message textarea values across re-renders.
const commitMessages = new Map<string, string>();

// Bug 2: Module-level discard-all guard to prevent concurrent discards.
const discardPendingRepos = new Set<string>();

// Bug 3: Track user-toggled collapse state (repos the user manually collapsed).
const userCollapsedRepos = new Set<string>();
const userExpandedRepos = new Set<string>();

// Bug 4: Track expanded inline diff paths across re-renders.
const expandedDiffPaths = new Set<string>();

// Per-paint cleanup: unbind functions from bindLoadingState calls.
let bindingCleanups: (() => void)[] = [];

// Deferred paint: set when paint bails due to focused textarea.
let paintDeferred = false;

/** Build the deps object for commit rendering functions. */
function commitDeps(): CommitDeps {
  return { commitMessages, bindingCleanups, diffAbort: diffAbortCtrl, refreshChanges, assertOk };
}

// --- Public API ---

/** The tab's filter box.
 *
 *  A POPUP since the search-box audit. It was an `<input type="search">`
 *  hand-authored into index.html with its own magnifier SVG beside it — the one
 *  page-level search in the app that did not go through the shared shell, and the
 *  reason the toolbar's magnifier was a DEAD DOOR on the git view: `find-dispatch`
 *  had no branch for this tab, so Ctrl-F fell through to the transcript's handler,
 *  which declined because the chat view was hidden.
 *
 *  Its close CLEARS, which matters more here than on a permanent box: this filter
 *  also drives each repo section's expansion default, so a query left applied
 *  behind a closed box would leave the page both narrowed and expanded with
 *  nothing on screen explaining either. */
export const changesFind: SearchPopup = createSearchPopup<null>({
  id: "git-changes-filter",
  kind: "filter",
  label: "Filter changes by path",
  placeholder: "Filter by path\u2026",
  host: () => document.getElementById("git-view"),
  query: (q) => {
    filterText = q.trim().toLowerCase();
    return null;
  },
  render: () => {
    paint();
  },
});

/** Initialise the Changes tab. Wires the global refresh button and the SSE
 *  forge-changed event. Idempotent. */
export function initChangesTab(): void {
  if (inited) {
    return;
  }
  inited = true;
  // Fire deferred paint when commit textarea loses focus.
  // Scoped to the mount container so the listener is tied to the tab's
  // DOM lifetime. focusout bubbles, so it reaches the container.
  const changesMount = document.getElementById("git-changes-mount");
  changesMount?.addEventListener(
    "focusout",
    (e) => {
      if (
        paintDeferred &&
        e.target instanceof HTMLTextAreaElement &&
        e.target.classList.contains("git-commit-input")
      ) {
        paint();
      }
    },
    { passive: true },
  );

  const refreshBtn = document.getElementById("git-refresh-all-btn") as HTMLButtonElement | null;
  if (refreshBtn !== null) {
    refreshBtn.innerHTML = ICON_REFRESH;
    refreshBtn.addEventListener("click", () => {
      // Default keepLabel=false: the icon is replaced by the
      // spinner while the refresh is in flight (then ✓/✗). The
      // button has no text label to keep, so this reads cleaner
      // than the icon + spinner side-by-side variant.
      // Explicit user refresh → opt into the server-side git fetch
      // so ahead/behind reflects the real remote state (18-F3).
      void withAsyncFeedback(refreshBtn, () => refreshChanges(true));
    });
  }

  // Refetch when the agent emits anything that touches files (it
  // emits this after every turn that wrote something), and when forge
  // accounts change (clones / removes ripple into the repo list).
  // Debounced so SSE bursts coalesce into a single refresh.
  let sseRefreshTimer: ReturnType<typeof setTimeout> | undefined;
  const debouncedRefresh = (): void => {
    clearTimeout(sseRefreshTimer);
    sseRefreshTimer = setTimeout(() => {
      void refreshChanges();
    }, 300);
  };
  onSSE("turn_ended", debouncedRefresh);
  onSSE("forges_changed", debouncedRefresh);

  // No initial refresh here (EX-1): initGitPanel's onGitTabChange
  // subscription fires immediately with the current tab, so opening
  // /git already triggers exactly one refreshChanges(true). A second
  // call here would only abort that first request's server scan.
}

/** Force a full /api/git/status-all refresh and repaint. Concurrent
 *  calls are safe: a generation counter ensures stale responses are
 *  discarded.
 *
 *  `doFetch=true` opts into a per-repo server-side `git fetch`
 *  (?fetch=1) so ahead/behind is measured against fresh origin refs —
 *  reserved for explicit user navigation (the Refresh-all button,
 *  git-tab activation). SSE-debounced and post-action refreshes stay
 *  fetch-free so agent turns never fan out N network fetches (18-F3). */
export async function refreshChanges(doFetch = false): Promise<void> {
  refreshAbort?.abort();
  const ctrl = new AbortController();
  refreshAbort = ctrl;
  const gen = ++refreshGeneration;
  const signal = AbortSignal.any([ctrl.signal, AbortSignal.timeout(15_000)]);
  const url = doFetch ? "/api/git/status-all?fetch=1" : "/api/git/status-all";
  const data = await apiGet<StatusAllResponse>(url, signal);
  if (gen < refreshGeneration) {
    return;
  } // stale — a newer call supersedes
  if (data === null) {
    if (!ctrl.signal.aborted) {
      paintError("Failed to load git status.");
    }
    return;
  }
  lastStatusAll = data.repos;
  paint();
}

// --- Render ---

function paint(): void {
  preserveGitScroll(paintInner);
}

function paintInner(): void {
  const root = document.getElementById("git-changes-mount");
  if (root === null) {
    return;
  }

  // Bug 1: Skip re-render entirely if a commit textarea is focused
  // to avoid destroying user input mid-typing.
  const focused = document.activeElement;
  if (focused instanceof HTMLTextAreaElement && focused.classList.contains("git-commit-input")) {
    paintDeferred = true;
    return;
  }
  paintDeferred = false;

  // Tear down previous bindLoadingState subscriptions before re-render.
  for (const fn of bindingCleanups) {
    fn();
  }
  bindingCleanups = [];

  // Smell fix: prune module-level Sets/Maps to keys present in lastStatusAll.
  const activeRepos = new Set(lastStatusAll.map((r) => r.repo));
  // Build per-repo path index for finer pruning of expandedDiffPaths.
  // Without this, renamed/deleted files leave stale `repo\0path` keys
  // behind that grow the Set unboundedly over edit cycles.
  const activePathsByRepo = new Map<string, Set<string>>();
  for (const r of lastStatusAll) {
    const paths = new Set<string>();
    for (const f of r.files) {
      paths.add(f.path);
    }
    activePathsByRepo.set(r.repo, paths);
  }
  for (const k of userCollapsedRepos) {
    if (!activeRepos.has(k)) {
      userCollapsedRepos.delete(k);
    }
  }
  for (const k of userExpandedRepos) {
    if (!activeRepos.has(k)) {
      userExpandedRepos.delete(k);
    }
  }
  for (const k of commitMessages.keys()) {
    if (!activeRepos.has(k)) {
      commitMessages.delete(k);
    }
  }
  for (const k of expandedDiffPaths) {
    const nulIdx = k.indexOf("\0");
    if (nulIdx === -1) {
      expandedDiffPaths.delete(k);
      continue;
    }
    const repo = k.slice(0, nulIdx);
    const path = k.slice(nulIdx + 1);
    const repoPaths = activePathsByRepo.get(repo);
    if (!repoPaths?.has(path)) {
      expandedDiffPaths.delete(k);
    }
  }
  // Cap expandedDiffPaths to prevent unbounded growth over long sessions.
  if (expandedDiffPaths.size > 200) {
    const excess = expandedDiffPaths.size - 200;
    const iter = expandedDiffPaths.values();
    for (let i = 0; i < excess; i++) {
      const next = iter.next();
      if (next.done === true) {
        break;
      }
      expandedDiffPaths.delete(next.value);
    }
  }

  // Bug 1: Capture current commit messages before destroying DOM.
  for (const ta of root.querySelectorAll<HTMLTextAreaElement>(".git-commit-input[data-repo]")) {
    const repo = ta.dataset["repo"];
    if (repo) {
      commitMessages.set(repo, ta.value);
    }
  }

  if (lastStatusAll.length === 0) {
    root.innerHTML = renderEmptyState({
      icon: ICON_REPO_EMPTY,
      title: "No repositories cloned",
      hint: "Open the <strong>Sources</strong> tab to clone one.",
    });
    return;
  }

  // Filter file entries by path (we keep the section if any file
  // matches OR if the search is empty).
  const visibleRepos: RepoStatus[] = [];
  for (const r of lastStatusAll) {
    if (filterText !== "") {
      const repoMatches = r.repo.toLowerCase().includes(filterText);
      const anyFileMatches = r.files.some((f) => f.path.toLowerCase().includes(filterText));
      if (!repoMatches && !anyFileMatches) {
        continue;
      }
    }
    visibleRepos.push(r);
  }

  // Drop any prior non-keyed empty-state placeholder before reconciling.
  for (const child of [...root.children]) {
    if ((child as HTMLElement).getAttribute("data-reconcile-key") === null) {
      child.remove();
    }
  }

  if (visibleRepos.length === 0) {
    reconcile(root, [] as RepoStatus[], {
      key: (r) => r.repo,
      mount: () => el("div"),
    });
    if (filterText !== "") {
      root.innerHTML = renderEmptyState({
        icon: ICON_FILTER,
        title: "No matching changes",
        hint: "Adjust your filter to see more.",
      });
    } else {
      root.innerHTML = renderEmptyState({
        icon: ICON_CLEAN,
        title: "All clean",
        hint: "Nothing to commit across your repositories.",
      });
    }
    return;
  }

  // Outer reconcile: keep section identity (and inline textareas /
  // commit-message drafts inside) across paints. Body content is
  // rebuilt fresh on update via renderRepoSection.
  reconcile(root, visibleRepos, {
    key: (r: RepoStatus) => r.repo,
    mount: (r: RepoStatus) => {
      const section = renderRepoSection(r);
      return section ?? el("section");
    },
    update: (section: HTMLElement, r: RepoStatus) => {
      const fresh = renderRepoSection(r);
      if (fresh === null) {
        return;
      }
      section.className = fresh.className;
      section.replaceChildren(...Array.from(fresh.childNodes));
    },
  });
}

// --- Empty-state markup helpers ---

function renderEmptyState(opts: { icon: string; title: string; hint: string }): string {
  return `
    <div class="git-multirepo-empty">
      <div class="git-multirepo-empty-icon">${opts.icon}</div>
      <div class="git-multirepo-empty-title">${opts.title}</div>
      <div class="git-multirepo-empty-hint">${opts.hint}</div>
    </div>
  `;
}

function paintError(msg: string): void {
  const root = document.getElementById("git-changes-mount");
  if (root === null) {
    return;
  }
  root.replaceChildren(el("div", { className: "git-multirepo-error" }, msg));
}

function renderRepoSection(r: RepoStatus): HTMLElement | null {
  // Apply filter: keep section if (a) filter empty, (b) repo name
  // matches, or (c) any file path matches.
  const filteredFiles =
    filterText === "" ? r.files : r.files.filter((f) => f.path.toLowerCase().includes(filterText));
  if (
    filterText !== "" &&
    filteredFiles.length === 0 &&
    !r.repo.toLowerCase().includes(filterText)
  ) {
    return null;
  }
  // Hide clean repos by default unless filter is active or repo
  // matched the filter explicitly.
  const dataDefault = r.has_dirty || r.ahead > 0 || r.behind > 0 || filterText !== "";
  // Bug 3: User-toggled state overrides data-driven default.
  let expandedDefault: boolean;
  if (userCollapsedRepos.has(r.repo)) {
    expandedDefault = false;
  } else if (userExpandedRepos.has(r.repo)) {
    expandedDefault = true;
  } else {
    expandedDefault = dataDefault;
  }

  const section = el("section", { className: "git-repo-section", "data-repo": r.repo });

  // Header — the disclosure trigger. A native <button>, so createDisclosure
  // handles Enter/Space through the native click; no extra keydown wiring.
  const header = el("button", {
    type: "button",
    className: "git-repo-section-header",
  });
  header.innerHTML = renderHeaderHTML(r);
  // The chevron is prepended as an ELEMENT rather than written into the template
  // above, so `chevronEl()` stays the app's one construction of it. Safe against
  // being wiped: `renderHeaderHTML` has exactly this one call site.
  const chevron = chevronEl();
  chevron.classList.add("git-repo-section-chevron");
  header.prepend(chevron);

  // Preserve the branch-chip interception: a click on the branch chip must open
  // the branch switcher, NOT toggle the section. createDisclosure adds its own
  // (bubble-phase) click listener to the trigger, so a dedicated listener on the
  // chip that stops propagation makes the chip win — the disclosure's click
  // never fires for chip clicks.
  const branchChip = header.querySelector<HTMLElement>("[data-branch-trigger]");
  if (branchChip !== null) {
    branchChip.addEventListener("click", (ev) => {
      ev.preventDefault();
      ev.stopPropagation();
      void import("./git-branch-switcher.js")
        .then(({ openBranchSwitcher }) => {
          openBranchSwitcher(r.repo, branchChip);
        })
        .catch(() => {
          /* noop */
        });
    });
  }
  section.appendChild(header);

  // Body — the collapsing disclosure region. createDisclosure owns the collapse
  // (inline height 0<->auto, the .uip-disclosure-region clip, aria-hidden/inert);
  // the visual padding + flex layout live on the inner wrapper so they collapse
  // with the height (no residual sliver when closed).
  const body = el("div", { className: "git-repo-section-body" });
  const inner = el("div", { className: "git-repo-section-body-inner" });
  body.appendChild(inner);

  // onToggle persists the user's explicit toggle so re-renders respect it —
  // exactly the bookkeeping the old click handler did.
  createDisclosure(header, body, {
    open: expandedDefault,
    onToggle: (open) => {
      if (open) {
        userExpandedRepos.add(r.repo);
        userCollapsedRepos.delete(r.repo);
      } else {
        userCollapsedRepos.add(r.repo);
        userExpandedRepos.delete(r.repo);
      }
    },
  });

  if (!r.is_repo) {
    inner.append(el("div", { className: "git-repo-row-error" }, "Not a git repository."));
    section.appendChild(body);
    return section;
  }

  // Action bar
  inner.appendChild(renderActionBar(r));

  // Open-PR hint after a successful push (transient).
  if (recentlyPushed.has(r.repo) && isFeatureBranch(r.branch)) {
    inner.appendChild(renderOpenPRHint(r));
  }

  // File list (staged first, then unstaged)
  if (filteredFiles.length === 0 && !r.has_dirty) {
    inner.appendChild(el("div", { className: "git-repo-row-clean" }, "Clean."));
  } else if (filteredFiles.length === 0) {
    inner.appendChild(el("div", { className: "git-repo-row-empty" }, "No paths match the filter."));
  } else {
    inner.appendChild(renderFileList(r, filteredFiles));
  }

  // Commit area (only if there are staged files)
  const stagedCount = r.files.filter((f) => f.staged).length;
  if (stagedCount > 0) {
    inner.appendChild(renderCommitArea(r, commitDeps()));
  }

  // Recent commits sub-section (collapsed by default; expand → fetch).
  inner.appendChild(renderRecentCommits(r, commitDeps()));

  section.appendChild(body);
  return section;
}

function renderHeaderHTML(r: RepoStatus): string {
  const ahead = r.ahead > 0 ? ` <span class="git-repo-ahead">↑${r.ahead}</span>` : "";
  const behind = r.behind > 0 ? ` <span class="git-repo-behind">↓${r.behind}</span>` : "";
  const dirty = r.has_dirty
    ? ` <span class="git-repo-dirty-dot" title="Has uncommitted changes" aria-label="dirty"></span>`
    : "";
  const stashes =
    r.stashes > 0
      ? ` <span class="git-repo-stashes" title="${r.stashes} stash${r.stashes === 1 ? "" : "es"}">📦${r.stashes}</span>`
      : "";
  const branch = escapeHTML(r.branch || "(detached)");
  // The branch chip is a span (not a nested button — buttons can't
  // be inside a button per HTML spec); a dedicated click listener on
  // the chip (wired in renderRepoSection) stops propagation and routes
  // to openBranchSwitcher, so the disclosure trigger's click never fires
  // for chip clicks.
  return `
    <span class="git-repo-section-name">${escapeHTML(r.repo)}</span>${dirty}
    <span class="git-repo-section-meta">
      <span class="git-repo-branch-chip" data-branch-trigger="${escapeHTML(r.repo)}" data-tooltip="Switch branch">${branch}</span>${ahead}${behind}${stashes}
    </span>
  `;
}

function renderActionBar(r: RepoStatus): HTMLElement {
  const bar = el("div", { className: "git-repo-action-bar" });

  const btn = (label: string, title: string, danger = false): HTMLButtonElement => {
    return el(
      "button",
      {
        type: "button",
        className: `btn-small${danger ? " btn-danger" : ""}`,
        "data-tooltip": title,
      },
      label,
    ) as HTMLButtonElement;
  };

  // State-gated rendering (18-F2), mirroring the existing Push/Pop
  // pattern: an action the repo state can't service (Stage all on a
  // fully-staged tree, Discard all on a clean repo, Pull when not
  // behind, Stash with nothing tracked to stash) doesn't render at all —
  // no confirm-then-noop paths, no "Already up to date" pulls.
  const unstagedCount = r.files.filter((f) => !f.staged).length;
  const dirtyCount = r.files.length; // has_dirty ⇔ dirtyCount > 0

  if (unstagedCount > 0) {
    const stageAllBtn = btn(`Stage all (${unstagedCount})`, "Stage every unstaged change");
    stageAllBtn.addEventListener("click", () => {
      // Non-empty by the unstagedCount > 0 render gate above.
      const files = r.files.filter((f) => !f.staged).map((f) => f.path);
      void withAsyncFeedback(stageAllBtn, async () => {
        assertOk(await stage.dispatch({ repo: r.repo, files }));
        await refreshChanges();
      });
    });
    bar.appendChild(stageAllBtn);
    bindingCleanups.push(bindLoadingState(["git.stage", "git.commit"], stageAllBtn));
  }

  if (dirtyCount > 0) {
    const discardAllBtn = btn(
      `Discard all (${dirtyCount})`,
      "Throw away all uncommitted changes (irreversible)",
      true,
    );
    discardAllBtn.addEventListener("click", () => {
      // Bug 2: Module-level guard prevents concurrent discard-all per repo.
      if (discardPendingRepos.has(r.repo)) {
        return;
      }
      discardPendingRepos.add(r.repo);
      void (async () => {
        try {
          const ok = await confirmDialog(
            `Discard ${dirtyCount} uncommitted change${dirtyCount === 1 ? "" : "s"} in ${r.repo}? This cannot be undone.`,
            "Discard",
            "destructive",
          );
          if (!ok) {
            return;
          }
          // Includes staged paths: the server resets them first, so
          // "Discard all" genuinely discards everything (EX-2).
          const files = r.files.map((f) => f.path);
          await withAsyncFeedback(discardAllBtn, async () => {
            assertOk(await discard.dispatch({ repo: r.repo, files }));
            await refreshChanges();
          });
        } finally {
          discardPendingRepos.delete(r.repo);
        }
      })();
    });
    bar.appendChild(discardAllBtn);
    bindingCleanups.push(bindLoadingState(["git.discard", "git.commit"], discardAllBtn));
  }

  // Separator between file ops and sync ops — only when file ops rendered.
  if (bar.childElementCount > 0) {
    bar.appendChild(el("span", { className: "action-bar-sep", "aria-hidden": "true" }));
  }

  if (r.behind > 0) {
    const pullBtn = btn(`Pull ↓${r.behind}`, "git pull --ff-only");
    pullBtn.addEventListener("click", () => {
      void withAsyncFeedback(pullBtn, async () => {
        assertOk(await pull.dispatch({ repo: r.repo }));
        await refreshChanges();
      });
    });
    bar.appendChild(pullBtn);
    bindingCleanups.push(
      bindLoadingState(["git.pull", "git.push", "git.stash", "git.stash_pop"], pullBtn),
    );
  }

  if (r.ahead > 0) {
    const pushBtn = btn("Push", `Push ${r.ahead} commit${r.ahead === 1 ? "" : "s"} to origin`);
    pushBtn.classList.add("btn-primary");
    pushBtn.addEventListener("click", () => {
      void withAsyncFeedback(pushBtn, async () => {
        assertOk(await push.dispatch({ repo: r.repo }));
        // Mark for "Open PR" hint surfacing on next renders.
        recentlyPushed.add(r.repo);
        setTimeout(() => {
          recentlyPushed.delete(r.repo);
          paint();
        }, RECENTLY_PUSHED_TTL_MS);
        await refreshChanges();
      });
    });
    bar.appendChild(pushBtn);
    bindingCleanups.push(
      bindLoadingState(["git.push", "git.pull", "git.stash", "git.stash_pop"], pushBtn),
    );
  }

  // `git stash push` runs WITHOUT `-u`, so an untracked file is not stashable
  // even though the status parse reports it — `stashableCount` (git-types.ts)
  // carries that rule and why the two counts differ. Gating on `dirtyCount`
  // would still offer Stash on a tree whose only changes are new files, and git
  // would answer "No local changes to save": exactly the confirm-then-noop this
  // bar's rule exists to prevent.
  const stashable = stashableCount(r.files);

  if (stashable > 0) {
    const stashBtn = btn("Stash", "Stash uncommitted changes to tracked files");
    stashBtn.addEventListener("click", () => {
      void withAsyncFeedback(stashBtn, async () => {
        assertOk(await stash.dispatch({ repo: r.repo }));
        await refreshChanges();
      });
    });
    bar.appendChild(stashBtn);
    bindingCleanups.push(
      bindLoadingState(["git.stash", "git.pull", "git.push", "git.stash_pop"], stashBtn),
    );
  }

  if (r.stashes > 0) {
    const pop = btn("Pop", "Pop the most recent stash");
    pop.addEventListener("click", () => {
      void withAsyncFeedback(pop, async () => {
        assertOk(await stashPop.dispatch({ repo: r.repo }));
        await refreshChanges();
      });
    });
    bar.appendChild(pop);
    bindingCleanups.push(
      bindLoadingState(["git.stash_pop", "git.pull", "git.push", "git.stash"], pop),
    );
  }

  return bar;
}

function renderFileList(r: RepoStatus, files: FileEntry[]): HTMLElement {
  // Sort: staged first, then unstaged; within each group alphabetical.
  const sorted = [...files].sort((a, b) => {
    if (a.staged !== b.staged) {
      return a.staged ? -1 : 1;
    }
    return a.path.localeCompare(b.path);
  });
  const list = el("ul", { className: "git-file-list" });
  for (const f of sorted) {
    list.appendChild(renderFileRow(r, f));
  }
  return list;
}

function renderFileRow(r: RepoStatus, f: FileEntry): HTMLElement {
  const li = el("li", { className: `git-file-row${f.staged ? " staged" : ""}` });

  // Top row: status + path + actions. The whole top row is the
  // click target for toggling the inline diff.
  const top = el("div", { className: "git-file-row-top" });

  const status = el(
    "span",
    { className: "git-file-status", "data-tooltip": describeStatus(f.status) },
    f.display || statusLetter(f.status),
  );
  top.appendChild(status);

  const path = el("span", { className: "git-file-path", "data-tooltip": f.path }, f.path);
  top.appendChild(path);

  const actions = el("span", { className: "git-file-actions" });

  const action = (
    label: string,
    title: string,
    fn: () => Promise<unknown>,
    danger = false,
  ): HTMLButtonElement => {
    const b = el(
      "button",
      {
        type: "button",
        className: `btn-small${danger ? " btn-danger" : ""}`,
        "data-tooltip": title,
      },
      label,
    ) as HTMLButtonElement;
    b.addEventListener("click", (ev) => {
      ev.stopPropagation();
      void withAsyncFeedback(b, fn);
    });
    return b;
  };

  if (f.staged) {
    actions.appendChild(
      action("Unstage", "Move out of staged area", async () => {
        assertOk(await unstage.dispatch({ repo: r.repo, files: [f.path] }));
        await refreshChanges();
      }),
    );
  } else {
    actions.appendChild(
      action("Stage", "Add to staged area", async () => {
        assertOk(await stage.dispatch({ repo: r.repo, files: [f.path] }));
        await refreshChanges();
      }),
    );
    actions.appendChild(
      action(
        "Discard",
        "Throw away this change",
        async () => {
          const ok = await confirmDialog(
            `Discard changes to ${f.path}? This cannot be undone.`,
            "Discard",
            "destructive",
          );
          if (!ok) {
            throw new CancelledError();
          }
          assertOk(await discard.dispatch({ repo: r.repo, files: [f.path] }));
          await refreshChanges();
        },
        true,
      ),
    );
  }
  top.appendChild(actions);
  li.appendChild(top);

  // Inline diff drawer (collapsed until toggled). Lazy-loaded; the collapse
  // is a disclosure region wired below (after loadDiff is defined).
  const diffDrawer = el("div", { className: "git-file-diff" });
  li.appendChild(diffDrawer);

  // Bug 4: Unique key for tracking expanded state across re-renders.
  const diffKey = `${r.repo}\0${f.path}`;
  let loadedDiff = false;

  const loadDiff = (): void => {
    loadedDiff = true;
    diffDrawer.textContent = "Loading diff…";
    const perFetch = new AbortController();
    const signal = AbortSignal.any([perFetch.signal, AbortSignal.timeout(10_000)]);
    void apiGet<{ diff?: string }>(
      `/api/git/file-diff?repo=${encodeURIComponent(r.repo)}&path=${encodeURIComponent(f.path)}`,
      signal,
    ).then((data) => {
      if (perFetch.signal.aborted) {
        return;
      }
      if (data === null) {
        diffDrawer.replaceChildren();
        diffDrawer.appendChild(el("span", null, "Failed to load diff."));
        const retryBtn = el("button", { type: "button", className: "btn-small" }, "Retry");
        retryBtn.addEventListener("click", () => {
          loadedDiff = false;
          loadDiff();
        });
        diffDrawer.appendChild(retryBtn);
        return;
      }
      const diff = data.diff ?? "";
      if (diff.trim() === "") {
        diffDrawer.textContent = "(no diff available — file may be binary or empty)";
        return;
      }
      diffDrawer.replaceChildren();
      const pre = el("pre", { className: "git-file-diff-pre" }, renderDiffWithColors(diff));
      diffDrawer.appendChild(pre);
    });
  };

  // The row is the disclosure trigger (the primitive gives it role=button,
  // tabindex, Enter/Space, and aria-expanded — the old hidden-class click
  // toggle was keyboard-unreachable and told AT nothing) and the drawer is
  // the animated region (aria-hidden + inert when collapsed). The inner
  // stage/discard buttons already stopPropagation, exactly like the repo
  // sections' branch chip. Bug 4: expanded state restores from previous
  // renders via `open`, and persists via onToggle.
  createDisclosure(top, diffDrawer, {
    open: expandedDiffPaths.has(diffKey),
    onToggle: (open) => {
      li.classList.toggle("expanded", open);
      if (open) {
        expandedDiffPaths.add(diffKey);
        if (!loadedDiff) {
          loadDiff();
        }
      } else {
        expandedDiffPaths.delete(diffKey);
      }
    },
  });
  if (expandedDiffPaths.has(diffKey)) {
    // Initial open state is applied without an onToggle callback: mirror it.
    li.classList.add("expanded");
    loadDiff();
  }

  return li;
}

/** Render a unified-diff string with simple +/- coloring. Splits on
 *  newlines and wraps each line in a span with a class derived from
 *  its first character. Hunk headers (@@) get their own class. */
function renderDiffWithColors(diff: string): DocumentFragment {
  const frag = document.createDocumentFragment();
  for (const line of diff.split("\n")) {
    const span = el("span", { className: "git-diff-line" });
    if (line.startsWith("+++") || line.startsWith("---")) {
      span.classList.add("git-diff-line-meta");
    } else if (line.startsWith("@@")) {
      span.classList.add("git-diff-line-hunk");
    } else if (line.startsWith("+")) {
      span.classList.add("git-diff-line-add");
    } else if (line.startsWith("-")) {
      span.classList.add("git-diff-line-del");
    }
    span.textContent = line + "\n";
    frag.appendChild(span);
  }
  return frag;
}

// --- Helpers ---

const DEFAULT_BRANCHES = new Set(["main", "master", "develop", "trunk"]);

/** Heuristic: branches named "main", "master", "develop", "trunk"
 *  aren't feature branches and shouldn't trigger the post-push
 *  "Open PR" hint. Anything else is treated as a feature branch
 *  candidate. */
function isFeatureBranch(branch: string): boolean {
  return branch !== "" && !DEFAULT_BRANCHES.has(branch.toLowerCase());
}

/** Banner shown briefly after a successful push: invites the user to
 *  open a PR for the just-pushed branch. Click switches to the PRs
 *  tab and opens the new-PR dialog with source_branch pre-filled. */
function renderOpenPRHint(r: RepoStatus): HTMLElement {
  const hint = el("div", { className: "git-open-pr-hint" });
  hint.append(
    el("span", { className: "git-open-pr-hint-icon", "aria-hidden": "true" }, "💡"),
    el(
      "span",
      { className: "git-open-pr-hint-msg" },
      "Pushed ",
      el("strong", null, r.branch),
      " to origin. Open a pull request?",
    ),
  );
  const btn = el("button", { type: "button", className: "btn-small btn-primary" }, "Open PR");
  btn.addEventListener("click", () => {
    void (async () => {
      const { setGitTab } = await import("./git-tabs.js");
      const { openNewPRForRepo } = await import("./git-prs-tab.js");
      setGitTab("prs");
      await openNewPRForRepo(r.repo, r.branch);
    })().catch(() => {
      /* noop */
    });
  });
  hint.appendChild(btn);
  return hint;
}

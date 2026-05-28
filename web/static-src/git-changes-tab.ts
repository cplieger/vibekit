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
import { ICON_REFRESH } from "./icons.js";
import { withAsyncFeedback } from "./async-button.js";
import { confirm as confirmDialog } from "./confirm.js";
import { preserveGitScroll } from "./git-scroll.js";
import {
  stage,
  discard,
  pull,
  push,
  stash,
  stashPop,
  unstage,
} from "./actions/git-changes.js";
import { bindLoadingState, registerCleanup } from "./actions/index.js";
import { reconcile } from "./reconcile.js";
import { escAttr as escapeHTML } from "./strings.js";
import {
  renderRecentCommits,
  renderCommitArea,
  type CommitDeps,
} from "./git-changes-commit.js";

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

interface StatusAllResponse {
  repos: RepoStatus[];
}

// --- State ---

let inited = false;
let lastStatusAll: RepoStatus[] = [];
let filterText = "";
let refreshGeneration = 0;
let refreshAbort: AbortController | null = null;
let diffAbort: AbortController | null = null;
registerCleanup(() => {
  refreshAbort?.abort();
});
registerCleanup(() => diffAbort?.abort());

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
  return { commitMessages, bindingCleanups, diffAbort, refreshChanges, assertOk };
}

// --- Public API ---

/** Initialise the Changes tab. Wires the filter input, the global
 *  refresh button, and the SSE forge-changed event. Idempotent. */
export function initChangesTab(): void {
  if (inited) {
    return;
  }
  inited = true;
  const filterEl = document.getElementById("git-changes-filter") as HTMLInputElement | null;
  filterEl?.addEventListener("input", () => {
    filterText = filterEl.value.trim().toLowerCase();
    paint();
  });

  // Fire deferred paint when commit textarea loses focus.
  document.addEventListener("focusout", (e) => {
    if (
      paintDeferred &&
      e.target instanceof HTMLTextAreaElement &&
      e.target.classList.contains("git-commit-input")
    ) {
      paint();
    }
  });

  const refreshBtn = document.getElementById("git-refresh-all-btn") as HTMLButtonElement | null;
  if (refreshBtn !== null) {
    refreshBtn.innerHTML = ICON_REFRESH;
    refreshBtn.addEventListener("click", () => {
      // Default keepLabel=false: the icon is replaced by the
      // spinner while the refresh is in flight (then ✓/✗). The
      // button has no text label to keep, so this reads cleaner
      // than the icon + spinner side-by-side variant.
      void withAsyncFeedback(refreshBtn, () => refreshChanges());
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

  // Initial paint.
  void refreshChanges();
}

/** Force a full /api/git/status-all refresh and repaint. Concurrent
 *  calls are safe: a generation counter ensures stale responses are
 *  discarded. */
export async function refreshChanges(): Promise<void> {
  refreshAbort?.abort();
  const ctrl = new AbortController();
  refreshAbort = ctrl;
  const gen = ++refreshGeneration;
  const signal = AbortSignal.any([ctrl.signal, AbortSignal.timeout(15_000)]);
  const data = await apiGet<StatusAllResponse>("/api/git/status-all", signal);
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

  // Abort any in-flight diff fetches from the previous paint.
  diffAbort?.abort();
  diffAbort = new AbortController();

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
      mount: () => document.createElement("div"),
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
      return section ?? document.createElement("section");
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

const ICON_REPO_EMPTY =
  '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">' +
  '<path d="M4 19.5A2.5 2.5 0 016.5 17H20"/>' +
  '<path d="M6.5 2H20v20H6.5A2.5 2.5 0 014 19.5v-15A2.5 2.5 0 016.5 2z"/>' +
  "</svg>";

const ICON_CLEAN =
  '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">' +
  '<path d="M22 11.08V12a10 10 0 11-5.93-9.14"/>' +
  '<polyline points="22 4 12 14.01 9 11.01"/>' +
  "</svg>";

const ICON_FILTER =
  '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">' +
  '<polygon points="22 3 2 3 10 12.46 10 19 14 21 14 12.46 22 3"/>' +
  "</svg>";

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
  root.innerHTML = `<div class="git-multirepo-error">${msg}</div>`;
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

  const section = document.createElement("section");
  section.className = "git-repo-section";
  section.dataset["repo"] = r.repo;
  if (expandedDefault) {
    section.classList.add("expanded");
  }

  // Header
  const header = document.createElement("button");
  header.type = "button";
  header.className = "git-repo-section-header";
  header.setAttribute("aria-expanded", expandedDefault ? "true" : "false");
  header.innerHTML = renderHeaderHTML(r);
  header.addEventListener("click", (ev) => {
    // If the click target is inside a branch chip, route to the
    // branch switcher instead of toggling the section. The chip
    // intercepts at the bubbling phase before this listener.
    const target = ev.target as HTMLElement | null;
    const chip = target?.closest<HTMLElement>("[data-branch-trigger]");
    if (chip !== null && chip !== undefined && header.contains(chip)) {
      ev.preventDefault();
      ev.stopPropagation();
      void import("./git-branch-switcher.js")
        .then(({ openBranchSwitcher }) => {
          openBranchSwitcher(r.repo, chip);
        })
        .catch(() => {
          /* noop */
        });
      return;
    }
    const open = section.classList.toggle("expanded");
    header.setAttribute("aria-expanded", open ? "true" : "false");
    // Bug 3: Record user's explicit toggle so re-renders respect it.
    if (open) {
      userExpandedRepos.add(r.repo);
      userCollapsedRepos.delete(r.repo);
    } else {
      userCollapsedRepos.add(r.repo);
      userExpandedRepos.delete(r.repo);
    }
  });
  section.appendChild(header);

  // Body (the part that toggles)
  const body = document.createElement("div");
  body.className = "git-repo-section-body";

  if (!r.is_repo) {
    body.innerHTML = `<div class="git-repo-row-error">Not a git repository.</div>`;
    section.appendChild(body);
    return section;
  }

  // Action bar
  body.appendChild(renderActionBar(r));

  // Open-PR hint after a successful push (transient).
  if (recentlyPushed.has(r.repo) && isFeatureBranch(r.branch)) {
    body.appendChild(renderOpenPRHint(r));
  }

  // File list (staged first, then unstaged)
  if (filteredFiles.length === 0 && !r.has_dirty) {
    const clean = document.createElement("div");
    clean.className = "git-repo-row-clean";
    clean.textContent = "Clean.";
    body.appendChild(clean);
  } else if (filteredFiles.length === 0) {
    const noMatch = document.createElement("div");
    noMatch.className = "git-repo-row-empty";
    noMatch.textContent = "No paths match the filter.";
    body.appendChild(noMatch);
  } else {
    body.appendChild(renderFileList(r, filteredFiles));
  }

  // Commit area (only if there are staged files)
  const stagedCount = r.files.filter((f) => f.staged).length;
  if (stagedCount > 0) {
    body.appendChild(renderCommitArea(r, commitDeps()));
  }

  // Recent commits sub-section (collapsed by default; expand → fetch).
  body.appendChild(renderRecentCommits(r, commitDeps()));

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
  // be inside a button per HTML spec) but the section header captures
  // the click and routes it through openBranchSwitcher when the
  // target was inside the chip.
  return `
    <span class="git-repo-section-chevron" aria-hidden="true">▸</span>
    <span class="git-repo-section-name">${escapeHTML(r.repo)}</span>${dirty}
    <span class="git-repo-section-meta">
      <span class="git-repo-branch-chip" data-branch-trigger="${escapeHTML(r.repo)}" data-tooltip="Switch branch">${branch}</span>${ahead}${behind}${stashes}
    </span>
  `;
}

function renderActionBar(r: RepoStatus): HTMLElement {
  const bar = document.createElement("div");
  bar.className = "git-repo-action-bar";

  const btn = (label: string, title: string, danger = false): HTMLButtonElement => {
    const b = document.createElement("button");
    b.type = "button";
    b.className = `btn-small${danger ? " btn-danger" : ""}`;
    b.textContent = label;
    b.setAttribute("data-tooltip", title);
    return b;
  };

  const stageAllBtn = btn("Stage all", "Stage every unstaged change");
  stageAllBtn.addEventListener("click", () => {
    const files = r.files.filter((f) => !f.staged).map((f) => f.path);
    if (files.length === 0) {
      return;
    }
    void withAsyncFeedback(stageAllBtn, async () => {
      assertOk(await stage.dispatch({ repo: r.repo, files }));
      await refreshChanges();
    });
  });
  bar.appendChild(stageAllBtn);
  bindingCleanups.push(bindLoadingState(["git.stage", "git.commit"], stageAllBtn));

  const discardAllBtn = btn(
    "Discard all",
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
          `Discard ALL uncommitted changes in ${r.repo}? This cannot be undone.`,
          "Discard",
          "destructive",
        );
        if (!ok) {
          return;
        }
        const files = r.files.map((f) => f.path);
        if (files.length === 0) {
          return;
        }
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

  const sep = document.createElement("span");
  sep.className = "action-bar-sep";
  sep.setAttribute("aria-hidden", "true");
  bar.appendChild(sep);

  const pullBtn = btn("Pull", "git pull");
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

  const stashBtn = btn("Stash", "Stash uncommitted changes");
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
  const list = document.createElement("ul");
  list.className = "git-file-list";
  for (const f of sorted) {
    list.appendChild(renderFileRow(r, f));
  }
  return list;
}

function renderFileRow(r: RepoStatus, f: FileEntry): HTMLElement {
  const li = document.createElement("li");
  li.className = `git-file-row${f.staged ? " staged" : ""}`;

  // Top row: status + path + actions. The whole top row is the
  // click target for toggling the inline diff.
  const top = document.createElement("div");
  top.className = "git-file-row-top";

  const status = document.createElement("span");
  status.className = "git-file-status";
  status.textContent = f.display || statusLetter(f.status);
  status.setAttribute("data-tooltip", describeStatus(f.status));
  top.appendChild(status);

  const path = document.createElement("span");
  path.className = "git-file-path";
  path.textContent = f.path;
  path.setAttribute("data-tooltip", f.path);
  top.appendChild(path);

  const actions = document.createElement("span");
  actions.className = "git-file-actions";

  const action = (
    label: string,
    title: string,
    fn: () => Promise<unknown>,
    danger = false,
  ): HTMLButtonElement => {
    const b = document.createElement("button");
    b.type = "button";
    b.className = `btn-small${danger ? " btn-danger" : ""}`;
    b.textContent = label;
    b.setAttribute("data-tooltip", title);
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

  // Inline diff drawer (hidden until clicked). Lazy-loaded.
  const diffDrawer = document.createElement("div");
  diffDrawer.className = "git-file-diff hidden";
  li.appendChild(diffDrawer);

  // Bug 4: Unique key for tracking expanded state across re-renders.
  const diffKey = `${r.repo}\0${f.path}`;
  let loadedDiff = false;

  const loadDiff = (): void => {
    loadedDiff = true;
    diffDrawer.textContent = "Loading diff…";
    const signal = diffAbort ? AbortSignal.any([diffAbort.signal, AbortSignal.timeout(10_000)]) : AbortSignal.timeout(10_000);
    void apiGet<{ diff?: string }>(
      `/api/git/file-diff?repo=${encodeURIComponent(r.repo)}&path=${encodeURIComponent(f.path)}`,
      signal,
    ).then((data) => {
      if (signal.aborted) {
        return;
      }
      if (data === null) {
        diffDrawer.replaceChildren();
        const msg = document.createElement("span");
        msg.textContent = "Failed to load diff.";
        diffDrawer.appendChild(msg);
        const retryBtn = document.createElement("button");
        retryBtn.type = "button";
        retryBtn.className = "btn-small";
        retryBtn.textContent = "Retry";
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
      const pre = document.createElement("pre");
      pre.className = "git-file-diff-pre";
      pre.appendChild(renderDiffWithColors(diff));
      diffDrawer.appendChild(pre);
    });
  };

  // Bug 4: Restore expanded state from previous render.
  if (expandedDiffPaths.has(diffKey)) {
    diffDrawer.classList.remove("hidden");
    li.classList.add("expanded");
    loadDiff();
  }

  top.addEventListener("click", () => {
    const opening = !diffDrawer.classList.toggle("hidden");
    li.classList.toggle("expanded", opening);
    // Bug 4: Persist toggle state.
    if (opening) {
      expandedDiffPaths.add(diffKey);
    } else {
      expandedDiffPaths.delete(diffKey);
    }
    if (opening && !loadedDiff) {
      loadDiff();
    }
  });

  return li;
}

/** Render a unified-diff string with simple +/- coloring. Splits on
 *  newlines and wraps each line in a span with a class derived from
 *  its first character. Hunk headers (@@) get their own class. */
function renderDiffWithColors(diff: string): DocumentFragment {
  const frag = document.createDocumentFragment();
  for (const line of diff.split("\n")) {
    const span = document.createElement("span");
    span.className = "git-diff-line";
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
  const hint = document.createElement("div");
  hint.className = "git-open-pr-hint";
  hint.innerHTML = `
    <span class="git-open-pr-hint-icon" aria-hidden="true">💡</span>
    <span class="git-open-pr-hint-msg">Pushed <strong>${escapeHTML(r.branch)}</strong> to origin. Open a pull request?</span>
  `;
  const btn = document.createElement("button");
  btn.type = "button";
  btn.className = "btn-small btn-primary";
  btn.textContent = "Open PR";
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

function statusLetter(s: string): string {
  if (s.length >= 1) {
    return s.charAt(0);
  }
  return "?";
}

const GIT_STATUS_LABELS: Readonly<Record<string, string>> = {
  M: "Modified",
  A: "Added",
  D: "Deleted",
  R: "Renamed",
  "?": "Untracked",
  U: "Unmerged",
};

function describeStatus(s: string): string {
  return GIT_STATUS_LABELS[s.charAt(0)] ?? s;
}



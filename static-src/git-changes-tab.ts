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
import {
  ICON_REFRESH,
  ICON_REPO_EMPTY,
  ICON_FILTER,
  ICON_GIT_DOWN_ARROW,
  ICON_WARN_12,
} from "./icons.js";
import { withAsyncFeedback } from "./async-button.js";
import { confirm as confirmDialog } from "./confirm.js";
import { preserveGitScroll } from "./git-scroll.js";
import {
  stage,
  discard,
  pull,
  pullAll,
  push,
  stash,
  stashPop,
  unstage,
} from "./actions/git-changes.js";
import { bindLoadingState, registerCleanup } from "./actions/index.js";
import { reconcile } from "./reconcile.js";
import { openChange } from "./navigate.js";
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

import type {
  GitFileEntry as FileEntry,
  GitPullResult,
  GitRepoStatus as RepoStatus,
} from "./git-types.js";
import {
  statusLetter,
  describeStatus,
  stashableCount,
  changedPathCount,
  distinctPaths,
  partiallyStagedPaths,
  isPullHeld,
  pullHeldWord,
} from "./git-types.js";

interface StatusAllResponse {
  repos: RepoStatus[];
}

// --- State ---

let inited = false;
let lastStatusAll: RepoStatus[] = [];
let filterText = "";
let refreshGeneration = 0;
let refreshAbort: AbortController | null = null;
/** Long-lived abort controller for the recent-commits log fetch — only
 *  aborted on tab teardown, NOT on every repaint. */
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

/** What the last Pull all could NOT do, per repo — the input to the mark on a
 *  repo's block. Only the two verdicts a reader has to act on are kept
 *  (`isPullHeld`); a pulled or skipped repo needs no mark.
 *
 *  An entry survives a refresh, because the statement it makes is about a pass
 *  that happened and a reader who walked away should still find it. It is
 *  dropped the moment the repo stops being behind: there is nothing left to pull
 *  then, so the flag has stopped describing anything. Replaced wholesale by the
 *  next pass. */
const pullFlags = new Map<string, GitPullResult>();

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

  const pullAllBtn = document.getElementById("git-pull-all-btn") as HTMLButtonElement | null;
  if (pullAllBtn !== null) {
    pullAllBtn.innerHTML = ICON_GIT_DOWN_ARROW;
    pullAllBtn.addEventListener("click", () => {
      void withAsyncFeedback(pullAllBtn, runPullAll);
    });
    // Bound once, NOT pushed into bindingCleanups: the toolbar sits outside the
    // repaint cycle that array is drained by. `git.pull` is in the set so a
    // single repo's Pull disables this button too — they are the same operation
    // at two scopes, and running both at once serializes on the server anyway.
    bindLoadingState(["git.pull_all", "git.pull"], pullAllBtn);
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

/** Pull every repo a fast-forward is safe for, and mark the ones it is not.
 *
 *  The verdicts drive two surfaces: the framework's own toast carries the
 *  summary (the action owns that), and the two verdicts a reader has to act on
 *  are recorded here so each lands as a mark on its own repo block. Nothing is
 *  decided client-side about what was safe — the pass judged that beside the
 *  pull it guards.
 *
 *  The follow-up refresh is deliberately fetch-FREE: the pass has just fetched
 *  every remote, so the refs are fresh and a second round of network fetches
 *  would only re-derive an answer already on disk. */
async function runPullAll(): Promise<void> {
  const results = await pullAll.dispatch();
  assertOk(results);
  pullFlags.clear();
  for (const r of results) {
    if (isPullHeld(r)) {
      pullFlags.set(r.repo, r);
    }
  }
  await refreshChanges();
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
  // A pull flag reports what the last pass could not do, so it expires when the
  // repo stops being behind — nothing is left to pull, so the mark describes
  // nothing — and when the repo goes away. Keyed on `behind` rather than on the
  // hazard itself: resolving a conflict does not pull the repo, and the mark
  // should stand until something does.
  for (const k of pullFlags.keys()) {
    const repo = lastStatusAll.find((r) => r.repo === k);
    if (repo === undefined || repo.behind === 0) {
      pullFlags.delete(k);
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

  // Which repos survive the filter, and which of their files. ONE predicate,
  // read here to build the reconcile list and again in renderRepoSection to
  // build the rows — it used to be written twice, and the two copies disagreed:
  // this one kept a repo whose NAME matched, that one then applied the path
  // filter regardless and emptied it.
  const visibleRepos = lastStatusAll.filter((r) => filteredFilesFor(r) !== null);

  // Drop any prior non-keyed empty-state placeholder before reconciling.
  for (const child of [...root.children]) {
    if ((child as HTMLElement).getAttribute("data-reconcile-key") === null) {
      child.remove();
    }
  }

  // Every repo dropped, which can ONLY be the filter: filteredFilesFor returns
  // the file array rather than null whenever filterText is empty, so an
  // unfiltered paint always has one section per repo.
  //
  // There is deliberately no aggregate "everything is quiet" state beside this
  // one. It would replace the repo list on a workspace whose repos are merely
  // behind origin, ahead of it, or holding a stash, hiding both their sync
  // buttons and their branch chip — the only door to the branch switcher. That
  // a workspace has nothing pending anywhere is the sidebar git badge's fact.
  if (visibleRepos.length === 0) {
    reconcile(root, [] as RepoStatus[], {
      key: (r) => r.repo,
      mount: () => el("div"),
    });
    root.innerHTML = renderEmptyState({
      icon: ICON_FILTER,
      title: "No matching changes",
      hint: "Adjust your filter to see more.",
    });
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

/** The files of `r` the current filter admits, or null when the filter
 *  excludes the repo entirely.
 *
 *  The whole filter rule, in one place, because it has two readers: paint
 *  builds the reconcile list from it and renderRepoSection builds the rows.
 *
 *  A REPO-NAME match admits every file in it. Naming a repo is a request to
 *  see that repo, and applying the path filter underneath it emptied the
 *  section instead: a repo whose changed paths did not happen to repeat the
 *  repo name rendered "No paths match the filter." under its own heading.
 *
 *  An admitted repo always yields a non-empty file list OR has nothing
 *  uncommitted, which is what makes "no paths match" unreachable and lets
 *  renderRepoSection's empty case be the honest one. */
function filteredFilesFor(r: RepoStatus): FileEntry[] | null {
  if (filterText === "" || r.repo.toLowerCase().includes(filterText)) {
    return r.files;
  }
  const hits = r.files.filter((f) => f.path.toLowerCase().includes(filterText));
  return hits.length > 0 ? hits : null;
}

function renderRepoSection(r: RepoStatus): HTMLElement | null {
  const filteredFiles = filteredFilesFor(r);
  if (filteredFiles === null) {
    return null;
  }
  // Hide clean repos by default unless filter is active or repo
  // matched the filter explicitly.
  //
  // A repo the last Pull all marked needs no clause here: the pass only judges a
  // repo that is BEHIND, and the mark is pruned the moment it stops being
  // (paintInner), so `r.behind > 0` already opens every marked section and the
  // reason in its body is read without a click. One was written, and the red
  // check showed nothing could make it matter.
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

  // Action bar — omitted entirely when the repo state services no action.
  // Every button in it is state-gated (see renderActionBar), so a clean,
  // in-sync, stash-free repo produced an EMPTY bar: a zero-height flex child
  // that still consumes the column's `gap: var(--sp-3)`, which read as 12px of
  // dead space above "Clean." with nothing to account for it.
  const actionBar = renderActionBar(r);
  if (actionBar.childElementCount > 0) {
    inner.appendChild(actionBar);
  }

  // Why the last Pull all left this repo alone, ABOVE the action bar: the bar
  // holds the Pull button this note explains the absence of, so the reason
  // wants to be read first.
  const flag = pullFlags.get(r.repo);
  if (flag !== undefined) {
    inner.insertBefore(renderPullFlag(flag), inner.firstChild);
  }

  // Open-PR hint after a successful push (transient).
  if (recentlyPushed.has(r.repo) && isFeatureBranch(r.branch)) {
    inner.appendChild(renderOpenPRHint(r));
  }

  // The file list, in one group per side of the index.
  //
  // An empty list here means nothing is uncommitted, never "the filter hid
  // everything": filteredFilesFor returns null rather than an empty array in
  // that case, and paint drops the section before it gets here.
  //
  // The sentence names the WORKING TREE and not the repo, because this row
  // renders directly under the sync actions: a repo behind origin, ahead of it,
  // or holding a stash reaches it with Pull, Push or Pop right above, and
  // "Clean." read as a verdict on those too. Scoped, it also earns its place
  // beside Pull, which cannot conflict with local edits there are none of.
  if (filteredFiles.length === 0) {
    inner.appendChild(el("div", { className: "git-repo-row-clean" }, "No uncommitted changes."));
  } else {
    inner.appendChild(renderFileList(r, filteredFiles));
  }

  // Commit area, only when something is staged — the index IS the
  // selection, so with nothing staged there is nothing to compose a
  // message for. It gets the staged FILE count (not the entry count) so
  // its button can name what it will commit.
  const stagedCount = changedPathCount(r.files.filter((f) => f.staged));
  if (stagedCount > 0) {
    inner.appendChild(renderCommitArea(r, commitDeps(), stagedCount));
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
  // The Pull-all mark. In the HEADER because a collapsed section is all a reader
  // scanning fifty repos sees, and carrying the warn glyph as well as a hue so
  // the state never rests on colour alone. The tooltip is omitted rather than
  // emptied when there is no detail: an empty one is a tooltip that opens onto
  // nothing.
  const flag = pullFlags.get(r.repo);
  let held = "";
  if (flag !== undefined) {
    const tip = flag.detail ?? "";
    const tipAttr = tip === "" ? "" : ` data-tooltip="${escapeHTML(tip)}"`;
    held =
      ` <span class="git-repo-pull-flag" data-verdict="${escapeHTML(flag.verdict)}"${tipAttr}>` +
      `${ICON_WARN_12}${escapeHTML(pullHeldWord(flag))}</span>`;
  }
  const branch = escapeHTML(r.branch || "(detached)");
  // The branch chip is a span (not a nested button — buttons can't
  // be inside a button per HTML spec); a dedicated click listener on
  // the chip (wired in renderRepoSection) stops propagation and routes
  // to openBranchSwitcher, so the disclosure trigger's click never fires
  // for chip clicks.
  return `
    <span class="git-repo-section-name">${escapeHTML(r.repo)}</span>${dirty}
    <span class="git-repo-section-meta">
      <span class="git-repo-branch-chip" data-branch-trigger="${escapeHTML(r.repo)}" data-tooltip="Switch branch">${branch}</span>${ahead}${behind}${stashes}${held}
    </span>
  `;
}

/** The note inside a repo section saying why the last Pull all left it alone.
 *
 *  Inside the section as well as in the header, because the header has room for
 *  a word and the reason needs a sentence: which files, how many commits, which
 *  operation. Shaped like the post-push Open-PR hint, since both are a transient
 *  statement about what just happened to this one repo. The sentence is the
 *  server's — it is the side that knows what it found. */
function renderPullFlag(f: GitPullResult): HTMLElement {
  const box = el("div", { className: "git-pull-flag-note", "data-verdict": f.verdict });
  const icon = el("span", { className: "git-pull-flag-icon", "aria-hidden": "true" });
  icon.innerHTML = ICON_WARN_12;
  const lead = f.verdict === "failed" ? "Pull failed." : "Not pulled.";
  const detail = f.detail ?? "";
  box.append(
    icon,
    el(
      "span",
      { className: "git-pull-flag-msg" },
      el("strong", null, lead),
      detail === "" ? "" : ` ${detail}`,
    ),
  );
  return box;
}

/** The repo's SYNC actions: pull, push, stash, pop.
 *
 *  Stage all and Discard all used to lead this bar and have moved onto
 *  the file groups they act on (renderFileGroup), which is what lets
 *  their counts be checked against a heading the reader can see. That
 *  also retired the separator this function inserted between the two
 *  clusters: with one cluster left there is nothing to separate, so the
 *  insert, the trailing-separator cleanup and the `.action-bar-sep` rule
 *  in 14-tools.css are all gone.
 *
 *  Every button stays state-gated (18-F2): an action the repo state
 *  cannot service does not render, so there are no confirm-then-noop
 *  paths and no "Already up to date" pulls. */
function renderActionBar(r: RepoStatus): HTMLElement {
  const bar = el("div", { className: "git-repo-action-bar" });

  const btn = (label: string, title: string): HTMLButtonElement => {
    return el(
      "button",
      {
        type: "button",
        className: "btn-small",
        "data-tooltip": title,
      },
      label,
    ) as HTMLButtonElement;
  };

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

/** The file list, split into a Staged group and a Changes group.
 *
 *  It used to be ONE flat list, sorted staged-first, whose only marker of
 *  staged-ness was a 6% teal wash on the row: 1.09:1 against its own
 *  background in dark and 1.03:1 in light, under the 1.25:1 floor
 *  01-tokens.css declares for a step on its own ramp and well under
 *  WCAG 1.4.11's 3:1 for a state boundary. With the status cell reading
 *  "Modified" either way and the Unstage button hidden until hover, a
 *  staged row and an unstaged one were indistinguishable at rest, and the
 *  only dependable signal that anything was staged at all was the commit
 *  box appearing further down the section.
 *
 *  So staged-ness moves to a HEADER, where it is a count and a word. Each
 *  group also owns the bulk action that acts on exactly what its header
 *  counts, which is what makes those counts checkable: "Discard all" on
 *  the Changes header discards what "Changes (7)" names, and nothing
 *  else. The repo action bar keeps the sync operations.
 *
 *  A group renders only when it has members, so an all-staged tree shows
 *  one group rather than an empty second heading. */
function renderFileList(r: RepoStatus, files: FileEntry[]): HTMLElement {
  const wrap = el("div", { className: "git-file-groups" });
  const partial = partiallyStagedPaths(files);
  const staged = files.filter((f) => f.staged);
  const unstaged = files.filter((f) => !f.staged);

  if (staged.length > 0) {
    wrap.appendChild(renderFileGroup(r, "staged", staged, partial));
  }
  if (unstaged.length > 0) {
    wrap.appendChild(renderFileGroup(r, "unstaged", unstaged, partial));
  }
  return wrap;
}

/** One group: a header stating what it holds and how many, its own bulk
 *  actions, then its rows alphabetically. */
function renderFileGroup(
  r: RepoStatus,
  kind: "staged" | "unstaged",
  files: FileEntry[],
  partial: ReadonlySet<string>,
): HTMLElement {
  const group = el("div", { className: `git-file-group git-file-group-${kind}` });
  const count = changedPathCount(files);
  const label = kind === "staged" ? "Staged" : "Changes";

  const head = el("div", { className: "git-file-group-head" });
  head.append(
    el("span", { className: "git-file-group-label" }, label),
    el(
      "span",
      { className: "git-file-group-count" },
      `${String(count)} file${count === 1 ? "" : "s"}`,
    ),
  );

  const actions = el("span", { className: "git-file-group-actions" });
  for (const b of kind === "staged"
    ? stagedGroupActions(r, files)
    : unstagedGroupActions(r, files)) {
    actions.appendChild(b);
  }
  head.appendChild(actions);
  group.appendChild(head);

  // The heading is a sibling <div>, so nothing connects it to the list for a
  // screen reader: a row's own status cell reads "Status: Modified" on both
  // sides of the index, so without this label the group is a purely visual
  // distinction and staged-ness stays unannounced — which is the defect this
  // whole rework is about, just for a different reader. The count rides the
  // label so entering the list says how big it is.
  const heading = `${label}, ${String(count)} file${count === 1 ? "" : "s"}`;
  const list = el("ul", { className: "git-file-list", "aria-label": heading });
  const sorted = [...files].sort((a, b) => a.path.localeCompare(b.path));
  for (const f of sorted) {
    list.appendChild(renderFileRow(r, f, partial.has(f.path)));
  }
  group.appendChild(list);
  return group;
}

/** Build one group-header button. */
function groupBtn(label: string, title: string, danger = false): HTMLButtonElement {
  return el(
    "button",
    {
      type: "button",
      className: `btn-small${danger ? " btn-danger" : ""}`,
      "data-tooltip": title,
    },
    label,
  ) as HTMLButtonElement;
}

/** The Staged group's bulk action: Unstage all.
 *
 *  New capability. Staging every file was one click ("Stage all") and
 *  reversing it was N, one per row, with each row's Unstage button
 *  hidden until hovered. */
function stagedGroupActions(r: RepoStatus, files: FileEntry[]): HTMLButtonElement[] {
  const paths = distinctPaths(files);
  const b = groupBtn("Unstage all", "Move every staged change out of the index");
  b.addEventListener("click", () => {
    void withAsyncFeedback(b, async () => {
      assertOk(await unstage.dispatch({ repo: r.repo, files: paths }));
      await refreshChanges();
    });
  });
  bindingCleanups.push(bindLoadingState(["git.unstage", "git.commit"], b));
  return [b];
}

/** The Changes group's bulk actions: Stage all, then Discard all.
 *
 *  Both are scoped to THIS group, and that scope is the fix. Discard all
 *  used to sit in the repo action bar and take every entry including the
 *  staged ones, counted as `files.length` — so one file edited on both
 *  sides of the index made a destructive confirm offer to discard "2
 *  uncommitted changes", and that path went out twice in the payload.
 *  A bulk action whose scope is invisible is one nobody can check before
 *  pressing it. Discarding staged work too is Unstage all followed by
 *  this, which is two clicks and shows its work. */
function unstagedGroupActions(r: RepoStatus, files: FileEntry[]): HTMLButtonElement[] {
  const paths = distinctPaths(files);
  const count = paths.length;

  const stageBtn = groupBtn("Stage all", "Add every change below to the index");
  stageBtn.addEventListener("click", () => {
    void withAsyncFeedback(stageBtn, async () => {
      assertOk(await stage.dispatch({ repo: r.repo, files: paths }));
      await refreshChanges();
    });
  });
  bindingCleanups.push(bindLoadingState(["git.stage", "git.commit"], stageBtn));

  const discardBtn = groupBtn("Discard all", "Throw away every change below (irreversible)", true);
  discardBtn.addEventListener("click", () => {
    // Module-level guard: a second click while the first confirm is open
    // would dispatch two discards for one repo.
    if (discardPendingRepos.has(r.repo)) {
      return;
    }
    discardPendingRepos.add(r.repo);
    void (async () => {
      try {
        // The scope is stated rather than implied. A reader who expects a
        // clean tree afterwards has to be told the index is untouched,
        // and this is the last moment to tell them.
        const stagedCount = changedPathCount(r.files.filter((f) => f.staged));
        const keeps =
          stagedCount > 0
            ? ` Your ${String(stagedCount)} staged file${stagedCount === 1 ? "" : "s"} stay${stagedCount === 1 ? "s" : ""} untouched.`
            : "";
        const ok = await confirmDialog(
          `Discard ${String(count)} unstaged change${count === 1 ? "" : "s"} in ${r.repo}? This cannot be undone.${keeps}`,
          "Discard",
          "destructive",
        );
        if (!ok) {
          return;
        }
        await withAsyncFeedback(discardBtn, async () => {
          assertOk(await discard.dispatch({ repo: r.repo, files: paths }));
          await refreshChanges();
        });
      } finally {
        discardPendingRepos.delete(r.repo);
      }
    })();
  });
  bindingCleanups.push(bindLoadingState(["git.discard", "git.commit"], discardBtn));

  return [stageBtn, discardBtn];
}

function renderFileRow(r: RepoStatus, f: FileEntry, partiallyStaged: boolean): HTMLElement {
  const li = el("li", { className: "git-file-row" });

  // Top row: status + path + actions. Clicking it opens the file's diff in its
  // own editor tab.
  const top = el("div", { className: "git-file-row-top" });

  // A fixed-width COLOURED LETTER, not the status word this cell used to
  // print. Two reasons, and the first is measurable: "Untracked" is nine
  // characters against "Modified"'s eight and "M"'s one, and the cell
  // sized to its content, so every row's filename started at a different
  // x and a twenty-file list had a ragged left edge where the reader
  // scans. The second is that the app already HAS a per-letter palette
  // (`git-st-*`, 14-tools.css) which the file browser emits and this
  // panel did not, so the same change on the same file was a coloured
  // letter in one view and a grey word in the other. The word is not
  // lost: it is the tooltip and the accessible name, and `display` is
  // preferred over the local table because the server owns the mapping.
  const letter = statusLetter(f.status);
  const word = f.display || describeStatus(f.status);
  const status = el(
    "span",
    {
      className: `git-file-status git-st-${letter.toLowerCase()}`,
      "data-tooltip": word,
      "aria-label": `Status: ${word}`,
    },
    letter,
  );
  top.appendChild(status);

  // The filename IS the link to its own diff, which is the convention every
  // other changed-file affordance in the app already follows (navigate.ts
  // openChange, reached here and from a turn's ledger row, a tool card's
  // filename and the file browser's status letter).
  //
  // A real <button> rather than a role on the row, which is what the inline
  // drawer's disclosure trigger used to be: `role="button"` is
  // Children-Presentational, so it flattened the Stage and Discard buttons
  // beside it out of the accessibility tree. The row keeps a mouse handler
  // below, so the wide click target survives without that cost.
  const path = el(
    "button",
    {
      type: "button",
      className: "git-file-path",
      "data-tooltip": f.path,
      "aria-label": `Open diff for ${f.path}`,
    },
    f.path,
  ) as HTMLButtonElement;
  path.addEventListener("click", (ev) => {
    ev.stopPropagation();
    openFileDiff(r, f);
  });
  top.appendChild(path);

  // Where a rename or copy came FROM. The server parsed this field and
  // threw it away, so a moved file rendered as "Renamed  path/to/new.ts"
  // with no way to tell what had moved — the one status whose whole
  // meaning is the pair of paths.
  const orig = f.orig_path ?? "";
  if (orig !== "") {
    top.appendChild(
      el(
        "span",
        {
          className: "git-file-orig",
          "data-tooltip": `${letter === "C" ? "Copied" : "Renamed"} from ${orig}`,
        },
        `\u2190 ${orig}`,
      ),
    );
  }

  // Why this path appears twice. Both of its rows carry the mark, so it
  // reads as one file in two states rather than as a duplicate.
  if (partiallyStaged) {
    top.appendChild(
      el(
        "span",
        {
          className: "git-file-partial",
          "data-tooltip":
            "This file is staged AND changed again since. Each side is listed in its own group.",
        },
        "partially staged",
      ),
    );
  }

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

  // The whole row stays a mouse target, exactly as the drawer's trigger was.
  // Keyboard users reach the same action through the filename button above, and
  // the Stage/Discard buttons already stopPropagation.
  top.addEventListener("click", () => {
    openFileDiff(r, f);
  });

  return li;
}

/** Open one changed file's diff against HEAD, in its own editor tab.
 *
 *  The inline drawer this replaced rendered the raw unified-diff TEXT in a
 *  `<pre>` inside the file list, so a line wider than that column was simply
 *  clipped, in a surface with no room to scroll it: no line numbers, no
 *  highlighting, and a hard 26rem ceiling. The editor's diff tab is the app's
 *  existing answer for a changed file, and it was already the surface every
 *  other changed-file click reached; the git panel was the one outlier.
 *
 *  Two conversions happen here and only here. The Changes tab holds REPO-relative
 *  paths with one repo per section, while `openChange` takes the
 *  workspace-relative form every other caller has, so the repo name is joined
 *  back on — the workspace-root repo is named "." and owns paths with no prefix.
 *  The repo is then deliberately NOT passed onward: the diff loader resolves the
 *  owning repository from a workspace-relative path (internal/git `ownerOf`), so
 *  one spelling serves both sides of its fetch. */
function openFileDiff(r: RepoStatus, f: FileEntry): void {
  openChange(r.repo === "." ? f.path : `${r.repo}/${f.path}`);
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

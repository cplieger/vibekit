// ---------------------------------------------------------------------------
// Account-repos detail panel rendering. Extracted from forge-auth.ts.
//
// Pure DOM factory: takes account + repos data and returns/mutates
// elements. No dependency on reactive state (signal, effect) or
// reconcile specs — those are injected via the RenderDeps interface.
// ---------------------------------------------------------------------------

import { ICON_DOWNLOAD, ICON_REPO, ICON_TRASH } from "./icons.js";
import { iconEl } from "./icon-el.js";
import { withAsyncFeedback } from "./async-button.js";
import type { ConfiguredForge, Repo } from "./wire/types.gen.js";
import { reconcile, type ReconcileSpec } from "./reconcile.js";
import {
  cloneAllForAccount,
  deleteAllForAccount,
  type RepoDeps,
} from "./forge-auth-repos.js";

export interface ReposRenderDeps {
  lastLocalNames: Set<string>;
  expandOnNextPaint: Set<string>;
  bumpState: () => void;
  repoDeps: RepoDeps;
  repoSpec: ReconcileSpec<Repo>;
}

export function buildAccountReposDetails(
  a: ConfiguredForge,
  repos: Repo[],
  deps: ReposRenderDeps,
): HTMLElement {
  const details = document.createElement("details");
  details.className = "forge-account-repos";
  details.dataset["accountId"] = a.id;
  if (deps.expandOnNextPaint.has(a.id)) {
    details.open = true;
    deps.expandOnNextPaint.delete(a.id);
  }

  const summary = document.createElement("summary");
  summary.className = "forge-account-repos-summary";
  const chevron = document.createElement("span");
  chevron.className = "forge-account-repos-chevron";
  chevron.setAttribute("aria-hidden", "true");
  chevron.textContent = "▸";
  const repoIcon = document.createElement("span");
  repoIcon.className = "forge-account-repos-icon";
  repoIcon.setAttribute("aria-hidden", "true");
  repoIcon.replaceChildren(iconEl(ICON_REPO));
  const labelSpan = document.createElement("span");
  labelSpan.className = "forge-account-repos-label";
  summary.replaceChildren(chevron, repoIcon, labelSpan);
  setAccountSummaryLabel(summary, repos, deps);
  refreshAccountSummaryButtons(summary, repos, deps);
  details.appendChild(summary);

  if (repos.length === 0) {
    const none = document.createElement("div");
    none.className = "forge-account-repos-empty";
    none.textContent = "No repositories accessible to this account.";
    details.appendChild(none);
    return details;
  }

  const list = document.createElement("ul");
  list.className = "forge-account-repos-list";
  details.appendChild(list);
  reconcile(list, sortRepos(repos, deps), deps.repoSpec);
  return details;
}

export function updateAccountReposDetails(
  details: HTMLElement,
  a: ConfiguredForge,
  repos: Repo[],
  deps: ReposRenderDeps,
): void {
  if (deps.expandOnNextPaint.has(a.id)) {
    (details as HTMLDetailsElement).open = true;
    deps.expandOnNextPaint.delete(a.id);
  }
  const summary = details.querySelector<HTMLElement>(":scope > .forge-account-repos-summary");
  if (summary !== null) {
    setAccountSummaryLabel(summary, repos, deps);
    refreshAccountSummaryButtons(summary, repos, deps);
  }

  // Empty-state placeholder vs list <ul>.
  const emptyEl = details.querySelector<HTMLElement>(":scope > .forge-account-repos-empty");
  let list = details.querySelector<HTMLElement>(":scope > .forge-account-repos-list");
  if (repos.length === 0) {
    list?.remove();
    if (emptyEl === null) {
      const none = document.createElement("div");
      none.className = "forge-account-repos-empty";
      none.textContent = "No repositories accessible to this account.";
      details.appendChild(none);
    }
    return;
  }
  emptyEl?.remove();
  if (list === null) {
    list = document.createElement("ul");
    list.className = "forge-account-repos-list";
    details.appendChild(list);
  }
  reconcile(list, sortRepos(repos, deps), deps.repoSpec);
}

export function sortRepos(repos: Repo[], deps: ReposRenderDeps): Repo[] {
  // Cloned first, then alpha by full_name. Stable for surgical
  // updates: a single repo flipping cloned-state moves between
  // groups, but the rest stay put. Reconcile preserves identity
  // during the move.
  return [...repos].sort((x, y) => {
    const xc = deps.lastLocalNames.has(x.name);
    const yc = deps.lastLocalNames.has(y.name);
    if (xc !== yc) {
      return xc ? -1 : 1;
    }
    return x.full_name.localeCompare(y.full_name);
  });
}

function setAccountSummaryLabel(
  summary: HTMLElement,
  repos: Repo[],
  deps: ReposRenderDeps,
): void {
  const total = repos.length;
  const cloned = repos.filter((r) => deps.lastLocalNames.has(r.name)).length;
  const label = summary.querySelector<HTMLElement>(".forge-account-repos-label");
  if (label !== null) {
    label.textContent = `${total} repo${total === 1 ? "" : "s"}, ${cloned} cloned locally`;
  }
}

/** Rebuild cloneAll/deleteAll buttons in the summary. Skips a button
 *  that is currently mid-async (`aria-busy="true"`) so a
 *  withAsyncFeedback loop's textContent updates don't get clobbered;
 *  the next bumpState after the action completes will refresh it. */
function refreshAccountSummaryButtons(
  summary: HTMLElement,
  repos: Repo[],
  deps: ReposRenderDeps,
): void {
  const cloneable = repos.filter(
    (r) => !deps.lastLocalNames.has(r.name) && typeof r.clone_url === "string" && r.clone_url !== "",
  );
  const clonedRepos = repos.filter((r) => deps.lastLocalNames.has(r.name));

  const oldCloneAll = summary.querySelector<HTMLButtonElement>(".forge-account-repos-clone-all");
  if (oldCloneAll?.getAttribute("aria-busy") !== "true") {
    oldCloneAll?.remove();
    if (cloneable.length > 0) {
      summary.appendChild(makeCloneAllButton(cloneable, deps));
    }
  }

  const oldDeleteAll = summary.querySelector<HTMLButtonElement>(".forge-account-repos-delete-all");
  if (oldDeleteAll?.getAttribute("aria-busy") !== "true") {
    oldDeleteAll?.remove();
    if (clonedRepos.length > 0) {
      summary.appendChild(makeDeleteAllButton(clonedRepos, deps));
    }
  }
}

function makeCloneAllButton(cloneable: Repo[], deps: ReposRenderDeps): HTMLButtonElement {
  const btn = document.createElement("button");
  btn.type = "button";
  btn.className = "btn-small forge-account-repos-clone-all";
  const countSpan = document.createElement("span");
  countSpan.textContent = String(cloneable.length);
  btn.replaceChildren(iconEl(ICON_DOWNLOAD), countSpan);
  btn.setAttribute(
    "data-tooltip",
    `Clone every uncloned repo on this account (${cloneable.length})`,
  );
  btn.setAttribute("aria-label", `Clone ${cloneable.length} uncloned repos`);
  btn.addEventListener("click", (ev) => {
    ev.stopPropagation();
    ev.preventDefault();
    void withAsyncFeedback(btn, () => cloneAllForAccount(cloneable, btn, deps.repoDeps)).then(() => {
      deps.bumpState();
    });
  });
  return btn;
}

function makeDeleteAllButton(clonedRepos: Repo[], deps: ReposRenderDeps): HTMLButtonElement {
  const btn = document.createElement("button");
  btn.type = "button";
  btn.className = "btn-small btn-danger forge-account-repos-delete-all";
  const delCountSpan = document.createElement("span");
  delCountSpan.textContent = String(clonedRepos.length);
  btn.replaceChildren(iconEl(ICON_TRASH), delCountSpan);
  btn.setAttribute(
    "data-tooltip",
    `Remove every locally-cloned repo on this account (${clonedRepos.length})`,
  );
  btn.setAttribute("aria-label", `Delete ${clonedRepos.length} local clones`);
  btn.addEventListener("click", (ev) => {
    ev.stopPropagation();
    ev.preventDefault();
    void withAsyncFeedback(btn, () => deleteAllForAccount(clonedRepos, btn, deps.repoDeps)).then(() => {
      deps.bumpState();
    });
  });
  return btn;
}

// ---------------------------------------------------------------------------
// Account-repos detail panel rendering. Extracted from forge-auth.ts.
//
// Pure DOM factory: takes account + repos data and returns/mutates
// elements. No dependency on reactive state (signal, effect) or
// reconcile specs — those are injected via the RenderDeps interface.
// ---------------------------------------------------------------------------

import { el } from "@cplieger/reactive";
import { ICON_DOWNLOAD, ICON_REPO, ICON_TRASH } from "./icons.js";
import { iconEl } from "./icon-el.js";
import { withAsyncFeedback } from "./async-button.js";
import type { ConfiguredForge, Repo } from "./wire/types.gen.js";
import { reconcile, type ReconcileSpec } from "./reconcile.js";
import { cloneAllForAccount, deleteAllForAccount, type RepoDeps } from "./forge-auth-repos.js";

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
  const details = el("details", {
    className: "forge-account-repos",
    "data-account-id": a.id,
  }) as HTMLDetailsElement;
  if (deps.expandOnNextPaint.has(a.id)) {
    details.open = true;
    deps.expandOnNextPaint.delete(a.id);
  }

  const summary = el(
    "summary",
    { className: "forge-account-repos-summary" },
    el("span", { className: "forge-account-repos-chevron", "aria-hidden": "true" }, "▸"),
    el("span", { className: "forge-account-repos-icon", "aria-hidden": "true" }, iconEl(ICON_REPO)),
    el("span", { className: "forge-account-repos-label" }),
  );
  setAccountSummaryLabel(summary, repos, deps);
  refreshAccountSummaryButtons(summary, repos, deps);
  details.appendChild(summary);

  if (repos.length === 0) {
    details.appendChild(
      el(
        "div",
        { className: "forge-account-repos-empty" },
        "No repositories accessible to this account.",
      ),
    );
    return details;
  }

  const list = el("ul", { className: "forge-account-repos-list" });
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
      details.appendChild(
        el(
          "div",
          { className: "forge-account-repos-empty" },
          "No repositories accessible to this account.",
        ),
      );
    }
    return;
  }
  emptyEl?.remove();
  if (list === null) {
    list = el("ul", { className: "forge-account-repos-list" });
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

function setAccountSummaryLabel(summary: HTMLElement, repos: Repo[], deps: ReposRenderDeps): void {
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
    (r) =>
      !deps.lastLocalNames.has(r.name) && typeof r.clone_url === "string" && r.clone_url !== "",
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
  const btn = el(
    "button",
    {
      type: "button",
      className: "btn-small forge-account-repos-clone-all",
      "data-tooltip": `Clone every uncloned repo on this account (${cloneable.length})`,
      "aria-label": `Clone ${cloneable.length} uncloned repos`,
    },
    iconEl(ICON_DOWNLOAD),
    el("span", null, String(cloneable.length)),
  ) as HTMLButtonElement;
  btn.addEventListener("click", (ev) => {
    ev.stopPropagation();
    ev.preventDefault();
    void withAsyncFeedback(btn, () => cloneAllForAccount(cloneable, btn, deps.repoDeps)).then(
      () => {
        deps.bumpState();
      },
    );
  });
  return btn;
}

function makeDeleteAllButton(clonedRepos: Repo[], deps: ReposRenderDeps): HTMLButtonElement {
  const btn = el(
    "button",
    {
      type: "button",
      className: "btn-small btn-danger forge-account-repos-delete-all",
      "data-tooltip": `Remove every locally-cloned repo on this account (${clonedRepos.length})`,
      "aria-label": `Delete ${clonedRepos.length} local clones`,
    },
    iconEl(ICON_TRASH),
    el("span", null, String(clonedRepos.length)),
  ) as HTMLButtonElement;
  btn.addEventListener("click", (ev) => {
    ev.stopPropagation();
    ev.preventDefault();
    void withAsyncFeedback(btn, () => deleteAllForAccount(clonedRepos, btn, deps.repoDeps)).then(
      () => {
        deps.bumpState();
      },
    );
  });
  return btn;
}

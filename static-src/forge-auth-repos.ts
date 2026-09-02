// ---------------------------------------------------------------------------
// Repo-row rendering and batch clone/delete actions for the forge panel.
// Extracted from forge-auth.ts.
// ---------------------------------------------------------------------------

import { el } from "@cplieger/reactive";
import { ICON_DOWNLOAD, ICON_EXTERNAL, ICON_GLOBE, ICON_TRASH } from "./icons.js";
import { iconEl } from "./icon-el.js";
import { withAsyncFeedback } from "./async-button.js";
import { error as toastError } from "./toast.js";
import { confirm as confirmDialog } from "./confirm.js";
import type { Repo } from "./wire/types.gen.js";
import { cloneRepo as cloneRepoAction, deleteLocal as deleteLocalAction } from "./actions/forge.js";

export interface RepoDeps {
  /** Check if a repo name is locally cloned. */
  isCloned: (name: string) => boolean;
  /** Mark a repo name as locally cloned. */
  addCloned: (name: string) => void;
  /** Remove a repo name from the cloned set. */
  removeCloned: (name: string) => void;
  /** Bump the state version to trigger a re-render. */
  bumpState: () => void;
}

export function renderRepoRow(repo: Repo, deps: RepoDeps): HTMLElement {
  const li = el("li", { className: "forge-account-repo-row" });
  const cloned = deps.isCloned(repo.name);

  li.appendChild(renderRepoState(cloned));

  const idEl = el(
    "div",
    { className: "forge-account-repo-identity" },
    el("span", { className: "forge-account-repo-name" }, repo.full_name),
  );
  const tags: string[] = [];
  if (repo.private === true) {
    tags.push("private");
  }
  if (repo.archived === true) {
    tags.push("archived");
  }
  if (repo.fork === true) {
    tags.push("fork");
  }
  if (repo.default_branch !== undefined && repo.default_branch !== "") {
    tags.push(repo.default_branch);
  }
  if (tags.length > 0) {
    idEl.appendChild(el("span", { className: "forge-account-repo-tags" }, tags.join(" · ")));
  }
  li.appendChild(idEl);

  li.appendChild(renderRepoActions(repo, cloned, deps));
  return li;
}

export function renderRepoState(cloned: boolean): HTMLElement {
  const state = el("span", { className: "forge-account-repo-state" });
  if (cloned) {
    state.appendChild(
      el("span", {
        className: "git-sources-cloned-dot",
        "aria-label": "Cloned",
        "data-tooltip": "Cloned and tracked",
      }),
    );
  } else {
    state.appendChild(iconEl(ICON_GLOBE));
    state.setAttribute("data-tooltip", "Remote, not cloned");
    state.setAttribute("aria-label", "Remote, not cloned");
  }
  return state;
}

/** Run a row action with button feedback AND a toast on failure.
 *
 *  withAsyncFeedback awaits without rethrowing, so a bare throw inside it
 *  reaches nobody: the button shows a ✗ glyph for 1.2s and any repaint of
 *  the actions row (a background revalidate, a forges_changed event)
 *  erases even that. A clone that git refused therefore read as a spinner
 *  followed by nothing at all. The toast is what actually reports the
 *  reason; the rethrow keeps the button's own error state. */
function withRowFeedback(btn: HTMLButtonElement, fn: () => Promise<void>): void {
  void withAsyncFeedback(btn, async () => {
    try {
      await fn();
    } catch (e) {
      toastError(e instanceof Error ? e.message : String(e));
      throw e;
    }
  });
}

export function renderRepoActions(repo: Repo, cloned: boolean, deps: RepoDeps): HTMLElement {
  const actions = el("span", { className: "forge-account-repo-actions" });

  if (repo.url !== undefined && repo.url !== "") {
    const open = el(
      "a",
      {
        href: repo.url,
        target: "_blank",
        rel: "noreferrer",
        className: "btn-small icon-only",
        "data-tooltip": "Open on forge",
        "aria-label": "Open on forge",
      },
      iconEl(ICON_EXTERNAL),
    );
    actions.appendChild(open);
  }

  if (cloned) {
    const trash = el(
      "button",
      {
        type: "button",
        className: "btn-small btn-danger icon-only",
        "data-tooltip": "Remove local copy",
        "aria-label": "Remove local copy",
      },
      iconEl(ICON_TRASH),
    ) as HTMLButtonElement;
    trash.addEventListener("click", () => {
      withRowFeedback(trash, () => removeLocalRepo(repo, deps));
    });
    actions.appendChild(trash);
  } else if (repo.clone_url !== undefined && repo.clone_url !== "") {
    const clone = el(
      "button",
      {
        type: "button",
        className: "btn-small icon-only",
        "data-tooltip": "Clone into workspace",
        "aria-label": "Clone into workspace",
      },
      iconEl(ICON_DOWNLOAD),
    ) as HTMLButtonElement;
    clone.addEventListener("click", () => {
      withRowFeedback(clone, () => cloneRepo(repo, deps));
    });
    actions.appendChild(clone);
  }

  return actions;
}

async function cloneRepo(repo: Repo, deps: RepoDeps): Promise<void> {
  const url = repo.clone_url ?? "";
  if (url === "") {
    throw new Error("no clone URL");
  }
  // Typed outcome: withRowFeedback toasts this throw (error: false on the
  // action suppresses the framework's own), so carry the real failure
  // reason instead of a synthetic "clone failed".
  const o = await cloneRepoAction.dispatch({ url }).outcome;
  if (o.status !== "success") {
    throw new Error(o.status === "error" ? o.error.message : "clone cancelled");
  }
  const res = o.value;
  if (res.error !== undefined && res.error !== "") {
    throw new Error(res.error);
  }
  deps.addCloned(repo.name);
  deps.bumpState();
}

export async function cloneAllForAccount(
  candidates: Repo[],
  btn: HTMLButtonElement,
  deps: RepoDeps,
): Promise<void> {
  if (candidates.length === 0) {
    return;
  }
  let done = 0;
  const failedNames: string[] = [];
  for (const repo of candidates) {
    btn.textContent = `Cloning ${done + 1}/${candidates.length}…`;
    const url = repo.clone_url ?? "";
    if (url === "") {
      failedNames.push(repo.name);
      done++;
      continue;
    }
    const res = await cloneRepoAction.dispatch({ url });
    if (res === null || (res.error !== undefined && res.error !== "")) {
      failedNames.push(repo.name);
    } else {
      deps.addCloned(repo.name);
      deps.bumpState();
    }
    done++;
  }
  if (failedNames.length > 0) {
    toastError(cloneFailureToast(failedNames, candidates.length));
  }
}

/** Word the batch-failure toast, NAMING the failed repos: a bare count
 *  ("1 of 63 failed") leaves the user diffing 63 directories to find out
 *  which one. Up to three names in full; the rest as a count. */
export function cloneFailureToast(failedNames: readonly string[], total: number): string {
  const shown = failedNames.slice(0, 3).join(", ");
  const more = failedNames.length - 3;
  const names = more > 0 ? `${shown} and ${String(more)} more` : shown;
  if (failedNames.length === 1) {
    return `Clone failed for ${names} (1 of ${String(total)} repos)`;
  }
  return `Clone failed for ${names} (${String(failedNames.length)} of ${String(total)} repos)`;
}

export async function deleteAllForAccount(
  candidates: Repo[],
  btn: HTMLButtonElement,
  deps: RepoDeps,
): Promise<void> {
  if (candidates.length === 0) {
    return;
  }
  const ok = await confirmDialog(
    `Delete the local copy of ${candidates.length} repo${candidates.length === 1 ? "" : "s"}? The remotes stay intact; you can re-clone any of them later.`,
    "Delete all",
    "destructive",
  );
  if (!ok) {
    return;
  }

  let done = 0;
  for (const repo of candidates) {
    btn.textContent = `Deleting ${done + 1}/${candidates.length}…`;
    const res = await deleteLocalAction.dispatch({ repoName: repo.name });
    if (res !== null && (res.error === undefined || res.error === "")) {
      deps.removeCloned(repo.name);
      deps.bumpState();
    }
    done++;
  }
}

async function removeLocalRepo(repo: Repo, deps: RepoDeps): Promise<void> {
  const ok = await confirmDialog(
    `Delete the local copy of ${repo.name}? The remote stays intact; you can re-clone later.`,
    "Delete",
    "destructive",
  );
  if (!ok) {
    return;
  }

  // Optimistic: flip clone state and bump; effect reconciles surgically.
  deps.removeCloned(repo.name);
  deps.bumpState();

  const res = await deleteLocalAction.dispatch({ repoName: repo.name });
  if (res === null || (res.error !== undefined && res.error !== "")) {
    // Rollback.
    deps.addCloned(repo.name);
    deps.bumpState();
    const msg = res?.error ?? "Couldn't remove local repo";
    throw new Error(msg);
  }
}

// ---------------------------------------------------------------------------
// Repo-row rendering and batch clone/delete actions for the forge panel.
// Extracted from forge-auth.ts.
// ---------------------------------------------------------------------------

import {
  ICON_DOWNLOAD,
  ICON_EXTERNAL,
  ICON_GLOBE,
  ICON_TRASH,
} from "./icons.js";
import { iconEl } from "./icon-el.js";
import { withAsyncFeedback } from "./async-button.js";
import { error as toastError } from "./toast.js";
import { confirm as confirmDialog } from "./confirm.js";
import type { Repo } from "./wire/types.gen.js";
import {
  cloneRepo as cloneRepoAction,
  deleteLocal as deleteLocalAction,
} from "./actions/forge.js";

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
  const li = document.createElement("li");
  li.className = "forge-account-repo-row";
  const cloned = deps.isCloned(repo.name);

  li.appendChild(renderRepoState(cloned));

  const idEl = document.createElement("div");
  idEl.className = "forge-account-repo-identity";
  const name = document.createElement("span");
  name.className = "forge-account-repo-name";
  name.textContent = repo.full_name;
  idEl.appendChild(name);
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
    const tagSpan = document.createElement("span");
    tagSpan.className = "forge-account-repo-tags";
    tagSpan.textContent = tags.join(" · ");
    idEl.appendChild(tagSpan);
  }
  li.appendChild(idEl);

  li.appendChild(renderRepoActions(repo, cloned, deps));
  return li;
}

export function renderRepoState(cloned: boolean): HTMLElement {
  const state = document.createElement("span");
  state.className = "forge-account-repo-state";
  if (cloned) {
    const dot = document.createElement("span");
    dot.className = "git-sources-cloned-dot";
    dot.setAttribute("aria-label", "Cloned");
    dot.setAttribute("data-tooltip", "Cloned and tracked");
    state.appendChild(dot);
  } else {
    state.appendChild(iconEl(ICON_GLOBE));
    state.setAttribute("data-tooltip", "Remote, not cloned");
    state.setAttribute("aria-label", "Remote, not cloned");
  }
  return state;
}

export function renderRepoActions(repo: Repo, cloned: boolean, deps: RepoDeps): HTMLElement {
  const actions = document.createElement("span");
  actions.className = "forge-account-repo-actions";

  if (repo.url !== undefined && repo.url !== "") {
    const open = document.createElement("a");
    open.href = repo.url;
    open.target = "_blank";
    open.rel = "noreferrer";
    open.className = "btn-small icon-only";
    open.replaceChildren(iconEl(ICON_EXTERNAL));
    open.setAttribute("data-tooltip", "Open on forge");
    open.setAttribute("aria-label", "Open on forge");
    actions.appendChild(open);
  }

  if (cloned) {
    const trash = document.createElement("button");
    trash.type = "button";
    trash.className = "btn-small btn-danger icon-only";
    trash.replaceChildren(iconEl(ICON_TRASH));
    trash.setAttribute("data-tooltip", "Remove local copy");
    trash.setAttribute("aria-label", "Remove local copy");
    trash.addEventListener("click", () => {
      void withAsyncFeedback(trash, () => removeLocalRepo(repo, deps));
    });
    actions.appendChild(trash);
  } else if (repo.clone_url !== undefined && repo.clone_url !== "") {
    const clone = document.createElement("button");
    clone.type = "button";
    clone.className = "btn-small icon-only";
    clone.replaceChildren(iconEl(ICON_DOWNLOAD));
    clone.setAttribute("data-tooltip", "Clone into workspace");
    clone.setAttribute("aria-label", "Clone into workspace");
    clone.addEventListener("click", () => {
      void withAsyncFeedback(clone, () => cloneRepo(repo, deps));
    });
    actions.appendChild(clone);
  }

  return actions;
}

export async function cloneRepo(repo: Repo, deps: RepoDeps): Promise<void> {
  const url = repo.clone_url ?? "";
  if (url === "") {
    throw new Error("no clone URL");
  }
  const res = await cloneRepoAction.dispatch({ url });
  if (res === null) {
    throw new Error("clone failed");
  }
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
  let failed = 0;
  for (const repo of candidates) {
    btn.textContent = `Cloning ${done + 1}/${candidates.length}…`;
    const url = repo.clone_url ?? "";
    if (url === "") {
      failed++;
      done++;
      continue;
    }
    const res = await cloneRepoAction.dispatch({ url });
    if (res === null || (res.error !== undefined && res.error !== "")) {
      failed++;
    } else {
      deps.addCloned(repo.name);
      deps.bumpState();
    }
    done++;
  }
  if (failed > 0) {
    toastError(`Clone failed for ${String(failed)} of ${String(candidates.length)} repos`);
  }
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

export async function removeLocalRepo(repo: Repo, deps: RepoDeps): Promise<void> {
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

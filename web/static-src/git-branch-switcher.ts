// ---------------------------------------------------------------------------
// Per-repo branch switcher: small popover anchored to a repo's
// section header that lists the local branches and offers a
// "Create new branch" action. Click → checkout, type a new name +
// Enter → checkout -b.
//
// Used by git-changes-tab.ts: each repo section's branch chip is
// wired to openBranchSwitcher(repo, anchorEl). The popover is a
// singleton (only one open at a time); reopening swaps the anchor.
// ---------------------------------------------------------------------------

import { apiGet } from "./api-client.js";
import { checkoutBranch } from "./actions/git-branch.js";
import { registerCleanup } from "./actions/cleanup.js";
import { bindLoadingState } from "./actions/index.js";

interface BranchEntry { name: string; current: boolean; }
interface BranchesResponse { branches: BranchEntry[]; current: string; }

let openPopover: HTMLDivElement | null = null;
let activeAnchor: HTMLElement | null = null;
let branchController: AbortController | null = null;
let popoverBindingCleanups: Array<() => void> = [];
registerCleanup(() => branchController?.abort());

/** Open the branch switcher anchored to anchorEl for repo. Idempotent
 *  on the same anchor (re-clicks toggle close); on a different anchor
 *  the previous popover closes and a new one opens. */
export function openBranchSwitcher(repo: string, anchorEl: HTMLElement): void {
  if (openPopover !== null && activeAnchor === anchorEl) {
    closePopover();
    return;
  }
  closePopover();
  activeAnchor = anchorEl;

  const pop = document.createElement("div");
  pop.className = "git-branch-popover";
  pop.setAttribute("role", "menu");
  pop.innerHTML = `
    <input type="search" class="tool-form-input git-branch-popover-filter" placeholder="Filter branches…" autocomplete="off">
    <div class="git-branch-popover-list" role="none">Loading…</div>
    <form class="git-branch-popover-create">
      <input type="text" class="tool-form-input git-branch-popover-create-input" placeholder="Create new branch…" autocomplete="off">
    </form>
  `;
  document.body.appendChild(pop);
  openPopover = pop;
  positionPopover(pop, anchorEl);

  const filter = pop.querySelector<HTMLInputElement>(".git-branch-popover-filter")!;
  const list = pop.querySelector<HTMLDivElement>(".git-branch-popover-list")!;
  const createForm = pop.querySelector<HTMLFormElement>(".git-branch-popover-create")!;
  const createInput = createForm.querySelector<HTMLInputElement>(".git-branch-popover-create-input")!;

  // Load branches.
  branchController?.abort();
  branchController = new AbortController();
  void apiGet<BranchesResponse>(
    `/api/git/branches?repo=${encodeURIComponent(repo)}`,
    branchController.signal,
  ).then((data) => {
    if (data === null) {
      list.textContent = "Failed to load branches.";
      return;
    }
    const render = (q: string): void => {
      const filtered = data.branches.filter((b) => b.name.toLowerCase().includes(q.toLowerCase()));
      list.replaceChildren();
      if (filtered.length === 0) {
        list.textContent = q === "" ? "No branches." : "No matching branches.";
        return;
      }
      for (const b of filtered) {
        const row = document.createElement("button");
        row.type = "button";
        row.className = `git-branch-popover-row${b.current ? " current" : ""}`;
        row.setAttribute("role", "menuitem");
        row.textContent = b.name;
        if (b.current) row.setAttribute("data-tooltip", "Current branch");
        row.addEventListener("click", () => {
          void doCheckout(repo, b.name, false);
        });
        list.appendChild(row);
        popoverBindingCleanups.push(bindLoadingState("git.checkout_branch", row));
      }
    };
    render("");
    filter.addEventListener("input", () => render(filter.value));
    filter.focus();
  });

  // Create-new submission.
  createForm.addEventListener("submit", (e) => {
    e.preventDefault();
    const name = createInput.value.trim();
    if (name === "") return;
    void doCheckout(repo, name, true);
  });
  popoverBindingCleanups.push(bindLoadingState("git.checkout_branch", createInput));

  // Close on outside click + Escape.
  setTimeout(() => {
    document.addEventListener("click", outsideClickHandler);
    document.addEventListener("keydown", escapeHandler);
  }, 0);
}

function closePopover(): void {
  if (openPopover === null) return;
  for (const fn of popoverBindingCleanups) fn();
  popoverBindingCleanups = [];
  branchController?.abort();
  branchController = null;
  openPopover.remove();
  openPopover = null;
  const savedAnchor = activeAnchor;
  activeAnchor = null;
  document.removeEventListener("click", outsideClickHandler);
  document.removeEventListener("keydown", escapeHandler);
  // Guard against focus on a detached element (anchor may have been
  // removed from the DOM during the request, e.g. git tab re-rendered).
  if (savedAnchor?.isConnected === true) savedAnchor.focus();
}

function outsideClickHandler(e: MouseEvent): void {
  const target = e.target as HTMLElement | null;
  if (target === null) return;
  if (openPopover === null) return;
  if (openPopover.contains(target)) return;
  if (activeAnchor !== null && activeAnchor.contains(target)) return;
  closePopover();
}

function escapeHandler(e: KeyboardEvent): void {
  if (e.key === "Escape") closePopover();
}

function positionPopover(pop: HTMLDivElement, anchor: HTMLElement): void {
  const rect = anchor.getBoundingClientRect();
  pop.style.position = "fixed";
  pop.style.top = `${rect.bottom + 4}px`;
  pop.style.left = `${rect.left}px`;
  pop.style.minWidth = `${Math.max(rect.width, 220)}px`;
  // After paint, clamp into viewport if it overflows.
  requestAnimationFrame(() => {
    const popRect = pop.getBoundingClientRect();
    const overflowX = popRect.right - window.innerWidth + 8;
    if (overflowX > 0) {
      pop.style.left = `${rect.left - overflowX}px`;
    }
    // Vertical: flip above anchor if popover overflows bottom.
    if (popRect.bottom > window.innerHeight - 8) {
      pop.style.top = `${rect.top - popRect.height - 4}px`;
    }
  });
}

async function doCheckout(repo: string, branch: string, create: boolean): Promise<void> {
  // Capture anchor + optimistic state in the closure (not in action args)
  // so structuredClone-on-retry never sees the DOM element. The action
  // is purely data-driven; UI mutation is the caller's responsibility.
  const anchor = activeAnchor;
  const prevText = anchor?.textContent ?? "";
  if (anchor !== null) {
    anchor.textContent = branch;
  }
  await checkoutBranch.dispatch(
    { repo, branch, create },
    {
      onSuccess: () => {
        void import("./git-changes-tab.js")
          .then((m) => m.refreshChanges())
          .catch((e) => console.error("[git-branch] refresh import failed", e));
      },
      onError: () => {
        // Restore the previous label if the anchor is still in the DOM.
        if (anchor?.isConnected === true) {
          anchor.textContent = prevText;
        }
      },
      onSettled: () => closePopover(),
    },
  );
}

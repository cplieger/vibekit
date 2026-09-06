// ---------------------------------------------------------------------------
// Per-repo branch switcher: small popover anchored to a repo's
// section header that lists the local branches and offers a
// "Create new branch" action. Click → checkout, type a new name +
// Enter → checkout -b.
//
// Used by git-changes-tab.ts: each repo section's branch chip is
// wired to openBranchSwitcher(repo, anchorEl). The popover is a
// singleton (only one open at a time); reopening swaps the anchor.
//
// The floating-panel mechanics are @cplieger/ui-primitives': createPopover
// owns anchored placement (flip + clamp, min-width matched to the chip),
// LIVE anchor tracking on scroll/resize/visualViewport (the old hand-rolled
// version positioned once and detached from its anchor when the git pane
// scrolled), outside-click + Escape dismissal, aria-expanded on the anchor,
// and focus return to the chip. rovingFocus supplies the WAI-ARIA menu
// keyboard contract over the branch rows. This module keeps only what is
// vibekit's: the panel content, the branches load, and the checkout actions.
// ---------------------------------------------------------------------------

import { apiGet } from "./api-client.js";
import { checkoutBranch, suggestBranchName } from "./actions/git-branch.js";
import { registerCleanup, bindLoadingState } from "./actions/index.js";
import { withAsyncFeedback } from "./async-button.js";
import { reconcile } from "./reconcile.js";
import { el } from "@cplieger/reactive";
import { iconEl } from "./icon-el.js";
import { ICON_GIT_BRANCH, ICON_SEND_14, ICON_SPARKLE, findGlyph } from "./icons.js";
import { createPopover, type PopoverController } from "@cplieger/ui-primitives/popover";
import { rovingFocus, type RovingFocusController } from "@cplieger/ui-primitives/roving-focus";

interface BranchEntry {
  name: string;
  current: boolean;
}
interface BranchesResponse {
  branches: BranchEntry[];
  current: string;
}

let openPopover: HTMLDivElement | null = null;
let popoverCtl: PopoverController | null = null;
let popoverNav: RovingFocusController | null = null;
let activeAnchor: HTMLElement | null = null;
let branchController: AbortController | null = null;
let popoverBindingCleanups: (() => void)[] = [];
/** Per-branch-row checkout-action loading-state unbinds, keyed by
 *  branch name. Cleared via reconcile.onRemove during filter typing
 *  and en masse via closePopover(). */
const rowUnbinds = new Map<string, () => void>();
registerCleanup(() => branchController?.abort());

/** Wrap an input in a field carrying a leading glyph.
 *
 *  The icon is a sibling of the input rather than a background image, so it
 *  inherits `currentColor` and stays a real node the CSP allows; the input
 *  reserves the room for it (`.git-branch-field > .tool-form-input` in
 *  22-git-multirepo.css). Both fields in this popover use it, which is what
 *  makes the two rows read as the same kind of control at the same height. */
function branchField(glyph: string, input: HTMLInputElement): HTMLDivElement {
  return el("div", { className: "git-branch-field" }, iconEl(glyph), input) as HTMLDivElement;
}

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

  // No role on the panel itself. It used to carry role="menu", which is a
  // CRITICAL aria-required-children violation (axe, measured): a menu may only
  // contain menuitem/group children and this panel holds a filter input and a
  // create form. The menu is the branch LIST one level in, so the role moves
  // there — onto the element whose children really are menuitems — and the
  // panel stays a plain container the popover library manages aria-expanded for.
  const pop = el("div", { className: "git-branch-popover" }) as HTMLDivElement;
  const filter = el("input", {
    type: "search",
    className: "tool-form-input git-branch-popover-filter",
    placeholder: "Filter branches…",
    autocomplete: "off",
    "aria-label": "Filter branches",
  }) as HTMLInputElement;
  const list = el("div", {
    className: "git-branch-popover-list",
    role: "menu",
    "aria-label": "Branches",
  }) as HTMLDivElement;
  // Loading / empty / failure text is a STATUS LINE beside the menu, never a
  // child of it. A role="menu" holding a bare text node is the same
  // aria-required-children violation the panel used to carry, and it would fire
  // on every open, because "Loading…" is the first thing every open shows. An
  // EMPTY menu is fine (measured with axe), so the list simply has no children
  // until the rows arrive. role="status" also means the reader is TOLD the
  // outcome instead of having to notice an empty box.
  const status = el(
    "div",
    { className: "git-branch-popover-status", role: "status" },
    "Loading…",
  ) as HTMLDivElement;
  const createInput = el("input", {
    type: "text",
    className: "tool-form-input git-branch-popover-create-input",
    placeholder: "Create new branch…",
    autocomplete: "off",
    "aria-label": "New branch name",
  }) as HTMLInputElement;
  // AI branch-name suggestion: fills the create input from the repo's
  // work in progress; the user edits, then presses Enter or the send button
  // to accept. Same pattern as the commit box's "AI message" button.
  const suggestBtn = el(
    "button",
    {
      type: "button",
      className: "btn-small icon-only git-branch-popover-suggest",
      "data-tooltip": "Suggest a branch name for the work in progress",
      "aria-label": "Suggest a branch name",
    },
    iconEl(ICON_SPARKLE),
  ) as HTMLButtonElement;
  suggestBtn.addEventListener("click", () => {
    void withAsyncFeedback(suggestBtn, async () => {
      const o = await suggestBranchName.dispatch({ repo }).outcome;
      if (o.status !== "success") {
        // Reject so the feedback helper shows the ✗ glyph, carrying the
        // real failure instead of a synthetic message (the framework
        // already toasted it).
        throw new Error(o.status === "error" ? o.error.message : "suggestion cancelled");
      }
      const res = o.value;
      // Only fill while this popover is still the open one, and never
      // wipe a name the user already typed past the suggestion.
      if (res.output !== undefined && res.output !== "" && openPopover === pop) {
        createInput.value = res.output;
        createInput.focus();
        // Select the whole name and scroll back to its head. Both halves earn
        // their line: a plain focus() leaves the caret at the end, so a name
        // longer than the field shows its TAIL — the half a reader does not
        // need — and the selection says the value is a suggestion, so typing
        // replaces it rather than appending to it.
        createInput.setSelectionRange(0, res.output.length);
        createInput.scrollLeft = 0;
      }
    });
  });
  // The submit control. Enter in the input already submits the form, but a
  // suggestion lands the caret in that input and the next thing a reader looks
  // for is the button that accepts it — a row whose only visible action
  // GENERATES a name reads as though there is nothing left to press.
  const createBtn = el(
    "button",
    {
      type: "submit",
      className: "btn-small icon-only btn-primary git-branch-popover-go",
      "data-tooltip": "Create and check out this branch",
      "aria-label": "Create branch",
    },
    iconEl(ICON_SEND_14),
  ) as HTMLButtonElement;
  const createForm = el(
    "form",
    { className: "git-branch-popover-create" },
    branchField(ICON_GIT_BRANCH, createInput),
    suggestBtn,
    createBtn,
  ) as HTMLFormElement;
  pop.append(branchField(findGlyph("filter"), filter), list, status, createForm);
  const ctl = createPopover(anchorEl, pop, {
    placement: "bottom",
    align: "start",
    offset: 4,
    margin: 8,
    matchAnchorWidth: 340,
    haspopup: "menu",
    // Focus goes back to the chip on any close path; the library guards
    // against a detached anchor (git tab re-rendered mid-request).
    returnFocus: anchorEl,
    onClose: cleanupSwitcher,
  });
  openPopover = pop;
  popoverCtl = ctl;
  // Menu keyboard contract over the rows. Items are queried live, so rows
  // appearing after the async load (or a filter re-render) just work;
  // refresh() after each reconcile restores the single-Tab-stop invariant.
  popoverNav = rovingFocus(pop, ".git-branch-popover-row");
  ctl.show();

  // Load branches.
  branchController?.abort();
  branchController = new AbortController();
  void apiGet<BranchesResponse>(
    `/api/git/branches?repo=${encodeURIComponent(repo)}`,
    branchController.signal,
  ).then((data) => {
    if (data === null) {
      status.textContent = "Failed to load branches.";
      return;
    }
    const render = (q: string): void => {
      const filtered = data.branches.filter((b) => b.name.toLowerCase().includes(q.toLowerCase()));
      const onRemoveRow = (_: HTMLElement, key: string): void => {
        const u = rowUnbinds.get(key);
        if (u !== undefined) {
          u();
          rowUnbinds.delete(key);
        }
      };
      // ONE reconcile for both outcomes. The empty case used to need a second
      // call with a stub spec plus a placeholder-stripping sweep, because the
      // "No branches." text went INTO the list and `textContent =` wiped the
      // keyed rows out from under reconcile's bookkeeping. With the text in the
      // status line, mount/update simply never run on an empty list.
      reconcile(list, filtered, {
        key: (b: BranchEntry) => b.name,
        mount: (b: BranchEntry) => {
          const row = el(
            "button",
            {
              type: "button",
              className: `git-branch-popover-row${b.current ? " current" : ""}`,
              role: "menuitem",
            },
            b.name,
          ) as HTMLButtonElement;
          if (b.current) {
            row.setAttribute("data-tooltip", "Current branch");
          }
          row.addEventListener("click", () => {
            void doCheckout(repo, b.name, false);
          });
          rowUnbinds.set(b.name, bindLoadingState("git.checkout_branch", row));
          return row;
        },
        update: (row, b: BranchEntry) => {
          // current-flag may flip when the active branch changes mid-popover
          // (e.g. the user picks a different one and the popover stays open).
          row.className = `git-branch-popover-row${b.current ? " current" : ""}`;
          if (b.current) {
            row.setAttribute("data-tooltip", "Current branch");
          } else {
            row.removeAttribute("data-tooltip");
          }
        },
        onRemove: onRemoveRow,
      });
      status.textContent =
        filtered.length === 0 ? (q === "" ? "No branches." : "No matching branches.") : "";
    };
    render("");
    filter.addEventListener("input", () => {
      render(filter.value);
      if (openPopover === pop) {
        popoverNav?.refresh();
      }
    });
    if (openPopover === pop) {
      popoverNav?.refresh();
      // Content just grew from "Loading…" to the row list — re-clamp.
      popoverCtl?.reposition();
    }
    filter.focus();
  });

  // Create-new submission.
  createForm.addEventListener("submit", (e) => {
    e.preventDefault();
    const name = createInput.value.trim();
    if (name === "") {
      return;
    }
    void doCheckout(repo, name, true);
  });
  popoverBindingCleanups.push(bindLoadingState("git.checkout_branch", createInput));
  popoverBindingCleanups.push(bindLoadingState("git.checkout_branch", createBtn));
}

/** Close the open switcher (if any) through the popover controller; its
 *  onClose runs cleanupSwitcher below. Outside-click and Escape arrive here
 *  too, via the controller's own dismissal wiring. */
function closePopover(): void {
  popoverCtl?.hide();
}

/** onClose cleanup: unbind loading states, abort the in-flight branches
 *  load, and drop the panel. Focus return + aria-expanded are the popover
 *  controller's job. Removing the panel immediately also ends the (unskinned)
 *  leave fade — matching the old instant removal. */
function cleanupSwitcher(): void {
  if (openPopover === null) {
    return;
  }
  for (const fn of popoverBindingCleanups) {
    fn();
  }
  popoverBindingCleanups = [];
  for (const unbind of rowUnbinds.values()) {
    unbind();
  }
  rowUnbinds.clear();
  branchController?.abort();
  branchController = null;
  popoverNav?.dispose();
  popoverNav = null;
  openPopover.remove();
  openPopover = null;
  popoverCtl = null;
  activeAnchor = null;
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
          .catch((e: unknown) => {
            console.error("[git-branch] refresh import failed", e);
          });
      },
      onError: () => {
        // Restore the previous label if the anchor is still in the DOM.
        if (anchor?.isConnected === true) {
          anchor.textContent = prevText;
        }
      },
      onSettled: () => {
        closePopover();
      },
    },
  );
}

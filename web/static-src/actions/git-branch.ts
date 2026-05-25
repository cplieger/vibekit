import { apiAction } from "./index.js";

interface CheckoutArgs {
  repo: string;
  branch: string;
  create: boolean;
  /** The branch chip element to optimistically update. */
  anchorEl?: HTMLElement;
}

export const checkoutBranch = apiAction<CheckoutArgs, void>({
  name: "git.checkout_branch",
  request: ({ repo, branch, create }) => ({
    method: "POST",
    path: "/api/git/checkout",
    body: { repo, branch, create },
  }),
  retryable: "network",
  error: "Branch checkout failed",
  optimistic: (args) => {
    if (!args.anchorEl) return undefined;
    const prev = args.anchorEl.textContent ?? "";
    args.anchorEl.textContent = args.branch;
    return prev;
  },
  rollback: (args, op) => {
    // Guard: the anchor element may have been removed from the DOM
    // (e.g. the git panel was closed during the request). Mutating a
    // detached element is harmless but pointless; skip it.
    if (args.anchorEl?.isConnected && typeof op === "string") {
      args.anchorEl.textContent = op;
    }
  },
});

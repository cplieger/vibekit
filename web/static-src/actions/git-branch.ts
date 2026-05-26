// Actions for git branch operations: checkout, create.
// ---------------------------------------------------------------------------

import { apiAction, retryNetwork } from "./index.js";
import { RETRY_STANDARD } from "./types.js";
import { truncate } from "../strings.js";

interface CheckoutArgs {
  repo: string;
  branch: string;
  create: boolean;
}

// Note: optimistic UI updates (e.g. updating a branch label in the
// switcher popover) are intentionally NOT in the action def. They
// belong to the caller, which has the live DOM context. This keeps
// args fully structuredClone-safe so retry can clone them without
// fallback to a mutable reference.
export const checkoutBranch = apiAction<CheckoutArgs, void>({
  name: "git.checkout_branch",
  scope: (args) => "git:" + args.repo,
  request: ({ repo, branch, create }) => ({
    method: "POST",
    path: "/api/git/checkout",
    body: { repo, branch, create },
  }),
  retryable: retryNetwork,
  retry: RETRY_STANDARD,
  error: (args) => `Couldn't check out \u201c${truncate(args.branch)}\u201d`,
});

// Actions for git branch operations: checkout, create, name suggestion.
// ---------------------------------------------------------------------------

import { apiAction, defineAction, retryNetwork, RETRY_STANDARD } from "./index.js";
import { runGitPost, type GitCmdResult } from "./git-changes.js";

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
// eslint-disable-next-line @typescript-eslint/no-invalid-void-type -- void used as generic type argument for action with no args/result
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

/** Ask the utility agent for a branch name describing the repo's work in
 *  progress (uncommitted changes, else recent commits). The result lands
 *  in the branch-switcher's create input for the user to edit or accept. */
export const suggestBranchName = defineAction<{ repo: string }, GitCmdResult>({
  name: "git.suggest_branch_name",
  scope: (args) => "git:" + args.repo,
  dedupe: (args) => args.repo,
  run: (args, signal, ctx) => runGitPost("/api/git/branch-name", args, signal, ctx),
  error: "Couldn't suggest a branch name",
  retryable: retryNetwork,
  retry: RETRY_STANDARD,
});

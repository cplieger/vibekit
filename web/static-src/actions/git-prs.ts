// Actions for the Git PRs tab: merge, close, refresh.
// Create PR and generate() are INLINE (error surfaces in the dialog)
// and are intentionally excluded.
// ---------------------------------------------------------------------------

import { apiAction, defineAction } from "./index.js";
import { removePRFromGroups, reinsertPRInGroups } from "../git-prs-tab.js";
import type { PRRemoveResult } from "../git-prs-tab.js";

// --- Types ---

interface PRArgs {
  forge_id: string;
  owner: string;
  name: string;
  pr_number: number;
}

function prPath(args: PRArgs, action: string): string {
  return `/api/forges/${encodeURIComponent(args.forge_id)}/repos/${encodeURIComponent(args.owner)}/${encodeURIComponent(args.name)}/prs/${args.pr_number}/${action}`;
}

function optimisticRemovePR(args: PRArgs): PRRemoveResult | undefined {
  return removePRFromGroups(args.forge_id, args.owner, args.name, args.pr_number);
}

function rollbackRemovePR(_args: PRArgs, op: unknown): void {
  if (op !== undefined) reinsertPRInGroups(op as PRRemoveResult);
}

// --- Actions ---

/** Merge a pull request. */
export const mergePRAction = apiAction<PRArgs, unknown>({
  name: "git.merge_pr",
  request: (args) => ({ method: "POST", path: prPath(args, "merge"), body: {} }),
  optimistic: optimisticRemovePR,
  rollback: rollbackRemovePR,
  error: "Merge failed",
  retryable: "network",
});

/** Close a pull request without merging. */
export const closePRAction = apiAction<PRArgs, unknown>({
  name: "git.close_pr",
  request: (args) => ({ method: "POST", path: prPath(args, "close"), body: {} }),
  optimistic: optimisticRemovePR,
  rollback: rollbackRemovePR,
  error: "Couldn't close PR",
  retryable: "network",
});

/** Refresh all PRs across connected forges. */
export const refreshPRsAction = defineAction<void, void>({
  name: "git.refresh_prs",
  run: async () => {
    const { refreshPRs } = await import("../git-prs-tab.js");
    await refreshPRs();
  },
  error: "Couldn't refresh PRs",
  retryable: "network",
});

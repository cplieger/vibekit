// Actions for the Git PRs tab: merge, close, refresh.
// Create PR and generate() are INLINE (error surfaces in the dialog)
// and are intentionally excluded.
// ---------------------------------------------------------------------------

import { apiAction, defineAction } from "./index.js";
import { removePRFromGroups, reinsertPRInGroups } from "../git-prs-state.js";
import type { PRRemoveResult } from "../git-prs-state.js";

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
  scope: (args) => "git:" + args.forge_id + ":" + args.owner + "/" + args.name,
  request: (args) => ({ method: "POST", path: prPath(args, "merge"), body: {} }),
  optimistic: optimisticRemovePR,
  rollback: rollbackRemovePR,
  error: (args) => `Merge failed for PR #${String(args.pr_number)}`,
  // Not retryable: a timed-out merge may have succeeded server-side.
  retryable: false,
});

/** Close a pull request without merging. */
export const closePRAction = apiAction<PRArgs, unknown>({
  name: "git.close_pr",
  scope: (args) => "git:" + args.forge_id + ":" + args.owner + "/" + args.name,
  request: (args) => ({ method: "POST", path: prPath(args, "close"), body: {} }),
  optimistic: optimisticRemovePR,
  rollback: rollbackRemovePR,
  error: (args) => `Couldn't close PR #${String(args.pr_number)}`,
  idempotencyKey: true,
  retryable: "network",
});

/** Refresh all PRs across connected forges. */
export const refreshPRsAction = defineAction<void, void>({
  name: "git.refresh_prs",
  dedupe: true,
  run: async (_args, signal) => {
    const { refreshPRs } = await import("../git-prs-tab.js");
    await refreshPRs(signal);
  },
  error: "Couldn't refresh PRs",
  retryable: "network",
  retry: { count: 2, delay: 300 },
});

// Actions for the Git PRs tab: merge, close, refresh.
// Create PR and generate() are INLINE (error surfaces in the dialog)
// and are intentionally excluded.
// ---------------------------------------------------------------------------

import { apiAction, defineAction, ActionError, retryNetwork } from "./index.js";
import { RETRY_STANDARD } from "./types.js";
import { removePRFromGroups, reinsertPRInGroups } from "../git-prs-state.js";
import type { PRRemoveResult } from "../git-prs-state.js";

// --- Types ---

interface PRArgs {
  forge_id: string;
  owner: string;
  name: string;
  pr_number: number;
}

/** Build the API path for a PR action (merge/close). */
function prPath(args: PRArgs, action: string): string {
  return `/api/forges/${encodeURIComponent(args.forge_id)}/repos/${encodeURIComponent(args.owner)}/${encodeURIComponent(args.name)}/prs/${args.pr_number}/${action}`;
}

/** Optimistically remove a PR from the UI groups. Returns the undo
 *  state needed by rollbackRemovePR, or undefined if the PR wasn't found. */
function optimisticRemovePR(args: PRArgs): PRRemoveResult | undefined {
  return removePRFromGroups(args.forge_id, args.owner, args.name, args.pr_number);
}

/** Rollback: re-insert the PR into its original group position. */
function rollbackRemovePR(_args: PRArgs, op: PRRemoveResult | undefined): void {
  if (op !== undefined) {
    reinsertPRInGroups(op);
  }
}

// --- Actions ---

/** Merge a pull request. */
export const mergePR = apiAction<PRArgs, unknown, PRRemoveResult>({
  name: "git.merge_pr",
  scope: (args) => "git:" + args.forge_id + ":" + args.owner + "/" + args.name,
  dedupe: true,
  request: (args) => ({ method: "POST", path: prPath(args, "merge"), body: {} }),
  optimistic: optimisticRemovePR,
  rollback: rollbackRemovePR,
  error: (args) => `Merge failed for PR #${String(args.pr_number)}`,
  // Not retryable: a timed-out merge may have succeeded server-side.
});

/** Close a pull request without merging. */
export const closePR = apiAction<PRArgs, unknown, PRRemoveResult>({
  name: "git.close_pr",
  scope: (args) => "git:" + args.forge_id + ":" + args.owner + "/" + args.name,
  dedupe: true,
  request: (args) => ({ method: "POST", path: prPath(args, "close"), body: {} }),
  optimistic: optimisticRemovePR,
  rollback: rollbackRemovePR,
  error: (args) => `Couldn't close PR #${String(args.pr_number)}`,
  idempotencyKey: true,
  retryable: retryNetwork,
  retry: RETRY_STANDARD,
});

/** Refresh all PRs across connected forges. */
export const refreshPRs = defineAction<void, void>({
  name: "git.refresh_prs",
  dedupe: true,
  run: async (_args, signal) => {
    const { refreshPRs } = await import("../git-prs-tab.js");
    try {
      await refreshPRs(signal);
    } catch (e) {
      if (signal.aborted) {
        throw new ActionError("cancelled", { code: "cancelled", cause: e });
      }
      throw new ActionError(e instanceof Error ? e.message : "network error", {
        code: "network",
        cause: e,
      });
    }
  },
  error: "Couldn't refresh PRs",
  retryable: retryNetwork,
  retry: RETRY_STANDARD,
});

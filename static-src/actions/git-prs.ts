// Actions for the Git PRs tab: create, merge, close, refresh.
// generate() (AI PR-description) stays INLINE (error surfaces in the
// dialog) and is intentionally excluded.
// ---------------------------------------------------------------------------

import { apiAction, defineAction, ActionError, retryNetwork, RETRY_STANDARD } from "./index.js";

import { removePRFromGroups, reinsertPRInGroups } from "../git-prs-state.js";
import type { PRRemoveResult } from "../git-prs-state.js";

// --- Types ---

interface PRArgs {
  forge_id: string;
  owner: string;
  name: string;
  pr_number: number;
}

/** Args for a PR action pinned to the head commit the row was rendered
 *  from, so the forge refuses to act when the branch moved since. Empty
 *  means the forge reported no head SHA, and the action goes unpinned.
 *
 *  Shared by merge, auto-merge and RE-RUN. The re-run needs it for the same
 *  reason the merge does and not a weaker one: the row's check chip is the
 *  folded state of that one commit, so a re-run resolved from the mutable
 *  branch instead can start CI for a different commit, with whatever
 *  deployment a workflow does behind it. */
interface PinnedPRArgs extends PRArgs {
  head_sha: string;
}

/** Build the API path for a PR action (merge/close/reopen/rerun). */
function prPath(args: PRArgs, action: string): string {
  return `/api/forges/${encodeURIComponent(args.forge_id)}/repos/${encodeURIComponent(args.owner)}/${encodeURIComponent(args.name)}/prs/${args.pr_number}/${action}`;
}

/** Build the query string for a pinned action. The head pin and the
 *  auto-merge arm ride query params, which is how ?method= already travels
 *  on this route. */
function pinnedQuery(args: PinnedPRArgs, auto: boolean): string {
  const q = new URLSearchParams();
  if (args.head_sha !== "") {
    q.set("head_sha", args.head_sha);
  }
  if (auto) {
    q.set("auto", "1");
  }
  const s = q.toString();
  return s === "" ? "" : "?" + s;
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

/** Merge a pull request, pinned to the head commit the row read. */
export const mergePR = apiAction<PinnedPRArgs, unknown, PRRemoveResult>({
  name: "git.merge_pr",
  scope: (args) => "git:" + args.forge_id + ":" + args.owner + "/" + args.name,
  dedupe: true,
  request: (args) => ({
    method: "POST",
    path: prPath(args, "merge") + pinnedQuery(args, false),
    body: {},
  }),
  optimistic: optimisticRemovePR,
  rollback: rollbackRemovePR,
  error: (args) => `Merge failed for PR #${String(args.pr_number)}`,
  // Not retryable: a timed-out merge may have succeeded server-side. The
  // head pin makes a retry SAFER (a second attempt against a moved head
  // fails closed rather than merging the wrong commit) but not safe, so
  // the rule is unchanged.
});

/** Arm the forge's own auto-merge: it merges once its requirements are
 *  met. Deliberately NOT an optimistic remove — arming does not merge, so
 *  the row must stay and re-render as armed once the server confirms. */
export const armAutoMerge = apiAction<PinnedPRArgs>({
  name: "git.arm_auto_merge",
  scope: (args) => "git:" + args.forge_id + ":" + args.owner + "/" + args.name,
  dedupe: true,
  request: (args) => ({
    method: "POST",
    path: prPath(args, "merge") + pinnedQuery(args, true),
    body: {},
  }),
  error: (args) => `Couldn't arm auto-merge for PR #${String(args.pr_number)}`,
  idempotencyKey: true,
  retryable: retryNetwork,
  retry: RETRY_STANDARD,
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

/** Reopen a closed pull request. No optimistic step: the PRs tab lists
 *  open PRs, so a reopened one is not in a group to mutate — the caller
 *  refreshes instead. */
export const reopenPR = apiAction<PRArgs>({
  name: "git.reopen_pr",
  scope: (args) => "git:" + args.forge_id + ":" + args.owner + "/" + args.name,
  dedupe: true,
  request: (args) => ({ method: "POST", path: prPath(args, "reopen"), body: {} }),
  error: (args) => `Couldn't reopen PR #${String(args.pr_number)}`,
  idempotencyKey: true,
  retryable: retryNetwork,
  retry: RETRY_STANDARD,
});

/** Re-run the failed CI of a pull request's head, pinned to the commit whose
 *  red chip the row is showing. No optimistic step: the chip flips to running
 *  only once the forge says so.
 *
 *  Sending the pin is what makes the button act on what the reader sees. The
 *  server resolves the failed run from the PR's own head-commit check set and
 *  refuses when the branch has moved since; unpinned (the forge reported no
 *  head SHA) it re-runs whatever the live head's failure is. */
export const rerunChecks = apiAction<PinnedPRArgs>({
  name: "git.rerun_checks",
  scope: (args) => "git:" + args.forge_id + ":" + args.owner + "/" + args.name,
  dedupe: true,
  request: (args) => ({
    method: "POST",
    path: prPath(args, "rerun") + pinnedQuery(args, false),
    body: {},
  }),
  error: (args) => `Couldn't re-run checks for PR #${String(args.pr_number)}`,
  idempotencyKey: true,
  retryable: retryNetwork,
  retry: RETRY_STANDARD,
});

/** Args for opening a new pull request. */
interface CreatePRArgs {
  forge_id: string;
  owner: string;
  name: string;
  source_branch: string;
  target_branch: string;
  title: string;
  body: string;
  draft: boolean;
}

/** Open a new pull request. The create dialog renders its own inline
 *  status/error line, so `error: false` (no toast) and the caller
 *  reads the resolved result. NOT retryable: a timed-out create may
 *  have opened the PR server-side, so a retry could open a duplicate
 *  (same rationale as mergePR). */
export const createPR = apiAction<CreatePRArgs, { number?: number; error?: string }>({
  name: "git.create_pr",
  scope: (args) =>
    "git:" +
    args.forge_id +
    ":" +
    args.owner +
    "/" +
    args.name +
    ":" +
    args.source_branch +
    ">" +
    args.target_branch,
  dedupe: true,
  request: (args) => ({
    method: "POST",
    path: `/api/forges/${encodeURIComponent(args.forge_id)}/repos/${encodeURIComponent(args.owner)}/${encodeURIComponent(args.name)}/prs`,
    body: {
      source_branch: args.source_branch,
      target_branch: args.target_branch,
      title: args.title,
      body: args.body,
      draft: args.draft,
    },
  }),
  error: false,
});

/** Refresh all PRs across connected forges.
 *
 *  `force` bypasses the server's listing cache. It is a real argument rather
 *  than a second action because dedupe is keyed per action: two spellings would
 *  let a tab-activation refresh and a button press run the fan-out twice. */
// eslint-disable-next-line @typescript-eslint/no-invalid-void-type -- void used as generic type argument for an action with no result
export const refreshPRs = defineAction<{ force: boolean }, void>({
  name: "git.refresh_prs",
  dedupe: true,
  run: async (args, signal) => {
    const { refreshPRs } = await import("../git-prs-tab.js");
    try {
      await refreshPRs(signal, args.force);
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

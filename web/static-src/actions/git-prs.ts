// Actions for the Git PRs tab: merge, close.
// Create PR and generate() are INLINE (error surfaces in the dialog)
// and are intentionally excluded.
// ---------------------------------------------------------------------------

import { apiAction } from "./index.js";

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

// --- Actions ---

/** Merge a pull request. */
export const mergePRAction = apiAction<PRArgs, unknown>({
  name: "git.merge_pr",
  request: (args) => ({ method: "POST", path: prPath(args, "merge"), body: {} }),
  error: "Merge failed",
});

/** Close a pull request without merging. */
export const closePRAction = apiAction<PRArgs, unknown>({
  name: "git.close_pr",
  request: (args) => ({ method: "POST", path: prPath(args, "close"), body: {} }),
  error: "Couldn't close PR",
});

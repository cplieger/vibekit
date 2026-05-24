// Actions for the Git PRs tab: refresh, merge, close.
// Create PR and generate() are INLINE (error surfaces in the dialog)
// and are intentionally excluded.
// ---------------------------------------------------------------------------

import { defineAction, ActionError } from "./index.js";
import { apiPost } from "../api-client.js";

// --- Types ---

interface MergePRArgs {
  forge_id: string;
  owner: string;
  name: string;
  pr_number: number;
}

interface ClosePRArgs {
  forge_id: string;
  owner: string;
  name: string;
  pr_number: number;
}

// --- Actions ---

/** Merge a pull request. */
export const mergePRAction = defineAction<MergePRArgs, void>({
  name: "git.merge_pr",
  run: async ({ forge_id, owner, name, pr_number }, signal) => {
    const res = await apiPost<{ status?: string; error?: string }>(
      `/api/forges/${encodeURIComponent(forge_id)}/repos/${encodeURIComponent(owner)}/${encodeURIComponent(name)}/prs/${pr_number}/merge`,
      {},
      signal,
    );
    if (res === null) throw new ActionError("network error");
    if (res.error !== undefined && res.error !== "") {
      throw new ActionError(res.error);
    }
  },
  error: "Merge failed",
});

/** Close a pull request without merging. */
export const closePRAction = defineAction<ClosePRArgs, void>({
  name: "git.close_pr",
  run: async ({ forge_id, owner, name, pr_number }, signal) => {
    const res = await apiPost<{ status?: string; error?: string }>(
      `/api/forges/${encodeURIComponent(forge_id)}/repos/${encodeURIComponent(owner)}/${encodeURIComponent(name)}/prs/${pr_number}/close`,
      {},
      signal,
    );
    if (res === null) throw new ActionError("network error");
    if (res.error !== undefined && res.error !== "") {
      throw new ActionError(res.error);
    }
  },
  error: "Couldn't close PR",
});

/**
 * Refresh all PRs. Wraps the caller-provided refresh function so that
 * total failure (forges fetch returns null) surfaces a toast. Per-repo
 * errors are shown inline and don't toast.
 *
 * The `run` callback receives a `doRefresh` function injected by the
 * dispatch site — this avoids circular imports with git-prs-tab.ts.
 */
export const refreshPRsAction = defineAction<() => Promise<void>, void>({
  name: "git.refresh_prs",
  run: async (doRefresh) => {
    await doRefresh();
  },
  error: "Couldn't refresh PRs",
});

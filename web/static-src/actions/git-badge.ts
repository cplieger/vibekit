// Action for refreshing the git sidebar badge. Replaces raw apiGet
// calls with proper dedupe + cancellation so rapid SSE bursts
// (turn_ended, forges_changed) coalesce into a single fetch pair.
// ---------------------------------------------------------------------------

import { apiAction } from "./api.js";
import { defineAction, ActionError } from "./index.js";

// --- Response types (mirrored from git-badge.ts) ---

interface RepoStatus {
  repo: string;
  is_repo: boolean;
  branch: string;
  ahead: number;
  behind: number;
  has_dirty: boolean;
}
interface StatusAllResponse { repos: RepoStatus[] }

interface ConfiguredForge {
  id: string;
  connected: boolean;
  last_error?: string;
}
interface ForgesListResponse { forges: ConfiguredForge[] }

export interface GitBadgeData {
  status: StatusAllResponse | null;
  forges: ForgesListResponse | null;
}

// --- Internal fetch actions (no toast, no retry — advisory data) ---

const fetchStatusAll = apiAction<void, StatusAllResponse>({
  name: "git-badge.status",
  request: () => ({ method: "GET", path: "/api/git/status-all" }),
  error: false,
  success: false,
});

const fetchForges = apiAction<void, ForgesListResponse>({
  name: "git-badge.forges",
  request: () => ({ method: "GET", path: "/api/forges" }),
  error: false,
  success: false,
});

/** Refresh git badge data. Deduped so concurrent SSE triggers collapse
 *  into one in-flight pair of fetches. Returns both responses (either
 *  may be null on failure). */
export const refreshGitBadgeAction = defineAction<void, GitBadgeData>({
  name: "git-badge.refresh",
  dedupe: true,
  run: async (_args, signal) => {
    const [status, forges] = await Promise.all([
      fetchStatusAll.dispatch(undefined).catch(() => null),
      fetchForges.dispatch(undefined).catch(() => null),
    ]);
    if (signal.aborted) throw new ActionError("cancelled", { code: "cancelled" });
    return { status, forges };
  },
  error: false,
  success: false,
});

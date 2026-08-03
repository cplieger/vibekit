// Action for the git badge's FORGES half. The git-status half moved to
// git-status-store.ts, which owns one poll shared by the badge, the docs page
// and the file browser — the badge used to fetch /api/git/status-all itself,
// which is why per-path status had no reader outside the git view.
//
// Forges stay here: a different endpoint with a different failure mode (an auth
// error trumps every status), refreshed on its own cadence and on
// forges_changed.
// ---------------------------------------------------------------------------

import { apiAction } from "./index.js";
import type { ConfiguredForge } from "../wire/types.gen.js";

/** Single source of truth for the /api/forges endpoint path. */
const API_PATH_FORGES = "/api/forges" as const;

// --- Response types ---

interface ForgesListResponse {
  forges: ConfiguredForge[];
}

/** Fetch the configured forges. Deduped so rapid SSE triggers coalesce.
 *  Advisory data: no toast, no retry. */
// eslint-disable-next-line @typescript-eslint/no-invalid-void-type -- void as a generic argument for an action taking no args
export const refreshForges = apiAction<void, ForgesListResponse>({
  name: "git-badge.forges",
  request: () => ({ method: "GET", path: API_PATH_FORGES }),
  dedupe: true,
  error: false,
  success: false,
});

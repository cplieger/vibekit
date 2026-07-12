// Action for fetching the spec-workflow board (GET /api/specs).
//
// Read-only/advisory: no success or error toast (the board renders its own
// empty/error states). Deduped so the poll's immediate tick and an
// SSE-driven refetch coalesce into a single in-flight request.
// ---------------------------------------------------------------------------

import { apiAction } from "./index.js";
import type { SpecsResponse } from "../types.js";

// eslint-disable-next-line @typescript-eslint/no-invalid-void-type -- void used as generic type argument for an action with no args
export const fetchSpecs = apiAction<void, SpecsResponse>({
  name: "specs.list",
  request: () => ({ method: "GET", path: "/api/specs" }),
  dedupe: true,
  error: false,
  success: false,
});

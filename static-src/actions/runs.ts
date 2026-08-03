// ---------------------------------------------------------------------------
// Workflow-run actions: the recipe list, launch, and cancel.
//
// Launch and cancel are the app's ONLY run verbs (user decision): there is no
// pause, resume, retry or delete anywhere. Cancel is a stop, the same gesture
// as closing the tab — which is exactly what dispatches it for a launcher-owned
// run tab.
// ---------------------------------------------------------------------------

import { apiAction, retryNetwork, RETRY_STANDARD } from "./index.js";
import type {
  RecipesResponse,
  RunLaunchRequest,
  RunLaunchedResponse,
  WorkflowRunRow,
  ResumableSessionRow,
} from "../types.js";

/** The launchable recipe list, bundled + workspace. */
export const loadRecipes = apiAction<
  // eslint-disable-next-line @typescript-eslint/no-invalid-void-type -- void used as generic type argument for action with no args
  void,
  RecipesResponse
>({
  name: "runs.recipes",
  dedupe: true,
  retryable: retryNetwork,
  retry: RETRY_STANDARD,
  request: () => ({ method: "GET", path: "/api/recipes" }),
  error: "Couldn't load workflows",
});

/** The current run inventory. Same endpoint as the history page, its own
 *  action: history cancels its dispatch on view teardown, and the Workflows
 *  tab's Run ⇄ Cancel state must not lose its refresh to another view's
 *  lifecycle. */
export const loadRuns = apiAction<
  // eslint-disable-next-line @typescript-eslint/no-invalid-void-type -- void used as generic type argument for action with no args
  void,
  { sessions: ResumableSessionRow[]; runs: WorkflowRunRow[] }
>({
  name: "runs.list",
  dedupe: true,
  retryable: retryNetwork,
  retry: RETRY_STANDARD,
  request: () => ({ method: "GET", path: "/api/sessions" }),
  error: false,
});

/** Launch one PARENTLESS run. The server answers 409 when the recipe already
 *  has a live run (the single-run rule); surface that message verbatim — it
 *  names the actual conflict, and the row flips to Cancel on the next refresh
 *  anyway. */
export const launchRun = apiAction<RunLaunchRequest, RunLaunchedResponse>({
  name: "runs.launch",
  request: (body) => ({ method: "POST", path: "/api/runs", body }),
  error: "Couldn't launch",
});

/** Ask a run to stop. The reply confirms the ASK — cancel is a node-boundary
 *  verb, so the terminal state (and the run_finished event) follows at the
 *  in-flight node's end. */
export const cancelRun = apiAction<string, { ok: boolean }>({
  name: "runs.cancel",
  request: (workflowID) => ({
    method: "POST",
    path: `/api/runs/${encodeURIComponent(workflowID)}/cancel`,
  }),
  error: "Couldn't cancel the run",
});

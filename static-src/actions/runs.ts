// ---------------------------------------------------------------------------
// Workflow-run actions: the recipe list, launch, and the four run controls.
//
// Cancel, pause, resume and retry are all KAS's own verbs; vibekit adds no
// control of its own and no policy. Cancel doubles as the tab-close gesture,
// which is what dispatches it for a launcher-owned run tab.
//
// This replaces an earlier note that said launch and cancel were the app's only
// run verbs by user decision. That decision assumed offering a control meant
// building one; the 2.16.1 sweep found every verb already live in the pinned
// binary, so the real choice was whether to route to them — and not routing left
// a paused run with no way forward except deleting the chat.
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

/** One run-control verb. All four share a shape: POST to a sub-path, no body,
 *  `{ok:true}` back. Built rather than written four times so a fifth verb is one
 *  line and cannot drift from the others. */
function runControl(verb: string, errorText: string) {
  return apiAction<string, { ok: boolean }>({
    name: `runs.${verb}`,
    request: (workflowID) => ({
      method: "POST",
      path: `/api/runs/${encodeURIComponent(workflowID)}/${verb}`,
    }),
    error: errorText,
  });
}

/** Stop a run at its next node boundary, keeping it resumable.
 *
 *  Like cancel, the reply confirms the ASK: KAS sets a pause flag and the
 *  in-flight node runs to completion, so the run is still `running` when this
 *  resolves. The paused state arrives as a run_progress invalidation. */
export const pauseRun = runControl("pause", "Couldn't pause the run");

/** Re-drive a paused run. Works even when the launching process is gone — KAS
 *  reloads the run from disk — which is why the button is offered on any paused
 *  run rather than only on one this browser started. */
export const resumeRun = runControl("resume", "Couldn't resume the run");

/** Re-drive a finished run from its failed node.
 *
 *  Only legal on a terminal run (completed, failed or aborted); the server
 *  answers 409 naming the current status otherwise, which is the honest response
 *  to clicking Retry on a run that resumed a moment ago. */
export const retryRun = runControl("retry", "Couldn't retry the run");

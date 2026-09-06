// ---------------------------------------------------------------------------
// Workflow-run actions: the recipe list, launch, and the four run controls.
//
// Cancel, pause and resume are all KAS's own verbs; vibekit adds no policy
// of its own. Cancel doubles as the tab-close gesture for a launcher-owned
// run tab.
// ---------------------------------------------------------------------------

import { apiAction, retryNetwork, RETRY_STANDARD } from "./index.js";
import type {
  RecipesResponse,
  RunAnswerRequest,
  RunLaunchRequest,
  RunLaunchedResponse,
  SessionListResponse,
} from "../types.js";
import { decodeSessionListResponse } from "../wire/decoders.gen.js";

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
  SessionListResponse
>({
  name: "runs.list",
  dedupe: true,
  retryable: retryNetwork,
  retry: RETRY_STANDARD,
  request: () => ({ method: "GET", path: "/api/sessions" }),
  // One endpoint, one decoded shape: this and chat.load_sessions read the same
  // reply, so a second structural claim here is the drift the generated type
  // removes.
  decode: decodeSessionListResponse,
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

/** Reset a failed run's failed and aborted steps (plus their ancestors) and
 *  re-drive it, keeping every completed step.
 *
 *  Unlike the other controls this one needs no live bridge: the server RE-HOSTS
 *  the run, because retry is legal exactly when the run's own bridge has already
 *  been closed. Offered only on a parentless run — an agent-parented run's
 *  recovery is the agent's. */
export const retryRun = runControl("retry", "Couldn't retry the run");

/** Re-drive a paused run. Works even when the launching process is gone — KAS
 *  reloads the run from disk — which is why the button is offered on any paused
 *  run rather than only on one this browser started. */
export const resumeRun = runControl("resume", "Couldn't resume the run");

/** Answer the question a parked workflow step asked.
 *
 *  The server claims the ask BEFORE it sends, so exactly one surface can answer it
 *  — two browser tabs and a run tab are all offered the same card. A 409 has TWO
 *  causes and the server's own sentence is what separates them, which is why this
 *  action shows that sentence alone. Somebody else got there first, or the step
 *  moved on: settled, the card retired by the `run_input_settled` frame, nothing to
 *  redo. Or the run is momentarily BETWEEN steps (a resume on its way back to the
 *  same park): the QUESTION is held rather than discarded server-side and comes back
 *  on a fresh `run_input_needed`, the WORDS are held client-side by the dock (which
 *  splices the card at the click, so the re-offered box would otherwise be empty),
 *  and the sentence says to try again.
 *
 *  NO argument-composite idempotency key, deliberately: inside that cache's window
 *  a repeated dispatch replays a cached success and never runs, and a re-sent
 *  answer must actually reach the step. The server's take-once claim is the guard,
 *  which is the same division `files.rename` learned the hard way. */
export const answerRunInput = apiAction<{ workflowID: string } & RunAnswerRequest, { ok: boolean }>(
  {
    name: "runs.answer_input",
    request: ({ workflowID, ask_id, text }) => ({
      method: "POST",
      path: `/api/runs/${encodeURIComponent(workflowID)}/answer`,
      body: { ask_id, text },
    }),
    // The SERVER's sentence ALONE on the refusal that actually happens, because a
    // prefix in front of it contradicts it. A static `error` string does not replace
    // the server's message, it PREFIXES it (`emitErrorToast`: `${spec}: ${err.message}`
    // — measured, and the opposite of what it looks like), so the 409 read "Couldn't
    // send your answer to the step: that question has already been answered, or the
    // step it belonged to has moved on" — asserting a failure and then explaining that
    // nothing needed sending. The same prefix also leaked the library's own
    // empty-body placeholder as "…to the step: HTTP 500".
    error: (_args, err) => serverSentence(err) ?? "Couldn't send your answer to the step",
  },
);

/** The server's own error sentence, or null when there is no sentence to show.
 *
 *  `@cplieger/fetch` lifts a `{"error": "..."}` body onto `message`, and falls back
 *  to the literal `HTTP <status>` when the body was empty or unparseable — so the
 *  field is present either way and its presence proves nothing. Both of the empty
 *  cases are compared exactly rather than sniffed, because that fallback is a
 *  string the library documents rather than a shape to guess at, and a transport
 *  failure carries `status === 0` with a browser sentence about a fetch. */
function serverSentence(err: {
  readonly message: string;
  readonly status?: number;
}): string | null {
  const status = err.status ?? 0;
  if (status < 400 || err.message === "" || err.message === `HTTP ${String(status)}`) {
    return null;
  }
  return err.message;
}

/** Let a parked step carry on with NO answer.
 *
 *  `set_step_status running` rather than Resume, and the difference is the whole
 *  reason this verb exists: KAS's resume clears the run's pause reason and leaves
 *  the step node's `need_input` signal, so the next step execution re-parks under a
 *  different sentence. Setting the status clears the signal, and the step then runs
 *  with its own default continuation instead of the user's words. */
export const continueRunStep = apiAction<{ workflowID: string; nodeID: string }, { ok: boolean }>({
  name: "runs.continue_step",
  request: ({ workflowID, nodeID }) => ({
    method: "POST",
    path: `/api/runs/${encodeURIComponent(workflowID)}/step`,
    body: { node_id: nodeID, status: "running" },
  }),
  error: "Couldn't let the step continue",
});

/** Delete a run and its on-disk state.
 *
 *  Not a `runControl`: it is a DELETE on the run's own path rather than a POST to
 *  a sub-path, and it is the one run verb that cannot be undone. KAS cancels a
 *  non-terminal run itself before removing it, so this is legal from any status —
 *  which it has to be, because this is the only way a run leaves the History page.
 *
 *  The caller confirms first (`modals.ts`); an action cannot, and a destructive
 *  verb that fires on the first click is the shape this page must not have. */
export const deleteRun = apiAction<string, { ok: boolean }>({
  name: "runs.delete",
  request: (workflowID) => ({
    method: "DELETE",
    path: `/api/runs/${encodeURIComponent(workflowID)}`,
  }),
  error: "Couldn't delete the run",
});

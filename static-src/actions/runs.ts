// ---------------------------------------------------------------------------
// Workflow-run actions: the recipe list, launch, and the four run controls.
//
// Cancel, pause and resume are all KAS's own verbs; vibekit adds no policy
// of its own. Cancel doubles as the tab-close gesture for a launcher-owned
// run tab.
// ---------------------------------------------------------------------------

import { apiAction, retryNetwork, RETRY_STANDARD } from "./index.js";
import { retryOutcomeNotice } from "../run-controls.js";
import { invalidateRun, invalidateRunControls } from "../run-store.js";
import { error as toastError, success as toastSuccess } from "../toast.js";
import { decodeRunRetriedResponse } from "../wire/decoders.gen.js";
import type { RunRetriedResponse } from "../wire/types.gen.js";
import type {
  RecipesResponse,
  RunAnswerRequest,
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

/** Reset a failed run's failed and aborted steps (plus their ancestors) and
 *  re-drive it, keeping every completed step.
 *
 *  NOT a `runControl`, and the difference is the whole point: its reply carries
 *  the OUTCOME — which nodes were reset — where the other three genuinely have
 *  nothing to report. It used to be built by that factory and answered
 *  `{ok:true}`, so a retry that reset five nodes and one that reset none were the
 *  same result here, with no notification and no refetch on either. "Nothing
 *  happened" was what a no-op retry was designed to look like.
 *
 *  Three things it now does that the factory cannot:
 *
 *   - DECODES the reply at the boundary, so a malformed outcome fails here rather
 *     than reaching the notification as `undefined`.
 *   - REPORTS what happened, through the channel the outcome deserves: a reset of
 *     zero nodes is a no-op the reader must be told about, not a success.
 *   - REFETCHES the run and its affordance. Repainting was left to a
 *     `run_progress` frame, which a no-op retry never produces — so on exactly
 *     the outcome the reader most needs to see, the page never moved.
 *
 *  Its refusals reach the reader as the SERVER's sentence, `answerRunInput`'s
 *  rule: the server classifies a KAS refusal as a 409 naming the reason, and a
 *  static prefix in front of that contradicts it. */
export const retryRun = apiAction<string, RunRetriedResponse>({
  name: "runs.retry",
  request: (workflowID) => ({
    method: "POST",
    path: `/api/runs/${encodeURIComponent(workflowID)}/retry`,
  }),
  decode: (data) => decodeRunRetriedResponse(data),
  error: (_args, err) => serverSentence(err) ?? "Couldn't retry the run",
  onSuccess: (res, workflowID) => {
    const notice = retryOutcomeNotice(res.retried_node_ids.length);
    if (notice.level === "success") {
      toastSuccess(notice.text);
    } else {
      toastError(notice.text);
    }
    // BOTH, and even on the zero-node outcome: the run's status can have moved
    // (KAS reports it in the same reply) and the verbs it offers move with it, so
    // a row left showing Retry after one would invite the same dead click again.
    invalidateRun(workflowID);
    invalidateRunControls(workflowID);
  },
});

/** Re-drive a paused run. Works even when the launching process is gone — KAS
 *  reloads the run from disk — which is why the button is offered on any paused
 *  run rather than only on one this browser started. */
export const resumeRun = runControl("resume", "Couldn't resume the run");

/** Answer the question a parked workflow step asked.
 *
 *  The server claims the ask BEFORE it sends, so exactly one surface can answer it
 *  — two browser tabs and a run tab are all offered the same card. A 409 means
 *  somebody else got there first or the step moved on, and the card is retired by
 *  the `run_input_settled` frame either way, so the message is worth showing but
 *  there is nothing for the reader to redo.
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

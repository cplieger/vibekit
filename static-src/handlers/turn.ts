// ---------------------------------------------------------------------------
// SSE handlers for turn lifecycle, the three decision types, and errors.
//
// A decision is ENQUEUED, not shown: `decision-dock.ts` owns a per-chat queue,
// so nothing here gates on `getActiveId()` — a permission raised on a
// background chat must still reach the dock with the tab dot pointing at it.
// ---------------------------------------------------------------------------

import { onSSE } from "../bus.js";
import {
  setWorkingLabel,
  setTurnSummary,
  get,
  getActiveId,
  setTurnOpen,
  outcomeLatch,
  applyLatch,
  dropSteers,
} from "../store.js";
import { notifyIfHidden, NOTIFY_TITLE } from "../notify.js";
import { noteAgentFinished } from "../agent-finished-cue.js";
import { pushDecision, collapseSettledDecision, dropTurnDecisions } from "../decision-dock.js";
import { setAgentDown, clearAgentDown } from "../send-state.js";
import { reportFailure } from "../failure-notice.js";
import { refreshGitBadge } from "../git.js";
import type { ToastRetry } from "../toast.js";
import { openSetting } from "../settings-highlight.js";
import { showLoginModal } from "../modals.js";
import { respondPermission, respondElicitation, respondUserInput } from "../actions/chat.js";
import { ERROR_ROUTES, type ErrorAction } from "./error-routing.js";
import { clearTurnState, retractStaleThinking } from "../turn-teardown.js";
import { refreshTurnRail } from "../turn-rail.js";
import { severityOf, defaultFailureReason } from "../turn-severity.js";
import type { TurnEndedPayload, TurnOutcome } from "../wire/types.gen.js";
export { ERROR_ROUTES };

// The per-kind switch, the replay dedup and the settle test all live in
// `agent-finished-cue.ts` now. This handler owns one fact — the turn ended, and this
// is what a cue for it would say — because whether that cue may be raised YET depends
// on the runs and asks the turn left behind, which is not a turn-frame question.

/** What an off-screen notification SAYS about a finished turn, and "" for a turn that must
 *  notify nothing.
 *
 *  A TOTAL switch on the SEVERITY with no default arm, so a fifth `TurnSeverity` member
 *  leaves a path with no return and fails `noImplicitReturns` rather than silently
 *  inheriting a wording. `unknown` says nothing, because an unreadable end says nothing
 *  about success. No sentence is authored here: `turn-severity.ts` owns the table, and the
 *  server's own push reads the same one (internal/agent/turn_finalize.go). */
function notifyBodyFor(outcome: TurnOutcome | undefined, name: string): string {
  switch (severityOf(outcome)) {
    case "clean":
      return `${name}: Agent finished`;
    case "broken":
      return `${name}: ${defaultFailureReason(outcome)}`;
    case "stopped":
    case "running":
      return "";
  }
}

/** Whose turn a `turn_ended` frame speaks for. */
type TurnFrameScope = "chat" | "displaced" | "run";

/** Which turn's end is this frame reporting?
 *
 *  The payload carries no turn identity, so its two markers ARE the identity: `superseded`
 *  means a replacement displaced this turn on the same chat, `workflow_step` means a run's
 *  step opened it rather than the reader. Absent means the chat's own turn, which is what an
 *  older server's frame keeps meaning. `outcome` decides nothing here, which is the fix — a
 *  `closerWireEnd` whose stop reason was unmeasured arrives as `unknown` too, and that end
 *  settles the chat's turn like any other. */
function scopeOf(p: TurnEndedPayload): TurnFrameScope {
  if (p.superseded === true) {
    return "displaced";
  }
  if (p.workflow_step === true) {
    return "run";
  }
  return "chat";
}

onSSE("working_label", (chatID, p) => {
  setWorkingLabel(chatID, p.label);
});

onSSE("turn_ended", (chatID, p) => {
  const scope = scopeOf(p);
  if (scope === "displaced") {
    // NONE of the gated effects: the replacement turn is running right now, so every one of
    // them would tear down THAT turn's state. The line is A6's revisit instrument for the
    // declined ClearAtTurnEnd gate, priced on how often this arm reaches a chat whose own
    // `thinking` was set.
    if (get(chatID)?.thinking === true) {
      console.warn("[turn_ended] displaced frame over a live turn", chatID, p.outcome ?? "");
    }
  } else if (scope === "run") {
    // A RUN's turn ended, and this frame says nothing about the chat's own turn, which may be
    // live right now: `clearTurnState`'s other four effects would damage it, above all
    // `clearLiveTurnMessage`, whose loss deletes the streaming reply on the next newest-page
    // `loadMessages`.
    const live = get(chatID)?.thinking === true;
    // The re-derivation inside reads the RESIDENT transcript, which for a step turn that
    // carried content holds that step's own carrier — so it MAY land the step's verdict,
    // accepted because a reload gives the same answer. What this arm refuses is
    // `applyLatch(outcomeLatch(p.outcome))`, which would latch it with NO carrier resident.
    retractStaleThinking(chatID);
    // A6's third line. A concurrent live turn holds the re-derived `done` dot until its next
    // `markTurnLive`, which fires from `message_appended`'s opensTurn arm and `message_chunk`
    // only — so inside a long tool call that is the length of the call, not one chunk.
    console.warn("[turn_ended] run frame retracted stale thinking", chatID, "own turn live:", live);
  }
  const settles = scope === "chat";
  // Every ask this turn raised is over — a workflow run's ask survives, since it outlives
  // the turn that launched it. Before the dot is re-derived, or a stale ask decides the
  // state one last time. Gated with the rest because the sweep keeps only RUN-scoped asks,
  // so on a frame reporting another turn's end it would retire the asks of a turn that is
  // still running and strand a live JSON-RPC request, which `handlers/run.ts` guards its own
  // copy against. The cost: a DISPLACED turn's unanswerable asks survive until the next
  // settled `turn_ended`, and `tabStatusFor` ranks `input` first, so that chat's dot reads
  // "blocked on you" instead of "working" for the length of the displacing turn.
  if (settles) {
    dropTurnDecisions(chatID);
  }
  // The turn's own verdict, latched even for the chat the reader is watching: skipping it
  // there hid the "I am done" state at the exact moment it happened. Cleared only by the
  // next turn's progress.
  //
  // Outcome decides, not stop reason, and `outcomeLatch` is the one table that says which
  // outcome latches what — the same call every RE-derivation makes. A hand-written mapping
  // here disagreed with them on `interrupted`, so one turn showed idle's hollow ring live
  // and a solid failed dot after the next reload.
  if (settles) {
    applyLatch(chatID, outcomeLatch(p.outcome));
    // A closer RAN, so the record is final: the carrier's own `message_appended`
    // echo is already on its way. Written at the CALL SITE rather than inside
    // `clearTurnState`, deliberately — that function also runs on `transport:gap`,
    // where dropping the server's last liveness statement at the exact moment
    // `thinking` is also cleared is the gap-path flash `turnLive` exists to remove.
    setTurnOpen(chatID, false);
    clearTurnState(chatID);
  }
  // This chat's turn index changed, so its rail record needs a re-read. Ungated:
  // it is the one effect that reads the server's own authoritative liveness.
  void refreshTurnRail(chatID);
  clearAgentDown();
  refreshGitBadge();
  // KAS clears its steering buffer at every turn boundary; anything still in
  // the dock was never read. `dropSteers` promotes each as "not delivered"
  // rather than deleting it silently.
  if (settles) {
    dropSteers(chatID);
  }

  // Inside the `settles` branch, and AFTER the writes above: the cue is a statement
  // about a turn this handler has settled, and `chatSettled` reads the turn state
  // those writes produce. Outside it, a displaced or run frame — reporting the end of a
  // turn that is NOT this chat's own — could raise a cue for work still in flight.
  if (settles) {
    noteAgentFinished(chatID, notifyBodyFor(p.outcome, get(chatID)?.name ?? "Chat"));
  }

  // Turn summary (credits · elapsed · files changed), stamped onto the last
  // assistant message; the renderer projects it into a keyed `.turn-footer`.
  // Unconditional so a background turn's footer is present on switch.
  const summary: {
    credits?: number;
    elapsedMs?: number;
    changedFiles?: typeof p.changed_files;
    model?: string;
  } = {};
  if (p.credits_delta !== undefined) {
    summary.credits = p.credits_delta;
  }
  if (p.elapsed_ms !== undefined) {
    summary.elapsedMs = p.elapsed_ms;
  }
  if (p.changed_files !== undefined) {
    summary.changedFiles = p.changed_files;
  }
  if (p.model !== undefined) {
    summary.model = p.model;
  }
  if (settles) {
    setTurnSummary(chatID, summary);
  }
});

// Each of the three asks below notifies unconditionally, gated only by the
// master notifications switch. Each blocks the turn until answered, so a
// per-kind mute would stall every later turn with nothing on screen saying
// why. Settings -> Permissions relaxation is what stops the asks entirely.

onSSE("permission_needed", (chatID, p) => {
  notifyIfHidden(
    NOTIFY_TITLE,
    p.files !== undefined && p.files.length > 0
      ? "Review this turn's changes"
      : "Permission needed",
  );
  pushDecision({
    kind: "permission",
    chatID,
    runID: p.run_id ?? "",
    requestID: p.request_id,
    payload: p,
    submit: (optionID, fileDecisions) => {
      void respondPermission.dispatch(
        fileDecisions !== undefined
          ? { chatID, requestID: p.request_id, optionID, fileDecisions }
          : { chatID, requestID: p.request_id, optionID },
      );
    },
  });
});

onSSE("elicitation_needed", (chatID, p) => {
  notifyIfHidden(NOTIFY_TITLE, "Input requested by a tool");
  pushDecision({
    kind: "elicitation",
    chatID,
    runID: p.run_id ?? "",
    requestID: p.request_id,
    payload: p,
    submit: (action, content) => {
      void respondElicitation.dispatch(
        content !== undefined
          ? { chatID, requestID: p.request_id, action, content }
          : { chatID, requestID: p.request_id, action },
      );
    },
  });
});

onSSE("user_input_needed", (chatID, p) => {
  notifyIfHidden(NOTIFY_TITLE, "The agent has a question");
  pushDecision({
    kind: "user_input",
    chatID,
    runID: p.run_id ?? "",
    requestID: p.request_id,
    payload: p,
    submit: (action, answer) => {
      void respondUserInput.dispatch(
        action === "answered" && answer !== undefined
          ? { chatID, requestID: p.request_id, action, answer }
          : { chatID, requestID: p.request_id, action },
      );
    },
  });
});

// Every ask above is offered to every surface at once; only the first answer
// is accepted, so the server names the settled request and this retires the
// card everywhere else.
onSSE("decision_settled", (chatID, p) => {
  collapseSettledDecision(chatID, p.kind, p.request_id, p.settled_by);
});

// --- Data-driven error classification (imported from error-routing.ts) ---

/** Turns a route's declared action into the toast's one action slot. */
function toastActionFor(action: ErrorAction | undefined): ToastRetry | undefined {
  if (action === undefined) {
    return undefined;
  }
  switch (action.kind) {
    case "setting":
      return {
        label: action.label,
        onClick: () => {
          openSetting(action.tab, action.control);
        },
      };
    case "sign-in":
      return { label: action.label, onClick: showLoginModal };
    default:
      action satisfies never;
      return undefined;
  }
}

// This handler touches no turn state: the server ends every turn exactly once
// via `turn_ended`, so an error is a report only.
onSSE("error", (chatID, p) => {
  const code = p.code;
  const msg = p.message;

  const route = ERROR_ROUTES[code];
  if (route === undefined) {
    reportFailure(chatID, msg !== "" ? msg : code);
    return;
  }
  switch (route.surface) {
    case "toast":
      // Reported for every chat the reader is not looking at, and NOT for the one they are
      // when the failure is turn-scoped: that turn's card carries the same reason durably,
      // so a corner overlay is a second copy over the top of the first. `failure-notice.ts`
      // owns the suppression.
      //
      // `turn_scoped` comes off the FRAME rather than the route, because whether a turn was
      // finalized is a property of the emission — `prompt_failed` has three server emitters
      // that open no turn at all. Absent means no, so an older frame reports.
      reportFailure(chatID, msg, toastActionFor(route.action), p.turn_scoped ?? false);
      break;
    case "agent-down":
      // Active chat only: this DOES paint a shared control, so a background
      // chat's dead bridge must not alert the button of the chat in use.
      if (chatID === getActiveId()) {
        setAgentDown(msg !== "" ? msg : "The agent could not be started for this chat.");
      }
      break;
    default:
      route.surface satisfies never;
  }
});

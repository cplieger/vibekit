// ---------------------------------------------------------------------------
// How a send or turn failure reaches the user.
//
// The surface is a bottom-right error TOAST, and this module exists because the
// surface it replaced was a hover tooltip on the send button. That tooltip was
// unreachable in four ordinary situations, all measured: on a touch device there
// is no hover and no :focus-visible, so a phone never showed it at all; the state
// behind it is a module-level signal, so a reload discarded it; it was suppressed
// for any chat that was not the active one; and the next Send cleared it. A user
// who hit a 429 saw "Turn interrupted" in the transcript and nothing else.
//
// The toast is HALF the fix. The other half is server-side and durable: the
// interrupted divider in the transcript now carries the same reason (see
// appendInterruptedEvent in internal/agent/bridge_coord.go), so the toast is the
// glance and the transcript is the record. That split is why this module caps the
// text and does not care about being missed: nothing is lost if a toast times out.
//
// A toast is a CORNER OVERLAY, so it has to say what it is about. A reader on
// Settings, the git panel or an editor tab has no chat in sight, so a bare reason
// there names nothing. Every notice for a chat that is not the tab on screen
// carries that chat's name and an "Open chat" button; see `raise`.
//
// NOT a retry surface, deliberately. The button is a jump, never a resend:
// submit.ts already re-sends under the failed attempt's own message id, so
// pressing Send IS the idempotent retry, and a retry button here would be a
// second path to the same thing with its own state to get wrong.
// ---------------------------------------------------------------------------

import { join } from "@cplieger/keyenc";
import { error as toastError, errorWithAction, type ToastRetry } from "./toast.js";
import { get } from "./store.js";
import { activateTab, getActiveTabId, tabIdFor } from "./tabs.js";
import { truncate } from "./strings.js";

/** How long two reports of one failure are treated as the same failure.
 *
 *  A failed prompt is reported TWICE by design: the command POST answers 500
 *  with the reason in its body, and the SSE `error` frame carries the identical
 *  string (CmdPrompt renders it once and sends it on both, because a 400, 413 or
 *  network death carries no SSE frame and a dead POST with a live turn carries no
 *  useful body). They arrive milliseconds apart, so anything above a second or so
 *  would do; five leaves room for a slow POST teardown without ever swallowing a
 *  genuinely repeated failure, which only a fresh Send can produce. */
const DEDUPE_WINDOW_MS = 5_000;

/** Longest reason a toast shows. The server caps its own prose at 2 KiB
 *  (rpcerr.Text), which is a fine size for a transcript row and a wall of text
 *  in a corner overlay. The untruncated reason is on the turn's divider, so this
 *  cap costs nothing a reader cannot reach. */
const MAX_TOAST_CHARS = 240;

/** Longest chat name a toast prefix shows. A chat is named from its first prompt
 *  (an 80-char truncation server-side), which is a paragraph opener rather than a
 *  title, so the prefix takes the leading words and no more. */
const MAX_NAME_CHARS = 40;

/** The label on the jump-to-the-affected-chat button. */
const OPEN_LABEL = "Open chat";

/** What a failure with no message from the server says. Reaching this means the
 *  server sent an error code with an empty message, which is a server bug, so the
 *  wording points at the one place the cause is still recoverable. */
const NO_REASON = "The request failed. Check the server log for the cause.";

/** The last failure reported per chat, for the window above. Per chat because a
 *  failure's identity includes its chat: one shared slot is overwritten by any
 *  other chat's failure, which un-latches the twin still to arrive. */
const latched = new Map<string, { key: string; at: number }>();

/** The live toast per chat, so the dead-POST rescue can retract one. Keyed by
 *  chat because two chats can fail independently and each owns its own notice. */
const live = new Map<string, () => void>();

/** The live remedy-bearing toast per FAILURE rather than per chat. Such a notice
 *  is sticky, so nothing expires it: an identical repeat dismisses the copy it
 *  repeats, and any other failure leaves it standing, because a remedy offered
 *  nowhere else must not be retracted to report something else. */
const remedies = new Map<string, () => void>();

/** Report a failure to the user.
 *
 *  `chatID` may be any chat, active or not: a background chat's failure is named
 *  with its chat and carries a jump to it, so the toast cannot be read as being
 *  about whatever is on screen. That is the one thing the old send-button surface
 *  could not do, and why a background failure used to leave nothing but a tab
 *  dot. An empty `chatID` is a workspace-global command, which names no chat.
 *
 *  `action` is the route's own remedy (a Settings jump, the login modal). It takes
 *  the single action slot from the "Open chat" jump and makes the toast STICKY,
 *  because a remedy offered nowhere else must not expire unread. The chat is still
 *  NAMED, which is the half that answers whose failure this is. */
export function reportFailure(chatID: string, message: string, action?: ToastRetry): void {
  const reason = message.trim() !== "" ? message.trim() : NO_REASON;
  // Dedupe on the TEXT, not on the code or the channel: the two channels agree on
  // the prose by construction (one server-side renderer), which is exactly what
  // makes the text a usable identity. A composite key rather than a template
  // literal because a reason is arbitrary upstream text and a chat id is not.
  const key = join(chatID, reason);
  const now = Date.now();
  const last = latched.get(chatID);
  if (last?.key === key && now - last.at < DEDUPE_WINDOW_MS) {
    latched.set(chatID, { key, at: now });
    return;
  }
  // Retract only what this raise supersedes. A sticky remedy is replaced by its
  // own repeat and by nothing else; an ordinary notice is replaced by whatever
  // this chat reports next.
  if (action !== undefined) {
    remedies.get(key)?.();
  } else {
    clearFailure(chatID);
  }
  // Latch AFTER the retraction: clearFailure drops this chat's latch, so latching
  // first leaves every failure after its first un-deduped, and its twin on the
  // other channel re-raises the toast that was just replaced.
  latched.set(chatID, { key, at: now });
  const dismiss = raise(chatID, truncate(reason, MAX_TOAST_CHARS), action);
  if (action !== undefined) {
    remedies.set(key, dismiss);
  } else {
    live.set(chatID, dismiss);
  }
}

/** Retract this chat's ordinary failure notice.
 *
 *  `reportFailure`'s own replace is the only caller and the export is the test
 *  seam. A remedy-bearing notice is deliberately out of reach: this retracts a
 *  report that turned out not to describe a failure, which a broken agent file
 *  is not. */
export function clearFailure(chatID: string): void {
  const dismiss = live.get(chatID);
  if (dismiss === undefined) {
    return;
  }
  live.delete(chatID);
  // A retraction also clears the dedupe latch. Without this, the retracted
  // failure keeps suppressing its own text for the rest of the window, so a
  // genuine repeat inside five seconds would be silent.
  latched.delete(chatID);
  dismiss();
}

/** Show the notice, naming and linking the affected chat unless its transcript is
 *  the thing on screen.
 *
 *  THE QUESTION IS "IS THIS CHAT ON SCREEN", NOT "IS THIS THE ACTIVE CHAT", and
 *  the difference is the whole reason this function exists. `store.getActiveId()`
 *  keeps naming the last chat a reader opened for as long as the app runs: nothing
 *  clears it when they move to Settings, the git panel, the file browser, a doc or
 *  an editor tab (`setActive` has exactly three callers, all inside chat.ts's
 *  activate path). So a reader sitting on Settings when a background chat throttles
 *  matched `chatID === getActiveId()`, and the notice arrived with no chat named
 *  and nothing to click — an unattributed failure on a screen with no chat in
 *  sight, which is worse than the tooltip it replaced. The tab id is the honest
 *  answer, because a chat tab's id IS the chat id. */
function raise(chatID: string, reason: string, action?: ToastRetry): () => void {
  // Resolved once: it answers both "is this chat the one on screen" and "is there
  // a tab to jump to", and "" is the second answer's no.
  const tabID = chatID === "" ? "" : tabIdFor("chat", chatID);
  const onScreen = chatID === "" || tabID === getActiveTabId();
  const name = onScreen ? "" : truncate(get(chatID)?.name ?? "", MAX_NAME_CHARS);
  const message = name !== "" ? `${name}: ${reason}` : reason;
  // A route's own remedy takes the one action slot, and takes it sticky: toast.ts
  // times out an action reachable another way, and neither of these is.
  if (action !== undefined) {
    return toastError(message, action);
  }
  // No tab, no button. `activateTab` no-ops on an id it does not hold, so offering
  // the jump for a chat with no tab would render a control that does nothing —
  // which teaches a reader to distrust every other one.
  if (onScreen || tabID === "") {
    return toastError(message);
  }
  return errorWithAction(message, {
    label: OPEN_LABEL,
    onClick: () => {
      activateTab(tabID);
    },
  });
}

/** Test-only: drop the dedupe latch and every tracked toast handle. */
export function _resetForTest(): void {
  latched.clear();
  live.clear();
  remedies.clear();
}

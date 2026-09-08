// ---------------------------------------------------------------------------
// How a send or turn failure reaches the user: a bottom-right error TOAST. The
// toast is the GLANCE and the transcript is the RECORD (the interrupted divider
// carries the same reason server-side), so nothing is lost if one times out. NOT a
// retry surface: `submit.ts` re-sends under the failed attempt's own message id.
// ---------------------------------------------------------------------------

import { join } from "@cplieger/keyenc";
import { error as toastError, errorWithAction, type ToastRetry } from "./toast.js";
import { get } from "./store.js";
import { activateTab, getActiveTabId, tabIdFor } from "./tabs.js";
import { truncate } from "./strings.js";

/** How long two reports of one failure are treated as the same failure.
 *
 *  A failed prompt is reported TWICE by design — the command POST's own body and
 *  the SSE `error` frame carry the identical string — milliseconds apart. Five
 *  seconds leaves room for a slow POST teardown; only a fresh Send repeats. */
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

/** The label on the jump-to-the-affected-chat button. ONE WORD, because the button
 *  sits inline with the message (04-uip-skin.css) inside a card capped at 384px, so
 *  every character it takes is a character the reason loses. "chat" was also saying
 *  it twice: this button is offered ONLY when the chat is not on screen, which is
 *  the same condition that puts that chat's name at the front of the message. */
const OPEN_LABEL = "Open";

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
 *  nowhere else must not be retracted to report something else. How many stand at
 *  once is bounded by the surface rather than here — toast.ts's MAX_STICKY — so a
 *  handle in this map may name a toast that is already gone; dismissing one is a
 *  no-op. */
const remedies = new Map<string, () => void>();

/** Report a failure to the user. `chatID` may be any chat, active or not; an empty one
 *  is a workspace-global command and names no chat.
 *
 *  `action` is the route's own remedy (a Settings jump, the login modal): it takes the
 *  jump button's one action slot and makes the toast STICKY, being offered nowhere else. */
export function reportFailure(
  chatID: string,
  message: string,
  action?: ToastRetry,
  turnScoped = false,
): void {
  if (suppressedAsAlreadyOnScreen(chatID, action, turnScoped)) {
    return;
  }
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
  // A sticky remedy is replaced by its own repeat and by nothing else; an ordinary
  // notice is replaced by whatever this chat reports next.
  if (action !== undefined) {
    remedies.get(key)?.();
  } else {
    clearFailure(chatID);
  }
  // Latch AFTER the retraction: `clearFailure` drops this chat's latch, so latching
  // first leaves every failure after the first un-deduped.
  latched.set(chatID, { key, at: now });
  const dismiss = raise(chatID, truncate(reason, MAX_TOAST_CHARS), action);
  if (action !== undefined) {
    remedies.set(key, dismiss);
  } else {
    live.set(chatID, dismiss);
  }
}

/** Whether the reader is ALREADY LOOKING at the durable report of this failure, so a
 *  corner overlay would be a second copy of it. Four conjuncts, each a case that must
 *  still toast: only a `turnScoped` failure has an inline home (the SERVER states it
 *  per frame, because it is a property of the emission and not of the code); an
 *  `action` is a remedy no inline row can offer; an empty `chatID` names no chat; and
 *  ON SCREEN needs both signals, or a hidden window is toasted into. */
function suppressedAsAlreadyOnScreen(
  chatID: string,
  action: ToastRetry | undefined,
  turnScoped: boolean,
): boolean {
  if (!turnScoped || action !== undefined || chatID === "") {
    return false;
  }
  return tabIdFor("chat", chatID) === getActiveTabId() && document.visibilityState === "visible";
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
  // The latch too, or the retracted text stays suppressed for the rest of the window.
  latched.delete(chatID);
  dismiss();
}

/** Show the notice, naming and linking the affected chat unless it is the thing on
 *  screen. THE TEST IS THE TAB, not `store.getActiveId()`: nothing clears that when
 *  the reader moves to Settings or an editor tab, so it names a chat off screen. */
function raise(chatID: string, reason: string, action?: ToastRetry): () => void {
  // Resolved once: it answers both "is this chat the one on screen" and "is there
  // a tab to jump to", and "" is the second answer's no.
  const tabID = chatID === "" ? "" : tabIdFor("chat", chatID);
  const onScreen = chatID === "" || tabID === getActiveTabId();
  const name = onScreen ? "" : truncate(get(chatID)?.name ?? "", MAX_NAME_CHARS);
  const message = name !== "" ? `${name}: ${reason}` : reason;
  // A route's own remedy takes the one action slot, sticky: it is reachable nowhere else.
  if (action !== undefined) {
    return toastError(message, action);
  }
  // No tab, no button: `activateTab` no-ops on an id it does not hold, so the jump
  // would be a control that does nothing.
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

// ---------------------------------------------------------------------------
// Carrying an unread steer into the next turn.
//
// A steer the agent never read used to be lost at the turn boundary: KAS drains
// its steering buffer at every boundary, so the message left the dock, landed in
// the transcript as a `dropped` mark, and the reader had to click it back into the
// composer and press Send. This module sends it for them.
//
// ONE MECHANISM, ONE FIRING POINT, ONE TEXT PRODUCER. The boundary is the only thing
// that reads a steer's text, so both boundary origins are covered with no gesture
// code: a manual stop emits `steering_cleared`, and `turn_ended` follows either way.
//
// The dock's send-now arrow therefore arms an ORDER, not a message. Arming text at
// click time dropped a steer confirmed between the click and the boundary; reading at
// the boundary makes "every user-origin steer the dock drops is carried" true by
// construction, and `fundamentals/steer-note.ts` states that invariant in its label.
//
// IT FIRES ON THE CHAT'S OWN SETTLED `turn_ended` FRAME AND NOWHERE ELSE.
// Not on `steer_cleared`, which arrives while the turn is still finishing, and not
// after the cancel POST resolves: `cancelTurn`'s optimistic write clears `thinking`
// locally while the turn is still open server-side, so an earlier send would take a
// 409 and `submit.ts` would convert it back into a STEER — putting the message
// straight into the buffer the boundary just drained.
//
// IT CANNOT LOOP, STRUCTURALLY. A turn opened by a resend ends with an empty
// waiting set, so the capture arms nothing and nothing further fires. The only way
// a resent turn produces another is the reader typing a new steer into it, which is
// the feature working. No counter and no flag for that; the counter below bounds
// something else — how many times ONE armed text may be re-offered after a refusal.
// ---------------------------------------------------------------------------

import { clearSteers } from "./actions/chat.js";
import { sendPromptTo } from "./chat-commands.js";
import { restoreFailedSend } from "./composer-state.js";
import { reportSendRefused } from "./send-state.js";
import { newMessageID } from "./transport.js";

/** Blank line between concatenated messages. Grounded twice rather than chosen:
 *  kiro-cli's own TUI joins queued steer messages with a blank line into one slot,
 *  and `submit.ts`'s `withAttachmentPaths` already joins a message and its trailing
 *  lines the same way. No prefix, no header, no "resent:" marker — the text is the
 *  reader's own words and vibekit does not put words in their mouth. */
const JOIN = "\n\n";

/** How many boundaries one armed text may be offered at. Two: the send, plus one
 *  retry for the honest race where a turn started underneath it. */
const MAX_ATTEMPTS = 2;

/** The send-error face for a resend the chat would not take. Reuses the existing
 *  failed-send surface, so the next Send is the retry, and says whose message it is
 *  because the reader did not press Send for this one. */
const REFUSED_FACE = "Couldn't send your unread message — it's back in the message box";

const armed = new Map<string, string>();
const attempts = new Map<string, number>();
const leadFirst = new Map<string, string>();

/** Record which waiting row the send-now arrow wants FIRST. An id, never a text, so a
 *  row confirmed between the click and the boundary is carried too. */
export function preferSteerFirst(chatID: string, steerID: string): void {
  if (chatID === "" || steerID === "") {
    return;
  }
  leadFirst.set(chatID, steerID);
}

/** Its cancel never landed, so no boundary is coming and the rows are still waiting. */
export function forgetSteerPreference(chatID: string): void {
  leadFirst.delete(chatID);
}

/** Arm the slot from a turn boundary, and LEAVE AN ARMED SLOT ALONE. The one text
 *  producer, called at both doors. Yielding is what lets a refused send re-offer its
 *  own text at the next boundary. */
export function noteBoundaryDrop(
  chatID: string,
  entries: readonly { id: string; text: string }[],
): void {
  if (chatID === "" || armed.has(chatID)) {
    return;
  }
  const text = join(leadPreferred(chatID, entries).map((e) => e.text));
  if (text === "") {
    return;
  }
  leadFirst.delete(chatID);
  armed.set(chatID, text);
}

/** Drop everything for this chat without sending: the chat is gone. */
export function forgetSteerResend(chatID: string): void {
  armed.delete(chatID);
  attempts.delete(chatID);
  leadFirst.delete(chatID);
}

/** The arrow's row first, then the rest in arrival order. A preference naming a row
 *  this boundary is not carrying is ignored, never fatal: the others are still owed
 *  a turn. */
function leadPreferred(
  chatID: string,
  entries: readonly { id: string; text: string }[],
): readonly { id: string; text: string }[] {
  const wanted = leadFirst.get(chatID);
  if (wanted === undefined) {
    return entries;
  }
  const lead = entries.find((e) => e.id === wanted);
  if (lead === undefined) {
    return entries;
  }
  return [lead, ...entries.filter((e) => e.id !== wanted)];
}

/** Send whatever is armed for this chat as a new turn. Safe to call when nothing is
 *  armed — it runs on every settled `turn_ended` of every chat. */
export function runArmedResend(chatID: string): void {
  const text = armed.get(chatID);
  if (text === undefined) {
    return;
  }
  armed.delete(chatID);
  void send(chatID, text);
}

async function send(chatID: string, text: string): Promise<void> {
  // Belt and braces, and it closes one real race: a steer POST confirmed just before
  // this boundary can still be sitting in KAS's buffer, in which case it would be
  // injected into the very turn the resend opens — the reader's message twice. One
  // idempotent dispatch; `CmdSteerClear` answers success for a chat with no bridge,
  // and KAS answers success against an empty buffer. Ahead of the prompt by SCOPE
  // rather than by an await: both actions hold `chat:<id>`, which is FIFO.
  void clearSteers.dispatch({ chatID });
  // `sendPromptTo`, never `submitPrompt`: that one takes the composer's staged
  // attachments, and this message is not the one the reader is composing. A steer's
  // text already carries any `Attached file:` lines folded in at steer time.
  const outcome = await sendPromptTo(chatID, text, { messageID: newMessageID() });
  if (outcome === "sent") {
    attempts.delete(chatID);
    return;
  }
  const spent = (attempts.get(chatID) ?? 0) + 1;
  // "queued" and "starting" both mean the chat is busy again — a turn started
  // underneath the boundary, or the admission slot is held by a spawn. Re-arm and let
  // the next settled boundary carry it; "failed" is a real refusal and stops here.
  if (outcome !== "failed" && spent < MAX_ATTEMPTS) {
    attempts.set(chatID, spent);
    // Yields like the boundary producer, so a capture that armed since the refusal
    // outranks the retry. The joined text is re-armed as-is: same batch, already
    // ordered.
    if (!armed.has(chatID)) {
      armed.set(chatID, text);
    }
    return;
  }
  attempts.delete(chatID);
  restoreFailedSend(chatID, text);
  reportSendRefused(REFUSED_FACE);
}

function join(texts: readonly string[]): string {
  return texts.filter((t) => t !== "").join(JOIN);
}

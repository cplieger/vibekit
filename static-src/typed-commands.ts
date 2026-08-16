// ---------------------------------------------------------------------------
// Typed-command intercepts: a HANDLER TABLE, not a palette.
//
// Two or three entries, checked before send. This exists because a typed slash
// command that KAS does not parse reaches the MODEL as prose, and the model
// answers as though it had run — `/compact` returns `end_turn` in ~3.4s with zero
// summarization frames while the reply says "Done — context compacted." That is
// the worst failure shape available: a lie with no tell.
//
// So a command vibekit wants to be DETERMINISTIC must own its handler and call
// its own endpoint. Passing the text through is asking the model to maybe do it.
//
// This is deliberately NOT the command palette, which was priced out and
// declined (see vibekit.md "Slash commands"). A palette advertises ~90 entries of
// which one category in four silently degrades to prose; a table of three
// entries advertises nothing and makes exactly those three true.
// ---------------------------------------------------------------------------

import { compactChat } from "./actions/chat.js";
import { isThinking, setThinking } from "./store.js";
import { showToast } from "./toast.js";

/** Handles a typed command. Returns true when it consumed the input, so the
 *  caller must NOT also send it as a prompt. */
type TypedHandler = (chatID: string) => boolean;

/** The table. Keyed on the bare verb, matched case-insensitively after the
 *  leading slash, and only when the input is the verb ALONE — `/compact` is a
 *  command, `/compact this file` is a sentence that starts with one, and
 *  guessing which was meant is how a table like this starts lying too. */
const HANDLERS: Record<string, TypedHandler> = {
  compact: (chatID) => {
    void compactChat.dispatch({ chatID });
    return true;
  },
  drop: dropTurn,
};

/**
 * `/drop` — end the turn CLIENT-SIDE and put the composer back in prompt mode.
 *
 * The problem it solves is that the two obvious recoveries both need the engine
 * to still be answering, and the state this fixes is the one where it is not.
 * While `thinking` is set, Send means STEER (submit.ts), so typing "continue"
 * goes into KAS's steering buffer where a dead turn never reads it; and the
 * button's click target is Cancel, which is `session/cancel` plus a grace budget
 * that only resolves when KAS answers the pending prompt. So the only thing that
 * worked was reloading the page, which rebuilds the whole client and refetches
 * to change one boolean.
 *
 * ONE write, and everything else follows from the existing reactive chain:
 * send-state.ts recomputes to `idle`, prompt-input.ts restores the button to
 * `type="submit"`, and submit.ts's `isThinking` check now routes the next Send
 * as a PROMPT. It asks the engine for nothing, which is the whole point.
 *
 * Not a break of "server state is canonical": `thinking` is client/stream-owned
 * (store.ts says so, and it is seeded false at chat creation), and the app
 * already performs this exact transition unattended — the `transport:gap`
 * handler clears it on EVERY chat as its safe default. This is the
 * user-triggered, single-chat version of that.
 *
 * If the turn was in fact alive, nothing is lost and nothing lies: the next
 * prompt reaches a busy session, the server answers 409, and submit.ts steers
 * into the running turn exactly as it would have.
 *
 * Deliberately does NOT clear KAS's steering buffer. That would be a command,
 * and a verb whose contract is "requires nothing from the engine" cannot depend
 * on one answering. `steer_clear` is the affordance for that and stays the
 * Discard × on the chip row.
 */
function dropTurn(chatID: string): boolean {
  if (!isThinking(chatID)) {
    // Already idle. Claimed anyway rather than passed through: sending "/drop"
    // to the model is worse than doing nothing, and the toast says why nothing
    // happened.
    showToast("No turn is running on this chat.", "info");
    return true;
  }
  setThinking(chatID, false);
  showToast("Dropped the turn locally. The agent was not told to stop.", "info");
  return true;
}

/**
 * Consume a typed command if the table claims it.
 *
 * Everything not in the table falls through unchanged, including slash text KAS
 * itself parses. vibekit does not try to enumerate what KAS handles — that list
 * is KAS's and it moves.
 */
export function handleTypedCommand(chatID: string, text: string): boolean {
  if (chatID === "") {
    return false;
  }
  const trimmed = text.trim();
  if (!trimmed.startsWith("/")) {
    return false;
  }
  const verb = trimmed.slice(1).toLowerCase();
  if (verb === "" || /\s/.test(verb)) {
    return false;
  }
  const handler = HANDLERS[verb];
  if (handler === undefined) {
    return false;
  }
  return handler(chatID);
}

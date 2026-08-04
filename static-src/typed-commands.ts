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
};

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

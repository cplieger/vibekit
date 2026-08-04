// The rewind action. ONE action, because rewind is one operation now.
//
// It used to be three — create / promote / discard — because a rewind FORKED a
// second chat, which then had to be either merged back over its parent or
// thrown away. A rewind reverts the chat it is in, so there is no branch to
// resolve and nothing to promote or discard.
// ---------------------------------------------------------------------------

import { transportAction } from "./index.js";

interface RewindArgs {
  chatID: string;
  /** The USER message to revert to. That message and everything after it are
   *  dropped, so this is not "revert to just after N" — see the confirm text. */
  messageID: string;
}

/** rewindChat reverts the chat to a past turn: KAS drops the addressed user
 *  message and its successors and rolls the files back from its own snapshots.
 *
 *  A transportAction rather than an apiAction because there is no longer a
 *  response body worth reading. The old create action went through apiAction
 *  purely to recover the server-assigned `rewind_id` so the caller could open
 *  and activate the new branch chat; with no branch, the reverted transcript
 *  arrives over SSE like any other change to the chat you are already looking
 *  at.
 *
 *  `idempotencyKey` still matters, and more than before: a timed-out-but-
 *  succeeded retry used to create a duplicate branch, which was untidy; now it
 *  would revert a SECOND time, from an already-truncated transcript, and take
 *  real turns with it. KAS's own per-session revert-in-progress guard is the
 *  backstop, but the request should not be sent twice in the first place. */
export const rewindChat = transportAction<RewindArgs>({
  name: "rewind.revert",
  networkMode: "always",
  scope: (args) => "rewind:" + args.chatID,
  idempotencyKey: true,
  command: ({ chatID, messageID }) => ({
    type: "rewind_chat",
    chat_id: chatID,
    request_id: `rewind-${String(Date.now())}`,
    payload: { message_id: messageID },
  }),
  error: "Couldn't rewind chat",
});

// The rewind action. ONE action, because rewind is one operation now.
//
// It used to be three — create / promote / discard — because a rewind FORKED a
// second chat, which then had to be either merged back over its parent or
// thrown away. A rewind reverts the chat it is in, so there is no branch to
// resolve and nothing to promote or discard.
// ---------------------------------------------------------------------------

import { transportAction } from "./index.js";
import { loadMessages } from "../store-load.js";

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
 *  and activate the new branch chat; with no branch, there is nothing to open.
 *
 *  THE REVERTED TRANSCRIPT DOES NOT ARRIVE OVER SSE — this comment claimed it
 *  did, and no such mechanism exists. Two facts make the refetch necessary:
 *  `chat_updated` carries only the header, and `upsertHeader` merges the count
 *  as `Math.max(local, incoming)`, so a SHRINK is discarded. Nothing else in the
 *  store removes messages in response to an event. So a rewind that reached KAS
 *  rolled the files back and truncated the record while the reader kept looking
 *  at the dropped turns until a reload.
 *
 *  `onSuccess` refetches rather than reconstructing, which is the pattern the run
 *  store already uses: the record is authoritative after the revert and the
 *  client's window is a page of it, so re-reading that page is both simpler and
 *  more correct than trying to replay the cut locally.
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
    payload: { message_id: messageID },
  }),
  onSuccess: (_result, { chatID }) => {
    void loadMessages(chatID);
  },
  error: "Couldn't rewind chat",
});

// Actions for crew (subagent) interactions.
// ---------------------------------------------------------------------------

import { transportAction } from "./transport.js";
import { RETRY_STANDARD } from "./types.js";

interface SendMessageArgs {
  chatID: string;
  subSessionID: string;
  text: string;
}

export const sendMessage = transportAction<SendMessageArgs>({
  name: "crew.send_message",
  scope: (args) => "crew:" + args.chatID + ":" + args.subSessionID,
  idempotencyKey: true,
  retryable: "network",
  retry: RETRY_STANDARD,
  command: ({ chatID, subSessionID, text }) => ({
    type: "message_subagent",
    chat_id: chatID,
    payload: { sub_session_id: subSessionID, text },
  }),
  error: "Failed to send message to subagent",
});

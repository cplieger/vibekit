// Actions for crew (subagent) interactions.
// ---------------------------------------------------------------------------

import { transportAction } from "./transport.js";

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
  retry: { count: 2, delay: 300 },
  command: ({ chatID, subSessionID, text }) => ({
    type: "message_subagent",
    chat_id: chatID,
    payload: { sub_session_id: subSessionID, text },
  }),
  error: "Failed to send message to subagent",
});

// Actions for crew (subagent) interactions.
// ---------------------------------------------------------------------------


import { retryNetwork, RETRY_STANDARD, transportAction } from "./index.js";


interface SendMessageArgs {
  chatID: string;
  subSessionID: string;
  text: string;
}

export const sendMessage = transportAction<SendMessageArgs>({
  name: "crew.send_message",
  networkMode: "always",
  scope: (args) => "crew:" + args.chatID + ":" + args.subSessionID,
  idempotencyKey: true,
  retryable: retryNetwork,
  retry: RETRY_STANDARD,
  command: ({ chatID, subSessionID, text }) => ({
    type: "message_subagent",
    chat_id: chatID,
    payload: { sub_session_id: subSessionID, text },
  }),
  error: "Failed to send message to subagent",
});

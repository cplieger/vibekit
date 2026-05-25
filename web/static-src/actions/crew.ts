import { transportAction } from "./index.js";

interface SendMessageArgs {
  chatID: string;
  subSessionID: string;
  text: string;
}

export const sendMessage = transportAction<SendMessageArgs>({
  name: "crew.send_message",
  scope: (args) => "crew:" + args.chatID + ":" + args.subSessionID,
  command: ({ chatID, subSessionID, text }) => ({
    type: "message_subagent",
    chat_id: chatID,
    payload: { sub_session_id: subSessionID, text },
  }),
  error: "Failed to send message to subagent",
});

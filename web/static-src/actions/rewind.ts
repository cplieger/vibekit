// Actions for rewind chat operations (promote / discard).
// ---------------------------------------------------------------------------

import { transportAction } from "./transport.js";

interface RewindArgs {
  chatID: string;
}

export const promoteRewindChat = transportAction<RewindArgs>({
  name: "rewind.promote",
  networkMode: "always",
  scope: (args) => "rewind:" + args.chatID,
  idempotencyKey: true,
  command: ({ chatID }) => ({
    type: "promote_rewind_chat",
    chat_id: chatID,
    request_id: `promote-${Date.now()}`,
  }),
  error: "Failed to promote rewind chat",
});

export const discardRewindChat = transportAction<RewindArgs>({
  name: "rewind.discard",
  networkMode: "always",
  scope: (args) => "rewind:" + args.chatID,
  idempotencyKey: true,
  command: ({ chatID }) => ({
    type: "discard_rewind_chat",
    chat_id: chatID,
    request_id: `discard-${Date.now()}`,
  }),
  error: "Failed to discard rewind chat",
});

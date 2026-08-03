// Actions for rewind chat operations (create / promote / discard).
// ---------------------------------------------------------------------------

import { apiAction, transportAction } from "./index.js";

interface RewindArgs {
  chatID: string;
}

interface RewindCreateArgs {
  chatID: string;
  turnIndex: number;
}

// rewindChat branches a new chat from a past turn. Unlike promote/discard
// (fire-and-forget transportActions), the caller needs the server-assigned
// branch id back so it can open + activate the new chat. transport.send
// discards the response body on success, so this goes through apiAction —
// which POSTs the same command envelope to /api/command and returns the
// parsed `{ ok, rewind_id }` body. idempotencyKey guards against a
// timed-out-but-succeeded retry creating a duplicate branch; no auto-retry
// for the same reason.
export const rewindChat = apiAction<RewindCreateArgs, { ok?: boolean; rewind_id?: string }>({
  name: "rewind.create",
  scope: (args) => "rewind:" + args.chatID,
  idempotencyKey: true,
  request: ({ chatID, turnIndex }) => ({
    method: "POST",
    path: "/api/command",
    body: {
      type: "rewind_chat",
      chat_id: chatID,
      request_id: `rewind-${String(Date.now())}`,
      payload: { turn_index: turnIndex },
    },
  }),
  error: "Couldn't rewind chat",
});

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

// discardRewindChat is GONE: its only caller was tabs.ts's rewind-child prompt,
// and under the sub-tab cascade there is no question to ask, so no dispatch to
// make. The server-side command follows in task T3 with the rest of the branch
// apparatus.

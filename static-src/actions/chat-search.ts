// Cross-chat search: the History page's box.
//
// Separate from the in-chat search (find-in-chat.ts, scoped to the chat being
// read) because they answer different questions — this one finds the
// conversation, that one finds the position within it.

import { apiAction, retryNetwork, RETRY_STANDARD } from "./index.js";
import type { ChatSearchResult } from "../chat-search-types.js";

export const searchChats = apiAction<string, ChatSearchResult>({
  name: "chat.search_all",
  dedupe: true,
  retryable: retryNetwork,
  retry: RETRY_STANDARD,
  request: (q) => ({ method: "GET", path: `/api/chats/search?q=${encodeURIComponent(q)}` }),
  // The box shows its own inline note; a toast per keystroke would be noise.
  error: false,
});

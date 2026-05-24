// Actions for chat lifecycle: delete, archive, restore, discard tangent,
// load history, delete archived.
// ---------------------------------------------------------------------------

import { apiAction, transportAction, defineAction, ActionError } from "./index.js";
import { get } from "../store.js";

// --- chat.delete ---

export const deleteChatAction = transportAction<string>({
  name: "chat.delete",
  command: (id) => ({ type: "delete_chat", chat_id: id }),
  optimistic: (id) => {
    const session = get(id);
    return session !== undefined ? { ...session } : undefined;
  },
  rollback: (_id, op) => {
    // Rollback not feasible without re-inserting into store; the SSE
    // chat_deleted handler is the canonical removal path. Since
    // transport failures are rare and the server echoes back, we
    // accept the race. A full rollback would require re-adding to
    // _sessions which isn't exposed. Log only.
  },
  error: "Couldn't delete chat",
});

// --- chat.archive ---

export const archiveChatAction = apiAction<string, unknown>({
  name: "chat.archive",
  request: (id) => ({
    method: "POST",
    path: `/api/chats/${encodeURIComponent(id)}/archive`,
  }),
  // Silent: auto-triggered on tab close, not user-initiated feedback.
  success: false,
  error: false,
});

// --- chat.discard_tangent ---

export const discardTangentAction = transportAction<string>({
  name: "chat.discard_tangent",
  command: (id) => ({ type: "discard_tangent", chat_id: id }),
  error: "Couldn't discard tangent",
});

// --- chat.restore ---

export const restoreChatAction = apiAction<string, { ok: boolean }>({
  name: "chat.restore",
  request: (id) => ({
    method: "POST",
    path: "/api/chats/archived",
    body: { id },
  }),
  error: "Couldn't restore chat",
});

// --- chat.delete_archived ---

export const deleteArchivedChatAction = apiAction<string, unknown>({
  name: "chat.delete_archived",
  request: (id) => ({
    method: "DELETE",
    path: `/api/chats/archived/${encodeURIComponent(id)}`,
  }),
  error: "Couldn't delete chat",
});

// --- chat.load_history ---

export const loadHistoryAction = defineAction<void, { chats: Array<{ id: string; name: string; summary?: string; updated_at: number }> }>({
  name: "chat.load_history",
  run: async (_args, signal) => {
    const r = await fetch("/api/chats/archived", { signal });
    if (!r.ok) {
      let serverError = "";
      try {
        const body = (await r.json()) as { error?: string };
        if (typeof body.error === "string") serverError = body.error;
      } catch { /* ignore */ }
      throw new ActionError(serverError || `HTTP ${String(r.status)}`, { status: r.status });
    }
    return (await r.json()) as { chats: Array<{ id: string; name: string; summary?: string; updated_at: number }> };
  },
  error: "Couldn't load chat history",
});

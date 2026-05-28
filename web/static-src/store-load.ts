// ---------------------------------------------------------------------------
// HTTP load operations for the session store — thin I/O shell.
// Pure state lives in store.ts; this module hydrates it from the server.
// Dependency is one-way: store-load.ts → store.ts (never reverse).
// ---------------------------------------------------------------------------

import type { Session, ChatHeader, Message } from "./types.js";
import { apiGetTyped } from "./api-client.js";
import { asObject, decodeArray, reqBool, type Decoder } from "./validators.js";
import { decodeChatHeader, decodeMessage } from "./wire/decoders.gen.js";
import { registerCleanup } from "./actions/index.js";
import {
  setSessions,
  get,
  getSessions,
  sessionsVersion,
  messagesVersion,
  rebuildMsgIndex,
} from "./store.js";

// --- Inline decoders ---
const decodeChatListResponseLocal: Decoder<{ chats?: ChatHeader[] }> = (v) => {
  const o = asObject(v, "$.chat_list");
  const out: { chats?: ChatHeader[] } = {};
  if (o["chats"] !== undefined) {
    out.chats = decodeArray(o["chats"], decodeChatHeader, "$.chat_list.chats");
  }
  return out;
};

const decodeChatGetResponseLocal: Decoder<{
  chat: ChatHeader;
  messages: Message[];
  has_more: boolean;
}> = (v) => {
  const o = asObject(v, "$.chat_get");
  return {
    chat: decodeChatHeader(o["chat"]),
    messages: decodeArray(o["messages"], decodeMessage, "$.chat_get.messages"),
    has_more: reqBool(o, "has_more", "$.chat_get"),
  };
};

// --- Abort controllers ---
let listController: AbortController | null = null;
const msgControllers = new Map<string, AbortController>();

registerCleanup(() => listController?.abort());
registerCleanup(() => {
  for (const c of msgControllers.values()) {
    c.abort();
  }
  msgControllers.clear();
});

// --- Helpers ---
function emitSessions(): void {
  sessionsVersion.value = sessionsVersion.peek() + 1;
}
function emitMessages(): void {
  messagesVersion.value = messagesVersion.peek() + 1;
}

// --- Load operations ---
export async function loadList(): Promise<boolean> {
  listController?.abort();
  const controller = new AbortController();
  listController = controller;

  const sessionIndex = new Map<string, Session>();
  for (const s of getSessions()) {
    sessionIndex.set(s.id, s);
  }
  const knownBefore = new Set(sessionIndex.keys());

  const d = await apiGetTyped("/api/chats", decodeChatListResponseLocal, controller.signal);
  if (controller.signal.aborted) {
    listController = null;
    return false;
  }
  if (d?.chats === undefined) {
    listController = null;
    return false;
  }
  const next: Session[] = [];
  for (const h of d.chats) {
    const existing = get(h.id);
    const parent_chat_id = h.parent_chat_id ?? existing?.parent_chat_id;
    const session: Session = {
      id: h.id,
      name: h.name,
      agent: h.agent ?? "",
      model: h.model ?? "",
      acp_session_id: h.acp_session_id ?? "",
      current_mode_id: h.current_mode_id ?? "",
      available_modes: h.available_modes ?? [],
      available_models: h.available_models ?? [],
      auto_approve_crew: h.auto_approve_crew ?? existing?.auto_approve_crew ?? false,
      supervised_mode: h.supervised_mode ?? false,
      pending_changes: existing?.pending_changes ?? [],
      usage: h.usage,
      message_count: h.message_count,
      messages: existing?.messages ?? [],
      has_more:
        existing !== undefined
          ? existing.has_more || h.message_count > existing.messages.length
          : h.message_count > 0,
      thinking: existing?.thinking ?? false,
      working_label: existing?.working_label ?? "Thinking",
      ...(parent_chat_id !== undefined && { parent_chat_id }),
      ...(existing?.prompt_queue !== undefined && { prompt_queue: existing.prompt_queue }),
      ...(existing?.trusted_this_turn !== undefined && {
        trusted_this_turn: existing.trusted_this_turn,
      }),
      ...(h.compaction_watermark !== undefined && { compaction_watermark: h.compaction_watermark }),
      ...(h.oldest_checkpoint_tag !== undefined && {
        oldest_checkpoint_tag: h.oldest_checkpoint_tag,
      }),
    };
    next.push(session);
  }
  // Preserve sessions added by SSE (upsertHeader) during the await.
  const currentSessions = getSessions();
  const currentIndex = new Map(currentSessions.map((s) => [s.id, s]));
  const nextIds = new Set(next.map((s) => s.id));
  for (const [id, s] of currentIndex) {
    if (!knownBefore.has(id) && !nextIds.has(id)) {
      next.push(s);
    }
  }
  setSessions(next);
  listController = null;
  emitSessions();
  return true;
}

export async function loadMessages(chatID: string, before?: number, limit = 50): Promise<boolean> {
  msgControllers.get(chatID)?.abort();
  const controller = new AbortController();
  msgControllers.set(chatID, controller);
  const params = new URLSearchParams({ limit: String(limit) });
  if (before !== undefined) {
    params.set("before", String(before));
  }
  const d = await apiGetTyped(
    `/api/chats/${encodeURIComponent(chatID)}?${params.toString()}`,
    decodeChatGetResponseLocal,
    controller.signal,
  );
  if (controller.signal.aborted) {
    msgControllers.delete(chatID);
    return false;
  }
  if (d === null) {
    msgControllers.delete(chatID);
    return false;
  }
  const session = get(chatID);
  if (session === undefined) {
    msgControllers.delete(chatID);
    return false;
  }
  if (before !== undefined) {
    session.messages = [...d.messages, ...session.messages];
  } else {
    session.messages = d.messages;
  }
  session.message_count = d.chat.message_count;
  session.has_more = d.has_more;
  rebuildMsgIndex(chatID, session.messages);
  msgControllers.delete(chatID);
  emitMessages();
  return true;
}

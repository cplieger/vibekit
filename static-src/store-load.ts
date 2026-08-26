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
  rebuildMsgIndex,
  emitMessages,
  normalizeMessage,
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
  draft: string;
}> = (v) => {
  const o = asObject(v, "$.chat_get");
  return {
    chat: decodeChatHeader(o["chat"]),
    messages: decodeArray(o["messages"], decodeMessage, "$.chat_get.messages"),
    has_more: reqBool(o, "has_more", "$.chat_get"),
    // `draft` rides this response rather than the header, so it stays off the
    // list endpoint and off every chat_updated frame. Optional-tolerant: a
    // server that predates the field, or a proxy that strips it, must not fail
    // the whole chat load.
    draft: typeof o["draft"] === "string" ? o["draft"] : "",
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
    const session: Session = {
      id: h.id,
      name: h.name,
      model: h.model ?? "",
      acp_session_id: h.acp_session_id ?? "",
      current_mode_id: h.current_mode_id ?? "",
      available_modes: h.available_modes ?? [],
      available_models: h.available_models ?? [],
      supervised_mode: h.supervised_mode ?? false,
      effort: h.effort ?? "",
      // Keep the client's live effort catalog when the header carries none: this
      // list endpoint rebuilds a Session from a header, and blanking the tiers
      // would empty the effort control for every chat on a refresh.
      effort_levels: h.effort_levels ?? existing?.effort_levels ?? [],
      effort_active: h.effort_active ?? existing?.effort_active ?? "",
      usage: h.usage,
      message_count: h.message_count,
      messages: existing?.messages ?? [],
      has_more:
        existing !== undefined
          ? existing.has_more || h.message_count > existing.messages.length
          : h.message_count > 0,
      thinking: existing?.thinking ?? false,
      working_label: existing?.working_label ?? "Thinking",
      ...(existing?.steers !== undefined && { steers: existing.steers }),
      // The promoted steers too, and this one is load-bearing rather than
      // symmetric: a mark's lifetime is the loaded TRANSCRIPT, not the turn, so
      // without it every reconnect would wipe the notes back out of turns the
      // reader can still see.
      ...(existing?.steer_marks !== undefined && { steer_marks: existing.steer_marks }),
      // Every CLIENT-ONLY projection carries over, not just steers. This list is
      // the header endpoint's blind spot: the server sends none of these fields,
      // so rebuilding a Session from a header alone silently resets them — and
      // `loadList` runs on EVERY `connected`, reconnects included, so an ordinary
      // network recovery repainted a failed tab as idle and dropped the agent's
      // declared status with it. The reconcile that IS entitled to drop them is
      // `transport:gap`, which clears them explicitly and runs first, so there is
      // nothing left here to preserve after a real replay gap.
      ...(existing?.turn_failed === true && { turn_failed: true as const }),
      ...(existing?.turn_done === true && { turn_done: true as const }),
      ...(existing?.agent_status !== undefined && { agent_status: existing.agent_status }),
      ...(existing?.agent_status_text !== undefined && {
        agent_status_text: existing.agent_status_text,
      }),
      ...(h.compaction_watermark !== undefined && { compaction_watermark: h.compaction_watermark }),
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
  return true;
}

export async function loadMessages(
  chatID: string,
  beforeID?: string,
  limit = 50,
): Promise<boolean> {
  msgControllers.get(chatID)?.abort();
  const controller = new AbortController();
  msgControllers.set(chatID, controller);
  const params = new URLSearchParams({ limit: String(limit) });
  if (beforeID !== undefined) {
    params.set("before_id", beforeID);
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
  if (beforeID !== undefined) {
    // Prepend older-page messages, deduped by id. The cursor is a message ID and
    // the server treats it as exclusive, so a boundary message cannot come back
    // twice the way the old millisecond cursor allowed. The id filter STAYS
    // anyway: it costs one Set, and it is what makes a re-issued or overlapping
    // page harmless rather than a double render that also corrupts the msg index.
    const seen = new Set(session.messages.map((m) => m.id));
    const older = d.messages.filter((m) => !seen.has(m.id)).map(normalizeMessage);
    session.messages = [...older, ...session.messages];
  } else {
    // Normalize replayed messages so legacy transcripts (persisted before the
    // blocks field) get synthesized blocks — the renderer is block-only.
    const fetched = d.messages.map(normalizeMessage);
    // Then re-adopt the local TAIL. The in-flight turn lives in the server's
    // in-memory assistant buffer and is flushed to the chat file once, at
    // turn_ended — so it is absent from this page while `turn_state` has already
    // put it in the store. A blind whole-array replace therefore DELETED the
    // reply the reader was watching, every time this ran mid-turn: on the boot
    // activation after a refresh, and on the gap handler's heal.
    //
    // The anchor is the newest message the page DOES carry. Everything local
    // after it that the page does not have is the tail; everything before it is
    // the page's own business (which is what keeps a scrolled-up window from
    // being re-appended out of order). A page whose last id is not local at all
    // — the refresh case, where the only local message is the streaming one —
    // anchors at 0 and keeps whatever the page omits.
    const fetchedIDs = new Set(fetched.map((m) => m.id));
    const lastID = fetched.at(-1)?.id;
    const anchor =
      lastID === undefined ? 0 : session.messages.findIndex((m) => m.id === lastID) + 1;
    const tail = session.messages.slice(anchor).filter((m) => !fetchedIDs.has(m.id));
    session.messages = tail.length === 0 ? fetched : [...fetched, ...tail];
  }
  session.message_count = d.chat.message_count;
  session.has_more = d.has_more;
  rebuildMsgIndex(chatID, session.messages);
  msgControllers.delete(chatID);
  // Park the server's draft on the session so the composer can adopt it. Only on
  // the newest page: an older page fetch is a scroll-up, not an open. This module
  // deliberately does not reach into the composer — chat.ts owns that call, right
  // where it already sequences the rest of the activation.
  if (beforeID === undefined) {
    session.draft = d.draft;
  }
  emitMessages();
  return true;
}

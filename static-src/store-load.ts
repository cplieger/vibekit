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
  bumpMessages,
  normalizeMessage,
  liveTurnMessage,
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
      // Residency describes the carried-over `messages` window, so it travels
      // with it: dropping it here would make every reconnect read a loaded
      // chat as never-loaded (or an evicted one as fresh).
      ...(existing?.residency !== undefined && { residency: existing.residency }),
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
  // The ids present BEFORE the request goes out. The server computes its answer
  // from the chat file when the handler runs, so anything that arrives while the
  // request is in flight is NEWER than that answer and the answer is not entitled
  // to drop it — a plan or event message persisted and broadcast inside that
  // window would otherwise vanish from the transcript until the next fetch.
  const knownBefore = new Set((get(chatID)?.messages ?? []).map((m) => m.id));
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
    // Then re-adopt what this page CANNOT know about. A blind whole-array
    // replace deleted the reply the reader was watching on every mid-turn
    // refetch: a tab switch, the boot activation after a refresh, and the gap
    // handler's own heal.
    //
    // Exactly two things qualify, and neither is decided by position:
    //
    //  - The in-flight turn. The server accumulates it in an in-memory buffer
    //    and appends it to the chat file once, at turn_ended, so the page cannot
    //    carry it and the store's own marker is what names it. The rule this
    //    replaces kept "everything after the newest id the page carries", and
    //    that boundary is wrong because the agent persists messages DURING a
    //    turn — every plan update, a compaction or safety event, the cancel
    //    badge — each landing after the streaming reply locally while sitting
    //    inside the page. One plan update stepped the boundary past the reply,
    //    and the replace deleted it; the reader saw their own prompt with an
    //    empty turn body until a reload, by which time the buffer had flushed.
    //
    //  - A message that arrived while the request was in flight, which is newer
    //    than the answer being applied.
    //
    // Both go at the END, which is where the server puts the finished turn too:
    // persistTurn appends it after anything persisted during it. Everything else
    // the page omits is the page's own business — older history it deliberately
    // left out, which re-appending would reorder.
    const fetchedIDs = new Set(fetched.map((m) => m.id));
    const liveID = liveTurnMessage(chatID);
    const kept = session.messages.filter(
      (m) => !fetchedIDs.has(m.id) && (m.id === liveID || !knownBefore.has(m.id)),
    );
    session.messages = kept.length === 0 ? fetched : [...fetched, ...kept];
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
    // A successful newest-page load is the ONE writer of `loaded`: the window
    // is now the server's answer, so an activation may trust it. An older-page
    // prepend extends an already-trusted window and asserts nothing new.
    session.residency = "loaded";
  }
  bumpMessages(chatID);
  return true;
}

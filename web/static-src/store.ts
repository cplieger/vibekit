// ---------------------------------------------------------------------------
// Client-side session store — signals-based rewrite.
//
// State lives in module-level data structures with O(1) indexing.
// A single `version` signal triggers reactive effects on mutation.
// Streaming paths use batch() for MessageChannel-deferred rendering.
// User-initiated mutations bump version synchronously for instant feedback.
// ---------------------------------------------------------------------------

import type {
  Session, ChatHeader, Message, Usage, ToolCall, PendingChange,
} from "./types.js";
import { apiGetTyped } from "./api-client.js";
import { asObject, decodeArray, reqBool, type Decoder } from "./validators.js";
import { decodeChatHeader, decodeMessage } from "./wire/decoders.gen.js";
import { signal, batch } from "./signals.js";
import { registerCleanup } from "./actions/cleanup.js";

// --- Reactive version counter: effects that read this re-run on mutation ---
export const version = signal(0);

function emit(): void { version.value = version.peek() + 1; }
function scheduleRender(): void { batch(() => { version.value = version.peek() + 1; }); }

// --- Model context sizes ---
export const MODEL_CONTEXT_SIZES: Record<string, number> = {};

export function parseContextSize(description: string): number | undefined {
  const m = /(\d+)\s*[Kk]\s*context/i.exec(description);
  if (m?.[1] !== undefined) return parseInt(m[1], 10) * 1_000;
  const m2 = /(\d+)\s*[Mm]\s*context/i.exec(description);
  if (m2?.[1] !== undefined) return parseInt(m2[1], 10) * 1_000_000;
  if (/\b1M\b/.test(description)) return 1_000_000;
  return undefined;
}

// --- State ---
let _sessions: Session[] = [];
let _activeId = "";
let sessionIndex = new Map<string, Session>();
const msgIndex = new Map<string, Map<string, number>>();
const _queuedAttachments = new Map<string, unknown[][]>();
let listController: AbortController | null = null;
const msgControllers = new Map<string, AbortController>();

// Abort any in-flight chat-list / message fetches when the page unloads.
// One sweep aborts the active list fetch + every per-chat message stream.
registerCleanup(() => listController?.abort());
registerCleanup(() => {
  for (const c of msgControllers.values()) c.abort();
  msgControllers.clear();
});

// --- Accessors ---
export function getSessions(): Session[] { return _sessions; }
export function getActiveId(): string { return _activeId; }
export function getActive(): Session | undefined { return sessionIndex.get(_activeId); }
export function get(id: string): Session | undefined { return sessionIndex.get(id); }

export function setSessions(v: Session[]): void {
  _sessions = v;
  sessionIndex = new Map(v.map((s) => [s.id, s]));
  msgIndex.clear();
}

export function setActive(id: string): void {
  if (_activeId === id) return;
  _activeId = id;
  emit();
}

export function isThinking(id: string): boolean {
  return get(id)?.thinking ?? false;
}

export function setThinking(id: string, v: boolean): void {
  const s = get(id);
  if (s === undefined) return;
  s.thinking = v;
  if (!v) s.working_label = "Thinking";
  emit();
}

export function setWorkingLabel(id: string, label: string): void {
  const s = get(id);
  if (s === undefined) return;
  s.working_label = label;
  scheduleRender();
}

export function queuedPrompt(id: string): string | undefined {
  const q = get(id)?.prompt_queue;
  if (q === undefined || q.length === 0) return undefined;
  return q[0];
}

export function enqueuePrompt(id: string, text: string, attachments?: readonly unknown[]): void {
  const s = get(id);
  if (s === undefined) return;
  if (s.prompt_queue === undefined) s.prompt_queue = [];
  s.prompt_queue.push(text);
  if (attachments !== undefined && attachments.length > 0) {
    if (!_queuedAttachments.has(id)) _queuedAttachments.set(id, []);
    _queuedAttachments.get(id)!.push([...attachments]);
  } else {
    if (!_queuedAttachments.has(id)) _queuedAttachments.set(id, []);
    _queuedAttachments.get(id)!.push([]);
  }
  emit();
}

export function dequeuePrompt(id: string): string | undefined {
  const s = get(id);
  if (s === undefined) return undefined;
  const q = s.prompt_queue;
  if (q === undefined || q.length === 0) return undefined;
  const next = q.shift();
  if (q.length === 0) delete s.prompt_queue;
  // Also shift attachments (consumed via dequeuePromptAttachments or discarded).
  const aq = _queuedAttachments.get(id);
  if (aq !== undefined) {
    aq.shift();
    if (aq.length === 0) _queuedAttachments.delete(id);
  }
  emit();
  return next;
}

/** Dequeue the attachments for the next queued prompt (peek without removing — 
 *  call before dequeuePrompt to capture them). */
export function peekQueuedAttachments(id: string): readonly unknown[] {
  const aq = _queuedAttachments.get(id);
  if (aq === undefined || aq.length === 0) return [];
  return aq[0]!;
}

/** Replace attachments on the last queued entry (used when the action
 *  enqueued text-only and the caller needs to attach files after). */
export function setLastQueuedAttachments(id: string, attachments: readonly unknown[]): void {
  const aq = _queuedAttachments.get(id);
  if (aq === undefined || aq.length === 0) return;
  aq[aq.length - 1] = [...attachments];
}

export function setQueuedPrompt(id: string, text: string | undefined): void {
  const s = get(id);
  if (s === undefined) return;
  if (text === undefined) { delete s.prompt_queue; _queuedAttachments.delete(id); }
  else { s.prompt_queue = [text]; _queuedAttachments.set(id, [[]]); }
  emit();
}

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

// --- Index helpers ---
export function clearMsgIndex(sessionID: string): void { msgIndex.delete(sessionID); }

/** Invalidate a background session's cache so the next switch refetches. */
export function invalidateSession(chatID: string): void {
  const s = get(chatID);
  if (s === undefined) return;
  s.messages = [];
  s.has_more = false;
  clearMsgIndex(chatID);
  emit();
}

function rebuildMsgIndex(sessionID: string, messages: Message[]): void {
  const idx = new Map<string, number>();
  for (let i = 0; i < messages.length; i++) idx.set(messages[i]!.id, i);
  msgIndex.set(sessionID, idx);
}

function getMsgIndex(sessionID: string, messages: Message[]): Map<string, number> {
  let mi = msgIndex.get(sessionID);
  if (mi === undefined) {
    mi = new Map<string, number>();
    for (let i = 0; i < messages.length; i++) mi.set(messages[i]!.id, i);
    msgIndex.set(sessionID, mi);
  }
  return mi;
}

// --- Load operations ---
export async function loadList(): Promise<boolean> {
  listController?.abort();
  const controller = new AbortController();
  listController = controller;
  const knownBefore = new Set(sessionIndex.keys());
  const d = await apiGetTyped("/api/chats", decodeChatListResponseLocal, controller.signal);
  if (controller.signal.aborted) return false;
  if (d === null || d.chats === undefined) return false;
  const next: Session[] = [];
  for (const h of d.chats) {
    const existing = get(h.id);
    const frozen = h.frozen ?? existing?.frozen;
    const is_tangent = h.is_tangent ?? existing?.is_tangent;
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
      available_commands: existing?.available_commands ?? [],
      available_prompts: existing?.available_prompts ?? [],
      auto_approve_crew: h.auto_approve_crew ?? existing?.auto_approve_crew ?? false,
      supervised_mode: h.supervised_mode ?? false,
      pending_changes: existing?.pending_changes ?? [],
      usage: h.usage,
      message_count: h.message_count,
      messages: existing?.messages ?? [],
      has_more: existing !== undefined ? (existing.has_more || h.message_count > existing.messages.length) : h.message_count > 0,
      thinking: existing?.thinking ?? false,
      working_label: existing?.working_label ?? "Thinking",
      ...(frozen !== undefined && { frozen }),
      ...(is_tangent !== undefined && { is_tangent }),
      ...(parent_chat_id !== undefined && { parent_chat_id }),
      ...(existing?.prompt_queue !== undefined && { prompt_queue: existing.prompt_queue }),
      ...(existing?.trusted_this_turn !== undefined && { trusted_this_turn: existing.trusted_this_turn }),
      ...(h.compaction_watermark !== undefined && { compaction_watermark: h.compaction_watermark }),
      ...(h.oldest_checkpoint_tag !== undefined && { oldest_checkpoint_tag: h.oldest_checkpoint_tag }),
    };
    next.push(session);
  }
  // Preserve sessions added by SSE (upsertHeader) during the await.
  const nextIds = new Set(next.map((s) => s.id));
  for (const [id, s] of sessionIndex) {
    if (!knownBefore.has(id) && !nextIds.has(id)) next.push(s);
  }
  setSessions(next);
  emit();
  return true;
}

export async function loadMessages(chatID: string, before?: number, limit = 50): Promise<boolean> {
  msgControllers.get(chatID)?.abort();
  const controller = new AbortController();
  msgControllers.set(chatID, controller);
  const params = new URLSearchParams({ limit: String(limit) });
  if (before !== undefined) params.set("before", String(before));
  const d = await apiGetTyped(
    `/api/chats/${encodeURIComponent(chatID)}?${params.toString()}`,
    decodeChatGetResponseLocal,
    controller.signal,
  );
  if (controller.signal.aborted) return false;
  if (d === null) return false;
  const session = get(chatID);
  if (session === undefined) return false;
  if (before !== undefined) {
    session.messages = [...d.messages, ...session.messages];
  } else {
    session.messages = d.messages;
  }
  session.message_count = d.chat.message_count;
  session.has_more = d.has_more;
  rebuildMsgIndex(chatID, session.messages);
  emit();
  return true;
}

// --- SSE-driven mutations ---
export function upsertHeader(h: ChatHeader): void {
  const existing = get(h.id);
  if (existing !== undefined) {
    existing.name = h.name;
    existing.agent = h.agent ?? "";
    existing.model = h.model ?? "";
    existing.acp_session_id = h.acp_session_id ?? "";
    existing.current_mode_id = h.current_mode_id ?? "";
    existing.available_modes = h.available_modes ?? [];
    existing.available_models = h.available_models ?? [];
    existing.supervised_mode = h.supervised_mode ?? false;
    if (h.auto_approve_crew !== undefined) existing.auto_approve_crew = h.auto_approve_crew;
    existing.usage = h.usage;
    existing.message_count = h.message_count;
    if (h.compaction_watermark !== undefined) existing.compaction_watermark = h.compaction_watermark;
    else delete existing.compaction_watermark;
    if (h.oldest_checkpoint_tag !== undefined) existing.oldest_checkpoint_tag = h.oldest_checkpoint_tag;
    else delete existing.oldest_checkpoint_tag;
    if (h.frozen !== undefined) existing.frozen = h.frozen; else delete existing.frozen;
    if (h.is_tangent !== undefined) existing.is_tangent = h.is_tangent; else delete existing.is_tangent;
    if (h.parent_chat_id !== undefined) existing.parent_chat_id = h.parent_chat_id; else delete existing.parent_chat_id;
  } else {
    const s: Session = {
      id: h.id, name: h.name, agent: h.agent ?? "", model: h.model ?? "",
      acp_session_id: h.acp_session_id ?? "", current_mode_id: h.current_mode_id ?? "",
      available_modes: h.available_modes ?? [], available_models: h.available_models ?? [],
      available_commands: [], available_prompts: [],
      auto_approve_crew: h.auto_approve_crew ?? false, supervised_mode: h.supervised_mode ?? false,
      pending_changes: [], usage: h.usage, message_count: h.message_count,
      messages: [], has_more: h.message_count > 0, thinking: false, working_label: "Thinking",
      ...(h.frozen !== undefined && { frozen: h.frozen }),
      ...(h.is_tangent !== undefined && { is_tangent: h.is_tangent }),
      ...(h.parent_chat_id !== undefined && { parent_chat_id: h.parent_chat_id }),
    };
    if (h.compaction_watermark !== undefined) s.compaction_watermark = h.compaction_watermark;
    if (h.oldest_checkpoint_tag !== undefined) s.oldest_checkpoint_tag = h.oldest_checkpoint_tag;
    _sessions.unshift(s);
    sessionIndex.set(s.id, s);
  }
  emit();
}

export function setAvailableCommands(
  id: string, commands: Session["available_commands"], prompts?: Session["available_prompts"],
): void {
  const s = get(id);
  if (s === undefined) return;
  s.available_commands = commands;
  s.available_prompts = prompts ?? [];
  emit();
}

export function setCurrentMode(id: string, modeID: string): void {
  const s = get(id);
  if (s === undefined) return;
  if (s.current_mode_id === modeID) return;
  s.current_mode_id = modeID;
  emit();
}

export function removeChat(id: string): void {
  const idx = _sessions.findIndex((s) => s.id === id);
  if (idx === -1) return;
  _sessions.splice(idx, 1);
  sessionIndex.delete(id);
  msgIndex.delete(id);
  _queuedAttachments.delete(id);
  if (_activeId === id) _activeId = _sessions[0]?.id ?? "";
  emit();
}

/** Re-insert a previously-removed session at a specific index (or at
 *  the head if no index given). Used by optimistic action rollbacks
 *  that captured the session before removeChat() and need to put it
 *  back on failure. Idempotent: if a session with the same id already
 *  exists, the existing entry is preserved. */
export function reinsertSession(session: Session, atIndex?: number): void {
  if (sessionIndex.has(session.id)) return;
  const target = atIndex !== undefined ? Math.max(0, Math.min(atIndex, _sessions.length)) : 0;
  _sessions.splice(target, 0, session);
  sessionIndex.set(session.id, session);
  emit();
}

export function appendMessage(chatID: string, msg: Message): void {
  const s = get(chatID);
  if (s === undefined) return;
  const mi = getMsgIndex(chatID, s.messages);
  if (mi.has(msg.id)) return;
  const newIdx = s.messages.length;
  mi.set(msg.id, newIdx);
  s.messages.push(msg);
  s.message_count = Math.max(s.message_count, s.messages.length);
  emit();
}

export function upsertMessage(chatID: string, msg: Message): void {
  const s = get(chatID);
  if (s === undefined) return;
  const mi = getMsgIndex(chatID, s.messages);
  const idx = mi.get(msg.id) ?? -1;
  if (idx === -1) {
    const newIdx = s.messages.length;
    s.messages.push(msg);
    s.message_count = Math.max(s.message_count, s.messages.length);
    mi.set(msg.id, newIdx);
  } else {
    s.messages[idx] = msg;
  }
  emit();
}

export function addPendingChange(chatID: string, change: PendingChange): void {
  const s = get(chatID);
  if (s === undefined) return;
  if (s.pending_changes.some((p) => p.tool_call_id === change.tool_call_id)) return;
  s.pending_changes = [...s.pending_changes, change];
  emit();
}

export function removePendingChange(chatID: string, toolCallID: string): void {
  const s = get(chatID);
  if (s === undefined) return;
  const next = s.pending_changes.filter((p) => p.tool_call_id !== toolCallID);
  if (next.length === s.pending_changes.length) return;
  s.pending_changes = next;
  emit();
}

export function clearPendingChanges(chatID: string): void {
  const s = get(chatID);
  if (s === undefined || s.pending_changes.length === 0) return;
  s.pending_changes = [];
  emit();
}

export function setSupervisedMode(chatID: string, enabled: boolean): void {
  const s = get(chatID);
  if (s === undefined || s.supervised_mode === enabled) return;
  s.supervised_mode = enabled;
  emit();
}

export function setAutoApproveCrew(chatID: string, enabled: boolean): void {
  const s = get(chatID);
  if (s === undefined || s.auto_approve_crew === enabled) return;
  s.auto_approve_crew = enabled;
  emit();
}

export function setFrozen(chatID: string, frozen: boolean): void {
  const s = get(chatID);
  if (s === undefined) return;
  if (frozen) { s.frozen = true; } else { delete s.frozen; }
  emit();
}

/** Set session model and notify subscribers. Used by switchModelAction. */
export function setModel(chatID: string, model: string): void {
  const s = get(chatID);
  if (s === undefined) return;
  s.model = model;
  emit();
}

/** Set session name and notify subscribers. */
export function setName(chatID: string, name: string): void {
  const s = get(chatID);
  if (s === undefined) return;
  s.name = name;
  emit();
}

/** Return the current index of a session in the list, or -1. */
export function indexOfSession(id: string): number {
  return _sessions.findIndex((s) => s.id === id);
}

export function setTrustedThisTurn(chatID: string, trusted: boolean): void {
  const s = get(chatID);
  if (s === undefined) return;
  const current = s.trusted_this_turn === true;
  if (current === trusted) return;
  s.trusted_this_turn = trusted;
  emit();
}

export function appendChunk(chatID: string, messageID: string, delta: string): void {
  const s = get(chatID);
  if (s === undefined) return;
  const mi = getMsgIndex(chatID, s.messages);
  const idx = mi.get(messageID) ?? -1;
  let msg: Message | undefined = idx !== -1 ? s.messages[idx] : undefined;
  if (msg === undefined) {
    msg = { id: messageID, role: "assistant", ts: Date.now(), content: "" };
    const newIdx = s.messages.length;
    s.messages.push(msg);
    s.message_count = Math.max(s.message_count, s.messages.length);
    mi.set(messageID, newIdx);
  }
  msg.content = (msg.content ?? "") + delta;
  scheduleRender();
}

export function upsertToolCall(chatID: string, messageID: string, call: ToolCall): void {
  const s = get(chatID);
  if (s === undefined) return;
  const mi = getMsgIndex(chatID, s.messages);
  const idx = mi.get(messageID) ?? -1;
  let msg: Message | undefined = idx !== -1 ? s.messages[idx] : undefined;
  if (msg === undefined) {
    msg = { id: messageID, role: "assistant", ts: Date.now(), content: "", tool_calls: [call] };
    const newIdx = s.messages.length;
    s.messages.push(msg);
    s.message_count = Math.max(s.message_count, s.messages.length);
    mi.set(messageID, newIdx);
    emit();
    return;
  }
  if (msg.tool_calls === undefined) msg.tool_calls = [];
  const tcIdx = msg.tool_calls.findIndex((tc) => tc.id === call.id);
  if (tcIdx === -1) msg.tool_calls.push(call);
  else msg.tool_calls[tcIdx] = call;
  scheduleRender();
}

// --- Utilities ---
export function contextSizeFor(modelID: string): number {
  return MODEL_CONTEXT_SIZES[modelID] ?? 0;
}

export function defaultUsage(): Usage {
  return {
    context_pct: 0, context_size: 0, credits: 0,
    turn_count: 0, last_turn_ms: 0, has_real_data: false,
  };
}



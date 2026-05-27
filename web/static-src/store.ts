// ---------------------------------------------------------------------------
// Client-side session store — signals-based rewrite.
//
// State lives in module-level data structures with O(1) indexing.
// A single `version` signal triggers reactive effects on mutation.
// Streaming paths use batch() for MessageChannel-deferred rendering.
// User-initiated mutations bump version synchronously for instant feedback.
// ---------------------------------------------------------------------------

import type {
  Session, ChatHeader, Message, Usage, ToolCall, PendingChange, Crew,
} from "./types.js";
import { apiGetTyped } from "./api-client.js";
import { asObject, decodeArray, reqBool, type Decoder } from "./validators.js";
import { decodeChatHeader, decodeMessage } from "./wire/decoders.gen.js";
import { signal, batch } from "./signals.js";
import { registerCleanup } from "./actions/index.js";

// --- Reactive version counter: effects that read this re-run on mutation ---
export const version = signal(0);

/** Per-message-id streaming text signal. Created lazily on first chunk
 *  when a message is already mounted (so reconcile doesn't re-walk the
 *  whole list every chunk). The streaming bubble in messages.ts
 *  subscribes to its own signal via effect(). The signal is removed
 *  on finalize / chat-switch. */
const streamingTextSig = new Map<string, ReturnType<typeof signal<string>>>();

/** Per-message-id reasoning signal. Sibling of streamingTextSig: the
 *  reasoning details element subscribes to this. Reasoning chunks
 *  (chunk.is_reasoning=true) update this signal; content chunks update
 *  streamingTextSig. Both grow in parallel during extended-thinking
 *  turns. */
const streamingReasoningSig = new Map<string, ReturnType<typeof signal<string>>>();

/** Per-tool-call signal. Tool-card mounts subscribe to their own signal
 *  via effect(); status / output / diff updates bypass the global
 *  reconcile loop entirely. Created on first upsert when the tool's
 *  parent message is already mounted. */
const toolCallSig = new Map<string, ReturnType<typeof signal<ToolCall>>>();

/** Per-crew-message signal. Crew event messages mount subscribes to
 *  this; subsequent kiro-cli list_update snapshots fan out via the
 *  signal without re-running the global reconcile loop. Created on
 *  first mount in messages.ts. */
const crewSig = new Map<string, ReturnType<typeof signal<Crew>>>();

export function getStreamingSig(messageID: string): ReturnType<typeof signal<string>> | undefined {
  return streamingTextSig.get(messageID);
}

export function getReasoningSig(messageID: string): ReturnType<typeof signal<string>> | undefined {
  return streamingReasoningSig.get(messageID);
}

export function getToolCallSig(toolID: string): ReturnType<typeof signal<ToolCall>> | undefined {
  return toolCallSig.get(toolID);
}

export function getCrewSig(messageID: string): ReturnType<typeof signal<Crew>> | undefined {
  return crewSig.get(messageID);
}

/** Get or create the content signal for a message id. Initialized to the
 *  current full content. */
export function ensureStreamingSig(messageID: string, initial: string): ReturnType<typeof signal<string>> {
  let sig = streamingTextSig.get(messageID);
  if (sig === undefined) {
    sig = signal(initial);
    streamingTextSig.set(messageID, sig);
  }
  return sig;
}

/** Get or create the reasoning signal for a message id. Initialized to
 *  the current full reasoning. */
export function ensureReasoningSig(messageID: string, initial: string): ReturnType<typeof signal<string>> {
  let sig = streamingReasoningSig.get(messageID);
  if (sig === undefined) {
    sig = signal(initial);
    streamingReasoningSig.set(messageID, sig);
  }
  return sig;
}

/** Get or create the signal for a tool call id. Initialized to the
 *  current ToolCall snapshot. */
export function ensureToolCallSig(toolID: string, initial: ToolCall): ReturnType<typeof signal<ToolCall>> {
  let sig = toolCallSig.get(toolID);
  if (sig === undefined) {
    sig = signal(initial);
    toolCallSig.set(toolID, sig);
  }
  return sig;
}

/** Get or create the signal for a crew event message id. Initialized
 *  to the current Crew snapshot. */
export function ensureCrewSig(messageID: string, initial: Crew): ReturnType<typeof signal<Crew>> {
  let sig = crewSig.get(messageID);
  if (sig === undefined) {
    sig = signal(initial);
    crewSig.set(messageID, sig);
  }
  return sig;
}

/** Remove the content signal for a message id. */
export function clearStreamingSig(messageID: string): void {
  streamingTextSig.delete(messageID);
}

/** Remove the reasoning signal for a message id. */
export function clearReasoningSig(messageID: string): void {
  streamingReasoningSig.delete(messageID);
}

/** Remove the per-tool signal. Called when the tool's parent message
 *  unmounts or on chat switch. */
export function clearToolCallSig(toolID: string): void {
  toolCallSig.delete(toolID);
}

/** Remove the per-crew signal. Called when the crew event message
 *  unmounts or on chat switch. */
export function clearCrewSig(messageID: string): void {
  crewSig.delete(messageID);
}

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
  if (controller.signal.aborted) { listController = null; return false; }
  if (d === null || d.chats === undefined) { listController = null; return false; }
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
      has_more: existing !== undefined ? (existing.has_more || h.message_count > existing.messages.length) : h.message_count > 0,
      thinking: existing?.thinking ?? false,
      working_label: existing?.working_label ?? "Thinking",
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
  listController = null;
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
  if (controller.signal.aborted) { msgControllers.delete(chatID); return false; }
  if (d === null) { msgControllers.delete(chatID); return false; }
  const session = get(chatID);
  if (session === undefined) { msgControllers.delete(chatID); return false; }
  if (before !== undefined) {
    session.messages = [...d.messages, ...session.messages];
  } else {
    session.messages = d.messages;
  }
  session.message_count = d.chat.message_count;
  session.has_more = d.has_more;
  rebuildMsgIndex(chatID, session.messages);
  msgControllers.delete(chatID);
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
    if (h.parent_chat_id !== undefined) existing.parent_chat_id = h.parent_chat_id; else delete existing.parent_chat_id;
  } else {
    const s: Session = {
      id: h.id, name: h.name, agent: h.agent ?? "", model: h.model ?? "",
      acp_session_id: h.acp_session_id ?? "", current_mode_id: h.current_mode_id ?? "",
      available_modes: h.available_modes ?? [], available_models: h.available_models ?? [],
      auto_approve_crew: h.auto_approve_crew ?? false, supervised_mode: h.supervised_mode ?? false,
      pending_changes: [], usage: h.usage, message_count: h.message_count,
      messages: [], has_more: h.message_count > 0, thinking: false, working_label: "Thinking",
      ...(h.parent_chat_id !== undefined && { parent_chat_id: h.parent_chat_id }),
    };
    if (h.compaction_watermark !== undefined) s.compaction_watermark = h.compaction_watermark;
    if (h.oldest_checkpoint_tag !== undefined) s.oldest_checkpoint_tag = h.oldest_checkpoint_tag;
    _sessions.unshift(s);
    sessionIndex.set(s.id, s);
  }
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
    emit();
    return;
  }
  s.messages[idx] = msg;
  // Crew-only update: fan out via the per-crew signal so the card
  // re-renders without walking the entire message list. Falls back to
  // emit() if the crew message hasn't been mounted yet (rare; the
  // mount creates the signal so subsequent updates flow through it).
  if (msg.event_kind === "crew" && msg.crew !== undefined) {
    const sig = crewSig.get(msg.id);
    if (sig !== undefined) {
      sig.value = msg.crew;
      return;
    }
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

/** Set session model and notify subscribers. Used by switchModel. */
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

export function appendChunk(chatID: string, messageID: string, delta: string, isReasoning: boolean): void {
  const s = get(chatID);
  if (s === undefined) return;
  const mi = getMsgIndex(chatID, s.messages);
  const idx = mi.get(messageID) ?? -1;
  let msg: Message | undefined = idx !== -1 ? s.messages[idx] : undefined;
  let isNew = false;
  if (msg === undefined) {
    msg = { id: messageID, role: "assistant", ts: Date.now(), content: "" };
    const newIdx = s.messages.length;
    s.messages.push(msg);
    s.message_count = Math.max(s.message_count, s.messages.length);
    mi.set(messageID, newIdx);
    isNew = true;
  }
  if (isReasoning) msg.reasoning = (msg.reasoning ?? "") + delta;
  else msg.content = (msg.content ?? "") + delta;

  // First chunk for this message: bump global so reconcile can mount
  // the new bubble. Subsequent chunks fan out via per-message signal.
  if (isNew) {
    scheduleRender();
    return;
  }
  if (isReasoning) {
    const sig = streamingReasoningSig.get(messageID);
    if (sig !== undefined) sig.value = msg.reasoning ?? "";
    else scheduleRender();
  } else {
    const sig = streamingTextSig.get(messageID);
    if (sig !== undefined) sig.value = msg.content ?? "";
    else scheduleRender();
  }
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
  if (tcIdx === -1) {
    msg.tool_calls.push(call);
    // New tool added to existing message — bump global so reconcile
    // mounts it. The signal is created on mount (see toolSpec.mount).
    scheduleRender();
    return;
  }
  msg.tool_calls[tcIdx] = call;
  // Existing tool updated — fan out via per-tool signal directly.
  // This bypasses the global reconcile loop so a 50-tool message
  // doesn't walk all 50 on every status flip. Falls back to
  // scheduleRender if no signal exists yet (e.g. the mount hasn't
  // subscribed yet because reconcile is still pending).
  const sig = toolCallSig.get(call.id);
  if (sig !== undefined) sig.value = call;
  else scheduleRender();
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



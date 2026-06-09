// ---------------------------------------------------------------------------
// Client-side session store.
//
// Sessions live in a createCollection<Session> (ordered, keyed by id, with a
// per-session signal + a structure/order signal). `activeId` is a signal;
// `activeSession` is a computed that tracks the active session's per-entity
// signal — subscribers read it to react only to active-session changes.
//
// MESSAGES are deliberately NOT a collection: each session owns a Message[]
// with sub-message (block/tool/crew) streaming signals in store-signals.ts,
// finer-grained than a per-message signal. The messages renderer subscribes
// to `messagesVersion` (coarse "list shape changed") while the SignalMaps do
// the per-block/per-tool/per-crew fine-grained work. Streaming paths coalesce
// renders via scheduleMessages (queueMicrotask).
// ---------------------------------------------------------------------------

import type { Session, ChatHeader, Message, Usage, ToolCall, PendingChange } from "./types.js";
import { signal, computed, createCollection } from "@cplieger/reactive";
import {
  streamingTextSigs,
  streamingReasoningSigs,
  blockTextSigs,
  blockThinkingSigs,
  blockKey,
  toolCallSigs,
  crewSigs,
} from "./store-signals.js";

// --- Messages reactivity: the renderer + task-list subscribe to this ---
/** Message list changes: append, upsert (non-streaming), tool calls, new block. */
export const messagesVersion = signal(0);

export function emitMessages(): void {
  messagesVersion.value = messagesVersion.peek() + 1;
}
let messagesScheduled = false;
function scheduleMessages(): void {
  // Coalesce multiple new-block/new-tool events arriving in one tick into a
  // single render on the next microtask. (The package's batch() flushes
  // synchronously, so the microtask deferral is owned here.)
  if (messagesScheduled) {
    return;
  }
  messagesScheduled = true;
  queueMicrotask(() => {
    messagesScheduled = false;
    messagesVersion.value = messagesVersion.peek() + 1;
  });
}

// --- Model context sizes ---
export const MODEL_CONTEXT_SIZES: Record<string, number> = {};

export function parseContextSize(description: string): number | undefined {
  const m = /(\d+)\s*[Kk]\s*context/i.exec(description);
  if (m?.[1] !== undefined) {
    return parseInt(m[1], 10) * 1_000;
  }
  const m2 = /(\d+)\s*[Mm]\s*context/i.exec(description);
  if (m2?.[1] !== undefined) {
    return parseInt(m2[1], 10) * 1_000_000;
  }
  if (/\b1M\b/.test(description)) {
    return 1_000_000;
  }
  return undefined;
}

// --- Session state: the collection is the single source of truth ---
/** Ordered keyed collection of sessions. Structure ops fire `sessions.ids`;
 *  per-session field writes (via collection.update) fire `signalFor(id)`.
 *  Module-private: consumers go through the typed accessors/mutators below
 *  and the `activeSession` computed. */
const sessions = createCollection<Session>((s) => s.id);
/** The active chat id. */
const activeId = signal("");
/** Active session, tracking the active id AND the active session's signal.
 *  Active-session UI subscribers read `activeSession.value` to re-render only
 *  when the active session (or which session is active) changes. */
export const activeSession = computed<Session | undefined>(() => {
  void sessions.ids.value; // also re-derive on structural changes (add/remove/setAll)
  const id = activeId.value;
  return id === "" ? undefined : sessions.signalFor(id)?.value;
});

const msgIndex = new Map<string, Map<string, number>>();
const _queuedAttachments = new Map<string, unknown[][]>();

// --- Accessors ---
export function getSessions(): Session[] {
  return sessions.items();
}
export function getActiveId(): string {
  return activeId.peek();
}
export function getActive(): Session | undefined {
  const id = activeId.peek();
  return id === "" ? undefined : sessions.get(id);
}
export function get(id: string): Session | undefined {
  return sessions.get(id);
}

export function setSessions(v: Session[]): void {
  sessions.setAll(v);
  msgIndex.clear();
}

export function setActive(id: string): void {
  if (activeId.peek() === id) {
    return;
  }
  // Setting activeId re-derives `activeSession` (and the messages renderer,
  // which tracks activeSession, repaints the new chat's #messages). Without
  // this the renderer would keep the previous chat's DOM until something else
  // bumped messagesVersion.
  activeId.value = id;
}

export function isThinking(id: string): boolean {
  return get(id)?.thinking ?? false;
}

export function setThinking(id: string, v: boolean): void {
  sessions.update(id, (s) => ({
    ...s,
    thinking: v,
    working_label: v ? s.working_label : "Thinking",
  }));
}

export function setWorkingLabel(id: string, label: string): void {
  sessions.update(id, (s) => ({ ...s, working_label: label }));
}

export function queuedPrompt(id: string): string | undefined {
  const q = get(id)?.prompt_queue;
  if (q === undefined || q.length === 0) {
    return undefined;
  }
  return q[0];
}

export function enqueuePrompt(id: string, text: string, attachments?: readonly unknown[]): void {
  const s = get(id);
  if (s === undefined) {
    return;
  }
  const queue = [...(s.prompt_queue ?? []), text];
  if (!_queuedAttachments.has(id)) {
    _queuedAttachments.set(id, []);
  }
  // eslint-disable-next-line @typescript-eslint/no-non-null-assertion
  _queuedAttachments.get(id)!.push(attachments !== undefined ? [...attachments] : []);
  sessions.update(id, (cur) => ({ ...cur, prompt_queue: queue }));
}

export function dequeuePrompt(id: string): string | undefined {
  const s = get(id);
  if (s === undefined) {
    return undefined;
  }
  const q = s.prompt_queue;
  if (q === undefined || q.length === 0) {
    return undefined;
  }
  const rest = q.slice(1);
  const next = q[0];
  sessions.update(id, (cur) => {
    const copy = { ...cur };
    if (rest.length === 0) {
      delete copy.prompt_queue;
    } else {
      copy.prompt_queue = rest;
    }
    return copy;
  });
  // Also shift attachments (consumed via dequeuePromptAttachments or discarded).
  const aq = _queuedAttachments.get(id);
  if (aq !== undefined) {
    aq.shift();
    if (aq.length === 0) {
      _queuedAttachments.delete(id);
    }
  }
  return next;
}

/** Peek the attachments for the next queued prompt (call before dequeuePrompt
 *  to capture them). */
export function peekQueuedAttachments(id: string): readonly unknown[] {
  const aq = _queuedAttachments.get(id);
  if (aq === undefined || aq.length === 0) {
    return [];
  }
  // eslint-disable-next-line @typescript-eslint/no-non-null-assertion
  return aq[0]!;
}

/** Replace attachments on the last queued entry (used when the action
 *  enqueued text-only and the caller needs to attach files after). */
export function setLastQueuedAttachments(id: string, attachments: readonly unknown[]): void {
  const aq = _queuedAttachments.get(id);
  if (aq === undefined || aq.length === 0) {
    return;
  }
  aq[aq.length - 1] = [...attachments];
}

export function setQueuedPrompt(id: string, text: string | undefined): void {
  if (text === undefined) {
    _queuedAttachments.delete(id);
    sessions.update(id, (cur) => {
      const copy = { ...cur };
      delete copy.prompt_queue;
      return copy;
    });
  } else {
    _queuedAttachments.set(id, [[]]);
    sessions.update(id, (cur) => ({ ...cur, prompt_queue: [text] }));
  }
}

// --- Index helpers ---
export function clearMsgIndex(sessionID: string): void {
  msgIndex.delete(sessionID);
}

/** Invalidate a background session's cache so the next switch refetches. */
export function invalidateSession(chatID: string): void {
  const s = get(chatID);
  if (s === undefined) {
    return;
  }
  // Messages cleared in place (messages live on the session object; the
  // messages renderer reacts to messagesVersion, not the session signal).
  s.messages = [];
  s.has_more = false;
  clearMsgIndex(chatID);
  emitMessages();
}

/** Rebuild the message index for a session. Exported for store-load.ts. */
export function rebuildMsgIndex(sessionID: string, messages: Message[]): void {
  const idx = new Map<string, number>();
  for (let i = 0; i < messages.length; i++) {
    // eslint-disable-next-line @typescript-eslint/no-non-null-assertion
    idx.set(messages[i]!.id, i);
  }
  msgIndex.set(sessionID, idx);
}

function getMsgIndex(sessionID: string, messages: Message[]): Map<string, number> {
  let mi = msgIndex.get(sessionID);
  if (mi === undefined) {
    mi = new Map<string, number>();
    for (let i = 0; i < messages.length; i++) {
      // eslint-disable-next-line @typescript-eslint/no-non-null-assertion
      mi.set(messages[i]!.id, i);
    }
    msgIndex.set(sessionID, mi);
  }
  return mi;
}

// --- SSE-driven mutations ---
export function upsertHeader(h: ChatHeader): void {
  const existing = get(h.id);
  if (existing !== undefined) {
    // Server-authoritative re-sync of the header fields (preserve messages,
    // pending_changes, thinking, working_label, trusted_this_turn which are
    // client/stream-owned).
    sessions.update(h.id, (s) => {
      const next: Session = {
        ...s,
        name: h.name,
        agent: h.agent ?? "",
        model: h.model ?? "",
        acp_session_id: h.acp_session_id ?? "",
        current_mode_id: h.current_mode_id ?? "",
        available_modes: h.available_modes ?? [],
        available_models: h.available_models ?? [],
        supervised_mode: h.supervised_mode ?? false,
        usage: h.usage,
        message_count: Math.max(s.message_count, h.message_count),
      };
      if (h.auto_approve_crew !== undefined) {
        next.auto_approve_crew = h.auto_approve_crew;
      }
      if (h.compaction_watermark !== undefined) {
        next.compaction_watermark = h.compaction_watermark;
      } else {
        delete next.compaction_watermark;
      }
      if (h.oldest_checkpoint_tag !== undefined) {
        next.oldest_checkpoint_tag = h.oldest_checkpoint_tag;
      } else {
        delete next.oldest_checkpoint_tag;
      }
      if (h.parent_chat_id !== undefined) {
        next.parent_chat_id = h.parent_chat_id;
      } else {
        delete next.parent_chat_id;
      }
      return next;
    });
    return;
  }
  const s: Session = {
    id: h.id,
    name: h.name,
    agent: h.agent ?? "",
    model: h.model ?? "",
    acp_session_id: h.acp_session_id ?? "",
    current_mode_id: h.current_mode_id ?? "",
    available_modes: h.available_modes ?? [],
    available_models: h.available_models ?? [],
    auto_approve_crew: h.auto_approve_crew ?? false,
    supervised_mode: h.supervised_mode ?? false,
    pending_changes: [],
    usage: h.usage,
    message_count: h.message_count,
    messages: [],
    has_more: h.message_count > 0,
    thinking: false,
    working_label: "Thinking",
    ...(h.parent_chat_id !== undefined && { parent_chat_id: h.parent_chat_id }),
  };
  if (h.compaction_watermark !== undefined) {
    s.compaction_watermark = h.compaction_watermark;
  }
  if (h.oldest_checkpoint_tag !== undefined) {
    s.oldest_checkpoint_tag = h.oldest_checkpoint_tag;
  }
  // New sessions go to the front of the list (matches the previous unshift).
  sessions.prepend([s]);
}

export function setCurrentMode(id: string, modeID: string): void {
  const s = get(id);
  if (s === undefined || s.current_mode_id === modeID) {
    return;
  }
  sessions.update(id, (cur) => ({ ...cur, current_mode_id: modeID }));
}

export function removeChat(id: string): void {
  if (!sessions.has(id)) {
    return;
  }
  const wasActive = activeId.peek() === id;
  const order = sessions.ids.peek();
  sessions.remove(id);
  msgIndex.delete(id);
  _queuedAttachments.delete(id);
  if (wasActive) {
    const remaining = order.filter((x) => x !== id);
    activeId.value = remaining[0] ?? "";
  }
}

/** Re-insert a previously-removed session at a specific index (or at the head
 *  if no index given). Used by optimistic action rollbacks. Idempotent. */
export function reinsertSession(session: Session, atIndex?: number): void {
  if (sessions.has(session.id)) {
    return;
  }
  const order = [...sessions.items()];
  const target = atIndex !== undefined ? Math.max(0, Math.min(atIndex, order.length)) : 0;
  order.splice(target, 0, session);
  sessions.setAll(order);
}

export function appendMessage(chatID: string, msg: Message): void {
  const s = get(chatID);
  if (s === undefined) {
    return;
  }
  const mi = getMsgIndex(chatID, s.messages);
  if (mi.has(msg.id)) {
    return;
  }
  const newIdx = s.messages.length;
  mi.set(msg.id, newIdx);
  s.messages.push(msg);
  s.message_count = Math.max(s.message_count, s.messages.length);
  emitMessages();
}

export function upsertMessage(chatID: string, msg: Message): void {
  const s = get(chatID);
  if (s === undefined) {
    return;
  }
  const mi = getMsgIndex(chatID, s.messages);
  const idx = mi.get(msg.id) ?? -1;
  if (idx === -1) {
    const newIdx = s.messages.length;
    s.messages.push(msg);
    s.message_count = Math.max(s.message_count, s.messages.length);
    mi.set(msg.id, newIdx);
    emitMessages();
    return;
  }
  s.messages[idx] = msg;
  if (msg.event_kind === "crew" && msg.crew !== undefined) {
    const sig = crewSigs.get(msg.id);
    if (sig !== undefined) {
      sig.value = msg.crew;
      return;
    }
  }
  emitMessages();
}

export function addPendingChange(chatID: string, change: PendingChange): void {
  const s = get(chatID);
  if (s === undefined) {
    return;
  }
  if (s.pending_changes.some((p) => p.tool_call_id === change.tool_call_id)) {
    return;
  }
  sessions.update(chatID, (cur) => ({
    ...cur,
    pending_changes: [...cur.pending_changes, change],
  }));
}

export function removePendingChange(chatID: string, toolCallID: string): void {
  const s = get(chatID);
  if (s === undefined) {
    return;
  }
  const next = s.pending_changes.filter((p) => p.tool_call_id !== toolCallID);
  if (next.length === s.pending_changes.length) {
    return;
  }
  sessions.update(chatID, (cur) => ({ ...cur, pending_changes: next }));
}

export function clearPendingChanges(chatID: string): void {
  const s = get(chatID);
  if (s === undefined || s.pending_changes.length === 0) {
    return;
  }
  sessions.update(chatID, (cur) => ({ ...cur, pending_changes: [] }));
}

export function setSupervisedMode(chatID: string, enabled: boolean): void {
  const s = get(chatID);
  if (s === undefined || s.supervised_mode === enabled) {
    return;
  }
  sessions.update(chatID, (cur) => ({ ...cur, supervised_mode: enabled }));
}

export function setAutoApproveCrew(chatID: string, enabled: boolean): void {
  const s = get(chatID);
  if (s === undefined || s.auto_approve_crew === enabled) {
    return;
  }
  sessions.update(chatID, (cur) => ({ ...cur, auto_approve_crew: enabled }));
}

/** Set session model and notify subscribers. Used by switchModel. */
export function setModel(chatID: string, model: string): void {
  if (!sessions.has(chatID)) {
    return;
  }
  sessions.update(chatID, (cur) => ({ ...cur, model }));
}

/** Set session name and notify subscribers. */
export function setName(chatID: string, name: string): void {
  if (!sessions.has(chatID)) {
    return;
  }
  sessions.update(chatID, (cur) => ({ ...cur, name }));
}

/** Return the current index of a session in the list, or -1. */
export function indexOfSession(id: string): number {
  return sessions.ids.peek().indexOf(id);
}

export function setTrustedThisTurn(chatID: string, trusted: boolean): void {
  const s = get(chatID);
  if (s === undefined) {
    return;
  }
  if ((s.trusted_this_turn === true) === trusted) {
    return;
  }
  sessions.update(chatID, (cur) => ({ ...cur, trusted_this_turn: trusted }));
}

export function appendChunk(
  chatID: string,
  messageID: string,
  delta: string,
  isReasoning: boolean,
  blockIndex: number,
): void {
  const s = get(chatID);
  if (s === undefined) {
    return;
  }
  const mi = getMsgIndex(chatID, s.messages);
  const idx = mi.get(messageID) ?? -1;
  let msg: Message | undefined = idx !== -1 ? s.messages[idx] : undefined;
  let isNew = false;
  if (msg === undefined) {
    msg = { id: messageID, role: "assistant", ts: Date.now(), content: "", blocks: [] };
    const newIdx = s.messages.length;
    s.messages.push(msg);
    s.message_count = Math.max(s.message_count, s.messages.length);
    mi.set(messageID, newIdx);
    isNew = true;
  }
  if (isReasoning) {
    msg.reasoning = (msg.reasoning ?? "") + delta;
  } else {
    msg.content = (msg.content ?? "") + delta;
  }
  // Mirror the delta into the chronological blocks array using the
  // server-provided block_index. The server guarantees consecutive
  // chunks for the same kind share an index; a tool_call between
  // text segments bumps the next text chunk to a new index.
  msg.blocks ??= [];
  const blockKind = isReasoning ? "thinking" : "text";
  if (msg.blocks[blockIndex] === undefined) {
    // pad any gaps (shouldn't happen, but defends against out-of-order
    // events). Empty placeholder blocks of the right kind.
    while (msg.blocks.length < blockIndex) {
      msg.blocks.push({ type: blockKind });
    }
    msg.blocks.push({ type: blockKind, ...(isReasoning ? { thinking: delta } : { text: delta }) });
  } else {
    const existing = msg.blocks[blockIndex];
    if (isReasoning) {
      existing.thinking = (existing.thinking ?? "") + delta;
    } else {
      existing.text = (existing.text ?? "") + delta;
    }
  }

  if (isNew) {
    scheduleMessages();
    return;
  }
  // Fire the per-block signal (fine-grained — only the block at blockIndex
  // re-renders) before the legacy per-message signal.
  const blockK = blockKey(messageID, blockIndex);
  const blockMap = isReasoning ? blockThinkingSigs : blockTextSigs;
  const blockSig = blockMap.get(blockK);
  if (blockSig !== undefined) {
    const fullText = isReasoning
      ? (msg.blocks[blockIndex]?.thinking ?? "")
      : (msg.blocks[blockIndex]?.text ?? "");
    blockSig.value = fullText;
  }
  if (isReasoning) {
    const sig = streamingReasoningSigs.get(messageID);
    if (sig !== undefined) {
      sig.value = msg.reasoning ?? "";
    } else if (blockSig === undefined) {
      scheduleMessages();
    }
  } else {
    const sig = streamingTextSigs.get(messageID);
    if (sig !== undefined) {
      sig.value = msg.content ?? "";
    } else if (blockSig === undefined) {
      scheduleMessages();
    }
  }
}

export function upsertToolCall(
  chatID: string,
  messageID: string,
  call: ToolCall,
  blockIndex: number,
): void {
  const s = get(chatID);
  if (s === undefined) {
    return;
  }
  const mi = getMsgIndex(chatID, s.messages);
  const idx = mi.get(messageID) ?? -1;
  let msg: Message | undefined = idx !== -1 ? s.messages[idx] : undefined;
  if (msg === undefined) {
    msg = {
      id: messageID,
      role: "assistant",
      ts: Date.now(),
      content: "",
      tool_calls: [call],
      blocks: [{ type: "tool_use", tool_call_id: call.id }],
    };
    const newIdx = s.messages.length;
    s.messages.push(msg);
    s.message_count = Math.max(s.message_count, s.messages.length);
    mi.set(messageID, newIdx);
    emitMessages();
    return;
  }
  msg.tool_calls ??= [];
  msg.blocks ??= [];
  const tcIdx = msg.tool_calls.findIndex((tc) => tc.id === call.id);
  if (tcIdx === -1) {
    msg.tool_calls.push(call);
    // First time we see this tool call — pin it to the chronological
    // block index the server reported.
    while (msg.blocks.length < blockIndex) {
      msg.blocks.push({ type: "text" });
    }
    if (msg.blocks[blockIndex] === undefined) {
      msg.blocks.push({ type: "tool_use", tool_call_id: call.id });
    }
    scheduleMessages();
    return;
  }
  msg.tool_calls[tcIdx] = call;
  const sig = toolCallSigs.get(call.id);
  if (sig !== undefined) {
    sig.value = call;
  } else {
    scheduleMessages();
  }
}

// --- Utilities ---
export function contextSizeFor(modelID: string): number {
  return MODEL_CONTEXT_SIZES[modelID] ?? 0;
}

export function defaultUsage(): Usage {
  return {
    context_pct: 0,
    context_size: 0,
    credits: 0,
    turn_count: 0,
    last_turn_ms: 0,
    has_real_data: false,
  };
}

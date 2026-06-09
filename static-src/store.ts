// ---------------------------------------------------------------------------
// Client-side session store — signals-based rewrite.
//
// State lives in module-level data structures with O(1) indexing.
// A single `version` signal triggers reactive effects on mutation.
// Streaming paths coalesce renders via scheduleMessages (queueMicrotask).
// User-initiated mutations bump version synchronously for instant feedback.
// ---------------------------------------------------------------------------

import type { Session, ChatHeader, Message, Usage, ToolCall, PendingChange } from "./types.js";
import { signal } from "@cplieger/reactive";
import {
  streamingTextSigs,
  streamingReasoningSigs,
  blockTextSigs,
  blockThinkingSigs,
  blockKey,
  toolCallSigs,
  crewSigs,
} from "./store-signals.js";

// --- Reactive version counters: effects subscribe to the relevant signal ---
/** Session list changes: add, remove, reorder, name, model, mode changes. */
export const sessionsVersion = signal(0);
/** Active session metadata: thinking, queue, supervised, pending_changes, usage. */
export const activeVersion = signal(0);
/** Message list changes: append, upsert (non-streaming), tool calls. */
export const messagesVersion = signal(0);

export function emitSessions(): void {
  sessionsVersion.value = sessionsVersion.peek() + 1;
}
function emitActive(): void {
  activeVersion.value = activeVersion.peek() + 1;
}
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

// --- State ---
let _sessions: Session[] = [];
let _activeId = "";
let sessionIndex = new Map<string, Session>();
const msgIndex = new Map<string, Map<string, number>>();
const _queuedAttachments = new Map<string, unknown[][]>();

// --- Accessors ---
export function getSessions(): Session[] {
  return _sessions;
}
export function getActiveId(): string {
  return _activeId;
}
export function getActive(): Session | undefined {
  return sessionIndex.get(_activeId);
}
export function get(id: string): Session | undefined {
  return sessionIndex.get(id);
}

export function setSessions(v: Session[]): void {
  _sessions = v;
  sessionIndex = new Map(v.map((s) => [s.id, s]));
  msgIndex.clear();
}

export function setActive(id: string): void {
  if (_activeId === id) {
    return;
  }
  _activeId = id;
  emitSessions();
  // Effects that read getActive() / depend on the active session
  // (messages renderer, follow pill, auto-approve, supervised pill,
  // chat banners) subscribe to activeVersion. Without bumping it
  // here the messages effect doesn't re-run on chat switch and
  // #messages keeps the previous chat's DOM children — visible as
  // stale messages bleeding through under the new chat's model
  // picker until something else (e.g. an arriving message) bumps
  // messagesVersion.
  emitActive();
}

export function isThinking(id: string): boolean {
  return get(id)?.thinking ?? false;
}

export function setThinking(id: string, v: boolean): void {
  const s = get(id);
  if (s === undefined) {
    return;
  }
  s.thinking = v;
  if (!v) {
    s.working_label = "Thinking";
  }
  emitActive();
}

export function setWorkingLabel(id: string, label: string): void {
  const s = get(id);
  if (s === undefined) {
    return;
  }
  s.working_label = label;
  emitActive();
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
  s.prompt_queue ??= [];
  s.prompt_queue.push(text);
  if (attachments !== undefined && attachments.length > 0) {
    if (!_queuedAttachments.has(id)) {
      _queuedAttachments.set(id, []);
    }
    // eslint-disable-next-line @typescript-eslint/no-non-null-assertion
    _queuedAttachments.get(id)!.push([...attachments]);
  } else {
    if (!_queuedAttachments.has(id)) {
      _queuedAttachments.set(id, []);
    }
    // eslint-disable-next-line @typescript-eslint/no-non-null-assertion
    _queuedAttachments.get(id)!.push([]);
  }
  emitActive();
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
  const next = q.shift();
  if (q.length === 0) {
    delete s.prompt_queue;
  }
  // Also shift attachments (consumed via dequeuePromptAttachments or discarded).
  const aq = _queuedAttachments.get(id);
  if (aq !== undefined) {
    aq.shift();
    if (aq.length === 0) {
      _queuedAttachments.delete(id);
    }
  }
  emitActive();
  return next;
}

/** Dequeue the attachments for the next queued prompt (peek without removing —
 *  call before dequeuePrompt to capture them). */
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
  const s = get(id);
  if (s === undefined) {
    return;
  }
  if (text === undefined) {
    delete s.prompt_queue;
    _queuedAttachments.delete(id);
  } else {
    s.prompt_queue = [text];
    _queuedAttachments.set(id, [[]]);
  }
  emitActive();
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
    existing.name = h.name;
    existing.agent = h.agent ?? "";
    existing.model = h.model ?? "";
    existing.acp_session_id = h.acp_session_id ?? "";
    existing.current_mode_id = h.current_mode_id ?? "";
    existing.available_modes = h.available_modes ?? [];
    existing.available_models = h.available_models ?? [];
    existing.supervised_mode = h.supervised_mode ?? false;
    if (h.auto_approve_crew !== undefined) {
      existing.auto_approve_crew = h.auto_approve_crew;
    }
    existing.usage = h.usage;
    existing.message_count = h.message_count;
    if (h.compaction_watermark !== undefined) {
      existing.compaction_watermark = h.compaction_watermark;
    } else {
      delete existing.compaction_watermark;
    }
    if (h.oldest_checkpoint_tag !== undefined) {
      existing.oldest_checkpoint_tag = h.oldest_checkpoint_tag;
    } else {
      delete existing.oldest_checkpoint_tag;
    }
    if (h.parent_chat_id !== undefined) {
      existing.parent_chat_id = h.parent_chat_id;
    } else {
      delete existing.parent_chat_id;
    }
  } else {
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
    _sessions.unshift(s);
    sessionIndex.set(s.id, s);
  }
  emitSessions();
}

export function setCurrentMode(id: string, modeID: string): void {
  const s = get(id);
  if (s === undefined) {
    return;
  }
  if (s.current_mode_id === modeID) {
    return;
  }
  s.current_mode_id = modeID;
  emitSessions();
}

export function removeChat(id: string): void {
  const idx = _sessions.findIndex((s) => s.id === id);
  if (idx === -1) {
    return;
  }
  _sessions.splice(idx, 1);
  sessionIndex.delete(id);
  msgIndex.delete(id);
  _queuedAttachments.delete(id);
  if (_activeId === id) {
    _activeId = _sessions[0]?.id ?? "";
  }
  emitSessions();
}

/** Re-insert a previously-removed session at a specific index (or at
 *  the head if no index given). Used by optimistic action rollbacks
 *  that captured the session before removeChat() and need to put it
 *  back on failure. Idempotent: if a session with the same id already
 *  exists, the existing entry is preserved. */
export function reinsertSession(session: Session, atIndex?: number): void {
  if (sessionIndex.has(session.id)) {
    return;
  }
  const target = atIndex !== undefined ? Math.max(0, Math.min(atIndex, _sessions.length)) : 0;
  _sessions.splice(target, 0, session);
  sessionIndex.set(session.id, session);
  emitSessions();
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
  s.pending_changes = [...s.pending_changes, change];
  emitActive();
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
  s.pending_changes = next;
  emitActive();
}

export function clearPendingChanges(chatID: string): void {
  const s = get(chatID);
  if (s === undefined || s.pending_changes.length === 0) {
    return;
  }
  s.pending_changes = [];
  emitActive();
}

export function setSupervisedMode(chatID: string, enabled: boolean): void {
  const s = get(chatID);
  if (s === undefined || s.supervised_mode === enabled) {
    return;
  }
  s.supervised_mode = enabled;
  emitActive();
}

export function setAutoApproveCrew(chatID: string, enabled: boolean): void {
  const s = get(chatID);
  if (s === undefined || s.auto_approve_crew === enabled) {
    return;
  }
  s.auto_approve_crew = enabled;
  emitActive();
}

/** Set session model and notify subscribers. Used by switchModel. */
export function setModel(chatID: string, model: string): void {
  const s = get(chatID);
  if (s === undefined) {
    return;
  }
  s.model = model;
  emitSessions();
}

/** Set session name and notify subscribers. */
export function setName(chatID: string, name: string): void {
  const s = get(chatID);
  if (s === undefined) {
    return;
  }
  s.name = name;
  emitSessions();
}

/** Return the current index of a session in the list, or -1. */
export function indexOfSession(id: string): number {
  return _sessions.findIndex((s) => s.id === id);
}

export function setTrustedThisTurn(chatID: string, trusted: boolean): void {
  const s = get(chatID);
  if (s === undefined) {
    return;
  }
  const current = s.trusted_this_turn === true;
  if (current === trusted) {
    return;
  }
  s.trusted_this_turn = trusted;
  emitActive();
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
  // text segments bumps the next text chunk to a new index. So we
  // either extend an existing block at this index or create one.
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
  // Fire the per-block signal (fine-grained — only the block at
  // blockIndex re-renders) before the legacy per-message signal
  // (back-compat for any code path that still subscribes by message
  // id only). When the renderer mounted the block via the
  // chronological path, the per-block signal exists and its effect
  // appends the delta directly to that block's bubble.
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
    // block index the server reported. This anchors the tool card
    // between the surrounding text blocks.
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

// ---------------------------------------------------------------------------
// Client-side session store.
//
// Sessions live in a createCollection<Session> (ordered, keyed by id, with a
// per-session signal + a structure/order signal). `activeId` is a signal;
// `activeSession` is a computed that tracks the active session's per-entity
// signal — subscribers read it to react only to active-session changes.
//
// MESSAGES are deliberately NOT a collection: each session owns a Message[]
// with sub-message (block/tool) streaming signals in store-signals.ts,
// finer-grained than a per-message signal. The messages renderer subscribes
// to `messagesVersion` (coarse "list shape changed") while the SignalMaps do
// the per-block/per-tool fine-grained work. Streaming paths coalesce
// renders via scheduleMessages (queueMicrotask).
// ---------------------------------------------------------------------------

import type {
  Session,
  ChatHeader,
  Message,
  Block,
  Usage,
  ToolCall,
  CodeReference,
  FileChange,
  PendingChange,
  QueuedPrompt,
} from "./types.js";
import { signal, computed, createCollection, batch } from "@cplieger/reactive";
import {
  streamingTextSigs,
  streamingReasoningSigs,
  blockTextSigs,
  blockThinkingSigs,
  blockKey,
  toolCallSigs,
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
  sessions.update(id, (s) => {
    const next: Session = {
      ...s,
      thinking: v,
      working_label: v ? s.working_label : "Thinking",
    };
    // A new turn invalidates the agent's previously declared status
    // (e.g. a lingering waiting_on_user): the agent re-declares via
    // focus updates as the turn progresses.
    if (v) {
      delete next.agent_status;
      delete next.agent_status_text;
    }
    return next;
  });
}

/** Derive the tab activity dot for a session: an in-flight turn shows the
 *  pulsing "thinking" dot; an agent-declared waiting_on_user (which
 *  deliberately survives turn end — it means "I asked you something")
 *  shows the amber "waiting" dot; anything else clears. Single rule shared
 *  by the store effect (chat.ts) and the turn_ended handler so the two
 *  writers can't disagree. */
export function tabStatusFor(s: Session | undefined): "" | "thinking" | "waiting" {
  if (s === undefined) {
    return "";
  }
  if (s.thinking) {
    return "thinking";
  }
  if (s.agent_status === "waiting_on_user") {
    return "waiting";
  }
  return "";
}

/** Record the agent-declared activity status/description for a chat
 *  (chat_status SSE, sourced from the KAS focus_update channel). Empty
 *  strings clear the respective field. */
export function setAgentStatus(id: string, status: string, text: string): void {
  const s = get(id);
  if (s === undefined) {
    return;
  }
  if ((s.agent_status ?? "") === status && (s.agent_status_text ?? "") === text) {
    return; // no-op: don't churn the session signal
  }
  sessions.update(id, (prev) => {
    const next: Session = { ...prev };
    if (status === "") {
      delete next.agent_status;
    } else {
      next.agent_status = status;
    }
    if (text === "") {
      delete next.agent_status_text;
    } else {
      next.agent_status_text = text;
    }
    return next;
  });
}

export function setWorkingLabel(id: string, label: string): void {
  sessions.update(id, (s) => ({ ...s, working_label: label }));
}

// --- Prompt queue (per-chat FIFO of prompts buffered while a turn runs) ---
//
// Text and its attachments travel together in one QueuedPrompt entry, so
// they can never desync (the old design kept attachments in a parallel map
// keyed positionally). The queue is client-only (never persisted or sent to
// the server) and reactive — a field on the session — so send-state.ts
// derives the "queued" button state and queued-prompts.ts renders the pending
// chips straight off `session.prompt_queue`. All lifecycle decisions
// (enqueue-on-409, drain, idle re-check, cancel) live in prompt-queue.ts;
// these are the pure data ops.

/** Number of prompts currently queued for a chat. */
export function queueLength(id: string): number {
  return get(id)?.prompt_queue?.length ?? 0;
}

/** Append a prompt (text + its attachments) to the back of the queue. */
export function enqueuePrompt(id: string, text: string, attachments?: readonly unknown[]): void {
  const s = get(id);
  if (s === undefined) {
    return;
  }
  const entry: QueuedPrompt = {
    text,
    attachments: attachments !== undefined ? [...attachments] : [],
  };
  const queue = [...(s.prompt_queue ?? []), entry];
  sessions.update(id, (cur) => ({ ...cur, prompt_queue: queue }));
}

/** Read the front entry without removing it. */
export function peekPrompt(id: string): QueuedPrompt | undefined {
  return get(id)?.prompt_queue?.[0];
}

/** Remove and return the front entry (FIFO). */
export function dequeuePrompt(id: string): QueuedPrompt | undefined {
  return removeQueuedAt(id, 0);
}

/** Remove and return the entry at `index` (0-based). Backs both the drain
 *  (front) and the UI cancel affordance (any position). No-op + undefined
 *  for an out-of-range index or unknown chat. */
export function removeQueuedAt(id: string, index: number): QueuedPrompt | undefined {
  const q = get(id)?.prompt_queue;
  if (q === undefined || index < 0 || index >= q.length) {
    return undefined;
  }
  const removed = q[index];
  if (removed === undefined) {
    return undefined;
  }
  const rest = q.filter((_, i) => i !== index);
  sessions.update(id, (cur) => {
    const copy = { ...cur };
    if (rest.length === 0) {
      delete copy.prompt_queue;
    } else {
      copy.prompt_queue = rest;
    }
    return copy;
  });
  return removed;
}

// --- Index helpers ---
function clearMsgIndex(sessionID: string): void {
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
        model: h.model ?? "",
        acp_session_id: h.acp_session_id ?? "",
        current_mode_id: h.current_mode_id ?? "",
        available_modes: h.available_modes ?? [],
        available_models: h.available_models ?? [],
        supervised_mode: h.supervised_mode ?? false,
        usage: h.usage,
        message_count: Math.max(s.message_count, h.message_count),
      };
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
    model: h.model ?? "",
    acp_session_id: h.acp_session_id ?? "",
    current_mode_id: h.current_mode_id ?? "",
    available_modes: h.available_modes ?? [],
    available_models: h.available_models ?? [],
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
  // Both reactive writes below — sessions.remove (fires sessions.ids) and the
  // activeId reassignment — feed the `activeSession` computed. Batch them so
  // active-session subscribers re-derive ONCE, not twice; otherwise removing
  // the active chat double-fires the computed and the messages renderer flashes
  // a transient teardown of the new chat's DOM.
  batch(() => {
    sessions.remove(id);
    msgIndex.delete(id);
    if (wasActive) {
      const remaining = order.filter((x) => x !== id);
      activeId.value = remaining[0] ?? "";
    }
  });
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

function nonEmptyStr(v: string | undefined): v is string {
  return v !== undefined && v !== "";
}

/** Spread helper: include `agent_subtask_id` on a block only when non-empty
 *  (exactOptionalPropertyTypes forbids setting an optional field to
 *  undefined; "" is the top-level default and is simply omitted). */
function subtaskField(id: string | undefined): { agent_subtask_id?: string } {
  return nonEmptyStr(id) ? { agent_subtask_id: id } : {};
}

/** Ensure an assistant message has a `blocks` array so the renderer has ONE
 *  path. v3 server messages already carry blocks; legacy replays (content /
 *  reasoning / tool_calls only) get synthesized blocks — thinking, then text,
 *  then a tool_use per tool call. Non-assistant or already-block messages pass
 *  through unchanged. */
export function normalizeMessage(m: Message): Message {
  if (m.role !== "assistant" || (m.blocks !== undefined && m.blocks.length > 0)) {
    return m;
  }
  const tools = m.tool_calls ?? [];
  if (!nonEmptyStr(m.content) && !nonEmptyStr(m.reasoning) && tools.length === 0) {
    return m; // e.g. a plan-only assistant message — nothing to synthesize
  }
  const blocks: Block[] = [];
  if (nonEmptyStr(m.reasoning)) {
    blocks.push({ type: "thinking", thinking: m.reasoning });
  }
  if (nonEmptyStr(m.content)) {
    blocks.push({ type: "text", text: m.content });
  }
  for (const tc of tools) {
    blocks.push({ type: "tool_use", tool_call_id: tc.id, ...subtaskField(tc.agent_subtask_id) });
  }
  return { ...m, blocks };
}

/** Merge a freshly-ingested server message over the existing one: adopt the
 *  incoming's non-empty fields, never clobber non-empty with empty. This is
 *  what lets the streamed assistant message and its final message_appended
 *  (same id) coexist — the final's sanitized fields win, but an empty
 *  message_created can't wipe streamed content. */
function mergeMessage(existing: Message, incoming: Message): Message {
  const merged: Message = { ...existing };
  if (nonEmptyStr(incoming.content)) {
    merged.content = incoming.content;
  }
  if (nonEmptyStr(incoming.reasoning)) {
    merged.reasoning = incoming.reasoning;
  }
  if (incoming.blocks !== undefined && incoming.blocks.length > 0) {
    merged.blocks = incoming.blocks;
  }
  if (incoming.tool_calls !== undefined && incoming.tool_calls.length > 0) {
    merged.tool_calls = incoming.tool_calls;
  }
  if (incoming.plan !== undefined && incoming.plan.length > 0) {
    merged.plan = incoming.plan;
  }
  if (incoming.code_references !== undefined && incoming.code_references.length > 0) {
    merged.code_references = incoming.code_references;
  }
  if (nonEmptyStr(incoming.checkpoint_tag)) {
    merged.checkpoint_tag = incoming.checkpoint_tag;
  }
  if (incoming.event_kind !== undefined) {
    merged.event_kind = incoming.event_kind;
  }
  if (incoming.ts > 0) {
    merged.ts = incoming.ts;
  }
  return merged;
}

/** Ingest a server-canonical message. message_created / message_appended /
 *  message_updated ALL route here. Upsert by id with a merge that never drops
 *  a message and never overwrites non-empty content with empty:
 *  - absent  → normalize (ensure blocks) + push.
 *  - present → mergeMessage (adopt incoming's non-empty fields).
 *  Fixes the "final message never renders" (old append no-op), the
 *  "message_created wipes streamed content" (old blind replace), and the
 *  out-of-order chunk-before-created bugs. Internal — the public entry points
 *  are appendMessage / upsertMessage. */
function ingestMessage(chatID: string, incoming: Message): void {
  const s = get(chatID);
  if (s === undefined) {
    return;
  }
  const mi = getMsgIndex(chatID, s.messages);
  const idx = mi.get(incoming.id) ?? -1;
  if (idx === -1) {
    mi.set(incoming.id, s.messages.length);
    s.messages.push(normalizeMessage(incoming));
    s.message_count = Math.max(s.message_count, s.messages.length);
    emitMessages();
    return;
  }
  const existing = s.messages[idx];
  if (existing !== undefined) {
    s.messages[idx] = mergeMessage(existing, normalizeMessage(incoming));
  }
  emitMessages();
}

/** message_appended → merge path (was a dedup no-op that dropped the final
 *  sanitized message). */
export function appendMessage(chatID: string, msg: Message): void {
  ingestMessage(chatID, msg);
}

/** message_created / message_updated → merge path (was a blind replace that
 *  wiped streamed content). */
export function upsertMessage(chatID: string, msg: Message): void {
  ingestMessage(chatID, msg);
}

/** Stamp the just-ended turn's summary (credits / elapsed / changed files)
 *  onto the chat's last assistant message. The renderer projects it into a
 *  keyed `.turn-footer` under that turn — replacing the old un-keyed direct
 *  DOM write in handlers/turn.ts that double-rendered on SSE replay and
 *  vanished on refresh. Applies to any chat (not just the active one) so a
 *  background turn's footer is present when the user switches to it. The
 *  server persists the same fields at flush time so it also survives reload. */
export function setTurnSummary(
  chatID: string,
  data: { credits?: number; elapsedMs?: number; changedFiles?: Record<string, FileChange> },
): void {
  const s = get(chatID);
  if (s === undefined) {
    return;
  }
  let target: Message | undefined;
  for (let i = s.messages.length - 1; i >= 0; i--) {
    const m = s.messages[i];
    if (m?.role === "assistant") {
      target = m;
      break;
    }
  }
  if (target === undefined) {
    return;
  }
  let changed = false;
  if (data.credits !== undefined && data.credits > 0) {
    target.turn_credits = data.credits;
    changed = true;
  }
  if (data.elapsedMs !== undefined && data.elapsedMs > 0) {
    target.turn_elapsed_ms = data.elapsedMs;
    changed = true;
  }
  if (data.changedFiles !== undefined && Object.keys(data.changedFiles).length > 0) {
    target.changed_files = data.changedFiles;
    changed = true;
  }
  if (changed) {
    emitMessages();
  }
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

/** Set session model and notify subscribers. Used by switchModel. The
 *  usage.context_size is derived from the model, so it is refreshed in the
 *  same update — callers must never mutate `session.usage` on a stale
 *  reference (sessions.update replaces the object; writes to the old one
 *  are invisible to subscribers and re-renders). */
export function setModel(chatID: string, model: string): void {
  if (!sessions.has(chatID)) {
    return;
  }
  sessions.update(chatID, (cur) => ({
    ...cur,
    model,
    usage: { ...cur.usage, context_size: contextSizeFor(model) },
  }));
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
  subtaskID: string,
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
  // chunks for the same kind (and same subtask) share an index; a
  // tool_call, a kind switch, or a subtask switch bumps to a new index.
  msg.blocks ??= [];
  const blockKind = isReasoning ? "thinking" : "text";
  if (msg.blocks[blockIndex] === undefined) {
    // pad any gaps (shouldn't happen, but defends against out-of-order
    // events). Empty placeholder blocks of the right kind.
    while (msg.blocks.length < blockIndex) {
      msg.blocks.push({ type: blockKind });
    }
    msg.blocks.push({
      type: blockKind,
      ...subtaskField(subtaskID),
      ...(isReasoning ? { thinking: delta } : { text: delta }),
    });
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

/** Attach licensed-code attributions to an in-flight assistant message
 *  (v3 code_references SSE). The server sends the full deduped list each
 *  time, so we replace rather than append. No-op if the target message
 *  isn't in the store yet — the refs still persist server-side and render
 *  on reload from Message.code_references. */
export function setCodeReferences(chatID: string, messageID: string, refs: CodeReference[]): void {
  const s = get(chatID);
  if (s === undefined) {
    return;
  }
  const mi = getMsgIndex(chatID, s.messages);
  const idx = mi.get(messageID) ?? -1;
  if (idx === -1) {
    return;
  }
  const msg = s.messages[idx];
  if (msg === undefined) {
    return;
  }
  msg.code_references = refs;
  emitMessages();
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
      blocks: [{ type: "tool_use", tool_call_id: call.id, ...subtaskField(call.agent_subtask_id) }],
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
      msg.blocks.push({
        type: "tool_use",
        tool_call_id: call.id,
        ...subtaskField(call.agent_subtask_id),
      });
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

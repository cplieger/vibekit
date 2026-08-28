// ---------------------------------------------------------------------------
// Per-entity streaming signal maps — generic registry for reactive per-ID
// signals that bypass the global reconcile loop.
// ---------------------------------------------------------------------------

import type { ToolCall } from "./types.js";
import { SignalMap, type Signal } from "@cplieger/reactive";
import { join } from "@cplieger/keyenc";

// SignalMap (the dynamic per-id signal registry) is provided by @cplieger/reactive.

// --- Signal registry instances ---

/** Per-message-id streaming text signal. */
export const streamingTextSigs = new SignalMap<string>();

/** Per-message-id reasoning signal. */
export const streamingReasoningSigs = new SignalMap<string>();

/** Per-(message-id, block-index) streaming text signal. Used by the
 *  block-aware renderer so each text block in a chronological assistant
 *  message subscribes only to its own deltas — chunks for block N
 *  don't trigger re-renders on block N-1. Key format: `${msgID}:${idx}`. */
export const blockTextSigs = new SignalMap<string>();

/** Per-(message-id, block-index) streaming thinking signal. Same
 *  rationale as blockTextSigs — chronologically interleaved thinking
 *  blocks each get their own subscription. */
export const blockThinkingSigs = new SignalMap<string>();

/** Per-(chat-id, tool-call-id) signal.
 *
 *  The chat is part of the key because a tool call id is BACKEND-authored and
 *  the wire carries no uniqueness guarantee for it, while `upsertToolCall` runs
 *  for whatever chat a frame arrived on — a background chat's data lands
 *  unconditionally and only the repaint is gated. Keyed on the call id alone, a
 *  collision wrote a background chat's card state into the visible chat's card,
 *  for as long as that card stayed mounted. */
export const toolCallSigs = new SignalMap<ToolCall>();

/** Key for `toolCallSigs`. Through keyenc rather than a template literal because
 *  a chat id is opaque hex while a tool call id is arbitrary text, so a
 *  separator the id may contain must not be able to shift the boundary. */
export function toolCallSigKey(chatID: string, toolID: string): string {
  return join(chatID, toolID);
}

// --- Public accessors ---

export function ensureStreamingSig(messageID: string, initial: string): Signal<string> {
  return streamingTextSigs.ensure(messageID, initial);
}

export function ensureReasoningSig(messageID: string, initial: string): Signal<string> {
  return streamingReasoningSigs.ensure(messageID, initial);
}

export function ensureToolCallSig(
  chatID: string,
  toolID: string,
  initial: ToolCall,
): Signal<ToolCall> {
  return toolCallSigs.ensure(toolCallSigKey(chatID, toolID), initial);
}

/** The current value of a tool call's signal, untracked, or undefined when no
 *  signal exists. For derivations that run inside someone ELSE's effect (the
 *  delegate footer sums its members on the invocation's ticks) — reading
 *  `.value` there would subscribe that effect to every member. */
export function peekToolCallSig(chatID: string, toolID: string): ToolCall | undefined {
  return toolCallSigs.get(toolCallSigKey(chatID, toolID))?.peek();
}

/** Key helper for per-(message, block-index) signal maps. */
export function blockKey(messageID: string, blockIndex: number): string {
  return `${messageID}:${String(blockIndex)}`;
}

export function ensureBlockTextSig(
  messageID: string,
  blockIndex: number,
  initial: string,
): Signal<string> {
  return blockTextSigs.ensure(blockKey(messageID, blockIndex), initial);
}

export function ensureBlockThinkingSig(
  messageID: string,
  blockIndex: number,
  initial: string,
): Signal<string> {
  return blockThinkingSigs.ensure(blockKey(messageID, blockIndex), initial);
}

export function clearStreamingSig(messageID: string): void {
  streamingTextSigs.clear(messageID);
}

export function clearReasoningSig(messageID: string): void {
  streamingReasoningSigs.clear(messageID);
}

export function clearToolCallSig(chatID: string, toolID: string): void {
  toolCallSigs.clear(toolCallSigKey(chatID, toolID));
}

/** Drop every per-(message, block-index) streaming signal. Called on chat
 *  switch / full teardown so block signals don't accumulate across chats
 *  (the per-key set isn't cheaply enumerable, so this is a wholesale wipe). */
export function clearAllBlockSigs(): void {
  blockTextSigs.clearAll();
  blockThinkingSigs.clearAll();
}

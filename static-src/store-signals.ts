// ---------------------------------------------------------------------------
// Per-entity streaming signal maps — generic registry for reactive per-ID
// signals that bypass the global reconcile loop.
// ---------------------------------------------------------------------------

import type { ToolCall } from "./types.js";
import { SignalMap, type Signal } from "@cplieger/reactive";

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

/** Per-tool-call signal. */
export const toolCallSigs = new SignalMap<ToolCall>();

// --- Public accessors ---

export function ensureStreamingSig(messageID: string, initial: string): Signal<string> {
  return streamingTextSigs.ensure(messageID, initial);
}

export function ensureReasoningSig(messageID: string, initial: string): Signal<string> {
  return streamingReasoningSigs.ensure(messageID, initial);
}

export function ensureToolCallSig(toolID: string, initial: ToolCall): Signal<ToolCall> {
  return toolCallSigs.ensure(toolID, initial);
}

/** The current value of a tool call's signal, untracked, or undefined when no
 *  signal exists. For derivations that run inside someone ELSE's effect (the
 *  delegate footer sums its members on the invocation's ticks) — reading
 *  `.value` there would subscribe that effect to every member. */
export function peekToolCallSig(toolID: string): ToolCall | undefined {
  return toolCallSigs.get(toolID)?.peek();
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

export function clearToolCallSig(toolID: string): void {
  toolCallSigs.clear(toolID);
}

/** Drop every per-(message, block-index) streaming signal. Called on chat
 *  switch / full teardown so block signals don't accumulate across chats
 *  (the per-key set isn't cheaply enumerable, so this is a wholesale wipe). */
export function clearAllBlockSigs(): void {
  blockTextSigs.clearAll();
  blockThinkingSigs.clearAll();
}

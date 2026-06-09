// ---------------------------------------------------------------------------
// Per-entity streaming signal maps — generic registry for reactive per-ID
// signals that bypass the global reconcile loop.
// ---------------------------------------------------------------------------

import type { ToolCall, Crew } from "./types.js";
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

/** Per-crew-message signal. */
export const crewSigs = new SignalMap<Crew>();

// --- Public accessors (thin delegates for backward compat) ---

export function getStreamingSig(messageID: string): Signal<string> | undefined {
  return streamingTextSigs.get(messageID);
}

export function getReasoningSig(messageID: string): Signal<string> | undefined {
  return streamingReasoningSigs.get(messageID);
}

export function getToolCallSig(toolID: string): Signal<ToolCall> | undefined {
  return toolCallSigs.get(toolID);
}

export function getCrewSig(messageID: string): Signal<Crew> | undefined {
  return crewSigs.get(messageID);
}

export function ensureStreamingSig(
  messageID: string,
  initial: string,
): Signal<string> {
  return streamingTextSigs.ensure(messageID, initial);
}

export function ensureReasoningSig(
  messageID: string,
  initial: string,
): Signal<string> {
  return streamingReasoningSigs.ensure(messageID, initial);
}

export function ensureToolCallSig(
  toolID: string,
  initial: ToolCall,
): Signal<ToolCall> {
  return toolCallSigs.ensure(toolID, initial);
}

export function ensureCrewSig(messageID: string, initial: Crew): Signal<Crew> {
  return crewSigs.ensure(messageID, initial);
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

export function getBlockTextSig(
  messageID: string,
  blockIndex: number,
): Signal<string> | undefined {
  return blockTextSigs.get(blockKey(messageID, blockIndex));
}

export function ensureBlockThinkingSig(
  messageID: string,
  blockIndex: number,
  initial: string,
): Signal<string> {
  return blockThinkingSigs.ensure(blockKey(messageID, blockIndex), initial);
}

export function getBlockThinkingSig(
  messageID: string,
  blockIndex: number,
): Signal<string> | undefined {
  return blockThinkingSigs.get(blockKey(messageID, blockIndex));
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

export function clearCrewSig(messageID: string): void {
  crewSigs.clear(messageID);
}

/** Clear all signal maps at once. Used on chat-switch to avoid stale
 *  subscriptions leaking into the new chat's render cycle. */
export function clearAllSignals(): void {
  streamingTextSigs.clearAll();
  streamingReasoningSigs.clearAll();
  blockTextSigs.clearAll();
  blockThinkingSigs.clearAll();
  toolCallSigs.clearAll();
  crewSigs.clearAll();
}

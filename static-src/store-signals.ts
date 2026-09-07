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

/** One per-block streaming write: the block's accumulated text plus the growth
 *  this write carries, so a consumer can append `delta` instead of re-deriving
 *  the tail from an ever-longer `full`.
 *
 *  INVARIANT: the pair is meaningful only under @cplieger/reactive's
 *  synchronous flush contract — a write re-runs every subscribed effect before
 *  the setter returns, so a consumer observes one value per chunk with none
 *  skipped. A batching layer between writer and effect would coalesce writes
 *  into a value whose `delta` no longer bridges the consumer's text to `full`,
 *  corrupting streamed prose silently. That dependency is why consumers keep a
 *  watermark (`accepted + delta.length === full.length`) and resync from `full`
 *  on any mismatch. */
export interface BlockSignalValue {
  readonly full: string;
  readonly delta: string;
}

/** Per-(message-id, block-index) streaming text signal. Used by the
 *  block-aware renderer so each text block in a chronological assistant
 *  message subscribes only to its own deltas — chunks for block N
 *  don't trigger re-renders on block N-1. Key format: `${msgID}:${idx}`. */
export const blockTextSigs = new SignalMap<BlockSignalValue>();

/** Per-(message-id, block-index) streaming thinking signal. Same
 *  rationale as blockTextSigs — chronologically interleaved thinking
 *  blocks each get their own subscription. */
export const blockThinkingSigs = new SignalMap<BlockSignalValue>();

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

/** Keys minted per message across BOTH block-signal maps, so a message's
 *  disposal can clear its signals without enumerating the maps (SignalMap
 *  exposes no key walk). One set serves both maps: text and thinking share the
 *  key format, and clearing a key the other map never held is a no-op. */
const blockSigKeysByMsg = new Map<string, Set<string>>();

function noteBlockKey(messageID: string, key: string): void {
  let keys = blockSigKeysByMsg.get(messageID);
  if (keys === undefined) {
    keys = new Set();
    blockSigKeysByMsg.set(messageID, keys);
  }
  keys.add(key);
}

export function ensureBlockTextSig(
  messageID: string,
  blockIndex: number,
  initial: string,
): Signal<BlockSignalValue> {
  const key = blockKey(messageID, blockIndex);
  noteBlockKey(messageID, key);
  // A signal minted at mount carries no growth: `initial` is already on screen.
  return blockTextSigs.ensure(key, { full: initial, delta: "" });
}

export function ensureBlockThinkingSig(
  messageID: string,
  blockIndex: number,
  initial: string,
): Signal<BlockSignalValue> {
  const key = blockKey(messageID, blockIndex);
  noteBlockKey(messageID, key);
  return blockThinkingSigs.ensure(key, { full: initial, delta: "" });
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

/** Drop ONE block's streaming signals, for a range the window dropped. Removes
 *  the key from the per-message set too, so the sweep below does not later
 *  iterate a key nothing holds. */
export function clearBlockSig(messageID: string, blockIndex: number): void {
  const key = blockKey(messageID, blockIndex);
  blockTextSigs.clear(key);
  blockThinkingSigs.clear(key);
  blockSigKeysByMsg.get(messageID)?.delete(key);
}

/** Drop one message's per-block streaming signals. The per-message half of
 *  `clearAllBlockSigs`: without it a block signal lives until the last chat
 *  closes, one entry per streamed block, for the whole page's life. */
export function clearBlockSigsFor(messageID: string): void {
  const keys = blockSigKeysByMsg.get(messageID);
  if (keys === undefined) {
    return;
  }
  for (const k of keys) {
    blockTextSigs.clear(k);
    blockThinkingSigs.clear(k);
  }
  blockSigKeysByMsg.delete(messageID);
}

/** Drop every per-(message, block-index) streaming signal. Called on full
 *  teardown (last chat closed); per-message disposal goes through
 *  `clearBlockSigsFor`. */
export function clearAllBlockSigs(): void {
  blockTextSigs.clearAll();
  blockThinkingSigs.clearAll();
  blockSigKeysByMsg.clear();
}

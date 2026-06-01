// ---------------------------------------------------------------------------
// Per-entity streaming signal maps — generic registry for reactive per-ID
// signals that bypass the global reconcile loop.
// ---------------------------------------------------------------------------

import type { ToolCall, Crew } from "./types.js";
import { signal } from "./lib/reactive/index.js";

// --- Generic SignalMap ---

/** A typed map of lazily-created signals keyed by string ID. Provides
 *  get/ensure/clear/clearAll operations for any value type. */
export class SignalMap<V> {
  private map = new Map<string, ReturnType<typeof signal<V>>>();

  get(id: string): ReturnType<typeof signal<V>> | undefined {
    return this.map.get(id);
  }

  ensure(id: string, initial: V): ReturnType<typeof signal<V>> {
    let sig = this.map.get(id);
    if (sig === undefined) {
      sig = signal(initial);
      this.map.set(id, sig);
    }
    return sig;
  }

  clear(id: string): void {
    this.map.delete(id);
  }

  clearAll(): void {
    this.map.clear();
  }
}

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

export function getStreamingSig(messageID: string): ReturnType<typeof signal<string>> | undefined {
  return streamingTextSigs.get(messageID);
}

export function getReasoningSig(messageID: string): ReturnType<typeof signal<string>> | undefined {
  return streamingReasoningSigs.get(messageID);
}

export function getToolCallSig(toolID: string): ReturnType<typeof signal<ToolCall>> | undefined {
  return toolCallSigs.get(toolID);
}

export function getCrewSig(messageID: string): ReturnType<typeof signal<Crew>> | undefined {
  return crewSigs.get(messageID);
}

export function ensureStreamingSig(
  messageID: string,
  initial: string,
): ReturnType<typeof signal<string>> {
  return streamingTextSigs.ensure(messageID, initial);
}

export function ensureReasoningSig(
  messageID: string,
  initial: string,
): ReturnType<typeof signal<string>> {
  return streamingReasoningSigs.ensure(messageID, initial);
}

export function ensureToolCallSig(
  toolID: string,
  initial: ToolCall,
): ReturnType<typeof signal<ToolCall>> {
  return toolCallSigs.ensure(toolID, initial);
}

export function ensureCrewSig(messageID: string, initial: Crew): ReturnType<typeof signal<Crew>> {
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
): ReturnType<typeof signal<string>> {
  return blockTextSigs.ensure(blockKey(messageID, blockIndex), initial);
}

export function getBlockTextSig(
  messageID: string,
  blockIndex: number,
): ReturnType<typeof signal<string>> | undefined {
  return blockTextSigs.get(blockKey(messageID, blockIndex));
}

export function ensureBlockThinkingSig(
  messageID: string,
  blockIndex: number,
  initial: string,
): ReturnType<typeof signal<string>> {
  return blockThinkingSigs.ensure(blockKey(messageID, blockIndex), initial);
}

export function getBlockThinkingSig(
  messageID: string,
  blockIndex: number,
): ReturnType<typeof signal<string>> | undefined {
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

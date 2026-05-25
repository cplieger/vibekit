// ---------------------------------------------------------------------------
// Event bus: decoupled cross-module communication.
//
// Two surfaces share the same registry:
//   onSSE(type, fn)   — typed subscription for SSE events. Payload type is
//                       inferred from the SSEPayloads map, callers don't
//                       unwrap `unknown`.
//   onBus(event, fn)  — typed subscription for cross-module notifications
//                       that don't fit the SSE surface (e.g. keys:escape,
//                       transport:gap). Payloads inferred from BusPayloads.
//   dispatch(evt)     — routes an incoming SSE event to every onSSE handler.
//   emitBus(event, …) — emits a typed bus event to every onBus handler.
// ---------------------------------------------------------------------------

import type {
  ServerEvent, ChatHeader, Message, MessageChunkPayload, ToolCall,
  TurnEndedPayload, PermissionNeeded, ErrorPayload, ConnectedPayload,
  MCPConnectedPayload, MCPOAuthPayload, MCPFailedPayload, MCPDisconnectedPayload,
  AvailableCommand,
  PendingChangeAddedPayload, PendingChangeResolvedPayload, PendingChangesClearedPayload,
  PendingTrustEnabledPayload, PendingTrustClearedPayload,
} from "./types.js";

// --- Typed SSE surface ---

/** Payload shape per SSE event type. Events with no payload use `void`;
 *  events with a well-known shape get their own entry. Events not listed
 *  here fall through to `unknown` and can still be subscribed via `on`. */
export interface SSEPayloads {
  readonly connected: ConnectedPayload;
  readonly chat_created: ChatHeader;
  readonly chat_updated: ChatHeader;
  readonly chat_deleted: { readonly id: string };
  readonly message_appended: Message;
  readonly message_created: Message;
  readonly message_updated: Message;
  readonly message_chunk: MessageChunkPayload;
  readonly tool_call: { readonly message_id: string; readonly tool_call: ToolCall };
  readonly tool_call_update: { readonly message_id: string; readonly tool_call: ToolCall };
  readonly turn_ended: TurnEndedPayload;
  readonly permission_needed: PermissionNeeded;
  readonly error: ErrorPayload;
  readonly settings_updated: void;
  readonly mcp_config_changed: void;
  readonly mcp_connected: MCPConnectedPayload;
  readonly mcp_oauth_needed: MCPOAuthPayload;
  readonly mcp_failed: MCPFailedPayload;
  readonly mcp_disconnected: MCPDisconnectedPayload;
  readonly mcp_prewarm: { readonly package: string; readonly state: string };
  readonly commands_updated: { readonly commands: AvailableCommand[]; readonly prompts?: AvailableCommand[] };
  readonly mode_changed: { readonly mode_id: string };
  readonly compaction_started: void;
  readonly working_label: { readonly label: string };
  readonly subagent_activity: { readonly sub_session_id: string; readonly event: unknown };
  /** Reserved for future crew-card auto-refresh; currently unused. */
  readonly session_list_updated: { readonly sessions: unknown[] };
  readonly steering_loaded: { readonly documents: string[] };
  readonly terminal_created: { readonly terminal_id: string; readonly command: string; readonly args?: string[] };
  readonly terminal_output: { readonly terminal_id: string; readonly data: string };
  readonly terminal_exited: { readonly terminal_id: string; readonly exit_code: number };
  readonly checkpoint_restored: { readonly tag: string; readonly message_count: number };
  readonly conflict_detected: {
    readonly path: string;
    readonly other_chat: string;
    readonly expected_sha: string;
    readonly actual_sha: string;
    readonly tag: string;
    readonly ts: number;
  };
  readonly forges_changed: void;
  readonly pending_change_added: PendingChangeAddedPayload;
  readonly pending_change_resolved: PendingChangeResolvedPayload;
  readonly pending_changes_cleared: PendingChangesClearedPayload;
  readonly pending_trust_enabled: PendingTrustEnabledPayload;
  readonly pending_trust_cleared: PendingTrustClearedPayload;
}

export type SSEHandler<K extends keyof SSEPayloads> = SSEPayloads[K] extends void
  ? (chatID: string) => void
  : (chatID: string, payload: SSEPayloads[K]) => void;

type AnyHandler = (...args: unknown[]) => void;

const sseHandlers = new Map<string, Set<AnyHandler>>();
const busHandlers = new Map<string, Set<AnyHandler>>();

/** Subscribe to an SSE event with a typed payload. Returns an unsubscribe
 *  function. */
export function onSSE<K extends keyof SSEPayloads>(
  type: K, fn: SSEHandler<K>,
): () => void {
  let set = sseHandlers.get(type as string);
  if (set === undefined) {
    set = new Set();
    sseHandlers.set(type as string, set);
  }
  set.add(fn as AnyHandler);
  return (): void => { set.delete(fn as AnyHandler); };
}

/** Route an incoming SSE event to all onSSE handlers registered for its
 *  type. Called by transport.ts when an event arrives. */
export function dispatch(evt: ServerEvent): void {
  const set = sseHandlers.get(evt.type);
  if (set === undefined) return;
  const chatID = evt.chat_id ?? "";
  for (const fn of [...set]) {
    try {
      fn(chatID, evt.payload);
    } catch (e) {
      console.error(`[bus] SSE handler error for "${evt.type}":`, e);
    }
  }
}

// --- Generic cross-module bus ---

// --- Typed bus event constants ---

export const BUS_TURN_IDLE = "turn:idle" as const;
export const BUS_TRANSPORT_GAP = "transport:gap" as const;
export const BUS_KEYS_ESCAPE = "keys:escape" as const;
export const BUS_PENDING_ADDED = "pending:added" as const;
export const BUS_PENDING_RESOLVED = "pending:resolved" as const;
export const BUS_PENDING_CLEARED = "pending:cleared" as const;
export const BUS_PENDING_TRUST_ENABLED = "pending:trust-enabled" as const;
export const BUS_PENDING_TRUST_CLEARED = "pending:trust-cleared" as const;
export const BUS_ACTIVATE_CHAT = "chat:activate" as const;

import type { PendingChange } from "./types.js";

/** Payload shape per bus event. Events with no payload use `void`. */
export interface BusPayloads {
  readonly [BUS_TURN_IDLE]: string; // chatID
  readonly [BUS_TRANSPORT_GAP]: { lastSeen: number; floor: number; head: number };
  readonly [BUS_KEYS_ESCAPE]: void;
  readonly [BUS_PENDING_ADDED]: { chatID: string; change: PendingChange };
  readonly [BUS_PENDING_RESOLVED]: { chatID: string; toolCallID: string; action: string };
  readonly [BUS_PENDING_CLEARED]: { chatID: string; reason: string };
  readonly [BUS_PENDING_TRUST_ENABLED]: { chatID: string };
  readonly [BUS_PENDING_TRUST_CLEARED]: { chatID: string; reason: string };
  readonly [BUS_ACTIVATE_CHAT]: { chatID: string; then?: () => void };
}

export type BusHandler<K extends keyof BusPayloads> = BusPayloads[K] extends void
  ? () => void
  : (payload: BusPayloads[K]) => void;

/** Subscribe to a typed bus event. Returns an unsubscribe function. */
export function onBus<K extends keyof BusPayloads>(
  event: K, fn: BusHandler<K>,
): () => void {
  let set = busHandlers.get(event);
  if (set === undefined) {
    set = new Set();
    busHandlers.set(event, set);
  }
  set.add(fn as AnyHandler);
  return (): void => { set.delete(fn as AnyHandler); };
}

/** Emit a typed bus event. */
export function emitBus<K extends keyof BusPayloads>(
  ...args: BusPayloads[K] extends void ? [event: K] : [event: K, payload: BusPayloads[K]]
): void {
  const [event, ...rest] = args;
  const set = busHandlers.get(event);
  if (set === undefined) return;
  for (const fn of [...set]) {
    try {
      fn(...rest);
    } catch (e) {
      console.error(`[bus] handler error for "${event}":`, e);
    }
  }
}

// --- SSE payload decoder registry ---
//
// Opt-in runtime shape validation at the SSE decode boundary.
// transport.ts looks up a registered decoder for each parsed event's
// type; if present, the decoder runs on `evt.payload` and either
// produces a typed payload (handlers continue with confidence) or
// throws (transport drops the event with a structured log).
//
// Events without a registered decoder fall through to the existing
// untyped path — this keeps the integration additive and per-event
// opt-in. See validators.ts for the available decoders and
// app.ts (or a dedicated boot module) for the registration call.

import type { Decoder } from "./validators.js";

const sseDecoders = new Map<keyof SSEPayloads, Decoder<unknown>>();

/** Register a runtime decoder for the given SSE event type. The decoder
 *  is invoked on the parsed `payload` field before handlers fire.
 *  Calling twice for the same type replaces the prior registration. */
export function registerSSEDecoder<K extends keyof SSEPayloads>(
  type: K,
  decoder: Decoder<SSEPayloads[K]>,
): void {
  sseDecoders.set(type, decoder as Decoder<unknown>);
}

/** Returns the registered decoder for `type`, or undefined if none.
 *  Used by transport.ts on each inbound event. */
export function lookupSSEDecoder(type: string): Decoder<unknown> | undefined {
  return sseDecoders.get(type as keyof SSEPayloads);
}

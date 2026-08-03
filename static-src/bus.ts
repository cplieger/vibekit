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

import { createBus } from "@cplieger/reactive";

import type {
  ServerEvent,
  ChatHeader,
  Message,
  MessageChunkPayload,
  ToolCallPayload,
  ToolCallUpdatePayload,
  TurnEndedPayload,
  TurnStatePayload,
  PermissionNeeded,
  ErrorPayload,
  ConnectedPayload,
  MCPConnectedPayload,
  MCPOAuthPayload,
  MCPFailedPayload,
  MCPDisconnectedPayload,
  PendingChangeAddedPayload,
  PendingChangeResolvedPayload,
  PendingChangesClearedPayload,
  PendingTrustEnabledPayload,
  PendingTrustClearedPayload,
  ElicitationNeededPayload,
  UserInputNeededPayload,
  OpenExternalURLPayload,
  CodeReferencesPayload,
  PermissionsChangedPayload,
  PolicyErrorPayload,
  SafetyStatusPayload,
  SafetyPropertiesPayload,
  GovernanceStatePayload,
  ToolJobChangedPayload,
  ToolJobOutputPayload,
} from "./types.js";

// --- Typed SSE surface ---

/** Payload shape per SSE event type. Events with no payload use `undefined`;
 *  events with a well-known shape get their own entry. Events not listed
 *  here fall through to `unknown` and can still be subscribed via `on`. */
export interface SSEPayloads {
  readonly connected: ConnectedPayload;
  readonly chat_created: ChatHeader;
  readonly chat_updated: ChatHeader;
  readonly chat_deleted: { readonly id: string };
  readonly chat_status: { readonly status?: string; readonly description?: string };
  readonly message_appended: Message;
  readonly message_created: Message;
  readonly message_updated: Message;
  readonly message_chunk: MessageChunkPayload;
  readonly code_references: CodeReferencesPayload;
  readonly tool_call: ToolCallPayload;
  readonly tool_call_update: ToolCallUpdatePayload;
  readonly turn_ended: TurnEndedPayload;
  readonly turn_state: TurnStatePayload;
  readonly permission_needed: PermissionNeeded;
  readonly permissions_changed: PermissionsChangedPayload;
  readonly policy_error: PolicyErrorPayload;
  readonly elicitation_needed: ElicitationNeededPayload;
  readonly user_input_needed: UserInputNeededPayload;
  readonly error: ErrorPayload;
  readonly settings_updated: undefined;
  readonly mcp_config_changed: undefined;
  readonly mcp_connected: MCPConnectedPayload;
  readonly mcp_oauth_needed: MCPOAuthPayload;
  readonly mcp_failed: MCPFailedPayload;
  readonly mcp_disconnected: MCPDisconnectedPayload;
  readonly mcp_prewarm: { readonly package: string; readonly state: string };
  readonly mode_changed: { readonly mode_id: string };
  readonly safety_status: SafetyStatusPayload;
  readonly safety_properties: SafetyPropertiesPayload;
  readonly governance_state: GovernanceStatePayload;
  readonly open_external_url: OpenExternalURLPayload;
  readonly compaction_started: undefined;
  readonly working_label: { readonly label: string };
  readonly terminal_created: {
    readonly terminal_id: string;
    readonly command: string;
    readonly args?: string[];
  };
  readonly terminal_output: { readonly terminal_id: string; readonly data: string };
  readonly terminal_exited: {
    readonly terminal_id: string;
    readonly exit_code?: number;
    readonly signal?: string;
  };
  readonly forges_changed: undefined;
  readonly hooks_changed: undefined;
  readonly tool_job_changed: ToolJobChangedPayload;
  readonly tool_job_output: ToolJobOutputPayload;
  readonly pending_change_added: PendingChangeAddedPayload;
  readonly pending_change_resolved: PendingChangeResolvedPayload;
  readonly pending_changes_cleared: PendingChangesClearedPayload;
  readonly pending_trust_enabled: PendingTrustEnabledPayload;
  readonly pending_trust_cleared: PendingTrustClearedPayload;
}

export type SSEHandler<K extends keyof SSEPayloads> = SSEPayloads[K] extends undefined
  ? (chatID: string) => void
  : (chatID: string, payload: SSEPayloads[K]) => void;

type AnyHandler = (...args: unknown[]) => void;

/** Snapshot-cached handler set. Rebuilds the iteration array only when
 *  the set is mutated (add/delete), not on every dispatch. Preserves
 *  the guarantee that handlers unsubscribed during iteration still fire
 *  (they were in the snapshot at dispatch time). */
interface HandlerSlot {
  set: Set<AnyHandler>;
  snapshot: AnyHandler[];
  dirty: boolean;
}

function getSlot(map: Map<string, HandlerSlot>, key: string): HandlerSlot {
  let slot = map.get(key);
  if (slot === undefined) {
    slot = { set: new Set(), snapshot: [], dirty: false };
    map.set(key, slot);
  }
  return slot;
}

function addHandler(map: Map<string, HandlerSlot>, key: string, fn: AnyHandler): () => void {
  const slot = getSlot(map, key);
  slot.set.add(fn);
  slot.dirty = true;
  return (): void => {
    slot.set.delete(fn);
    slot.dirty = true;
  };
}

function getSnapshot(slot: HandlerSlot): AnyHandler[] {
  if (slot.dirty) {
    slot.snapshot = Array.from(slot.set);
    slot.dirty = false;
  }
  return slot.snapshot;
}

const sseHandlers = new Map<string, HandlerSlot>();

/** Subscribe to an SSE event with a typed payload. Returns an unsubscribe
 *  function. */
export function onSSE<K extends keyof SSEPayloads>(type: K, fn: SSEHandler<K>): () => void {
  return addHandler(sseHandlers, type, fn as AnyHandler);
}

/** Route an incoming SSE event to all onSSE handlers registered for its
 *  type. Called by transport.ts when an event arrives. */
export function dispatch(evt: ServerEvent): void {
  const slot = sseHandlers.get(evt.type);
  if (slot === undefined) {
    return;
  }
  const fns = getSnapshot(slot);
  const chatID = evt.chat_id ?? "";
  for (const fn of fns) {
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

/** Payload shape per bus event. Events with no payload use `undefined`. */
interface BusPayloads {
  readonly [BUS_TURN_IDLE]: string; // chatID
  readonly [BUS_TRANSPORT_GAP]: { lastSeen: number; floor: number; head: number };
  readonly [BUS_KEYS_ESCAPE]: undefined;
  readonly [BUS_PENDING_ADDED]: { chatID: string; change: PendingChange };
  readonly [BUS_PENDING_RESOLVED]: { chatID: string; toolCallID: string; action: string };
  readonly [BUS_PENDING_CLEARED]: { chatID: string; reason: string };
  readonly [BUS_PENDING_TRUST_ENABLED]: { chatID: string };
  readonly [BUS_PENDING_TRUST_CLEARED]: { chatID: string; reason: string };
  readonly [BUS_ACTIVATE_CHAT]: { chatID: string; then?: () => void };
}

// The generic cross-module bus is backed by @cplieger/reactive's createBus
// (the SSE surface above stays bespoke: it routes ServerEvents with a
// chatID-prepended handler shape + a decoder registry).
const bus = createBus<BusPayloads>();

/** Subscribe to a typed bus event. Returns an unsubscribe function. */
export const onBus = bus.on;

/** Emit a typed bus event. */
export const emitBus = bus.emit;

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
  sseDecoders.set(type, decoder);
}

/** Returns the registered decoder for `type`, or undefined if none.
 *  Used by transport.ts on each inbound event. */
export function lookupSSEDecoder(type: string): Decoder<unknown> | undefined {
  return sseDecoders.get(type as keyof SSEPayloads);
}

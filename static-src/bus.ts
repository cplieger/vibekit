// ---------------------------------------------------------------------------
// Event bus: decoupled cross-module communication.
//
// Two surfaces share the same registry:
//   onSSE(type, fn)   — typed subscription for SSE events. Payload type is
//                       inferred from the SSEPayloads map, callers don't
//                       unwrap `unknown`.
//   onBus(event, fn)  — typed subscription for cross-module notifications
//                       that don't fit the SSE surface (e.g. keys:escape,
//                       transport:reconcile). Payloads inferred from BusPayloads.
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
  SteerQueuedPayload,
  SteerInjectedPayload,
  SteerClearedPayload,
  AgentNoticePayload,
  PendingSnapshotPayload,
  StatusSnapshotPayload,
  TabsChangedPayload,
  PermissionNeeded,
  ErrorPayload,
  ConnectedPayload,
  MCPConnectedPayload,
  MCPOAuthPayload,
  MCPFailedPayload,
  MCPDisconnectedPayload,
  ElicitationNeededPayload,
  UserInputNeededPayload,
  DecisionSettledPayload,
  DraftChangedPayload,
  OpenExternalURLPayload,
  CodeReferencesPayload,
  PermissionsChangedPayload,
  PolicyErrorPayload,
  SafetyStatusPayload,
  SafetyPropertiesPayload,
  GovernanceStatePayload,
  ToolJobChangedPayload,
  ToolJobOutputPayload,
  TerminalCreatedPayload,
  TerminalOutputPayload,
  TerminalExitedPayload,
  RunStartedPayload,
  RunStepPayload,
  RunProgressPayload,
  RunFinishedPayload,
  RunInputNeededPayload,
  RunInputSettledPayload,
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
  // Mid-turn steering, three signals kept apart on purpose: queued = KAS's
  // buffer has it, injected = the model has read it, cleared = the turn
  // boundary dropped it unread. Collapsing them would hide the only
  // distinction that matters to somebody correcting a running turn.
  readonly steer_queued: SteerQueuedPayload;
  readonly steer_injected: SteerInjectedPayload;
  readonly steer_cleared: SteerClearedPayload;
  // The agent's voice on the steering channel: a workflow step or a subagent
  // reporting progress into the session that launched it. Its own event so no
  // consumer has to decide whose words a steer holds.
  readonly agent_notice: AgentNoticePayload;
  // The two connect-hook aggregates, one per workspace-wide digest subject: the WHOLE
  // pending set (every unanswered permission, run ask and steer, as raw envelopes to
  // re-dispatch) and the WHOLE retained waiting-status set, each possibly empty. An
  // empty one is the case that matters — a per-item replay of an empty set writes
  // nothing, so a row resolved elsewhere would stay on screen.
  readonly pending_snapshot: PendingSnapshotPayload;
  readonly status_snapshot: StatusSnapshotPayload;
  // The server could not publish a frame (over the frame cap) and names the subject
  // it would have moved instead; the transport runs that subject's refetch and
  // observes nothing. Empty payload: the envelope's `subject` is the message.
  readonly subject_changed: undefined;
  // ONE aggregate frame per COMMITTED mutation of the open-tab set. One event
  // rather than a membership event beside an order event: two types can be
  // applied in either order by a client, and a close of a parent with children
  // is one mutation, so a singular event would have forced either several frames
  // sharing one version or several bumps for one mutation. Removal is STATED per
  // id in `removed_ids` and never inferred from absence — inferring it is what
  // closed tabs nobody closed. `version` is the client's only watermark and only
  // an EVENT may advance it; see tabs.ts applyTabsChanged for the three rules.
  readonly tabs_changed: TabsChangedPayload;
  readonly permission_needed: PermissionNeeded;
  readonly permissions_changed: PermissionsChangedPayload;
  readonly policy_error: PolicyErrorPayload;
  readonly elicitation_needed: ElicitationNeededPayload;
  readonly user_input_needed: UserInputNeededPayload;
  // One event retires any of the three asks above on the surfaces that did not
  // answer it, which is every surface but one: they are all offered the same
  // decision and only the first answer is accepted.
  readonly decision_settled: DecisionSettledPayload;
  readonly draft_changed: DraftChangedPayload;
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
  // The generated wire types, not a hand-written restatement of them. The three
  // shapes here used to be inline object literals that had already fallen behind
  // the server: `terminal_output` gained `spans` and `offset`, and a payload
  // shape declared twice tracks the server in only one of its copies.
  readonly terminal_created: TerminalCreatedPayload;
  readonly terminal_output: TerminalOutputPayload;
  readonly terminal_exited: TerminalExitedPayload;
  readonly forges_changed: undefined;
  readonly hooks_changed: undefined;
  readonly tool_job_changed: ToolJobChangedPayload;
  readonly tool_job_output: ToolJobOutputPayload;
  readonly run_started: RunStartedPayload;
  readonly run_progress: RunProgressPayload;
  readonly run_finished: RunFinishedPayload;
  readonly run_step: RunStepPayload;
  // A workflow STEP asking a person a question, and the second run event that
  // carries its payload rather than saying "refetch". It has to: KAS parks the run
  // with one fixed pauseReason literal and an empty pauseDetail, so `inspect` says
  // a step wants input and never says what it asked. Keyed to the LAUNCHING chat
  // when the run has one and to `run:<workflowId>` when it does not, which is the
  // same pair the three request-shaped asks use.
  readonly run_input_needed: RunInputNeededPayload;
  // Its twin: retire the card on every surface that did not answer. Separate from
  // decision_settled because a run ask is identified by a string rather than by an
  // int64 request id.
  readonly run_input_settled: RunInputSettledPayload;
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
/** Every claim this client holds is void and the whole projection is re-read: the
 *  stream bound to a new hub epoch (a server restart), or the digest answered
 *  `must_refetch`. The payload names the cause and carries the revalidation's signal,
 *  which every fetch the reconcile issues takes. */
export const BUS_RECONCILE = "transport:reconcile" as const;
/** The page came back from a suspension real time passed under. Distinct from a
 *  RECONCILE, which says the held state is void: a resume undermines only the views
 *  whose kind has no digest subject, so it nudges the active view and nothing else. */
export const BUS_PAGE_RESUMED = "transport:resumed" as const;
export const BUS_KEYS_ESCAPE = "keys:escape" as const;
export const BUS_ACTIVATE_CHAT = "chat:activate" as const;
/** A workflow run appeared or reached a terminal state, so any list of runs is
 *  stale. On the bus rather than a direct call because the run handler and the
 *  history page are two UI affordances that should not know about each other —
 *  and concretely because importing the history page from a handler drags the
 *  whole chat module in behind it. */
export const BUS_RUNS_CHANGED = "runs:changed" as const;
/** The active tab changed, so any affordance scoped to the tab you were LOOKING
 *  at is now scoped to nothing.
 *
 *  On the bus rather than a direct call because the tab store must not know what
 *  a search box is, and because the alternative — hiding the affordance with its
 *  view — is what let the transcript's find survive a tab switch with its
 *  observer connected, its highlights welded into the DOM and its
 *  search-opened folds still open. A view becoming invisible is not the same
 *  event as a feature closing, and only the second one runs a teardown. */
export const BUS_TAB_CHANGED = "tabs:changed" as const;
/** An editor buffer's first read settled: its bytes arrived, or the read failed
 *  and `FileState.error` says so. `FileState.loaded` is a plain field, so nothing
 *  reactive can observe the arrival, and the writes around it (`current`, then
 *  `loaded`, then the repaint) flush effects one at a time, so an effect on any
 *  one signal would run mid-sequence. The in-file find re-runs on this instead:
 *  it opened over a buffer that had not arrived and owes an answer once it has.
 *  On the bus because the loader must not know what a find bar is. */
export const BUS_EDITOR_FILE_LOADED = "editor:loaded" as const;

/** Payload shape per bus event. Events with no payload use `undefined`. */
interface BusPayloads {
  readonly [BUS_TURN_IDLE]: string; // chatID
  readonly [BUS_RECONCILE]: { readonly cause: string; readonly signal: AbortSignal };
  readonly [BUS_PAGE_RESUMED]: undefined;
  readonly [BUS_KEYS_ESCAPE]: undefined;
  readonly [BUS_ACTIVATE_CHAT]: { chatID: string; then?: () => void };
  readonly [BUS_RUNS_CHANGED]: undefined;
  readonly [BUS_TAB_CHANGED]: { to: string; kind: string | null };
  readonly [BUS_EDITOR_FILE_LOADED]: { path: string };
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

import { type Decoder, asObject, reqStr } from "./validators.js";
import { decodeSubjectStamp } from "./wire/decoders.gen.js";

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

/** Returns the registered decoder for `type`, or undefined if none. */
export function lookupSSEDecoder(type: string): Decoder<unknown> | undefined {
  return sseDecoders.get(type as keyof SSEPayloads);
}

/** Decode one parsed envelope into a `ServerEvent`, or throw. The payload goes through
 *  its registered decoder when one exists and falls through untyped otherwise; a
 *  `subject` stamp is decoded whenever present. ONE door for both carriers: the
 *  transport's live frames and the `pending_snapshot` items the connect hook re-sends,
 *  which are whole envelopes the live path would have published. A throw drops the
 *  event, so no handler sees a partial shape. */
export function decodeEnvelope(raw: unknown): ServerEvent {
  const o = asObject(raw, "$.event");
  const type = reqStr(o, "type", "$.event");
  // The wire's type string is trusted as a member: an unknown type reaches no handler.
  const evt: ServerEvent = { type: type as keyof SSEPayloads };
  const chatID = o["chat_id"];
  if (typeof chatID === "string") {
    evt.chat_id = chatID;
  }
  const decoder = lookupSSEDecoder(type);
  if (o["payload"] !== undefined) {
    evt.payload = decoder === undefined ? o["payload"] : decoder(o["payload"]);
  }
  if (o["subject"] !== undefined && o["subject"] !== null) {
    evt.subject = decodeSubjectStamp(o["subject"]);
  }
  return evt;
}

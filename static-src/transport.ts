// ---------------------------------------------------------------------------
// Transport: SSE for server→client, fetch POST for client→server.
//
// Every command carries an Idempotency-Key header so the server can dedupe
// retries; an action that declares idempotencyKey supplies its own, which is
// what makes a RETRY dedupe (see send). The dispatcher receives typed
// ServerEvents from SSE.
//
// Errors surface through send-state.ts (which drives the send-button
// error face + tooltip). There is no inline error card; the button is
// the single error surface, and it stays clickable so the next Send is the
// retry. A plain 409 busy is a handshake, not an error — callers steer
// instead; a 409 whose envelope carries `reason: "starting"` is a real
// failure the caller renders through the send-error face (see SendResult).
//
// Reconnect model:
//   - EventSource's native auto-reconnect covers transient drops with
//     a spec-defined retry (Firefox/Chrome default 3s, resettable via
//     `retry:` fields the server doesn't emit).
//   - On terminal errors (SSE stream ends with a non-2xx body, proxy
//     strips keep-alives, iOS backgrounds the tab long enough that the
//     browser closes the source), `readyState` transitions to CLOSED
//     and the browser gives up. We handle this by tearing down the
//     current EventSource and reopening with exponential backoff.
//   - visibilitychange + pageshow kick an immediate reconnect attempt
//     when the tab returns to the foreground. Covers the common case
//     where iOS killed the stream while the tab was in the background.
// ---------------------------------------------------------------------------

import type { ServerEvent, ConnectedPayload, ConnectionStatus, TabKind } from "./types.js";
import { setSSEStatus } from "./send-state.js";
import { reportFailure } from "./failure-notice.js";
import { emitBus, BUS_TRANSPORT_GAP, lookupSSEDecoder } from "./bus.js";
import {
  registerCleanup,
  hasErrorString,
  IDEMPOTENCY_HEADER,
  IDEMPOTENCY_COMMAND_FIELD,
} from "./actions/index.js";
import { computeBackoff } from "./lib/backoff.js";

type MsgHandler = (evt: ServerEvent) => void;
type StatusHandler = (s: ConnectionStatus) => void;

type CommandType =
  | "prompt"
  | "cancel"
  | "delete_chat"
  | "switch_model"
  | "set_supervised_mode"
  | "permission_response"
  | "elicitation_response"
  | "set_effort"
  | "set_draft"
  | "set_mode"
  | "rewind_chat";

export interface Command {
  type: CommandType;
  chat_id?: string;
  payload?: Record<string, unknown>;
}

// --- Typed command discriminated union ---
// Provides compile-time payload validation for each command type.
// The wire format is unchanged (JSON.stringify produces the same output).

export type TypedCommand =
  | {
      type: "prompt";
      chat_id: string;
      payload: {
        text: string;
        attachments?: readonly unknown[];
        message_id?: string;
        model?: string;
      };
    }
  | { type: "cancel"; chat_id: string }
  | { type: "delete_chat"; chat_id: string }
  | { type: "switch_model"; chat_id: string; payload: { model: string } }
  | { type: "set_supervised_mode"; chat_id: string; payload: { enabled: boolean } }
  | {
      type: "permission_response";
      chat_id: string;
      // file_decisions answers a TURN APPROVAL on the ordinary permission
      // reply: action id → keep. Omitting an id it offered is a rollback.
      payload: {
        request_id: number;
        option_id: string;
        file_decisions?: Record<string, boolean>;
      };
    }
  | {
      type: "elicitation_response";
      chat_id: string;
      payload: { request_id: number; action: string; content?: Record<string, unknown> };
    }
  | { type: "set_effort"; chat_id: string; payload: { level: string } }
  // The composer text typed and not sent. An empty string is a real value: it
  // is how a sent or abandoned draft is cleared.
  | { type: "set_draft"; chat_id: string; payload: { text: string } }
  | { type: "set_mode"; chat_id: string; payload: { mode_id: string } }
  // Addresses a USER MESSAGE, not a turn ordinal: KAS's revertMultiple takes a
  // messageId and refuses a non-user one.
  | { type: "rewind_chat"; chat_id: string; payload: { message_id: string } }
  // The three CREATING commands, and they are the only members here with NO
  // `chat_id`: the server mints the chat id and returns it, so there is nothing
  // for the envelope to address. That absence is the wire contract of stage 1b,
  // which is why it is spelled in the type rather than left to a runtime default.
  //
  // `op_id` correlates every attempt of ONE create gesture so a repeat resolves to
  // the chat the first attempt made. It is NOT the idempotency token — that is the
  // header, per-dispatch, in a 5-minute cache; this one has to survive a retry the
  // user makes minutes later. See command/create_ledger.go.
  | { type: "create_chat"; payload: { op_id: string; name?: string; model?: string } }
  | { type: "resume_session"; payload: { op_id: string; session_id: string; name: string } }
  | { type: "fork_chat"; payload: { op_id: string; parent_chat_id: string; title?: string } }
  // The four TAB mutations, and they have no `chat_id` for a reason of their own:
  // the open-tab set is workspace-global, so a tab command addresses a tab or a
  // whole arrangement, never a conversation. A chat TAB names its chat in `ref`,
  // which is a subject field rather than an envelope one — the envelope's chat id
  // routes an event to a chat's topic, and `tabs_changed` is broadcast to every
  // client with no topic at all.
  //
  // Every one carries `op_id`, echoed back on the frame so a client can tell its
  // own committed mutation from another device's. That is its only job here: no
  // TTL, no cache, no 409 branch, and no authority over what is open.
  | {
      type: "open_tab";
      payload: { kind: TabKind; op_id: string; ref?: string; parent?: string; owns?: boolean };
    }
  | { type: "close_tab"; payload: { id: string; op_id: string } }
  | { type: "reorder_tabs"; payload: { order: string[]; op_id: string } }
  | { type: "pin_tab"; payload: { id: string; pinned: boolean; op_id: string } };

const TRANSPORT_ERROR_CODES = {
  TIMEOUT: "timeout",
  CANCELLED: "cancelled",
  NETWORK: "network",
} as const;

export interface SendResult {
  ok: boolean;
  /** HTTP status. 0 for non-HTTP failures (timeout, network). */
  status: number;
  /** Server error message, if any. */
  error?: string;
  /** Machine-readable failure class from the error envelope's additive
   *  `reason` field (internal/command writeErr), so a caller branches on a
   *  VALUE rather than on error prose. One reason exists today: "starting",
   *  on the 409 prompt refusal whose admission holder cannot receive a steer
   *  (a cold spawn, a shell, a prime). */
  reason?: string;
  /** Structured error code for non-HTTP failures. */
  code?: string;
  /** The success body, undecoded. Present only when the response parsed as JSON.
   *
   *  Almost every command answers `{"ok":true}` and its caller reads nothing, so
   *  this stayed unread for a long time. The creating commands are what need it:
   *  `create_chat`, `fork_chat` and `resume_session` MINT the chat id server-side
   *  now, so the response is the only place the caller can learn the id of the
   *  thing it just asked for. The alternative was waiting for the `chat_created`
   *  SSE frame, which is the identity-the-caller-cannot-address problem inverted.
   *
   *  Undecoded on purpose: this module owns the transport, and which wire shape a
   *  given command answers with is the action's business. */
  body?: unknown;
}

interface SendOptions {
  /** Caller-supplied signal for cancellation (e.g. on tab close or chat delete). */
  signal?: AbortSignal;
  /** Timeout in ms. Defaults to 15 minutes. */
  timeoutMs?: number;
  /** When true (default), a failure is reported to the user through
   *  failure-notice.ts (a bottom-right toast naming the reason). The action
   *  framework adapter (transportAction) passes false because it owns the error
   *  surface via its own toast — letting both fire produces duplicate user
   *  feedback for one failure.
   *
   *  It no longer touches the send button: an attempt that failed is not a claim
   *  that the agent is unreachable, and that button now says only the latter. See
   *  send-state.ts. */
  reportSendState?: boolean;
}

/** Read the key an action attached to its command, if any.
 *
 *  The actions framework puts it on the command object under
 *  IDEMPOTENCY_COMMAND_FIELD. It is NOT forwarded as a body field: the server's
 *  envelope has no such member and never read one, so sending it would be a
 *  third spelling of a concept that now has exactly one. It becomes the header
 *  instead. */
function idempotencyKeyOf(cmd: TypedCommand | Command): string | undefined {
  const v = (cmd as Record<string, unknown>)[IDEMPOTENCY_COMMAND_FIELD];
  return typeof v === "string" && v !== "" ? v : undefined;
}

/** The chat a command is addressed to, or "" when it addresses none.
 *
 *  A helper rather than three `cmd.chat_id ?? ""` reads, because since the
 *  creating commands lost their `chat_id` the union genuinely has members without
 *  the field and each read would need its own narrowing. "" is what the envelope
 *  carries for them, and it is also what `reportFailure` treats as workspace-wide,
 *  which is correct: a failed create belongs to no chat. */
function chatIDOf(cmd: TypedCommand | Command): string {
  return "chat_id" in cmd ? (cmd.chat_id ?? "") : "";
}

/** Generate a client-side request id (also used as a message id). */
export function newRequestID(): string {
  const arr = new Uint8Array(10);
  crypto.getRandomValues(arr);
  let out = "r-" + Date.now().toString(36) + "-";
  for (const b of arr) {
    out += b.toString(36);
  }
  return out;
}

/** Generate a client-side user-message id. Shares entropy with
 *  `newRequestID()` but uses the `m-` prefix the server expects for
 *  user-generated message IDs. Use this everywhere a user message is
 *  about to be sent. */
export function newMessageID(): string {
  return newRequestID().replace("r-", "m-");
}

/** Generate a correlation id for ONE create gesture, so every attempt of it
 *  resolves to the same chat.
 *
 *  Distinct from `newRequestID()`'s idempotency role, which is per-DISPATCH and
 *  lives in a 5-minute cache. This one has to survive a retry the user makes
 *  minutes later from the failure toast, which is why the server keys its own
 *  ledger on it (command/create_ledger.go).
 *
 *  MINT IT AT THE DISPATCH SITE, never inside an action's `run()`: the framework
 *  re-invokes `run()` per retry attempt, so an id minted there would be fresh on
 *  every attempt and defeat its own purpose. The `op-` prefix keeps it inside
 *  ids.ValidIdent, which is what the command boundary gates it with. */
export function newOpID(): string {
  return newRequestID().replace("r-", "op-");
}

interface GapInfo {
  lastSeen: number;
  floor: number;
  head: number;
}

const HIDDEN_ABORT_MS = 30_000;

/** Default timeout for bridge command channel (long-running agent turns). */
const COMMAND_TIMEOUT_MS = 15 * 60 * 1000;

// ---------------------------------------------------------------------------
// TransportController: owns all SSE connection state as instance fields.
// ---------------------------------------------------------------------------

type ConnState =
  | { phase: "idle" }
  | { phase: "connecting"; source: EventSource }
  | { phase: "connected"; source: EventSource }
  | { phase: "reconnecting"; timer: ReturnType<typeof setTimeout> };

/** How long a connection must stay open before the reconnect backoff
 *  ramp resets. Resetting on mere `onopen` (or on the first message —
 *  the server writes the `connected` handshake immediately) would let a
 *  connect→drop loop hammer the server at the 500ms base forever; the
 *  documented 500ms→30s ramp only works if short-lived connections
 *  carry the previous backoff forward. */
const BACKOFF_STABLE_RESET_MS = 30_000;

/** How long the hydration gate waits for `markHydrated` before releasing what
 *  it held anyway. Generous, because it is racing three sequential HTTP
 *  round-trips (settings, whoami, the chat list) on a cold container, and the
 *  cost of expiring early is only that the store's own missing-session guards
 *  drop the frames — which is exactly the behaviour the gate replaced. */
const HYDRATE_TIMEOUT_MS = 20_000;

/** Ceiling on the held queue. A busy workspace's connect replay is tens of
 *  frames, so reaching this means hydration is not coming and the stream should
 *  move rather than grow a buffer without bound. */
const MAX_PENDING_FRAMES = 2000;

class TransportController {
  private onMsg: MsgHandler = () => {
    /* noop */
  };
  private onStatus: StatusHandler = () => {
    /* noop */
  };

  private conn: ConnState = { phase: "idle" };
  private lastSeenEventID = 0;
  /** lastSeenEventID snapshotted at the moment the CURRENT EventSource
   *  was opened — the cursor gap detection compares against. onmessage
   *  advances lastSeenEventID before handleConnected runs (the
   *  `connected` handshake frame itself carries `id: head`), so
   *  comparing the live cursor against floor/head could never detect a
   *  wrapped ring; only this pre-connect snapshot can. */
  private cursorAtConnect = 0;
  /** Date.now() at the current connection's onopen; 0 while unopened.
   *  Cleared on every connect attempt so stability is measured on the
   *  CURRENT connection, never a predecessor. */
  private openedAt = 0;
  /** The last computed reconnect backoff, carried in its own field:
   *  onerror fires from connecting/connected (never from reconnecting),
   *  so reading the previous backoff out of the conn state — the old
   *  design — restarted the ramp at 500ms on every cycle. */
  private lastBackoffMs = 0;
  private hiddenSince: number | null = null;

  /** Whether the chat store has been populated, so a frame can find the chat
   *  it names. See `holdUntilHydrated` for why this gate exists. */
  private hydrated = false;
  /** Frames that arrived before hydration, in arrival order. */
  private pending: ServerEvent[] = [];
  /** Watchdog that opens the gate if hydration never reports in. */
  private hydrateTimer: ReturnType<typeof setTimeout> | null = null;

  private readonly inflight = new Set<AbortController>();

  /** Abort every in-flight HTTP request started via this transport.
   *  Used by the global beforeunload cleanup so navigation away
   *  doesn't leak request handles. */
  cancelInflight(): void {
    for (const ctrl of this.inflight) {
      ctrl.abort();
    }
    this.inflight.clear();
  }

  /** Hold every incoming frame until the chat store has been populated, then
   *  release them in arrival order.
   *
   *  WHY. The EventSource is opened synchronously during `init`, and the server
   *  answers immediately with its whole connect replay: the `connected`
   *  handshake, every unanswered permission ask, and ONE `turn_state` per BUSY
   *  chat. That `turn_state` is the only channel carrying an in-flight turn's
   *  state to a new client — it is connect-time synthesis and is never broadcast
   *  live, so there is no second chance at it. Meanwhile the store is empty
   *  until `GET /api/chats` resolves several awaits later, and every consumer of
   *  a chat-scoped frame correctly bails when it cannot find the chat it names.
   *
   *  So on a manual page refresh the whole replay was dropped, and four
   *  user-visible defects followed from that one ordering fact: every tab's
   *  activity dot read `idle` (the busy signal never landed), the streaming
   *  transcript came back blank (the accumulated message never landed), the
   *  composer showed Send instead of Cancel over a live turn — which turns a
   *  stale draft into a mid-turn steer, because a Send that meets a 409 steers —
   *  and a reader with no dot to tell a live tab from a dead one closed the
   *  wrong ones, and closing a chat tab cancels its turn.
   *
   *  A refresh was therefore a WEAKER recovery than a dropped connection: an SSE
   *  blip reconnects with a non-zero cursor, which fires `transport:gap` and
   *  runs the full reconcile. The first connection of a page load deliberately
   *  skips that (nothing was missed yet), so nothing healed it.
   *
   *  Holding rather than replaying is what keeps the frames' ORDER intact, and
   *  order is load-bearing here: a `message_chunk` that raced the snapshot is
   *  made idempotent by the watermark the snapshot installs, so a chunk released
   *  before its `turn_state` would be double-appended. */
  private holdUntilHydrated(): void {
    this.hydrated = false;
    this.pending = [];
    if (this.hydrateTimer !== null) {
      clearTimeout(this.hydrateTimer);
    }
    // Never wedge the stream on a hydration that failed (an auth bounce, a dead
    // /api/chats). The gate is an ordering aid, not a correctness requirement:
    // the store's missing-session guards still hold underneath it.
    this.hydrateTimer = setTimeout(() => {
      if (!this.hydrated) {
        console.warn("sse: hydration did not report in, releasing held frames");
        this.markHydrated();
      }
    }, HYDRATE_TIMEOUT_MS);
  }

  /** Open the gate and drain. Idempotent, and once open it stays open — a
   *  reconnect does not re-hold, because by then the store is populated and the
   *  reconnect's own gap reconcile is the recovery path. */
  markHydrated(): void {
    if (this.hydrateTimer !== null) {
      clearTimeout(this.hydrateTimer);
      this.hydrateTimer = null;
    }
    if (this.hydrated) {
      return;
    }
    this.hydrated = true;
    const held = this.pending;
    this.pending = [];
    for (const evt of held) {
      this.onMsg(evt);
    }
  }

  init(msg: MsgHandler, status: StatusHandler): void {
    this.onMsg = msg;
    this.holdUntilHydrated();
    this.onStatus = (s) => {
      setSSEStatus(s);
      status(s);
    };
    this.connectSSE();

    document.addEventListener("visibilitychange", () => {
      if (document.visibilityState === "visible" && this.sseIsDead()) {
        this.scheduleReconnect({ delay: 0 });
      }
      // Hidden-abort logic
      if (document.visibilityState === "hidden") {
        this.hiddenSince = Date.now();
      } else {
        if (this.hiddenSince !== null && Date.now() - this.hiddenSince >= HIDDEN_ABORT_MS) {
          for (const ctrl of this.inflight) {
            ctrl.abort();
          }
          this.inflight.clear();
        }
        this.hiddenSince = null;
      }
    });
    window.addEventListener("pageshow", (e: PageTransitionEvent) => {
      if (e.persisted || this.sseIsDead()) {
        this.scheduleReconnect({ delay: 0 });
      }
    });
  }

  private sseIsDead(): boolean {
    return this.conn.phase !== "connected" && this.conn.phase !== "connecting";
  }

  // --- SSE ---

  /** Tear down the current connection state, transitioning to idle.
   *  Handles cleanup of EventSource and reconnect timers regardless of
   *  current phase. Called before (re)connecting to ensure a clean slate. */
  private teardown(): void {
    if (this.conn.phase === "idle") {
      return;
    }
    if (this.conn.phase === "reconnecting") {
      clearTimeout(this.conn.timer);
    }
    if (this.conn.phase === "connecting" || this.conn.phase === "connected") {
      try {
        this.conn.source.close();
      } catch {
        /* best-effort */
      }
    }
    this.conn = { phase: "idle" };
  }

  private connectSSE(): void {
    this.teardown();
    this.onStatus("connecting");
    this.cursorAtConnect = this.lastSeenEventID;
    this.openedAt = 0;
    const source = new EventSource("/api/events");
    this.conn = { phase: "connecting", source };
    source.onopen = (): void => {
      this.openedAt = Date.now();
      this.conn = { phase: "connected", source };
      this.onStatus("connected");
    };
    source.onmessage = (e: MessageEvent): void => {
      if (e.lastEventId !== "") {
        const id = Number(e.lastEventId);
        if (Number.isFinite(id) && id > this.lastSeenEventID) {
          this.lastSeenEventID = id;
        }
      }
      let evt: ServerEvent;
      try {
        evt = JSON.parse(e.data as string) as ServerEvent;
      } catch {
        // ignore malformed frames
        return;
      }
      // Opt-in runtime validation: if a decoder is registered for this
      // event type, run it on the payload before dispatching. On decode
      // failure, log the structured path and drop the event rather than
      // letting handlers see a partial shape. Events without a decoder
      // fall through to the existing untyped path so the integration is
      // strictly additive. See validators.ts and bus.ts for details.
      const decoder = lookupSSEDecoder(evt.type);
      if (decoder !== undefined) {
        try {
          evt = { ...evt, payload: decoder(evt.payload) };
        } catch (decodeErr) {
          const msg = decodeErr instanceof Error ? decodeErr.message : String(decodeErr);
          console.error(`sse: decoder rejected ${evt.type}:`, msg);
          return;
        }
      }
      if (evt.type === "connected") {
        this.handleConnected(evt);
      }
      if (!this.hydrated) {
        // Hold it: see holdUntilHydrated. handleConnected still ran above,
        // because that is transport bookkeeping (the cursor, the floor/head gap
        // check) and depends on no store state.
        this.pending.push(evt);
        if (this.pending.length >= MAX_PENDING_FRAMES) {
          // A queue this long means hydration is not coming. Late is better than
          // dropped, and the store's own missing-session guards are the floor.
          console.warn(`sse: ${String(this.pending.length)} frames held, releasing early`);
          this.markHydrated();
        }
        return;
      }
      this.onMsg(evt);
    };
    source.onerror = (): void => {
      if (source.readyState === EventSource.CLOSED) {
        const bo = this.nextBackoff();
        this.conn = { phase: "idle" };
        this.onStatus("disconnected");
        this.scheduleReconnect(bo);
      }
    };
  }

  private nextBackoff(): { delay: number } {
    // Reset the ramp only after the current connection stayed open for a
    // stable interval; a connection that opened and quickly died carries
    // the previous backoff forward so the ramp actually escalates.
    const stable = this.openedAt !== 0 && Date.now() - this.openedAt >= BACKOFF_STABLE_RESET_MS;
    const bo = computeBackoff(stable ? 0 : this.lastBackoffMs);
    this.lastBackoffMs = bo.backoffMs;
    return { delay: bo.delay };
  }

  private scheduleReconnect(info: { delay: number }): void {
    if (this.conn.phase === "reconnecting") {
      clearTimeout(this.conn.timer);
    }
    const timer = setTimeout(() => {
      this.connectSSE();
    }, info.delay);
    this.conn = { phase: "reconnecting", timer };
  }

  private handleConnected(evt: ServerEvent): void {
    const p = evt.payload as ConnectedPayload | undefined;
    if (p === undefined) {
      return;
    }
    const cursor = this.cursorAtConnect;
    if (cursor === 0) {
      // First connection of this page load: nothing to have missed.
      return;
    }
    // Three gap classes, all judged against the PRE-connect cursor:
    //  - floor === 0: the server restarted with an empty ring; anything
    //    we saw before is gone.
    //  - cursor < floor: the ring wrapped past our cursor during the
    //    outage — events were evicted unseen.
    //  - head < cursor: the server restarted and re-seeded ids below our
    //    cursor (ids are per-process); without this arm the stale high
    //    cursor also suppresses onmessage cursor advancement until the
    //    new process catches up.
    const gap = p.floor === 0 || cursor < p.floor || p.head < cursor;
    if (gap) {
      const info: GapInfo = { lastSeen: cursor, floor: p.floor, head: p.head };
      emitBus(BUS_TRANSPORT_GAP, info);
      // Re-align with the new server's id space so future comparisons
      // (and the next reconnect's snapshot) start from server truth.
      this.lastSeenEventID = p.head;
    }
  }

  // --- POST /api/command ---

  async send(cmd: TypedCommand | Command, opts?: SendOptions): Promise<SendResult> {
    // An action declaring `idempotencyKey` generates ONE key per dispatch and
    // the framework threads it through every retry attempt, so honouring it is
    // what makes a retry dedupe. Minting a fresh key here for such a command
    // would defeat the whole mechanism — which is exactly what this transport
    // did before: it built the body field by field, so the framework's key was
    // dropped and never reached the server at all. Rewind is the case that
    // makes it matter (a second revert cuts from an already-truncated
    // transcript), and its idempotency was decorative until this line.
    //
    // A bare send() has no such key and gets a fresh one, which is right: two
    // deliberate sends are two operations.
    const requestID = idempotencyKeyOf(cmd) ?? newRequestID();
    const timeoutMs = opts?.timeoutMs ?? COMMAND_TIMEOUT_MS;
    const ctrl = new AbortController();
    this.inflight.add(ctrl);

    const signals: AbortSignal[] = [ctrl.signal, AbortSignal.timeout(timeoutMs)];
    if (opts?.signal) {
      signals.push(opts.signal);
    }
    const combined = AbortSignal.any(signals);

    try {
      const r = await fetch("/api/command", {
        method: "POST",
        headers: {
          "Content-Type": "application/json",
          // The idempotency token is a HEADER, not a body field. It used to ride
          // the envelope as request_id, which meant the command dispatcher had to
          // run its own dedup cache keyed on a body field — and that one had no
          // in-flight marker, so two concurrent sends of the same id both
          // executed. As a header it reaches the server's one idempotency
          // middleware, which marks in-flight and answers 409 instead.
          [IDEMPOTENCY_HEADER]: requestID,
        },
        signal: combined,
        body: JSON.stringify({
          type: cmd.type,
          chat_id: chatIDOf(cmd),
          // eslint-disable-next-line @typescript-eslint/no-unnecessary-condition
          payload: "payload" in cmd && cmd.payload != null ? cmd.payload : {},
        }),
      });
      if (r.ok) {
        // A command whose body is not JSON is still a success — the status is what
        // says so, and only the creating commands read a body at all. Parsed here
        // rather than by each caller so there is one place the response is read.
        try {
          return { ok: true, status: r.status, body: await r.json() };
        } catch {
          return { ok: true, status: r.status };
        }
      }

      let errMsg = `HTTP ${String(r.status)}`;
      let reason: string | undefined;
      try {
        const d: unknown = await r.json();
        if (hasErrorString(d)) {
          errMsg = d.error;
        }
        // Lifted beside the error string: the envelope's additive `reason`
        // field is the machine-readable failure class (see SendResult.reason).
        const rawReason = (d as { reason?: unknown }).reason;
        if (typeof rawReason === "string" && rawReason !== "") {
          reason = rawReason;
        }
      } catch {
        /* non-JSON */
      }
      // No failure toast for ANY 409. A plain 409 is a queue signal the caller
      // converts to a steer, and a 409 with reason "starting" IS a failure —
      // carved out of that collapse via `reason` on the result — but its
      // surface is the send-error face submit.ts renders through send-state,
      // not a toast. Otherwise honour the caller's reportSendState preference
      // (defaults to true for legacy direct callers; transportAction sets it
      // false to avoid double-feedback with its toast).
      const reportSendState = opts?.reportSendState ?? true;
      if (r.status !== 409 && reportSendState) {
        reportFailure(chatIDOf(cmd), errMsg);
      }
      return {
        ok: false,
        status: r.status,
        error: errMsg,
        ...(reason !== undefined ? { reason } : {}),
      };
    } catch (e: unknown) {
      const err = e instanceof Error ? e : null;
      let msg: string;
      let code: string;
      if (err?.name === "TimeoutError") {
        msg = "Request timed out";
        code = TRANSPORT_ERROR_CODES.TIMEOUT;
      } else if (err?.name === "AbortError") {
        msg = "Request cancelled";
        code = TRANSPORT_ERROR_CODES.CANCELLED;
      } else {
        msg = err?.message ?? "Network error";
        code = TRANSPORT_ERROR_CODES.NETWORK;
      }
      const reportSendState = opts?.reportSendState ?? true;
      if (reportSendState) {
        reportFailure(chatIDOf(cmd), msg);
      }
      return { ok: false, status: 0, error: msg, code };
    } finally {
      this.inflight.delete(ctrl);
    }
  }
}

// ---------------------------------------------------------------------------
// Backoff computation — re-exported from lib/backoff.ts for backward compat.
// ---------------------------------------------------------------------------

export { computeBackoff, BACKOFF_CAP_MS } from "./lib/backoff.js";

// ---------------------------------------------------------------------------
// Singleton instance + function exports that form the module's public API.
// ---------------------------------------------------------------------------

const instance = new TransportController();
registerCleanup(() => {
  instance.cancelInflight();
});

export function init(msg: MsgHandler, status: StatusHandler): void {
  instance.init(msg, status);
}

/** Tell the transport the chat store is populated, releasing every frame held
 *  since the connection opened. Called once from the boot path as soon as
 *  `GET /api/chats` has been folded into the store. See `holdUntilHydrated`. */
export function markHydrated(): void {
  instance.markHydrated();
}

export async function send(cmd: TypedCommand | Command, opts?: SendOptions): Promise<SendResult> {
  return instance.send(cmd, opts);
}

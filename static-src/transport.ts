// Transport: SSE for server→client, fetch POST for client→server.
//
// Errors surface through send-state.ts. The send button is the single error
// surface and stays clickable, so the next Send is the retry. A plain 409 busy
// is a handshake the caller converts to a steer; a 409 carrying
// `reason: "starting"` is a real failure it renders through the error face.

import type { ServerEvent, ConnectedPayload, ConnectionStatus, TabKind } from "./types.js";
import { setSSEStatus } from "./send-state.js";
import { reportFailure } from "./failure-notice.js";
import { emitBus, BUS_PAGE_RESUMED, BUS_TRANSPORT_GAP, lookupSSEDecoder } from "./bus.js";
import {
  registerCleanup,
  hasErrorString,
  IDEMPOTENCY_HEADER,
  IDEMPOTENCY_COMMAND_FIELD,
} from "./actions/index.js";
import { computeBackoff } from "./lib/backoff.js";
import { bumpSyncEpoch } from "./tab-freshness.js";

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
  // A message delivered INTO the running turn (`_session/steer`). Typed here
  // for the steer action's custom runner — the wire shape every other sender
  // (the framework's loose TransportCommand) already produces.
  | { type: "steer"; chat_id: string; payload: { text: string; message_id: string } }
  // Addresses a USER MESSAGE, not a turn ordinal: KAS's revertMultiple takes a
  // messageId and refuses a non-user one.
  | { type: "rewind_chat"; chat_id: string; payload: { message_id: string } }
  // The three CREATING commands: the only members with no `chat_id`, because the
  // server mints the id and returns it. `op_id` correlates every attempt of ONE
  // gesture and is NOT the idempotency token — that one is the header, per
  // dispatch, in a 5-minute cache.
  | { type: "create_chat"; payload: { op_id: string; name?: string; model?: string } }
  | { type: "resume_session"; payload: { op_id: string; session_id: string; name: string } }
  | { type: "fork_chat"; payload: { op_id: string; parent_chat_id: string; title?: string } }
  // The four TAB mutations, with no `chat_id` because the open-tab set is
  // workspace-global; a chat tab names its chat in `ref` instead. `op_id` is echoed back
  // on the frame so a client can tell its own committed mutation from another device's.
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
  /** The success body, undecoded, and present only when the response parsed as
   *  JSON. The creating commands need it: the response is the only place a caller
   *  learns the chat id the server just minted. Undecoded on purpose — which wire
   *  shape a command answers with is the action's business, not the transport's. */
  body?: unknown;
}

interface SendOptions {
  /** Caller-supplied signal for cancellation (e.g. on tab close or chat delete). */
  signal?: AbortSignal;
  /** Timeout in ms. Defaults to 15 minutes. */
  timeoutMs?: number;
  /** When true (default), a failure gets a failure-notice.ts toast.
   *  `transportAction` passes false because it owns that surface itself, and both
   *  firing is duplicate feedback for one failure. */
  reportSendState?: boolean;
}

/** Read the key an action attached to its command, if any. It becomes the
 *  HEADER, never a body field: the server's envelope has no such member. */
function idempotencyKeyOf(cmd: TypedCommand | Command): string | undefined {
  const v = (cmd as Record<string, unknown>)[IDEMPOTENCY_COMMAND_FIELD];
  return typeof v === "string" && v !== "" ? v : undefined;
}

/** The chat a command is addressed to, or "" when it addresses none. "" is what
 *  the envelope carries for the creating commands, and what `reportFailure`
 *  treats as workspace-wide — correct, since a failed create belongs to no chat. */
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

/** A correlation id for ONE create gesture, so every attempt resolves to the same
 *  chat; it has to survive a retry the user makes minutes later.
 *
 *  MINT IT AT THE DISPATCH SITE, never inside an action's `run()`: the framework
 *  re-invokes `run()` per attempt, so an id minted there is fresh every time and
 *  defeats its own purpose. The `op-` prefix keeps it inside the identifier shape
 *  the command boundary gates it with. */
export function newOpID(): string {
  return newRequestID().replace("r-", "op-");
}

interface GapInfo {
  lastSeen: number;
  floor: number;
  head: number;
}

const HIDDEN_ABORT_MS = 30_000;

/** How recently a frame must have arrived for a resume to trust the stream
 *  without reconnecting. NOT sized against the heartbeat: the clock it reads
 *  counts real frames, so an idle stream ages past it whatever the beat does. A
 *  cost saver, never the correctness mechanism. */
const RESUME_LIVENESS_MS = 5_000;

/** How long this page's own timers must have failed to run for a resume to treat
 *  the stream as suspect. Errs low: a wrong "suspect" costs one small reconnect,
 *  a wrong "alive" costs the whole stale-tab defect. */
const RESUME_SUSPECT_FROZEN_MS = 10_000;

/** How long one resume decision suppresses the next. A resume fires several
 *  signals at once (pageshow beside visibilitychange, online behind both), and a
 *  rapid visible→hidden→visible must not open several streams. */
const RESUME_COALESCE_MS = 1_000;

/** Interval of the suspension detector. See `lastTickAt`. */
const RESUME_TICK_MS = 15_000;

/** The server's NAMED keepalive (`heartbeatEventName`, internal/agent/heartbeat.go).
 *  A named event never reaches `onmessage`, so it needs its own listener — which is
 *  the whole reason the server publishes a named event rather than relying on the
 *  transport's COMMENT keepalive, which the EventSource parser discards. */
const SSE_HEARTBEAT_EVENT = "heartbeat";

/** Cadence of the received-event watchdog, matching the server's heartbeat interval
 *  (keepaliveInterval, internal/agent). A finer tick could observe nothing new. */
const SSE_WATCHDOG_TICK_MS = 15_000;

/** How long the stream may deliver NO event before the watchdog reconnects it.
 *  Five missed heartbeats, which has to clear 2x the interval with margin: the
 *  beat is idle-gated, so worst-case silence on a healthy stream is 30s. */
const SSE_SILENCE_MS = 75_000;

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

/** How long a connection must stay open before the backoff ramp resets.
 *  Resetting on `onopen`, or on the first message, would let a connect→drop loop
 *  hammer the server at the 500ms base forever. */
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

/** The chat this device is SHOWING, read at connect time. An injected getter
 *  rather than an import, because importing `store.ts` risks a cycle.
 *  Unregistered it answers "", which the server reads as "declare nothing". */
let snapshotChatProvider: () => string = () => "";

/** Register the reader for the chat whose transcript is on screen. The server
 *  cannot derive it — the active chat is per-device localStorage state — and it may
 *  not ride `chat_id`, which is the hub topic filter. See `parseSnapshotChats` in
 *  `internal/agent/sse.go`. */
export function setSnapshotChatProvider(fn: () => string): void {
  snapshotChatProvider = fn;
}

/** The events URL. The cursor rides a query parameter because EventSource sends
 *  `Last-Event-ID` on ITS OWN retry only and `teardown` closes the source; the server
 *  reads the parameter only when the header is absent. `snapshot` names the chat whose
 *  in-flight transcript this connect needs, so the server pays for no other. */
function eventsURL(cursor: number): string {
  const params = new URLSearchParams();
  if (cursor > 0) {
    params.set("last_event_id", String(cursor));
  }
  const snapshot = snapshotChatProvider();
  if (snapshot !== "") {
    params.set("snapshot", snapshot);
  }
  const query = params.toString();
  return query === "" ? "/api/events" : `/api/events?${query}`;
}

class TransportController {
  private onMsg: MsgHandler = () => {
    /* noop */
  };
  private onStatus: StatusHandler = () => {
    /* noop */
  };

  private conn: ConnState = { phase: "idle" };
  private lastSeenEventID = 0;
  /** lastSeenEventID as of the CURRENT source opening, and the only cursor gap
   *  detection may compare: the handshake frame carries `id: head`, so onmessage
   *  has already advanced the live cursor by the time `handleConnected` runs. */
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

  /** Every `document`/`window` listener this controller installed. `init` aborts
   *  the previous one, so calling it twice replaces the listeners instead of
   *  doubling them. */
  private listeners: AbortController | null = null;
  /** Date.now() when the last `onmessage` frame was OBSERVED — deliberately NOT
   *  stamped by the named heartbeat, which stamps `lastEventAt`. A recent frame
   *  proves the pipe carried bytes and that this page was running to receive
   *  them; an old one proves nothing, because an idle stream carries none. */
  private lastFrameAt = Date.now();
  /** Stamped by an interval that exists to be MISSED: a suspended page runs no
   *  timers, so the shortfall against RESUME_TICK_MS is how long this page was
   *  not running. Nothing depends on the tick firing. */
  private lastTickAt = Date.now();
  private tickTimer: ReturnType<typeof setInterval> | null = null;
  /** Date.now() until which a further resume signal is a duplicate. */
  private resumeGateUntil = 0;
  /** Whether the page reported its own suspension on the way out. */
  private wasFrozen = false;

  /** Date.now() when this connection last delivered an EVENT — an `onmessage` frame
   *  or a named heartbeat. Keepalives are comments the parser discards, so an event's
   *  arrival is the only byte-recency signal a browser has. Stamped when the watchdog
   *  arms, so the silence window is measured from the CONNECT rather than from load. */
  private lastEventAt = Date.now();
  private watchdogTimer: ReturnType<typeof setInterval> | null = null;

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

  /** Hold every incoming frame until the chat store is populated, then release them in
   *  arrival order. The connect replay carries the one `turn_state` per busy chat that is
   *  never broadcast live, so a frame an empty store drops has no second chance — and
   *  ORDER is load-bearing, because a `message_chunk` released before the `turn_state`
   *  whose watermark makes it idempotent is double-appended. */
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
    this.listeners?.abort();
    const listeners = new AbortController();
    this.listeners = listeners;
    const { signal } = listeners;

    this.onMsg = msg;
    this.holdUntilHydrated();
    this.onStatus = (s) => {
      setSSEStatus(s);
      status(s);
    };
    this.startTick();
    this.connectSSE();

    document.addEventListener(
      "visibilitychange",
      () => {
        if (document.visibilityState === "visible") {
          this.maybeResume(false);
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
      },
      { signal },
    );
    window.addEventListener(
      "pageshow",
      (e: PageTransitionEvent) => {
        this.maybeResume(e.persisted);
      },
      { signal },
    );
    // A network coming back says nothing about the socket that was open across
    // the outage, so it is a forced decision rather than an input to one.
    window.addEventListener(
      "online",
      () => {
        this.maybeResume(true);
      },
      { signal },
    );
    // The Page Lifecycle pair, where the platform reports its own suspension
    // rather than leaving it to be inferred. Absent on iOS, which is why the
    // timer gap exists.
    document.addEventListener(
      "resume",
      () => {
        this.maybeResume(true);
      },
      { signal },
    );
    document.addEventListener(
      "freeze",
      () => {
        this.wasFrozen = true;
      },
      { signal },
    );
  }

  /** Undo `init` completely, for tests that boot the singleton more than once.
   *  `vi.resetModules()` cannot substitute in Browser Mode: the module map is
   *  URL-keyed, so a re-import hands back this instance with its listeners live. */
  _resetForTest(): void {
    this.listeners?.abort();
    this.listeners = null;
    this.stopTick();
    this.teardown();
    if (this.hydrateTimer !== null) {
      clearTimeout(this.hydrateTimer);
      this.hydrateTimer = null;
    }
    this.onMsg = () => {
      /* noop */
    };
    this.onStatus = () => {
      /* noop */
    };
    this.lastSeenEventID = 0;
    this.cursorAtConnect = 0;
    this.openedAt = 0;
    this.lastBackoffMs = 0;
    this.hiddenSince = null;
    this.hydrated = false;
    this.pending = [];
    this.lastFrameAt = Date.now();
    this.lastTickAt = Date.now();
    this.resumeGateUntil = 0;
    this.wasFrozen = false;
  }

  /** Stamp `lastTickAt` so a missed tick is measurable. A SECOND interval at the
   *  watchdog's cadence that may NOT be folded into it: the watchdog belongs to a
   *  CONNECTION and stops at teardown, while this belongs to the PAGE and must keep
   *  stamping across a reconnect, or every backoff wait reads as a suspension. */
  private startTick(): void {
    this.stopTick();
    this.lastTickAt = Date.now();
    this.tickTimer = setInterval(() => {
      this.lastTickAt = Date.now();
    }, RESUME_TICK_MS);
  }

  /** Release the suspension detector. Public because the module-level unload
   *  cleanup owns it, beside `cancelInflight`. */
  stopTick(): void {
    if (this.tickTimer !== null) {
      clearInterval(this.tickTimer);
      this.tickTimer = null;
    }
  }

  /** How long this page's own timers were not running: scheduling jitter on a
   *  live page, the suspension length on a resumed one. Chromium's background
   *  throttling reads as tens of seconds for a long-backgrounded tab, which is
   *  not a false positive — that connection may well be gone too. */
  private frozenGapMs(now: number): number {
    return Math.max(0, now - this.lastTickAt - RESUME_TICK_MS);
  }

  /** Decide whether a page-lifecycle signal has to force a reconnect. `readyState` is
   *  not a liveness signal here — iOS answers OPEN for a stream the OS tore down — so
   *  the decision reads a definitive CLOSED, then a frame inside the liveness window,
   *  then the page's own suspension. It reconnects at delay 0 without taking the backoff
   *  ramp, which bounds UNATTENDED retry, and leaves `lastBackoffMs` untouched. */
  private maybeResume(force: boolean): void {
    const now = Date.now();
    if (now < this.resumeGateUntil) {
      return;
    }
    this.resumeGateUntil = now + RESUME_COALESCE_MS;
    // Consumed BEFORE any early return, so a freeze cannot leak into a later
    // resume that has to decide on its own evidence. Reading it after the dead
    // check left it set whenever a resume met an already-dead stream, so the next
    // resume — deciding about a fresh connection — inherited it and forced one
    // more reconnect.
    const frozen = this.wasFrozen;
    this.wasFrozen = false;
    if (this.sseIsDead()) {
      this.resumeNow();
      return;
    }
    if (now - this.lastFrameAt < RESUME_LIVENESS_MS) {
      return;
    }
    if (force || frozen || this.frozenGapMs(now) >= RESUME_SUSPECT_FROZEN_MS) {
      this.resumeNow();
    }
  }

  /** Announce the resume and reconnect. ONE helper for both delay-0 branches: a
   *  resume that finds the stream definitively gone is the strongest case for the
   *  nudge, and the first branch RETURNS before the second's condition is read. */
  private resumeNow(): void {
    emitBus(BUS_PAGE_RESUMED);
    this.scheduleReconnect({ delay: 0 });
  }

  /** Whether the stream is definitively gone — ONE input to the resume decision,
   *  read off the source's `readyState` rather than the phase, which `onerror`
   *  demotes only on CLOSED. Mid-handshake is ALIVE: `pageshow` fires on every
   *  cold load, so a bare `!== OPEN` would reopen the stream every load. */
  private sseIsDead(): boolean {
    switch (this.conn.phase) {
      case "connected":
        return this.conn.source.readyState !== EventSource.OPEN;
      case "connecting":
        return this.conn.source.readyState === EventSource.CLOSED;
      default:
        return true;
    }
  }

  /** Arm the received-event watchdog for the connection about to open, stamping the
   *  clock so the silence window starts at THIS connect. */
  private armWatchdog(): void {
    this.stopWatchdog();
    this.lastEventAt = Date.now();
    this.watchdogTimer = setInterval(() => {
      this.checkSilence();
    }, SSE_WATCHDOG_TICK_MS);
  }

  /** Release the watchdog. Public because the module-level unload cleanup owns it,
   *  beside `cancelInflight`, so navigating away does not leak a timer. */
  stopWatchdog(): void {
    if (this.watchdogTimer !== null) {
      clearInterval(this.watchdogTimer);
      this.watchdogTimer = null;
    }
  }

  /** Reconnect when the stream has delivered NO event for the whole silence window: a
   *  second liveness signal that reads no `readyState` at all. Through `nextBackoff`
   *  rather than delay 0 so REPEATED silence escalates, which bounds a SAME-DOCUMENT
   *  loop only. Skipped while hidden — the visibilitychange kick covers the return. */
  private checkSilence(): void {
    if (document.visibilityState === "hidden") {
      return;
    }
    if (Date.now() - this.lastEventAt <= SSE_SILENCE_MS) {
      return;
    }
    // `scheduleReconnect` tears the source down itself, which is what stops the dead
    // one delivering beside its own replacement and moving the cursor.
    this.scheduleReconnect(this.nextBackoff());
  }

  // --- SSE ---

  /** Tear down the current connection state, transitioning to idle.
   *  Handles cleanup of EventSource and reconnect timers regardless of
   *  current phase. Called before (re)connecting to ensure a clean slate. */
  private teardown(): void {
    // Before the idle early-return: the watchdog belongs to a CONNECTION, and the
    // backoff window between a teardown and its reconnect has none to watch.
    this.stopWatchdog();
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
    // Re-armed per connect; `teardown` above just cleared the previous one.
    this.armWatchdog();
    const source = new EventSource(eventsURL(this.lastSeenEventID));
    this.conn = { phase: "connecting", source };
    source.onopen = (): void => {
      this.openedAt = Date.now();
      this.conn = { phase: "connected", source };
      this.onStatus("connected");
    };
    source.onmessage = (e: MessageEvent): void => {
      // Before the cursor read and before any decoder can drop the payload: the
      // liveness question is whether BYTES arrived, not whether they parsed.
      this.lastFrameAt = Date.now();
      // The watchdog's clock: an EVENT arrived, whether or not it parses below.
      this.lastEventAt = Date.now();
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
      // Opt-in validation: a registered decoder runs before dispatch and a
      // failure DROPS the event, so no handler sees a partial shape. An event
      // with no decoder falls through untyped.
      const decoder = lookupSSEDecoder(evt.type);
      if (decoder !== undefined) {
        try {
          evt = { ...evt, payload: decoder(evt.payload) };
        } catch (decodeErr) {
          const msg = decodeErr instanceof Error ? decodeErr.message : String(decodeErr);
          console.error(`sse: decoder rejected ${evt.type}:`, msg);
          // This frame has no other recovery: the cursor already advanced, so no
          // reconnect replays it, and nothing downstream can notice a state change it
          // never saw — an older bundle against a newer server drops every
          // `tool_call_update` carrying a status it does not know, and each one is a
          // card left mid-flight for the life of the document. So the claim every
          // loaded view makes goes with it, which converts a permanent hole into one
          // refetch per view the reader actually visits.
          bumpSyncEpoch();
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
    // A named event never reaches `onmessage`, which is why this listener exists at
    // all. It also advances the cursor, so the browser's own `Last-Event-ID` and this
    // module's stay in step across a reconnect.
    source.addEventListener(SSE_HEARTBEAT_EVENT, (e: Event): void => {
      this.lastEventAt = Date.now();
      // Narrowing: `addEventListener`'s generic overload types the argument as
      // `Event`, and every frame the server sends under this name is a MessageEvent.
      const msg = e as MessageEvent<string>;
      if (msg.lastEventId !== "") {
        const id = Number(msg.lastEventId);
        if (Number.isFinite(id) && id > this.lastSeenEventID) {
          this.lastSeenEventID = id;
        }
      }
    });
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
    // Close what is live, not just a pending timer: the kick fires while a source
    // can still be OPEN or mid-retry, and an unclosed one keeps its `onmessage`
    // bound — it would deliver frames beside its own replacement and move the
    // cursor.
    this.teardown();
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
    // Judged against the PRE-connect cursor. The third arm exists because ids
    // are per-process: a restart re-seeds them BELOW our cursor, and without it
    // the stale high cursor also suppresses onmessage advancement until the new
    // process catches up.
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
    // The framework threads an action's own key through every retry attempt, so
    // honouring it here is what makes a retry dedupe; minting a fresh one would
    // defeat the mechanism. A bare send() has none and gets a fresh one, which
    // is right — two deliberate sends are two operations.
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
          // A HEADER and never a body field: as a header it reaches the server's
          // one idempotency middleware, which marks in-flight and answers 409.
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
      // No failure toast for ANY 409: a plain one is a queue signal the caller
      // converts to a steer, and a "starting" one IS a failure whose surface is
      // the send-error face rather than a toast.
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
  instance.stopWatchdog();
  instance.stopTick();
});

export function init(msg: MsgHandler, status: StatusHandler): void {
  instance.init(msg, status);
}

/** Reset the singleton to its pre-`init` state. Test-only; see `_resetForTest`
 *  on the controller for why `vi.resetModules()` cannot stand in for it. */
export function _resetForTest(): void {
  instance._resetForTest();
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

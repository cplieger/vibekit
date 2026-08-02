// ---------------------------------------------------------------------------
// Transport: SSE for server→client, fetch POST for client→server.
//
// Every command gets a fresh client-generated request_id so the server can
// dedupe retries. The dispatcher receives typed ServerEvents from SSE.
//
// Errors surface through send-state.ts (which drives the send-button
// blocked state + tooltip). There is no inline error card; the button is
// the single error surface. 409 busy is a handshake, not an error —
// callers handle it locally by queueing the prompt.
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

import type { ServerEvent, ConnectedPayload, ConnectionStatus } from "./types.js";
import { setLastError, setSSEStatus } from "./send-state.js";
import { emitBus, BUS_TRANSPORT_GAP, lookupSSEDecoder } from "./bus.js";
import { registerCleanup, hasErrorString } from "./actions/index.js";
import { computeBackoff } from "./lib/backoff.js";

type MsgHandler = (evt: ServerEvent) => void;
type StatusHandler = (s: ConnectionStatus) => void;

type CommandType =
  | "prompt"
  | "cancel"
  | "delete_chat"
  | "switch_model"
  | "set_supervised_mode"
  | "resolve_pending_change"
  | "resolve_pending_change_partial"
  | "resolve_all_pending_changes"
  | "trust_pending_changes"
  | "clear_pending_trust"
  | "permission_response"
  | "elicitation_response"
  | "restore_checkpoint"
  | "set_effort"
  | "set_mode"
  | "promote_rewind_chat"
  | "discard_rewind_chat"
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
        request_id?: string;
        model?: string;
      };
    }
  | { type: "cancel"; chat_id: string }
  | { type: "delete_chat"; chat_id: string }
  | { type: "switch_model"; chat_id: string; payload: { model: string } }
  | { type: "set_supervised_mode"; chat_id: string; payload: { enabled: boolean } }
  | {
      type: "resolve_pending_change";
      chat_id: string;
      payload: { tool_call_id: string; action: string };
    }
  | {
      type: "resolve_pending_change_partial";
      chat_id: string;
      payload: { tool_call_id: string; merged_text: string };
    }
  | { type: "resolve_all_pending_changes"; chat_id: string; payload: { action: string } }
  | { type: "trust_pending_changes"; chat_id: string }
  | { type: "clear_pending_trust"; chat_id: string }
  | {
      type: "permission_response";
      chat_id: string;
      payload: { request_id: number; option_id: string };
    }
  | {
      type: "elicitation_response";
      chat_id: string;
      payload: { request_id: number; action: string; content?: Record<string, unknown> };
    }
  | { type: "restore_checkpoint"; chat_id: string; payload: { tag: string } }
  | { type: "set_effort"; chat_id: string; request_id: string; payload: { level: string } }
  | { type: "set_mode"; chat_id: string; payload: { mode_id: string } }
  | { type: "promote_rewind_chat"; chat_id: string; request_id: string }
  | { type: "discard_rewind_chat"; chat_id: string; request_id: string }
  | { type: "rewind_chat"; chat_id: string; request_id: string; payload: { turn_index: number } };

const TRANSPORT_ERROR_CODES = {
  TIMEOUT: "timeout",
  CANCELLED: "cancelled",
  NETWORK: "network",
} as const;

interface SendResult {
  ok: boolean;
  /** HTTP status. 0 for non-HTTP failures (timeout, network). */
  status: number;
  /** Server error message, if any. */
  error?: string;
  /** Structured error code for non-HTTP failures. */
  code?: string;
}

interface SendOptions {
  /** Caller-supplied signal for cancellation (e.g. on tab close or chat delete). */
  signal?: AbortSignal;
  /** Timeout in ms. Defaults to 15 minutes. */
  timeoutMs?: number;
  /** When true (default), failures call setLastError() so the prompt
   *  send button shows a blocked state with the error tooltip. The
   *  action framework adapter (transportAction) passes false because
   *  it owns the error surface via toast — letting both fire produces
   *  duplicate user feedback for one failure. */
  reportSendState?: boolean;
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

  init(msg: MsgHandler, status: StatusHandler): void {
    this.onMsg = msg;
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
    const requestID = newRequestID();
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
        headers: { "Content-Type": "application/json" },
        signal: combined,
        body: JSON.stringify({
          type: cmd.type,
          request_id: requestID,
          chat_id: cmd.chat_id ?? "",
          // eslint-disable-next-line @typescript-eslint/no-unnecessary-condition
          payload: "payload" in cmd && cmd.payload != null ? cmd.payload : {},
        }),
      });
      if (r.ok) {
        return { ok: true, status: r.status };
      }

      let errMsg = `HTTP ${String(r.status)}`;
      try {
        const d: unknown = await r.json();
        if (hasErrorString(d)) {
          errMsg = d.error;
        }
      } catch {
        /* non-JSON */
      }
      // 409 is a queue signal — never reported via setLastError.
      // Otherwise honour the caller's reportSendState preference
      // (defaults to true for legacy direct callers; transportAction
      // sets it false to avoid double-feedback with its toast).
      const reportSendState = opts?.reportSendState ?? true;
      if (r.status !== 409 && reportSendState) {
        setLastError(errMsg);
      }
      return { ok: false, status: r.status, error: errMsg };
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
        setLastError(msg);
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

export async function send(cmd: TypedCommand | Command, opts?: SendOptions): Promise<SendResult> {
  return instance.send(cmd, opts);
}

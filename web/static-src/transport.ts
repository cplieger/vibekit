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

type MsgHandler = (evt: ServerEvent) => void;
type StatusHandler = (s: ConnectionStatus) => void;

type CommandType =
  | "prompt"
  | "cancel"
  | "delete_chat"
  | "switch_model"
  | "fork_chat"
  | "merge_tangent"
  | "discard_tangent"
  | "set_supervised_mode"
  | "resolve_pending_change"
  | "resolve_pending_change_partial"
  | "resolve_all_pending_changes"
  | "trust_pending_changes"
  | "clear_pending_trust"
  | "permission_response"
  | "restore_checkpoint"
  | "undo_edit"
  | "message_subagent"
  | "set_auto_approve_crew"
  | "rename_chat";

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
        agent?: string;
        model?: string;
        active_file?: string;
        open_files?: readonly string[];
      };
    }
  | { type: "cancel"; chat_id: string }
  | { type: "delete_chat"; chat_id: string }
  | { type: "switch_model"; chat_id: string; payload: { model: string } }
  | { type: "fork_chat"; chat_id: string; payload?: { tangent_id?: string } }
  | { type: "merge_tangent"; chat_id: string }
  | { type: "discard_tangent"; chat_id: string }
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
  | { type: "restore_checkpoint"; chat_id: string; payload: { tag: string } }
  | { type: "undo_edit"; chat_id: string; payload: { tag: string; file_path: string } }
  | { type: "message_subagent"; chat_id: string; payload: { sub_session_id: string; text: string } }
  | { type: "set_auto_approve_crew"; chat_id: string; payload: { enabled: boolean } }
  | { type: "rename_chat"; chat_id: string; payload: { name: string } };

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

export const BACKOFF_CAP_MS = 30_000;

const HIDDEN_ABORT_MS = 30_000;

// ---------------------------------------------------------------------------
// TransportController: owns all SSE connection state as instance fields.
// ---------------------------------------------------------------------------

type ConnState =
  | { phase: "idle" }
  | { phase: "connecting"; source: EventSource }
  | { phase: "connected"; source: EventSource }
  | { phase: "reconnecting"; timer: ReturnType<typeof setTimeout>; backoffMs: number };

class TransportController {
  private onMsg: MsgHandler = () => {
    /* noop */
  };
  private onStatus: StatusHandler = () => {
    /* noop */
  };

  private conn: ConnState = { phase: "idle" };
  private lastSeenEventID = 0;
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
        this.scheduleReconnect({ delay: 0, backoffMs: 0 });
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
        this.scheduleReconnect({ delay: 0, backoffMs: 0 });
      }
    });
  }

  private sseIsDead(): boolean {
    return this.conn.phase !== "connected" && this.conn.phase !== "connecting";
  }

  // --- SSE ---

  private connectSSE(): void {
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

    this.onStatus("connecting");
    const source = new EventSource("/api/events");
    this.conn = { phase: "connecting", source };
    source.onopen = (): void => {
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

  private nextBackoff(): { delay: number; backoffMs: number } {
    const prev = this.conn.phase === "reconnecting" ? this.conn.backoffMs : 0;
    return computeBackoff(prev);
  }

  private scheduleReconnect(info: { delay: number; backoffMs: number }): void {
    if (this.conn.phase === "reconnecting") {
      clearTimeout(this.conn.timer);
    }
    const timer = setTimeout(() => {
      this.connectSSE();
    }, info.delay);
    this.conn = { phase: "reconnecting", timer, backoffMs: info.backoffMs };
  }

  private handleConnected(evt: ServerEvent): void {
    const p = evt.payload as ConnectedPayload | undefined;
    if (p === undefined) {
      return;
    }
    if (this.lastSeenEventID === 0) {
      return;
    }
    const gap = p.floor === 0 || this.lastSeenEventID < p.floor;
    if (gap) {
      const info: GapInfo = { lastSeen: this.lastSeenEventID, floor: p.floor, head: p.head };
      emitBus(BUS_TRANSPORT_GAP, info);
    }
  }

  // --- POST /api/command ---

  async send(cmd: TypedCommand | Command, opts?: SendOptions): Promise<SendResult> {
    const requestID = newRequestID();
    const timeoutMs = opts?.timeoutMs ?? 15 * 60 * 1000;
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
// Backoff computation (pure, exported for testing).
// ---------------------------------------------------------------------------

/** Compute the next backoff delay given the previous backoffMs.
 *  Doubles from 500ms up to a 30s cap; delay is randomized within [0, next). */
export function computeBackoff(prevBackoffMs: number): { delay: number; backoffMs: number } {
  const next = Math.min(prevBackoffMs === 0 ? 500 : prevBackoffMs * 2, BACKOFF_CAP_MS);
  return { delay: Math.floor(Math.random() * next), backoffMs: next };
}

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

// ---------------------------------------------------------------------------
// Shell WebSocket lifecycle: connection management, reconnect backoff,
// message dispatch, and close-code handling. Extracted from shell.ts for
// testability — no xterm.js or DOM dependencies.
//
// Wire protocol (binary WS frames):
//   server → client: raw PTY output bytes
//   client → server: raw terminal input bytes
//   client → server: JSON control prefixed with 0x00:
//     {"type":"resize","cols":N,"rows":N}
//     {"type":"signal","name":"SIGINT"}
//     {"type":"kill"}
// ---------------------------------------------------------------------------

import { registerCleanup } from "./actions/index.js";

// Reconnect backoff: 250ms → 500ms → 1s → 2s → 4s → 8s (cap).
const RECONNECT_BASE_MS = 250;
const RECONNECT_MAX_MS = 8000;

/** Callbacks the shell UI provides to react to socket events. */
export interface ShellWSCallbacks {
  onMessage(data: ArrayBuffer | string): void;
  onOpen(): void;
  onClose(code: number): void;
  onReconnecting(): void;
}

export const encoder = new TextEncoder();

// ---------------------------------------------------------------------------
// Discriminated union for connection state — eliminates invalid
// combinations like (ws !== null && wsConnecting === true) or
// (intentionalClose === true && isActive === true).
// ---------------------------------------------------------------------------

type ShellWSState =
  | { kind: "idle" }
  | { kind: "connecting" }
  | { kind: "connected"; ws: WebSocket }
  | { kind: "reconnecting"; attempt: number }
  | { kind: "closed"; intentional: boolean };

/** Connect timeout for the WebSocket handshake (ms). */
export const CONNECT_TIMEOUT_MS = 10_000;

/** Open a new WebSocket. Returns a promise that resolves on "open" and
 *  rejects on "error" or premature "close". Times out after CONNECT_TIMEOUT_MS. */
function openSocket(timeoutMs = CONNECT_TIMEOUT_MS): Promise<WebSocket> {
  return new Promise((resolve, reject) => {
    const proto = location.protocol === "https:" ? "wss:" : "ws:";
    const sock = new WebSocket(`${proto}//${location.host}/api/shell/ws`);
    sock.binaryType = "arraybuffer";

    const timer = setTimeout(() => {
      sock.removeEventListener("open", onOpen);
      sock.removeEventListener("error", onError);
      sock.removeEventListener("close", onCloseBeforeOpen);
      sock.close();
      reject(new Error("websocket connect timeout"));
    }, timeoutMs);

    const onOpen = (): void => {
      clearTimeout(timer);
      sock.removeEventListener("error", onError);
      sock.removeEventListener("close", onCloseBeforeOpen);
      resolve(sock);
    };
    const onError = (): void => {
      clearTimeout(timer);
      sock.removeEventListener("open", onOpen);
      sock.removeEventListener("close", onCloseBeforeOpen);
      reject(new Error("websocket error"));
    };
    const onCloseBeforeOpen = (): void => {
      clearTimeout(timer);
      sock.removeEventListener("open", onOpen);
      sock.removeEventListener("error", onError);
      reject(new Error("websocket closed before open"));
    };

    sock.addEventListener("open", onOpen, { once: true });
    sock.addEventListener("error", onError, { once: true });
    sock.addEventListener("close", onCloseBeforeOpen, { once: true });
  });
}

// ---------------------------------------------------------------------------
// ShellWS class — encapsulates all connection state and lifecycle.
// ---------------------------------------------------------------------------

export class ShellWS {
  private state: ShellWSState = { kind: "idle" };
  private reconnectTimer: ReturnType<typeof setTimeout> | null = null;
  private callbacks: ShellWSCallbacks | null = null;
  private isActive = false;
  private openWaiters: { resolve: () => void; reject: (e: Error) => void }[] = [];
  private reconnectAttempt = 0;
  private connectGen = 0;

  /** Set the callback interface. Must be called before connect(). */
  setCallbacks(cb: ShellWSCallbacks): void {
    this.callbacks = cb;
  }

  /** Mark the shell as active (open). Controls whether reconnect fires. */
  setActive(active: boolean): void {
    this.isActive = active;
  }

  /** Open a new WebSocket connection. No-op if already connected/connecting. */
  async connect(): Promise<void> {
    if (this.state.kind === "connected" || this.state.kind === "connecting") {
      return;
    }
    this.state = { kind: "connecting" };
    const gen = ++this.connectGen;

    let sock: WebSocket;
    try {
      sock = await openSocket();
    } catch {
      if (this.connectGen !== gen) {
        return;
      }
      this.state = { kind: "idle" };
      if (this.isActive) {
        this.callbacks?.onReconnecting();
        this.scheduleReconnect();
      }
      return;
    }

    // If a newer connect() or disconnect() fired while openSocket was
    // in-flight, close the newly opened socket and bail.
    // Note: cast needed because tsgo narrows this.state to "connecting"
    // from the pre-await assignment, but after await it may have changed.
    if (this.connectGen !== gen || (this.state as ShellWSState).kind === "closed") {
      sock.close();
      return;
    }

    this.state = { kind: "connected", ws: sock };
    this.reconnectAttempt = 0;
    this.cancelReconnect();
    this.callbacks?.onOpen();
    this.notifySocketOpen();

    registerCleanup(() => {
      this.cancelReconnect();
      this.disconnect();
    });

    sock.addEventListener("message", (e: MessageEvent) => {
      if (e.data instanceof ArrayBuffer) {
        this.callbacks?.onMessage(e.data);
      } else if (typeof e.data === "string") {
        this.callbacks?.onMessage(e.data);
      }
    });

    sock.addEventListener("close", (e: CloseEvent) => {
      const wasIntentional = this.state.kind === "closed" && this.state.intentional;
      if (this.state.kind !== "closed") {
        this.state = { kind: "idle" };
      }

      if (wasIntentional || !this.isActive) {
        this.callbacks?.onClose(e.code);
        return;
      }
      if (e.code === 1000) {
        this.callbacks?.onClose(e.code);
      } else {
        this.callbacks?.onReconnecting();
        this.scheduleReconnect();
      }
    });
  }

  /** Close the WebSocket and cancel any pending reconnect. */
  disconnect(): void {
    this.cancelReconnect();
    this.reconnectAttempt = 0;
    this.rejectSocketWaiters("shell closed");
    const prevWs = this.state.kind === "connected" ? this.state.ws : null;
    this.state = { kind: "closed", intentional: true };
    if (prevWs !== null) {
      prevWs.close();
    }
  }

  /** Send a JSON control message (resize/signal/kill). Prefixed with 0x00. */
  sendControl(obj: Record<string, unknown>): void {
    if (this.state.kind !== "connected") {
      return;
    }
    const { ws } = this.state;
    if (ws.readyState !== WebSocket.OPEN) {
      return;
    }
    const body = encoder.encode(JSON.stringify(obj));
    const frame = new Uint8Array(1 + body.length);
    frame[0] = 0x00;
    frame.set(body, 1);
    ws.send(frame);
  }

  /** Send raw bytes to the PTY. No-op if the socket isn't open. */
  sendRaw(data: Uint8Array<ArrayBuffer>): void {
    if (this.state.kind !== "connected") {
      return;
    }
    const { ws } = this.state;
    if (ws.readyState !== WebSocket.OPEN) {
      return;
    }
    ws.send(data);
  }

  /** Wait for the socket to reach OPEN state, with a timeout. */
  whenSocketReady(timeoutMs: number): Promise<void> {
    if (this.state.kind === "connected" && this.state.ws.readyState === WebSocket.OPEN) {
      return Promise.resolve();
    }
    return new Promise((resolve, reject) => {
      const waiter = { resolve, reject };
      this.openWaiters.push(waiter);
      setTimeout(() => {
        const idx = this.openWaiters.indexOf(waiter);
        if (idx === -1) {
          return;
        }
        this.openWaiters.splice(idx, 1);
        reject(new Error("websocket not ready"));
      }, timeoutMs);
    });
  }

  /** Reconnect if the socket is stale (e.g. after iOS sleep). */
  reconnectIfStale(): void {
    if (!this.isActive) {
      return;
    }
    if (this.state.kind === "connected" || this.state.kind === "connecting") {
      return;
    }
    this.cancelReconnect();
    this.state = { kind: "idle" };
    void this.connect();
  }

  // --- Private helpers ---

  private scheduleReconnect(): void {
    this.cancelReconnect();
    const attempt = this.reconnectAttempt;
    const delay = Math.min(RECONNECT_BASE_MS * 2 ** attempt, RECONNECT_MAX_MS);
    this.reconnectAttempt = attempt + 1;
    this.state = { kind: "reconnecting", attempt };
    this.reconnectTimer = setTimeout(() => {
      this.reconnectTimer = null;
      if (this.isActive) {
        void this.connect();
      }
    }, delay);
  }

  private cancelReconnect(): void {
    if (this.reconnectTimer !== null) {
      clearTimeout(this.reconnectTimer);
      this.reconnectTimer = null;
    }
  }

  private notifySocketOpen(): void {
    const pending = this.openWaiters.splice(0, this.openWaiters.length);
    for (const w of pending) {
      w.resolve();
    }
  }

  private rejectSocketWaiters(reason: string): void {
    const pending = this.openWaiters.splice(0, this.openWaiters.length);
    for (const w of pending) {
      w.reject(new Error(reason));
    }
  }
}

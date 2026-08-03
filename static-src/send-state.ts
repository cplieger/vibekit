// ---------------------------------------------------------------------------
// Derives the send-button state from the global pieces of the world:
//   - SSE connection status        (signal, set by transport.ts)
//   - last command error           (signal, set by transport.ts / turn.ts;
//                                    network / 5xx / etc. — NOT 409 busy)
//   - queued prompt for the active chat   (reactive via the store)
//   - thinking flag for the active chat   (reactive via the store)
//
// The two externally-driven inputs are `signal`s; the resulting SendState is a
// `computed` that also reads the already-reactive `activeSession` store input,
// so it auto-tracks every dependency. A single `effect` pushes the value to
// prompt-input.ts on any change — no manual recompute call anywhere.
//
// The state machine replaces the old error-card inline rendering: any reason
// the user shouldn't submit surfaces as a single blocked state on the send
// button with a tooltip explaining why.
//
// Precedence: disconnected > lastError > streaming > queued > idle. The order
// matters precisely at streaming+queued: with ONE button, `queued` winning
// would show Send during a live turn — and then nothing on screen cancels the
// turn, which is the one thing the single control exists to guarantee. (The
// old order was right for the SPLIT button, where Cancel had its own half.)
// ---------------------------------------------------------------------------

import { signal, computed, effect } from "@cplieger/reactive";
import { activeSession } from "./store.js";
import { setSendState } from "./prompt-input.js";
import type { SendState } from "./prompt-input.js";
import type { ConnectionStatus } from "./types.js";

const sseStatus = signal<ConnectionStatus>("connecting");
const lastError = signal("");

const sendState = computed<SendState>(() => {
  if (sseStatus.value === "disconnected") {
    return { kind: "blocked", reason: "Disconnected from the server. Reconnecting…" };
  }
  if (lastError.value !== "") {
    return { kind: "blocked", reason: lastError.value };
  }
  const session = activeSession.value;
  if (session === undefined) {
    return { kind: "idle" };
  }
  if (session.thinking) {
    return { kind: "streaming" };
  }
  // Observable with nothing streaming: the queue survives reconnect and the
  // window between turn_ended and the drain, so a bare Send above pending
  // rows would misreport the state.
  const queue = session.prompt_queue;
  if (queue !== undefined && queue.length > 0) {
    return { kind: "queued", count: queue.length };
  }
  return { kind: "idle" };
});

effect(() => {
  setSendState(sendState.value);
});

// Clear a sticky send-error when the active chat changes. `lastError` is a
// global block, but it's raised for one chat, and the errors that set it
// (prompt_failed / bridge_start_failed — see handlers/turn.ts) emit NO
// turn_ended to clear it. Without this, a failure on one chat would wedge the
// send button (and the textarea) on EVERY chat until an SSE reconnect;
// switching chats — or opening a New Chat — is the natural in-app retry.
let lastErrorActiveID = "";
effect(() => {
  const id = activeSession.value?.id ?? "";
  if (id !== lastErrorActiveID) {
    lastErrorActiveID = id;
    if (lastError.peek() !== "") {
      lastError.value = "";
    }
  }
});

export function setSSEStatus(s: ConnectionStatus): void {
  if (sseStatus.peek() === s) {
    return;
  }
  sseStatus.value = s;
  if (s === "connected") {
    lastError.value = "";
  }
}

export function setLastError(msg: string): void {
  lastError.value = msg;
}

export function clearLastError(): void {
  lastError.value = "";
}

// ---------------------------------------------------------------------------
// Derives the send-button state from the global pieces of the world:
//   - SSE connection status        (signal, set by transport.ts)
//   - last command error           (signal, set by transport.ts / turn.ts;
//                                    network / 5xx / etc. — NOT 409 busy)
//   - thinking flag for the active chat   (reactive via the store)
//
// The two externally-driven inputs are `signal`s; the resulting SendState is a
// `computed` that also reads the already-reactive `activeSession` store input,
// so it auto-tracks every dependency. A single `effect` pushes the value to
// prompt-input.ts on any change — no manual recompute call anywhere.
//
// Precedence: disconnected > lastError > streaming > idle.
//
// The top two rungs report an `error`, which is an ADVISORY state: the button
// changes face and explains itself, and the composer stays usable so the next
// Send is the retry. Neither rung earns a lock. A failed prompt leaves the
// server idle, so the chat is immediately promptable; and a dropped SSE stream
// says nothing about the command POST, which travels its own connection and
// usually still lands (the reconnect replay then catches the transcript up).
// prompt-input.ts's header carries the full reasoning. The state used to be
// `blocked` and disabled the textarea, which turned one throttled turn into a
// dead thread.
//
// There used to be a `queued` state between streaming and idle, and its removal
// is the point rather than a simplification: a prompt typed mid-turn is a STEER
// now, delivered into the running turn instead of buffered client-side, so there
// is no pending-send for the button to report. What IS pending — a steer the
// agent has not read yet — is server state and belongs on the chip row that
// projects it, not on the one control whose job is to stop the turn.
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
    return { kind: "error", reason: "Disconnected from the server. Reconnecting…" };
  }
  if (lastError.value !== "") {
    return { kind: "error", reason: lastError.value };
  }
  const session = activeSession.value;
  if (session === undefined) {
    return { kind: "idle" };
  }
  if (session.thinking) {
    return { kind: "streaming" };
  }
  return { kind: "idle" };
});

effect(() => {
  setSendState(sendState.value);
});

// Clear a stale send-error when the active chat changes. `lastError` is global
// but it is raised for one chat, and the errors that set it (prompt_failed /
// bridge_start_failed — see handlers/turn.ts) emit no turn_ended to clear it, so
// without this one chat's failure decorates the button on EVERY chat. It is no
// longer the escape hatch it once was: the composer stays live through an error
// now, and submit.ts clears the error on the next attempt, so retrying in place
// is the retry.
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

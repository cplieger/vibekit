// ---------------------------------------------------------------------------
// Computes the send-button state from the global pieces of the world:
//   - SSE connection status
//   - last command error (network / 5xx / etc. — NOT 409 busy)
//   - queued prompt for the active chat
//   - thinking flag for the active chat
//
// Exposes setters for each input; the module recomputes and pushes the
// resulting SendState to prompt-input.ts on every change. The state machine
// replaces the old error-card inline rendering: any reason the user
// shouldn't submit surfaces as a single blocked state on the send button
// with a tooltip explaining why.
// ---------------------------------------------------------------------------

import { getActiveId, isThinking, queuedPrompt } from "./store.js";
import { setSendState } from "./prompt-input.js";
import type { SendState } from "./prompt-input.js";
import type { ConnectionStatus } from "./types.js";

class SendStateController {
  private sseStatus: ConnectionStatus = "connecting";
  private lastError = "";

  setSSEStatus(s: ConnectionStatus): void {
    if (this.sseStatus === s) {
      return;
    }
    this.sseStatus = s;
    if (s === "connected") {
      this.lastError = "";
    }
    this.recompute();
  }

  setLastError(msg: string): void {
    if (this.lastError === msg) {
      return;
    }
    this.lastError = msg;
    this.recompute();
  }

  clearLastError(): void {
    if (this.lastError === "") {
      return;
    }
    this.lastError = "";
    this.recompute();
  }

  recompute(): void {
    const state = this.computeState();
    setSendState(state);
  }

  private computeState(): SendState {
    if (this.sseStatus === "disconnected") {
      return { kind: "blocked", reason: "Disconnected from the server. Reconnecting…" };
    }
    if (this.lastError !== "") {
      return { kind: "blocked", reason: this.lastError };
    }
    const activeID = getActiveId();
    if (activeID === "") {
      return { kind: "idle" };
    }
    if (queuedPrompt(activeID) !== undefined) {
      return { kind: "queued" };
    }
    if (isThinking(activeID)) {
      return { kind: "busy" };
    }
    return { kind: "idle" };
  }
}

const instance = new SendStateController();

export function setSSEStatus(s: ConnectionStatus): void {
  instance.setSSEStatus(s);
}
export function setLastError(msg: string): void {
  instance.setLastError(msg);
}
export function clearLastError(): void {
  instance.clearLastError();
}
export function recompute(): void {
  instance.recompute();
}

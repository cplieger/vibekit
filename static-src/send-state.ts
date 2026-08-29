// ---------------------------------------------------------------------------
// Derives the send-button state from the global pieces of the world:
//   - SSE connection status               (signal, set by transport.ts)
//   - the agent being unreachable         (signal, set by handlers/turn.ts)
//   - thinking flag for the active chat   (reactive via the store)
//
// The two externally-driven inputs are `signal`s; the resulting SendState is a
// `computed` that also reads the already-reactive `activeSession` store input,
// so it auto-tracks every dependency. A single `effect` pushes the value to
// prompt-input.ts on any change — no manual recompute call anywhere.
//
// Precedence: disconnected > sendBlocked > streaming > idle.
//
// THE ERROR FACE MEANS "A SEND CANNOT SUCCEED RIGHT NOW", NOT "THE LAST SEND
// FAILED", and narrowing it to that is the point of this file's current shape
// (2026-08, user decision). The `sendBlocked` rung used to be a general
// `lastError` that every failure wrote: a 429 throttle, a 5xx, a dead POST, a
// timeout, a refused model switch. An alert icon on the one control whose job
// is to send communicates that the chat is dead and nothing can be sent, which
// was false in every one of those cases — a failed prompt leaves the server
// IDLE (CmdPrompt's deferred ReleaseAfterPrompt runs on the error path too), so
// the chat is promptable the instant the error lands. Those failures go to
// failure-notice.ts (a toast) and to the turn's own transcript divider, which
// are surfaces that report a past event without making a claim about the
// future.
//
// Three states earn the face, and each is a statement about what happens to a
// send NOW rather than about one past attempt:
//   - the SSE stream is down, so this client is not talking to the server at all
//   - `bridge_start_failed`, so kiro-cli could not be spawned for this chat and
//     there is no ACP connection behind it to send to
//   - a prompt was refused with 409 reason:"starting" — the chat's admission
//     slot is held by something that cannot take a steer (a cold spawn, a
//     shell, a prime), so a send right now cannot land. submit.ts owns that
//     copy and pushes it through `reportSendRefused`.
//
// Neither rung is a LOCK. The composer stays live through both: nothing here sets
// `disabled`, because a dropped SSE stream says nothing about the command POST
// (different connection, usually still lands, and the reconnect replay catches the
// transcript up), and a bridge that failed to start is retried by the next prompt.
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
/** The reason a send cannot succeed right now, or "" when none is known. Fed by
 *  two writers with distinct meanings — setAgentDown (no agent behind this
 *  chat) and reportSendRefused (admission refused, holder cannot take a
 *  steer) — sharing one signal because they share the surface AND the
 *  lifecycle: cleared on every new attempt, on chat switch, on turn end and on
 *  SSE reconnect. */
const sendBlocked = signal("");

const sendState = computed<SendState>(() => {
  if (sseStatus.value === "disconnected") {
    return { kind: "error", reason: "Disconnected from the server. Reconnecting…" };
  }
  if (sendBlocked.value !== "") {
    return { kind: "error", reason: sendBlocked.value };
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

// Clear a stale send-blocked state when the active chat changes. The signal
// is global but it is raised for ONE chat: a bridge that could not start (or an
// admission refusal) belongs to the chat that asked, and the next chat may be
// perfectly sendable. The errors that set it emit no turn_ended to clear it, so
// without this one chat's dead bridge decorates the button on every chat.
let sendBlockedActiveID = "";
effect(() => {
  const id = activeSession.value?.id ?? "";
  if (id !== sendBlockedActiveID) {
    sendBlockedActiveID = id;
    if (sendBlocked.peek() !== "") {
      sendBlocked.value = "";
    }
  }
});

export function setSSEStatus(s: ConnectionStatus): void {
  if (sseStatus.peek() === s) {
    return;
  }
  sseStatus.value = s;
  if (s === "connected") {
    sendBlocked.value = "";
  }
}

/** Report that there is no agent to send to: the ACP subprocess for this chat
 *  could not be started. Advisory — it changes the button's face and tooltip and
 *  never whether a send is allowed, because the next prompt is what retries the
 *  spawn. Anything that is merely a FAILED ATTEMPT goes to failure-notice.ts. */
export function setAgentDown(reason: string): void {
  sendBlocked.value = reason;
}

/** Report that the server refused a send it cannot deliver right now: the
 *  prompt admission answered 409 reason:"starting". The caller (submit.ts)
 *  owns the copy; this renders it on the send button's error face. Advisory
 *  exactly like setAgentDown — the composer stays live and the next Send is
 *  the retry, which is also what clears it (clearAgentDown runs on every
 *  attempt). */
export function reportSendRefused(reason: string): void {
  sendBlocked.value = reason;
}

/** Clear the send-blocked state. Called on every send attempt and at every
 *  turn end, so a bridge that has since started — or an admission slot that has
 *  since freed — leaves no residue on the button. */
export function clearAgentDown(): void {
  sendBlocked.value = "";
}

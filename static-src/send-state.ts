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
// Precedence: disconnected > agentDown > streaming > idle.
//
// THE ERROR FACE MEANS "THE AGENT IS NOT REACHABLE", NOT "THE LAST SEND FAILED",
// and narrowing it to that is the point of this file's current shape (2026-08,
// user decision). The `agentDown` rung used to be a general `lastError` that every
// failure wrote: a 429 throttle, a 5xx, a dead POST, a timeout, a refused model
// switch. An alert icon on the one control whose job is to send communicates that
// the chat is dead and nothing can be sent, which was false in every one of those
// cases — a failed prompt leaves the server IDLE (CmdPrompt's deferred
// ReleaseAfterPrompt runs on the error path too), so the chat is promptable the
// instant the error lands. Those failures go to failure-notice.ts (a toast) and to
// the turn's own transcript divider, which are surfaces that report a past event
// without making a claim about the future.
//
// Only two states earn the face, and both are statements about reachability
// rather than about one attempt:
//   - the SSE stream is down, so this client is not talking to the server at all
//   - `bridge_start_failed`, so kiro-cli could not be spawned for this chat and
//     there is no ACP connection behind it to send to
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
const agentDown = signal("");

const sendState = computed<SendState>(() => {
  if (sseStatus.value === "disconnected") {
    return { kind: "error", reason: "Disconnected from the server. Reconnecting…" };
  }
  if (agentDown.value !== "") {
    return { kind: "error", reason: agentDown.value };
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

// Clear a stale unreachable-agent state when the active chat changes. The signal
// is global but it is raised for ONE chat: a bridge that could not start belongs
// to the chat that asked for it, and the next chat may have a live bridge. The
// error that sets it emits no turn_ended to clear it, so without this one chat's
// dead bridge decorates the button on every chat.
let agentDownActiveID = "";
effect(() => {
  const id = activeSession.value?.id ?? "";
  if (id !== agentDownActiveID) {
    agentDownActiveID = id;
    if (agentDown.peek() !== "") {
      agentDown.value = "";
    }
  }
});

export function setSSEStatus(s: ConnectionStatus): void {
  if (sseStatus.peek() === s) {
    return;
  }
  sseStatus.value = s;
  if (s === "connected") {
    agentDown.value = "";
  }
}

/** Report that there is no agent to send to: the ACP subprocess for this chat
 *  could not be started. Advisory — it changes the button's face and tooltip and
 *  never whether a send is allowed, because the next prompt is what retries the
 *  spawn. Anything that is merely a FAILED ATTEMPT goes to failure-notice.ts. */
export function setAgentDown(reason: string): void {
  agentDown.value = reason;
}

/** Clear the unreachable-agent state. Called on every send attempt and at every
 *  turn end, so a bridge that has since started leaves no residue on the button. */
export function clearAgentDown(): void {
  agentDown.value = "";
}

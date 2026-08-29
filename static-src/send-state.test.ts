// Unit tests for send-state.ts — verifies the SendState computed derives the
// send-button state reactively from the SSE-status / agent-down signals plus
// the reactive store (activeSession), pushing to prompt-input via a single
// effect with NO manual recompute call.
//
// The vocabulary here is narrow on purpose: `agentDown` means there is nothing to
// send to, and an ordinary failed attempt never reaches it (failure-notice.ts owns
// that). A test that writes a throttle message into this signal is asserting the
// wrong thing about the button.
import { describe, it, expect, beforeEach, vi } from "vitest";
import type { SendState } from "./prompt-input.js";
import { setSendState } from "./prompt-input.js";
import { setSSEStatus, setAgentDown, clearAgentDown, reportSendRefused } from "./send-state.js";
import { setSessions, setActive, setThinking, recordSteerQueued } from "./store.js";
import type { Session } from "./types.js";

// Capture every SendState pushed to prompt-input without loading its DOM deps.
vi.mock("./prompt-input.js", () => ({ setSendState: vi.fn() }));

const pushed = vi.mocked(setSendState);

function lastPushed(): SendState | undefined {
  return pushed.mock.calls.at(-1)?.[0];
}

function makeSession(id: string): Session {
  return {
    id,
    name: "test",
    model: "",
    acp_session_id: "",
    current_mode_id: "",
    available_modes: [],
    available_models: [],
    supervised_mode: false,
    usage: {
      context_pct: 0,
      context_size: 0,
      credits: 0,
      turn_count: 0,
      last_turn_ms: 0,
      has_real_data: false,
    },
    message_count: 0,
    messages: [],
    has_more: false,
    thinking: false,
    working_label: "Thinking",
  };
}

beforeEach(() => {
  // Reset the shared module-level reactive graph to a known idle baseline.
  setActive("");
  setSessions([]);
  clearAgentDown();
  setSSEStatus("connecting");
  pushed.mockClear();
});

describe("send-state precedence", () => {
  it("orders disconnected > agentDown > streaming > idle", () => {
    const id = "c1";
    setSessions([makeSession(id)]);
    setActive(id);
    // Make every lower-priority condition true at once.
    setThinking(id, true);
    setAgentDown("The agent could not be started for this chat.");
    setSSEStatus("disconnected");
    expect(lastPushed()).toEqual({
      kind: "error",
      reason: "Disconnected from the server. Reconnecting…",
    });

    // Drop disconnected via "connecting" (not "connected", which would clear
    // agentDown) → agentDown now wins.
    setSSEStatus("connecting");
    expect(lastPushed()).toEqual({
      kind: "error",
      reason: "The agent could not be started for this chat.",
    });

    // Clear it → STREAMING wins. Cancelling the turn is what the one control has
    // to guarantee, so nothing below it may take the button.
    clearAgentDown();
    expect(lastPushed()).toEqual({ kind: "streaming" });

    // Turn ends → idle. There is no state between the two: a message typed
    // mid-turn was steered into that turn, so nothing is pending client-side to
    // report.
    setThinking(id, false);
    expect(lastPushed()).toEqual({ kind: "idle" });
  });

  // Both reachability rungs report `error`, and neither locks the composer: the
  // next Send respawns the bridge, and a dropped SSE stream says nothing about the
  // command POST. They used to push `blocked`, which disabled the textarea and
  // turned one throttled turn into a dead thread.
  it("reports both unreachable states as the advisory error state", () => {
    const id = "c1";
    setSessions([makeSession(id)]);
    setActive(id);
    setSSEStatus("connected");

    setAgentDown("The agent could not be started for this chat.");
    expect(lastPushed()?.kind).toBe("error");

    clearAgentDown();
    setSSEStatus("disconnected");
    expect(lastPushed()?.kind).toBe("error");
  });

  // The regression this file exists to catch after 2026-08: a failed ATTEMPT must
  // leave the button alone. Nothing on the prompt-failure path calls setAgentDown,
  // so a throttled turn on a connected client settles back to idle and the send
  // icon stays a send icon. If a future change routes prompt_failed here again,
  // this is what fails.
  it("leaves the button idle when a turn fails on a reachable agent", () => {
    const id = "c1";
    setSessions([makeSession(id)]);
    setActive(id);
    setSSEStatus("connected");
    setThinking(id, true);
    expect(lastPushed()).toEqual({ kind: "streaming" });

    // What handlers/turn.ts does for a `prompt_failed` frame, in full: clear
    // thinking, and report the prose somewhere that is not this module.
    setThinking(id, false);
    expect(lastPushed()).toEqual({ kind: "idle" });
  });

  // Outstanding steers must NOT reach the button. They are server state about
  // what the agent has read, shown on the chip row; putting them here would give
  // the one control two jobs, and during a turn it would stop offering Cancel.
  it("ignores pending steers entirely", () => {
    const id = "c1";
    setSessions([makeSession(id)]);
    setActive(id);
    setSSEStatus("connected");
    recordSteerQueued(id, { id: "steer-1", text: "one" });
    recordSteerQueued(id, { id: "steer-2", text: "two" });
    expect(lastPushed()).toEqual({ kind: "idle" });

    setThinking(id, true);
    expect(lastPushed()).toEqual({ kind: "streaming" });
  });
});

describe("send-state auto-tracking", () => {
  it("setThinking flips the pushed SendState with no explicit recompute", () => {
    const id = "c1";
    setSessions([makeSession(id)]);
    setActive(id);
    setSSEStatus("connected");
    expect(lastPushed()).toEqual({ kind: "idle" });
    pushed.mockClear();

    // Only a store mutation + flush — the computed re-derives on its own.
    setThinking(id, true);
    expect(pushed).toHaveBeenCalledTimes(1);
    expect(lastPushed()).toEqual({ kind: "streaming" });
  });
});

// The third face state: a refused send (409 reason:"starting"). submit.ts owns
// the copy; this module only renders it, on the same rung and with the same
// lifecycle as the unreachable-agent state — the next attempt clears it via
// clearAgentDown, and a chat switch drops it because the refusal was one
// chat's.
describe("send-state refused-send face", () => {
  it("renders the caller's copy through the error surface", () => {
    const id = "c1";
    setSessions([makeSession(id)]);
    setActive(id);
    setSSEStatus("connected");

    reportSendRefused("The chat is busy right now — send again to retry");
    expect(lastPushed()).toEqual({
      kind: "error",
      reason: "The chat is busy right now — send again to retry",
    });
  });

  it("outranks streaming, exactly like the agent-down rung", () => {
    // The store may read busy for the holder's own turn (SSE turn_state); the
    // refusal face must still win, or the reader never learns their send was
    // refused.
    const id = "c1";
    setSessions([makeSession(id)]);
    setActive(id);
    setSSEStatus("connected");
    setThinking(id, true);
    expect(lastPushed()).toEqual({ kind: "streaming" });

    reportSendRefused("The chat is busy right now — send again to retry");
    expect(lastPushed()?.kind).toBe("error");
  });

  it("clears on the next attempt (clearAgentDown), settling back to idle", () => {
    const id = "c1";
    setSessions([makeSession(id)]);
    setActive(id);
    setSSEStatus("connected");
    reportSendRefused("The chat is busy right now — send again to retry");

    // What submitPrompt does at the top of every attempt.
    clearAgentDown();
    expect(lastPushed()).toEqual({ kind: "idle" });
  });

  it("does not survive a chat switch — the refusal belonged to one chat", () => {
    setSessions([makeSession("c1"), makeSession("c2")]);
    setActive("c1");
    setSSEStatus("connected");
    reportSendRefused("The chat is busy right now — send again to retry");
    expect(lastPushed()?.kind).toBe("error");

    setActive("c2");
    expect(lastPushed()).toEqual({ kind: "idle" });
  });
});

describe("send-state disconnected override", () => {
  it("setSSEStatus('disconnected') wins over a live turn", () => {
    const id = "c1";
    setSessions([makeSession(id)]);
    setActive(id);
    setSSEStatus("connected");
    setThinking(id, true);
    expect(lastPushed()).toEqual({ kind: "streaming" });

    setSSEStatus("disconnected");
    expect(lastPushed()).toEqual({
      kind: "error",
      reason: "Disconnected from the server. Reconnecting…",
    });
  });
});

// Unit tests for send-state.ts — verifies the SendState computed derives the
// send-button state reactively from the SSE-status / last-error signals plus
// the reactive store (activeSession), pushing to prompt-input via a single
// effect with NO manual recompute call.
import { describe, it, expect, beforeEach, vi } from "vitest";
import { flushSync } from "@cplieger/reactive";
import type { SendState } from "./prompt-input.js";
import { setSendState } from "./prompt-input.js";
import { setSSEStatus, setLastError, clearLastError } from "./send-state.js";
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
  clearLastError();
  setSSEStatus("connecting");
  flushSync();
  pushed.mockClear();
});

describe("send-state precedence", () => {
  it("orders disconnected > lastError > streaming > idle", () => {
    const id = "c1";
    setSessions([makeSession(id)]);
    setActive(id);
    // Make every lower-priority condition true at once.
    setThinking(id, true);
    setLastError("boom");
    setSSEStatus("disconnected");
    flushSync();
    expect(lastPushed()).toEqual({
      kind: "error",
      reason: "Disconnected from the server. Reconnecting…",
    });

    // Drop disconnected via "connecting" (not "connected", which would clear
    // lastError) → lastError now wins.
    setSSEStatus("connecting");
    flushSync();
    expect(lastPushed()).toEqual({ kind: "error", reason: "boom" });

    // Clear the error → STREAMING wins. Cancelling the turn is what the one
    // control has to guarantee, so nothing below it may take the button.
    clearLastError();
    flushSync();
    expect(lastPushed()).toEqual({ kind: "streaming" });

    // Turn ends → idle. There is no state between the two: a message typed
    // mid-turn was steered into that turn, so nothing is pending client-side to
    // report.
    setThinking(id, false);
    flushSync();
    expect(lastPushed()).toEqual({ kind: "idle" });
  });

  // Every failure rung reports `error`, never a state that locks the composer.
  // A failed prompt leaves the server idle and a dropped SSE stream says nothing
  // about the command POST, so the next Send is the retry in both cases. The two
  // rungs used to push `blocked`, which disabled the textarea and turned one
  // throttled turn into a dead thread.
  it("reports every failure as the advisory error state", () => {
    const id = "c1";
    setSessions([makeSession(id)]);
    setActive(id);
    setSSEStatus("connected");
    flushSync();

    setLastError('{"errorType":"ClientThrottleError","retryErrorType":"THROTTLING"}');
    flushSync();
    expect(lastPushed()?.kind).toBe("error");

    clearLastError();
    setSSEStatus("disconnected");
    flushSync();
    expect(lastPushed()?.kind).toBe("error");
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
    flushSync();
    expect(lastPushed()).toEqual({ kind: "idle" });

    setThinking(id, true);
    flushSync();
    expect(lastPushed()).toEqual({ kind: "streaming" });
  });
});

describe("send-state auto-tracking", () => {
  it("setThinking flips the pushed SendState with no explicit recompute", () => {
    const id = "c1";
    setSessions([makeSession(id)]);
    setActive(id);
    setSSEStatus("connected");
    flushSync();
    expect(lastPushed()).toEqual({ kind: "idle" });
    pushed.mockClear();

    // Only a store mutation + flush — the computed re-derives on its own.
    setThinking(id, true);
    flushSync();
    expect(pushed).toHaveBeenCalledTimes(1);
    expect(lastPushed()).toEqual({ kind: "streaming" });
  });
});

describe("send-state disconnected override", () => {
  it("setSSEStatus('disconnected') wins over a live turn", () => {
    const id = "c1";
    setSessions([makeSession(id)]);
    setActive(id);
    setSSEStatus("connected");
    setThinking(id, true);
    flushSync();
    expect(lastPushed()).toEqual({ kind: "streaming" });

    setSSEStatus("disconnected");
    flushSync();
    expect(lastPushed()).toEqual({
      kind: "error",
      reason: "Disconnected from the server. Reconnecting…",
    });
  });
});

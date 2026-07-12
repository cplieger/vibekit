// Unit tests for send-state.ts — verifies the SendState computed derives the
// send-button state reactively from the SSE-status / last-error signals plus
// the reactive store (activeSession), pushing to prompt-input via a single
// effect with NO manual recompute call.
import { describe, it, expect, beforeEach, vi } from "vitest";
import { flushSync } from "@cplieger/reactive";
import type { SendState } from "./prompt-input.js";
import { setSendState } from "./prompt-input.js";
import { setSSEStatus, setLastError, clearLastError } from "./send-state.js";
import { setSessions, setActive, setThinking, enqueuePrompt, dequeuePrompt } from "./store.js";
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
    pending_changes: [],
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
  it("orders disconnected > lastError > queued > thinking > idle", () => {
    const id = "c1";
    setSessions([makeSession(id)]);
    setActive(id);
    // Make every lower-priority condition true at once.
    setThinking(id, true);
    enqueuePrompt(id, "hi");
    setLastError("boom");
    setSSEStatus("disconnected");
    flushSync();
    expect(lastPushed()).toEqual({
      kind: "blocked",
      reason: "Disconnected from the server. Reconnecting…",
    });

    // Drop disconnected via "connecting" (not "connected", which would clear
    // lastError) → lastError now wins.
    setSSEStatus("connecting");
    flushSync();
    expect(lastPushed()).toEqual({ kind: "blocked", reason: "boom" });

    // Clear the error → queued wins.
    clearLastError();
    flushSync();
    expect(lastPushed()).toEqual({ kind: "queued" });

    // Drain the queue → thinking wins.
    dequeuePrompt(id);
    flushSync();
    expect(lastPushed()).toEqual({ kind: "busy" });

    // Stop thinking → idle.
    setThinking(id, false);
    flushSync();
    expect(lastPushed()).toEqual({ kind: "idle" });
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
    expect(lastPushed()).toEqual({ kind: "busy" });
  });
});

describe("send-state disconnected override", () => {
  it("setSSEStatus('disconnected') wins over a queued state", () => {
    const id = "c1";
    setSessions([makeSession(id)]);
    setActive(id);
    setSSEStatus("connected");
    enqueuePrompt(id, "hi");
    flushSync();
    expect(lastPushed()).toEqual({ kind: "queued" });

    setSSEStatus("disconnected");
    flushSync();
    expect(lastPushed()).toEqual({
      kind: "blocked",
      reason: "Disconnected from the server. Reconnecting…",
    });
  });
});

// @vitest-environment happy-dom
// ---------------------------------------------------------------------------
// Tests for handlers/steer.ts: the only writer of `session.steers`.
//
// The real store is driven so the projection is observable; only the bus is
// mocked, to capture the handler registrations.
//
// These cases are about the WIRE rather than about the store's mechanics (which
// store.test.ts owns): the three frames arriving in the wrong order, twice, or
// without each other, because that is what an SSE reconnect and a second device
// actually produce.
// ---------------------------------------------------------------------------

import { vi, describe, it, expect, beforeEach } from "vitest";
import { fireSSE, createBusMock } from "./__test-helpers__/sse-capture.js";

vi.mock("../bus.js", () => createBusMock());

import { setSessions, setActive, get, steerCount } from "../store.js";
import type { Session } from "../types.js";

// Import after the mock so the handler registers against it.
await import("./steer.js");

function makeSession(id: string): Session {
  return {
    id,
    name: "test",
    model: "claude",
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
  setSessions([makeSession("c1"), makeSession("c2")]);
  setActive("c1");
});

describe("steer_queued", () => {
  it("records the steer as waiting", () => {
    fireSSE("steer_queued", "c1", { steer_id: "steer-1", text: "use tabs" });
    expect(get("c1")?.steers).toEqual([{ id: "steer-1", text: "use tabs", injected: false }]);
  });

  it("carries a severity through when KAS classified a notification", () => {
    fireSSE("steer_queued", "c1", {
      steer_id: "notify-1",
      text: "[notification/error] step failed",
      severity: "error",
    });
    expect(get("c1")?.steers?.[0]?.severity).toBe("error");
  });

  // The row is per-chat, so a steer raised on a BACKGROUND chat must land on
  // that chat rather than on whichever one happens to be open.
  it("keys by the event's chat, not the active one", () => {
    fireSSE("steer_queued", "c2", { steer_id: "steer-1", text: "elsewhere" });
    expect(steerCount("c1")).toBe(0);
    expect(steerCount("c2")).toBe(1);
  });
});

describe("steer_injected", () => {
  it("flips a waiting steer to read", () => {
    fireSSE("steer_queued", "c1", { steer_id: "steer-1", text: "use tabs" });
    fireSSE("steer_injected", "c1", { steer_id: "steer-1", text: "use tabs" });
    expect(get("c1")?.steers?.[0]?.injected).toBe(true);
    expect(steerCount("c1")).toBe(1);
  });

  // Out of order, and it has to work: the injected frame can arrive first if the
  // queued one was dropped, or if another device sent the steer and this client
  // connected mid-turn. Silence here would mean the transcript shows the agent
  // changing course with nothing on screen explaining why.
  it("records a steer it never saw queued", () => {
    fireSSE("steer_injected", "c1", { steer_id: "steer-ghost", text: "from another tab" });
    expect(get("c1")?.steers).toEqual([
      { id: "steer-ghost", text: "from another tab", injected: true },
    ]);
  });

  // A reconnect replays the queued frame; it must not un-read what the model has
  // already consumed.
  it("survives a replayed queued frame afterwards", () => {
    fireSSE("steer_injected", "c1", { steer_id: "steer-1", text: "one" });
    fireSSE("steer_queued", "c1", { steer_id: "steer-1", text: "one" });
    expect(get("c1")?.steers?.[0]?.injected).toBe(true);
    expect(steerCount("c1")).toBe(1);
  });
});

describe("steer_cleared", () => {
  it("drops only the named steers", () => {
    fireSSE("steer_queued", "c1", { steer_id: "steer-1", text: "one" });
    fireSSE("steer_queued", "c1", { steer_id: "steer-2", text: "two" });
    fireSSE("steer_cleared", "c1", { steer_ids: ["steer-1"] });
    expect(get("c1")?.steers?.map((e) => e.id)).toEqual(["steer-2"]);
  });

  // Clearing by id rather than wholesale is what lets an explicit discard of two
  // steers coexist with a third that arrived while the request was in flight.
  it("leaves a steer that arrived after the cleared set was decided", () => {
    fireSSE("steer_queued", "c1", { steer_id: "steer-1", text: "one" });
    fireSSE("steer_queued", "c1", { steer_id: "steer-2", text: "two" });
    fireSSE("steer_queued", "c1", { steer_id: "steer-3", text: "three" });
    fireSSE("steer_cleared", "c1", { steer_ids: ["steer-1", "steer-2"] });
    expect(get("c1")?.steers?.map((e) => e.id)).toEqual(["steer-3"]);
  });

  it("removes the field entirely once the last steer goes", () => {
    fireSSE("steer_queued", "c1", { steer_id: "steer-1", text: "one" });
    fireSSE("steer_cleared", "c1", { steer_ids: ["steer-1"] });
    expect(get("c1")?.steers).toBeUndefined();
  });

  it("ignores ids it does not hold", () => {
    fireSSE("steer_queued", "c1", { steer_id: "steer-1", text: "one" });
    fireSSE("steer_cleared", "c1", { steer_ids: ["steer-nope"] });
    expect(steerCount("c1")).toBe(1);
  });
});

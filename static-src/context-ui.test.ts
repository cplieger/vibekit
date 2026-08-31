// ---------------------------------------------------------------------------
// The context breakdown's summarized-message count.
//
// The subject is what this module HANDS the renderer, so status.ts is replaced
// and the captured argument is the assertion surface. That is also the contract
// under test: `summarizedCount` absent means "this window cannot say", and the
// renderer prints nothing for it.
// ---------------------------------------------------------------------------

import { vi, describe, it, expect, beforeEach } from "vitest";
import type { Message, Session } from "./types.js";

const mocks = vi.hoisted(() => ({
  updateContextBar: vi.fn(),
  getActiveId: vi.fn(() => "other-chat"),
}));

vi.mock("./status.js", () => ({ updateContextBar: mocks.updateContextBar }));
vi.mock("./store.js", () => ({ getActiveId: mocks.getActiveId }));
vi.mock("./prompt-input.js", () => ({ contextFull: { value: false } }));
vi.mock("./effort.js", () => ({ nonDefaultEffortLabel: () => "" }));
vi.mock("./picker.js", () => ({ getCachedModels: () => [] }));
vi.mock("./session-context.js", () => ({ getLastEffortFor: () => "" }));

import { refreshContextUI } from "./context-ui.js";

function msg(id: string): Message {
  return { id, role: "assistant", content: "" } as Message;
}

function session(over: Partial<Session> = {}): Session {
  return {
    id: "c-1",
    model: "",
    messages: [msg("m1"), msg("m2"), msg("m3")],
    usage: {
      context_pct: 10,
      context_size: 200_000,
      credits: 0,
      turn_count: 1,
      last_turn_ms: 0,
    },
    ...over,
  } as Session;
}

/** The one `updateContextBar` argument this refresh produced. */
function bar(over: Partial<Session> = {}): Record<string, unknown> {
  refreshContextUI(session(over));
  expect(mocks.updateContextBar).toHaveBeenCalledTimes(1);
  return mocks.updateContextBar.mock.calls[0]?.[0] as Record<string, unknown>;
}

beforeEach(() => {
  vi.clearAllMocks();
});

describe("the summarized-message count", () => {
  it("counts up to and including the watermark message", () => {
    expect(bar({ compaction_watermark: "m2" })["summarizedCount"]).toBe(2);
  });

  it("counts every message when the watermark is the last one", () => {
    expect(bar({ compaction_watermark: "m3" })["summarizedCount"]).toBe(3);
  });

  it("reports zero when the chat carries no watermark", () => {
    expect(bar()["summarizedCount"]).toBe(0);
  });

  // THE DEFECT: the watermark rides the chat header while `messages` is a
  // paginated window, so a chat compacted before its oldest loaded message names
  // one that is not resident. Counting while scanning reported all three.
  it("withholds the count when the watermark is not in the loaded window", () => {
    const b = bar({ compaction_watermark: "m0-paged-out" });
    expect(b).not.toHaveProperty("summarizedCount");
    expect(b["msgCount"]).toBe(3);
  });

  // Withholding, not zero: zero says "nothing was summarized" on a chat that was
  // compacted, which is the same wrong readout with the sign flipped.
  it("does not report the withheld count as zero", () => {
    expect(bar({ compaction_watermark: "m0-paged-out" })["summarizedCount"]).not.toBe(0);
  });

  // A rewind truncates the messages and leaves the watermark naming a message
  // that no longer exists anywhere, for the life of the chat.
  it("withholds the count after a rewind past the compaction point", () => {
    expect(bar({ compaction_watermark: "m2", messages: [msg("m1")] })).not.toHaveProperty(
      "summarizedCount",
    );
  });

  it("counts nothing summarized in an empty window with no watermark", () => {
    expect(bar({ messages: [] })["summarizedCount"]).toBe(0);
  });
});

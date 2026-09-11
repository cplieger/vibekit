// The two teardown doors, against the REAL store.
//
// `store-load.test.ts` and `handlers/{turn,system}.test.ts` all mock this module,
// because none of them owns what it does — they own which door they reach. This
// file is where the doors themselves are driven, so the store is real and only the
// three surfaces `clearTurnState` reaches OUT to are replaced: the tab strip, the
// dock's queue and the model-switch queue. Those three are the observables for the
// facts the store cannot carry.
//
// The whole subject is the DIFFERENCE between the two doors, so every case is
// written so that swapping one for the other fails it.
import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";

const mocks = vi.hoisted(() => ({
  setTabStatus: vi.fn(),
  hasPendingDecision: vi.fn(() => false),
  drainModelSwitchQueue: vi.fn(),
}));

vi.mock("./tabs.js", async () => ({
  ...(await import("./__test-helpers__/tabs-mock.js")).tabsMock(),
  setTabStatus: mocks.setTabStatus,
}));
vi.mock("./decision-dock.js", () => ({ hasPendingDecision: mocks.hasPendingDecision }));
vi.mock("./model-switcher.js", () => ({ drainModelSwitchQueue: mocks.drainModelSwitchQueue }));

import { healSettledChat, retractStaleThinking } from "./turn-teardown.js";
import {
  chunkWatermark,
  get,
  isTruncatedSnapshot,
  liveTurnMessage,
  noteLiveTurnMessage,
  noteTruncatedSnapshot,
  removeChat,
  setChunkWatermark,
  setSessions,
  setThinking,
} from "./store.js";
import type { Message, Session } from "./types.js";
import type { TurnOutcome } from "./wire/types.gen.js";

const CHAT = "c-teardown";
const LIVE_MSG = "m-live";

function outcomeRow(outcome: TurnOutcome): Message {
  return { id: "m-done", role: "assistant", ts: 1, content: "done", turn_outcome: outcome };
}

function session(messages: Message[]): Session {
  return {
    id: CHAT,
    name: "teardown",
    model: "",
    acp_session_id: "",
    current_mode_id: "",
    supervised_mode: false,
    usage: {
      context_pct: 0,
      context_size: 0,
      credits: 0,
      turn_count: 0,
      last_turn_ms: 0,
      has_real_data: false,
    },
    message_count: messages.length,
    messages,
    has_more: false,
    thinking: false,
    working_label: "Thinking",
  };
}

/** A chat mid-turn, holding every per-turn marker plus a retained outcome from the
 *  turn BEFORE the one now running. That is the state both doors are reached in, and
 *  it is what makes the preservation assertions able to fail. */
function midTurn(outcome: TurnOutcome = "failed"): void {
  setSessions([session([outcomeRow(outcome)])]);
  setThinking(CHAT, true);
  setChunkWatermark(CHAT, LIVE_MSG, 7);
  noteLiveTurnMessage(CHAT, LIVE_MSG);
  noteTruncatedSnapshot(CHAT, LIVE_MSG);
}

beforeEach(() => {
  mocks.hasPendingDecision.mockReturnValue(false);
});

// `removeChat` is what clears the three side tables (watermarks, live-turn ids,
// truncated-snapshot notes) — they are Maps outside the session collection, so
// `setSessions` alone would carry one case's markers into the next.
afterEach(() => {
  removeChat(CHAT);
});

describe("healSettledChat", () => {
  it("clears every per-turn marker and THEN latches from the retained outcome", () => {
    midTurn("failed");

    healSettledChat(CHAT);

    const s = get(CHAT);
    expect(s?.thinking, "thinking").toBe(false);
    expect(chunkWatermark(CHAT, LIVE_MSG), "chunk watermark").toBeUndefined();
    expect(liveTurnMessage(CHAT), "live-turn marker").toBeUndefined();
    expect(isTruncatedSnapshot(CHAT, LIVE_MSG), "truncated-snapshot note").toBe(false);
    expect(mocks.drainModelSwitchQueue, "queued model switch drained").toHaveBeenCalledWith(CHAT);
    // The ORDERING is what this pins, and it is the whole reason the door runs the
    // teardown first: `relatchTurnVerdict` refuses a chat whose `thinking` is still
    // true, so a latch here can only have been taken after the clear.
    expect(s?.turn_failed, "latched from the retained outcome").toBe(true);
  });

  it("is a no-op on a chat with no row", () => {
    setSessions([]);

    expect(() => {
      healSettledChat("c-absent");
    }).not.toThrow();

    expect(get("c-absent"), "no row was minted").toBeUndefined();
  });
});

describe("retractStaleThinking", () => {
  it("clears thinking and latches from the retained outcome", () => {
    midTurn("completed");

    retractStaleThinking(CHAT);

    const s = get(CHAT);
    expect(s?.thinking, "thinking").toBe(false);
    expect(s?.turn_done, "latched from the retained outcome").toBe(true);
  });

  it("latches from the HEADER for a chat whose window was never fetched", () => {
    // The population this door actually reaches: the connect retraction names chats
    // the busy set omits, and a chat nobody has opened holds no message carrying an
    // outcome — so the relatch's header fallback is the only thing between it and
    // the hollow ring that means the chat has never initiated. The row carries the
    // header's outcome directly rather than being driven through `loadList`, which
    // is what keeps the case order-free: a retraction reaching this row before the
    // list landed would find no outcome and pass whether the fallback exists or not.
    setSessions([{ ...session([]), last_turn_outcome: "failed" }]);
    setThinking(CHAT, true);

    retractStaleThinking(CHAT);

    expect(get(CHAT)?.turn_failed, "latched from the header").toBe(true);
  });

  it("preserves the four facts that belong to a turn still streaming", () => {
    midTurn();

    retractStaleThinking(CHAT);

    // Each of the four is the whole difference from the full teardown, so each is
    // asserted on its own: the narrow door's statement is about this chat's own
    // turn, and every marker below belongs to whatever turn is genuinely still
    // streaming — above all the live-turn marker, which is the only thing stopping
    // `loadMessages`' array replacement from deleting an in-flight reply.
    expect(liveTurnMessage(CHAT), "live-turn marker").toBe(LIVE_MSG);
    expect(chunkWatermark(CHAT, LIVE_MSG), "chunk watermark").toBe(7);
    expect(isTruncatedSnapshot(CHAT, LIVE_MSG), "truncated-snapshot note").toBe(true);
    expect(mocks.drainModelSwitchQueue, "queued model switch").not.toHaveBeenCalled();
  });

  it("paints no tab status of its own", () => {
    midTurn();

    retractStaleThinking(CHAT);

    // The full teardown ends in a repaint; this door leans on the session signal
    // `setThinking` already churns, which is what makes it paint once where the
    // full form paints twice.
    expect(mocks.setTabStatus).not.toHaveBeenCalled();
  });
});

describe("the two doors are not interchangeable", () => {
  // BOTH directions over one fact, so a copy-paste that swaps them fails whichever
  // way round it was written.
  it("drops the live-turn marker on the full form and keeps it on the narrow one", () => {
    midTurn();
    healSettledChat(CHAT);
    expect(liveTurnMessage(CHAT), "full teardown").toBeUndefined();

    removeChat(CHAT);
    midTurn();
    retractStaleThinking(CHAT);
    expect(liveTurnMessage(CHAT), "narrow retraction").toBe(LIVE_MSG);
  });
});

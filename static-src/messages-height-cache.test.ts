// When the transcript FORGETS what it measured.
//
// `block-heights.ts` is the one per-message store an unmount deliberately keeps:
// its numbers are what price the spacers standing in for ordinals nobody has
// mounted, so dropping them at the unmount would defeat the cache. That leaves the
// view's dispose as the moment they stop standing for anything, and until 2026-09
// `forgetHeights` had no production caller at all — every measurement a session ever
// took outlived its chat.
import { describe, it, expect, vi, beforeEach } from "vitest";

for (const id of [
  "messages",
  "messages-wrap",
  "messages-wrap-outer",
  "chat-view",
  "scroll-bottom",
]) {
  const d = document.createElement("div");
  d.id = id;
  document.body.appendChild(d);
}

// scroll.ts is a self-initialising singleton over a real scroller; the canonical
// mock is what every other suite in this graph uses.
vi.mock("./scroll.js", () => import("./__test-helpers__/scroll-mock.js").then((m) => m.scrollMock));
vi.mock("./actions/messages.js", () => ({
  copyClipboard: { dispatch: () => Promise.resolve() },
  explainError: { dispatch: () => Promise.resolve(null) },
}));
vi.mock("./api-client.js", async () => ({
  ...(await vi.importActual<Record<string, unknown>>("./api-client.js")),
  apiGet: vi.fn(() => Promise.resolve(null)),
}));

const { mountChatView, disposeChatView } = await import("./messages.js");
const { setSessions, setActive, bumpMessages } = await import("./store.js");
const { recordRowHeight, spacerHeight } = await import("./block-heights.js");
const { resetFoldState } = await import("./fold-state.js");
const { resetTurnRail } = await import("./turn-rail.js");

import type { Turn } from "./turns.js";
import type { Message } from "./types.js";

/** What an unmeasured text block costs, and `.turn-body`'s flex gap. */
const TEXT_PX = 48;
const GAP_PX = 12;

/** Eight text blocks in one row: the COLD price of the turn below. */
const COLD_PX = 8 * TEXT_PX + 7 * GAP_PX;

/** A height no estimate can produce, so a read that returns it can only have come
 *  from the cache. */
const MEASURED_PX = 999;

function reply(): Message {
  return {
    id: "a1",
    role: "assistant",
    ts: 2,
    content: "",
    blocks: Array.from({ length: 8 }, (_, b) => ({ type: "text", text: `chunk ${String(b)}` })),
  } as unknown as Message;
}

/** The projection `spacerHeight` takes, built by hand: this suite is about the
 *  cache's lifetime, not about what the renderer projects. */
function turn(): Turn {
  return {
    id: "u1",
    n: 1,
    trigger: undefined,
    body: [reply()],
    ts: 1,
    outcome: "completed",
    rewindTo: undefined,
  };
}

/** The whole turn, priced: nothing mounted, so the tail spacer stands in for all
 *  eight ordinals. */
function wholeTurn(): number {
  return spacerHeight(turn(), { from: 0, to: 0 }, "tail");
}

function activate(chatID: string): void {
  setSessions([
    {
      id: chatID,
      name: "c",
      model: "",
      acp_session_id: "",
      current_mode_id: "",
      supervised_mode: false,
      effort: "",
      effort_levels: [],
      effort_active: "",
      usage: { context_size: 0 },
      message_count: 2,
      messages: [{ id: "u1", role: "user", ts: 1, content: "prompt" }, reply()],
      has_more: false,
      thinking: false,
      working_label: "Thinking",
    },
  ] as never);
  setActive(chatID);
  bumpMessages(chatID);
}

beforeEach(() => {
  mountChatView();
  localStorage.clear();
  resetFoldState();
  resetTurnRail();
  setSessions([] as never);
  setActive("");
});

describe("the measurement cache over a view's life", () => {
  it("answers from the measurement while the view lives", () => {
    // The control. Without it the case below passes for the wrong reason: a
    // `spacerHeight` that never consulted the cache also returns the estimate.
    activate("c-height-1");
    recordRowHeight("a1", { from: 0, to: 8 }, MEASURED_PX);
    expect(wholeTurn()).toBe(MEASURED_PX);
    bumpMessages("c-height-1", "shape");
    expect(wholeTurn()).toBe(MEASURED_PX);
  });

  it("forgets the chat's measurements when its view is disposed", () => {
    activate("c-height-2");
    recordRowHeight("a1", { from: 0, to: 8 }, MEASURED_PX);
    expect(wholeTurn()).toBe(MEASURED_PX);

    // Tab close, LRU eviction and teardown all run this. The DOM those numbers
    // described is gone, so they price nothing that exists.
    disposeChatView("c-height-2");
    expect(wholeTurn()).toBe(COLD_PX);
  });
});

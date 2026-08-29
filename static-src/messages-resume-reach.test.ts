// ---------------------------------------------------------------------------
// The resume control counts blocks the reader can REACH.
//
// A delegate's blocks are members of the PARENT assistant message's `blocks`
// array, so the naive sum over `m.blocks.length` counts them whether or not
// anything on the page renders them. It does not: a delegate card collapses to
// `block-size: 0` with `overflow: hidden`, so while it is shut its members
// contribute zero document height. A count that includes them makes the control
// promise a distance that does not exist — the reader resumes expecting N new
// blocks and lands on the view they parked at.
//
// Both directions are asserted, for both container kinds:
//   - a COLLAPSED delegate's blocks are not counted; expanding its card counts
//     the same blocks;
//   - a workflow step's blocks need BOTH containers open (the run card AND the
//     step row inside it), because either one shut hides the step's body.
//
// The label is read through the scroll mock's `setResumeLabel`, and the count is
// driven through the reading-state callback `initFollowModel` registers — the
// same path the real scroller drives. `readingState` is mocked independently of
// the callback argument, which is what lets a second call recompute the label
// without re-capturing the baseline.
// ---------------------------------------------------------------------------

import { describe, it, expect, vi, beforeEach } from "vitest";
import type { Block, Message } from "./types.js";
import type { ReadingState } from "./scroll.js";
import type * as Store from "./store.js";

/** Just enough of a session for the count: the id `getActiveId` reports and the
 *  messages `blockCount` walks. */
interface TestSession {
  id: string;
  messages: Message[];
  thinking?: boolean;
}

// messages.ts reads `$.messages` and `$.messagesWrapOuter` at module scope /
// mount, and `byId` throws on a missing element — so the hosts exist before any
// import resolves.
document.body.innerHTML = `<div id="messages-wrap-outer"><div id="messages"></div></div>`;

// The SHARED scroll mock, not a copy of it. A hand-rolled one here drifts
// silently: messages.ts imports ./turn-rail.js, which imports `scrollableBy` from
// ./scroll.js, and a factory namespace missing that name produces "[vitest] There
// was an error when mocking a module" with no file, no export and no import chain
// — visible only in a FULL-suite run while this file stays green in isolation.
// __test-helpers__/scroll-mock.test.ts guards the helper's totality; it cannot
// guard an inline copy, which is exactly how the copy this replaced went stale
// (it still carried `scrollEl`, deleted upstream, and lacked `scrollableBy`).
vi.mock("./scroll.js", () => import("./__test-helpers__/scroll-mock.js").then((m) => m.scrollMock));
import { scrollMock } from "./__test-helpers__/scroll-mock.js";

// The store is the REAL module with two readers overridden, rather than a
// hand-written stand-in: every module in messages.ts's graph imports from it and
// real-ESM linking checks the whole surface, so a literal mock is ~50 names that
// drift the moment the store gains one.
//
// The session is handed over through a PLAIN variable, not a signal, and that is
// deliberate: the active-id and per-chat version signals stay real and
// untouched, so writing a session here cannot wake the paint effect. The subject is
// the count, and a paint would drag the whole turn renderer in behind it for
// nothing.
let currentSession: TestSession | undefined;
vi.mock("./store.js", async () => {
  const actual = await vi.importActual<typeof Store>("./store.js");
  return {
    ...actual,
    getActive: () => currentSession,
    getActiveId: () => currentSession?.id ?? "",
  };
});

const messages = await import("./messages.js");

/** The reading-state listener messages.ts registered at mount. Captured once —
 *  `mountChatView` is idempotent, so it registers exactly one. */
let onReading: (s: ReadingState) => void;

messages.mountChatView();
{
  const call = scrollMock.onReadingStateChange.mock.calls[0];
  if (call === undefined) {
    throw new Error("mountChatView registered no reading-state listener");
  }
  onReading = call[0] as (s: ReadingState) => void;
}

const messagesEl = document.getElementById("messages")!;

/** One text block, optionally attributed to a delegate. */
function block(text: string, subtask?: string): Block {
  return subtask === undefined
    ? { type: "text", text }
    : { type: "text", text, agent_subtask_id: subtask };
}

/** One assistant message carrying the given blocks. */
function msg(blocks: Block[]): Message {
  return { id: "m-1", role: "assistant", content: "", blocks } as Message;
}

/** A delegate card as `messages-blocks.ts` builds it: `.subagent-block` carrying
 *  its subtask id, `collapsed` unless the reader opened it. */
function delegateCard(subtask: string, open: boolean): void {
  const box = document.createElement("div");
  box.className = open ? "subagent-block" : "subagent-block collapsed";
  box.dataset["subtask"] = subtask;
  messagesEl.appendChild(box);
}

/** A run card and one step row, as `fundamentals/run-card.ts` builds them: the
 *  card keyed by `data-run` (mounts OPEN), the row by `data-node` (mounts
 *  COLLAPSED). Both carry the `collapsed` class when shut. */
function runCard(workflowID: string, nodePath: string, cardOpen: boolean, rowOpen: boolean): void {
  const card = document.createElement("div");
  card.className = cardOpen ? "run-card" : "run-card collapsed";
  card.dataset["run"] = workflowID;
  const row = document.createElement("div");
  row.className = rowOpen ? "run-step" : "run-step collapsed";
  row.dataset["node"] = nodePath;
  card.appendChild(row);
  messagesEl.appendChild(card);
}

/** Park the reader with a zero baseline, then hand them `session` and read the
 *  label back.
 *
 *  Two calls into the listener: the first captures the baseline (the "reading"
 *  ARGUMENT is what does that, per `initFollowModel`), the second only recomputes.
 *  `readingState` is mocked independently of the argument, which is what lets the
 *  second call reach `refreshResumeLabel`'s body instead of its early return. */
function labelFor(session: TestSession): string {
  scrollMock.readingState.mockReturnValue("reading");
  currentSession = { id: session.id, messages: [] };
  onReading("reading");
  currentSession = session;
  scrollMock.setResumeLabel.mockClear();
  onReading("following");
  const call = scrollMock.setResumeLabel.mock.calls.at(-1);
  return call === undefined ? "<no label>" : (call[0] as string);
}

beforeEach(() => {
  messagesEl.replaceChildren();
  scrollMock.setResumeLabel.mockClear();
});

describe("the resume label counts only blocks the reader can reach", () => {
  const withDelegate = (): TestSession => ({
    id: "c-1",
    messages: [
      msg([
        block("parent one"),
        block("parent two"),
        block("delegate a", "sa-1"),
        block("delegate b", "sa-1"),
        block("delegate c", "sa-1"),
      ]),
    ],
  });

  it("does NOT count a collapsed delegate's blocks", () => {
    delegateCard("sa-1", false);
    // Two parent blocks are inline; the three inside the shut card contribute no
    // document height, so the reader has two blocks of distance to travel.
    expect(labelFor(withDelegate())).toBe("2 new blocks");
  });

  it("counts the SAME blocks once the delegate's card is expanded", () => {
    delegateCard("sa-1", true);
    expect(labelFor(withDelegate())).toBe("5 new blocks");
  });

  it("counts a block whose delegate card is not on the page at all as unreachable", () => {
    // No card mounted: the id resolves to no container, so its blocks render
    // nowhere and must not be promised.
    expect(labelFor(withDelegate())).toBe("2 new blocks");
  });

  it("counts parent-stream blocks whatever is collapsed around them", () => {
    delegateCard("sa-1", false);
    const s: TestSession = { id: "c-1", messages: [msg([block("only parent")])] };
    expect(labelFor(s)).toBe("1 new block");
  });
});

describe("a workflow step needs BOTH of its containers open", () => {
  const STEP = "wf:wf_1:wf_1/build";
  const stepSession = (): TestSession => ({
    id: "c-2",
    messages: [msg([block("parent"), block("step out", STEP), block("step err", STEP)])],
  });

  it("does not count a step whose ROW is collapsed, even with the card open", () => {
    runCard("wf_1", "wf_1/build", true, false);
    // This is the mount state: a run card opens, its step rows do not.
    expect(labelFor(stepSession())).toBe("1 new block");
  });

  it("does not count a step whose CARD is collapsed, even with the row open", () => {
    runCard("wf_1", "wf_1/build", false, true);
    expect(labelFor(stepSession())).toBe("1 new block");
  });

  it("counts the step's blocks when the card AND the row are open", () => {
    runCard("wf_1", "wf_1/build", true, true);
    expect(labelFor(stepSession())).toBe("3 new blocks");
  });

  it("keys a step by its own node path, not by its run", () => {
    // The open row is a DIFFERENT step of the same run: reassembling
    // `wf:<run>:<node>` is what keeps one open row from vouching for its siblings.
    runCard("wf_1", "wf_1/lint", true, true);
    expect(labelFor(stepSession())).toBe("1 new block");
  });
});

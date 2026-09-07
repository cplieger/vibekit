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
// Both directions are asserted for the one container kind that has them:
//   - a COLLAPSED delegate's blocks are not counted; expanding its card counts
//     the same blocks.
//
// A WORKFLOW STEP is the other half and it has no directions at all: its blocks
// are DROPPED by the dispatcher, so nothing renders them and folding the run card
// either way changes nothing. That used to be a four-case describe about needing
// BOTH containers open (the card and the step row inside it); it is one case now,
// asserting the count does not move.
//
// Reachability is the renderer's open-container registry now (messages-blocks
// maintains it where the disclosures toggle), so the containers here are the
// REAL views: the store lands the message, the paint mounts the delegate boxes
// and run cards, and the tests open them by clicking the same headers a reader
// clicks. The count recomputes on FULL passes only, so each flow is production's
// own order — park (baseline), message arrives (shape paint counts the mount
// state), a disclosure click plus the next shape paint counts the change.
// The label is read through the scroll mock's `setResumeLabel`, and the count is
// driven through the reading-state callback `initFollowModel` registers — the
// same path the real scroller drives.
// ---------------------------------------------------------------------------

import { describe, it, expect, vi, beforeEach } from "vitest";
import type { Block, Message, Session } from "./types.js";
import type { ReadingState } from "./scroll.js";

// messages.ts's graph reads the shared DOM registry at module scope / mount,
// and `byId` throws on a missing element — so the hosts exist before any import
// resolves (the composer pair for the send-state effect the graph wires).
for (const id of [
  "messages",
  "messages-wrap",
  "messages-wrap-outer",
  "chat-view",
  "scroll-bottom",
  "send-btn",
  "prompt-input",
]) {
  const d = document.createElement(id === "prompt-input" ? "textarea" : "div");
  d.id = id;
  document.body.appendChild(d);
}

// The SHARED scroll mock, not a copy of it. A hand-rolled one here drifts
// silently: messages.ts imports ./turn-rail.js, which imports `scrollableBy` from
// ./scroll.js, and a factory namespace missing that name produces "[vitest] There
// was an error when mocking a module" with no file, no export and no import chain
// — visible only in a FULL-suite run while this file stays green in isolation.
// __test-helpers__/scroll-mock.test.ts guards the helper's totality; it cannot
// guard an inline copy, which is exactly how the copy this replaced went stale.
vi.mock("./scroll.js", () => import("./__test-helpers__/scroll-mock.js").then((m) => m.scrollMock));
import { scrollMock } from "./__test-helpers__/scroll-mock.js";

const store = await import("./store.js");
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

/** One text block, optionally attributed to a delegate or a workflow step. */
function block(text: string, subtask?: string): Block {
  return (
    subtask === undefined
      ? { type: "text", text }
      : { type: "text", text, agent_subtask_id: subtask }
  ) as Block;
}

/** Chat ids are minted per flow so every mount is a fresh chat switch; message
 *  ids ride them because the reconcile is keyed by message id across chats. */
let chatSeq = 0;

function session(id: string): Session {
  return {
    id,
    name: id,
    messages: [],
    message_count: 0,
    has_more: false,
    thinking: false,
    working_label: "",
  } as unknown as Session;
}

/** Park the reader on an empty chat (zero baseline), then land one assistant
 *  message carrying `blocks` — the shape paint mounts its REAL containers and
 *  recounts. Returns the chat id for follow-up toggles. */
function parkThenLand(blocks: Block[]): string {
  const chat = `c-${String(++chatSeq)}`;
  store.setSessions([session(chat)]);
  store.setActive(chat);
  scrollMock.readingState.mockReturnValue("reading");
  onReading("reading"); // baseline: nothing reachable yet
  store.appendMessage(chat, {
    id: `m-${String(chatSeq)}`,
    role: "assistant",
    ts: 2,
    content: "",
    blocks,
  } as Message);
  return chat;
}

/** The label after the LAST full pass. */
function lastLabel(): string {
  const call = scrollMock.setResumeLabel.mock.calls.at(-1);
  return call === undefined ? "<no label>" : (call[0] as string);
}

/** Click a real disclosure header, then repaint (a toggle bumps no version of
 *  its own, and the count recomputes on full passes only). */
function toggleAndRepaint(chat: string, selector: string): void {
  const header = messagesEl.querySelector<HTMLElement>(selector);
  if (header === null) {
    throw new Error(`no disclosure header for ${selector}`);
  }
  header.click();
  store.bumpMessages(chat);
}

beforeEach(() => {
  scrollMock.setResumeLabel.mockClear();
});

describe("the resume label counts only blocks the reader can reach", () => {
  const delegateBlocks = (): Block[] => [
    block("parent one"),
    block("parent two"),
    block("delegate a", "sa-1"),
    block("delegate b", "sa-1"),
    block("delegate c", "sa-1"),
  ];

  it("does NOT count a collapsed delegate's blocks", () => {
    parkThenLand(delegateBlocks());
    // The mount state: a delegate box is collapsed by default, so the three
    // blocks inside it contribute no document height — two blocks of distance.
    expect(lastLabel()).toBe("2 new blocks");
  });

  it("counts the SAME blocks once the delegate's card is expanded", () => {
    const chat = parkThenLand(delegateBlocks());
    toggleAndRepaint(chat, '.subagent-block[data-subtask="sa-1"] > .subagent-header');
    expect(lastLabel()).toBe("5 new blocks");
  });

  it("stops counting them again when the reader closes the card", () => {
    const chat = parkThenLand(delegateBlocks());
    toggleAndRepaint(chat, '.subagent-block[data-subtask="sa-1"] > .subagent-header');
    toggleAndRepaint(chat, '.subagent-block[data-subtask="sa-1"] > .subagent-header');
    expect(lastLabel()).toBe("2 new blocks");
  });

  it("counts parent-stream blocks whatever is collapsed around them", () => {
    parkThenLand([block("only parent"), block("delegate", "sa-1")]);
    expect(lastLabel()).toBe("1 new block");
  });
});

describe("a workflow step's blocks are never reachable", () => {
  const STEP = "wf:wf_1:wf_1/build";
  // The launch, so the card exists to fold: it is the card's only creator now.
  const launch = {
    id: "t1",
    title: "Run Workflow",
    kind: "other",
    status: "completed",
    workflow_id: "wf_1",
  };
  const stepBlocks = (): Block[] => [
    block("parent"),
    { type: "tool_use", tool_call_id: "t1" } as Block,
    block("step out", STEP),
    block("step err", STEP),
  ];
  const cardHead = '.run-card[data-run="wf_1"] > .run-head';

  /** Park, then land a message whose card is the launch and whose step blocks are
   *  dropped. Same flow as `parkThenLand`, plus the tool call the card needs. */
  function landWithLaunch(): string {
    const chat = `c-wf-${String(Date.now())}${String(Math.random()).slice(2, 8)}`;
    store.setSessions([session(chat)]);
    store.setActive(chat);
    scrollMock.readingState.mockReturnValue("reading");
    onReading("reading");
    store.appendMessage(chat, {
      id: `m-wf-${String(Math.random()).slice(2, 8)}`,
      role: "assistant",
      ts: 2,
      content: "",
      blocks: stepBlocks(),
      tool_calls: [launch],
    } as unknown as Message);
    return chat;
  }

  // ONE case, both card states, because the count no longer depends on either: a
  // step's blocks are DROPPED by the dispatcher, so nothing on the page renders them
  // and no disclosure can make them reachable. The parent block and the tool_use
  // block that became the card are the two the reader can reach.
  it("counts neither with the card open nor with it closed", () => {
    const chat = landWithLaunch();
    expect(lastLabel()).toBe("2 new blocks");
    toggleAndRepaint(chat, cardHead); // the card mounts open, so this closes it
    expect(lastLabel()).toBe("2 new blocks");
    toggleAndRepaint(chat, cardHead); // and open again
    expect(lastLabel()).toBe("2 new blocks");
  });
});

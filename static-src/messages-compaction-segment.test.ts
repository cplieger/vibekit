// ---------------------------------------------------------------------------
// A compaction that lands MID-TURN, as the transcript renders it.
//
// The server now seals the turn at the compaction point, so both the live path
// and the `session/load` replay produce [user, segment-1, event, segment-2].
// This file is the claim that the CLIENT needed no change for that: a turn body
// is a keyed list read in array order, and the caret follows the LAST assistant
// message of a thinking session, so segment 1 seals itself the moment segment 2
// exists.
//
// It drives the real renderer through the real store, like
// messages-paint-causes.test.ts, because reasoning from `isLikelyLiveStreaming`
// is what a test here is meant to replace.
// ---------------------------------------------------------------------------

import { describe, it, expect, vi } from "vitest";
import { KEY_ATTR } from "@cplieger/reactive";
import type { Message, Session } from "./types.js";

// The renderer's import graph reaches the shared DOM registry, which throws on a
// missing app root, so these ids exist before the import is evaluated.
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

// Only the scroller is replaced (the shared helper, so its surface stays total);
// the projection, the reconcile, the fold pass and the block dispatcher are all
// real, because they are the subject.
vi.mock("./scroll.js", () => import("./__test-helpers__/scroll-mock.js").then((m) => m.scrollMock));

const store = await import("./store.js");
const messages = await import("./messages.js");

messages.mountChatView();

/** The active view's element — the paint root under the multiplexer. */
function viewRoot(): HTMLElement {
  return messages.activeTranscriptView() ?? document.getElementById("messages")!;
}

function user(id: string, content: string): Message {
  return { id, role: "user", ts: 1, content } as Message;
}

function assistant(id: string, text: string): Message {
  return {
    id,
    role: "assistant",
    ts: 2,
    content: text,
    blocks: [{ type: "text", text }],
  } as Message;
}

function compacted(id: string, summary: string): Message {
  return { id, role: "event", ts: 2, event_kind: "compacted", content: summary } as Message;
}

function session(id: string, msgs: Message[], thinking: boolean): Session {
  return {
    id,
    name: id,
    messages: msgs,
    message_count: msgs.length,
    has_more: false,
    thinking,
    working_label: "",
  } as unknown as Session;
}

/** Mount `msgs` as a fresh chat and return the painted turn cards. */
function mount(chatID: string, msgs: Message[], thinking: boolean): HTMLElement[] {
  store.setSessions([session(chatID, msgs, thinking)]);
  store.setActive(chatID);
  return [...viewRoot().children] as HTMLElement[];
}

/** The reconcile keys of a card's body rows, in DOM order. */
function bodyKeys(card: HTMLElement): (string | null)[] {
  return [...card.querySelectorAll<HTMLElement>(`:scope > .turn-body > [${KEY_ATTR}]`)].map((row) =>
    row.getAttribute(KEY_ATTR),
  );
}

describe("a turn split at a compaction point", () => {
  it("renders both segments and the summary between them, in one card, in array order", () => {
    const chat = "seg-order";
    const cards = mount(
      chat,
      [
        user("so-u1", "do the thing"),
        assistant("so-a1", "before the compaction"),
        compacted("so-evt", "the summary"),
        assistant("so-a2", "after the compaction"),
      ],
      false,
    );

    // ONE card: the event carries no turn_outcome, so it does not close the turn
    // and neither segment opens a headerless one.
    expect(cards.length).toBe(1);
    const card = cards[0]!;
    expect(bodyKeys(card)).toEqual(["so-a1", "so-evt", "so-a2"]);
    // Array order is DOM order, which is the whole reason the server's ordering
    // fix needed no renderer change.
    expect(card.textContent).toContain("before the compaction");
    expect(card.textContent).toContain("after the compaction");
  });

  it("seals segment 1 while the turn is still streaming into segment 2", async () => {
    const chat = "seg-caret";
    mount(chat, [user("sc-u1", "do the thing"), assistant("sc-a1", "before the compaction")], true);
    // The live tail keeps its caret: nothing follows it yet.
    await vi.waitFor(() => {
      expect(viewRoot().querySelector(".message.assistant.streaming")).not.toBeNull();
    });

    // The compaction lands and the rest of the turn opens a fresh message, with
    // the session still thinking.
    store.upsertMessage(chat, compacted("sc-evt", "the summary"));
    store.upsertMessage(chat, assistant("sc-a2", "after the compaction"));
    await vi.waitFor(() => {
      expect(viewRoot().querySelectorAll(`.turn-body > [${KEY_ATTR}]`).length).toBe(3);
    });

    // Exactly ONE streaming caret, and it is on segment 2: a finalized segment 1
    // beside a live segment 2 is what makes the split invisible to the reader.
    const streaming = [...viewRoot().querySelectorAll<HTMLElement>(".streaming")].map((el) =>
      el.closest(`[${KEY_ATTR}]`)?.getAttribute(KEY_ATTR),
    );
    expect([...new Set(streaming)]).toEqual(["sc-a2"]);
  });
});

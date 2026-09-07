// ---------------------------------------------------------------------------
// A row the server persisted MID-TURN, as the transcript renders it.
//
// The claim that the CLIENT needed no renderer change for the ordering rule: a turn
// body is a keyed list read in array order, so moving the row in the array is the
// whole of it and the caret stays on the reply. It drives the real renderer through
// the real store, because reasoning about the dispatcher is what a test replaces.
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

vi.mock("./scroll.js", () => import("./__test-helpers__/scroll-mock.js").then((m) => m.scrollMock));

const store = await import("./store.js");
const messages = await import("./messages.js");

messages.mountChatView();

function viewRoot(): HTMLElement {
  return messages.activeTranscriptView() ?? document.getElementById("messages")!;
}

function session(id: string, msgs: Message[]): Session {
  return {
    id,
    name: id,
    messages: msgs,
    message_count: msgs.length,
    has_more: false,
    thinking: true,
    working_label: "",
  } as unknown as Session;
}

/** A card's body rows by reconcile key, in DOM order. */
function bodyKeys(card: HTMLElement): (string | null)[] {
  return [...card.querySelectorAll<HTMLElement>(`:scope > .turn-body > [${KEY_ATTR}]`)].map((row) =>
    row.getAttribute(KEY_ATTR),
  );
}

/** Mount a chat mid-reply: one prompt, one streaming assistant message. */
async function streamingChat(chatID: string): Promise<void> {
  store.setSessions([
    session(chatID, [
      { id: "u-1", role: "user", ts: 1, content: "do the thing" } as Message,
      {
        id: "a-1",
        role: "assistant",
        ts: 2,
        content: "the reply so far",
        blocks: [{ type: "text", text: "the reply so far" }],
      } as Message,
    ]),
  ]);
  store.setActive(chatID);
  store.noteLiveTurnMessage(chatID, "a-1");
  await vi.waitFor(() => {
    expect(viewRoot().querySelector(".message.assistant.streaming")).not.toBeNull();
  });
}

describe("a mid-turn persisted row renders above the live reply", () => {
  it("puts the turn's plan row above it, in one card, with the caret unmoved", async () => {
    const chat = "mto-plan";
    await streamingChat(chat);

    store.appendMessage(chat, {
      id: "a-1p",
      role: "assistant",
      ts: 3,
      content: "",
      plan: [{ content: "step one", priority: "high", status: "pending" }],
    } as Message);

    await vi.waitFor(() => {
      expect(viewRoot().querySelectorAll(`.turn-body > [${KEY_ATTR}]`).length).toBe(2);
    });
    const cards = [...viewRoot().children] as HTMLElement[];
    // ONE card: neither row carries a turn_outcome, so nothing closes the turn.
    expect(cards.length).toBe(1);
    expect(bodyKeys(cards[0]!)).toEqual(["a-1p", "a-1"]);

    // Exactly one streaming caret, still on the reply. The row moved above it in
    // the array; the caret follows the message the turn is accumulating into.
    const streaming = [...viewRoot().querySelectorAll<HTMLElement>(".streaming")].map((el) =>
      el.closest(`[${KEY_ATTR}]`)?.getAttribute(KEY_ATTR),
    );
    expect([...new Set(streaming)]).toEqual(["a-1"]);
  });

  it("puts a compaction-failed event above it too", async () => {
    const chat = "mto-evt";
    await streamingChat(chat);

    store.appendMessage(chat, {
      id: "e-1",
      role: "event",
      ts: 3,
      event_kind: "compaction_failed",
      content: "the context could not be summarised",
    } as Message);

    await vi.waitFor(() => {
      expect(viewRoot().querySelectorAll(`.turn-body > [${KEY_ATTR}]`).length).toBe(2);
    });
    const cards = [...viewRoot().children] as HTMLElement[];
    expect(cards.length).toBe(1);
    expect(bodyKeys(cards[0]!)).toEqual(["e-1", "a-1"]);
  });
});

// ---------------------------------------------------------------------------
// The transcript placeholder and the transcript may never share the container.
//
// This is the invariant a tab switch used to break in the other direction: the
// activation armed a skeleton on every open, so a chat whose messages were
// already in the store painted the whole conversation and then appended a
// shimmer under the last turn until the refresh landed. The activation now arms
// it only over an empty transcript (chat.test.ts covers that half) and drops it
// here the moment real turns arrive, which is the half that cannot be left to a
// call order: reconcile inserts the newest turn AFTER any unkeyed sibling, so a
// skeleton still mounted when content lands ends up ABOVE the conversation.
// ---------------------------------------------------------------------------

import { describe, it, expect, vi, beforeEach } from "vitest";

// The render graph reaches the shared DOM registry, which throws on a missing app
// root. Every id has to exist before the imports below are evaluated.
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

const { mountChatView } = await import("./messages.js");
const { CHAT_SKELETON_ID, chatSkeleton } = await import("./skeleton.js");
const { setSessions, setActive, bumpMessages } = await import("./store.js");

const messagesEl = document.getElementById("messages") as HTMLElement;

/** A session carrying `messages`, seeded into the real store and activated. */
function activate(messages: unknown[]): void {
  setSessions([
    {
      id: "c-1",
      name: "c",
      model: "",
      acp_session_id: "",
      current_mode_id: "",
      available_modes: [],
      available_models: [],
      supervised_mode: false,
      effort: "",
      effort_levels: [],
      effort_active: "",
      usage: { context_size: 0 },
      message_count: messages.length,
      messages,
      has_more: false,
      thinking: false,
      working_label: "Thinking",
    },
  ] as never);
  setActive("c-1");
}

/** An `event` message: the cheapest thing that projects to a real turn. */
function event(id: string): unknown {
  return { id, role: "event", content: "", event_kind: "cancelled", blocks: [] };
}

beforeEach(() => {
  mountChatView();
  messagesEl.replaceChildren();
  activate([]);
});

describe("the transcript's loading placeholder", () => {
  it("is dropped by the paint that brings in the first turn", () => {
    messagesEl.appendChild(chatSkeleton());
    expect(document.getElementById(CHAT_SKELETON_ID)).not.toBeNull();

    activate([event("m1")]);
    bumpMessages("c-1");

    expect(document.getElementById(CHAT_SKELETON_ID)).toBeNull();
    expect(messagesEl.querySelector(".turn")).not.toBeNull();
  });

  it("never ends up above the turns, which is where reconcile would leave it", () => {
    // The failure this rules out is positional, not just co-presence: reconcile
    // walks the list backwards from `target = null`, so the newest turn is
    // appended after every unkeyed sibling already in the container.
    messagesEl.appendChild(chatSkeleton());
    activate([event("m1"), event("m2")]);
    bumpMessages("c-1");

    const first = messagesEl.firstElementChild;
    expect(first?.id).not.toBe(CHAT_SKELETON_ID);
    expect(first?.classList.contains("turn")).toBe(true);
  });

  it("survives a paint that produces no turns, because that is what it is for", () => {
    // An empty turn list is a chat still loading. Dropping the placeholder on
    // that paint would clear the container and show nothing at all.
    messagesEl.appendChild(chatSkeleton());
    activate([]);
    bumpMessages("c-1");

    expect(document.getElementById(CHAT_SKELETON_ID)).not.toBeNull();
  });
});

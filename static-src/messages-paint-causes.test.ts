// ---------------------------------------------------------------------------
// paint() branches on the DECLARED render cause (design §B2): the store says
// what a version bump was FOR, and the renderer skips exactly the work that
// cause makes unnecessary.
//
//   chunk — a mounted block's signal painted the text; paint is tail
//           bookkeeping only. No projection, no reconcile, no fold pass, no
//           per-turn update.
//   tool  — the owning message's card refreshes through the existing keyed
//           update; sibling turns are never touched.
//   shape — the full pass, and it must run even when nothing about the array's
//           SHAPE says so (gpt R3-H3's case: an in-place same-length
//           message_updated is invisible to any identity- or length-based
//           inference, so the store's declaration is the only honest signal).
//
// The skip assertions are spy deltas on the real seams paint drives —
// projectTurns (projection), reconcile (mount/update), isTurnOpen (fold pass),
// observeTurns (rail) and the messages-blocks entry points (per-turn updates) —
// plus element-identity checks over `#messages`' children, because a reconcile
// that rebuilt a card would mint new nodes even if it produced equal markup.
// Everything below drives the REAL store: sessions land through setSessions /
// appendChunk / upsertToolCall / upsertMessage, and the paint under test is the
// one the transcript effect runs.
// ---------------------------------------------------------------------------

import { describe, it, expect, vi } from "vitest";
import type { Message, Session, ToolCall } from "./types.js";

// The renderer's import graph reaches the shared DOM registry, which throws on
// a missing app root. These ids have to exist before the import is evaluated
// (the composer pair because a send-state effect in the graph tracks the active
// chat and paints the send button on every switch).
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

// The scroller is mocked (the shared helper, so the surface stays total); the
// paint path only needs its calls to be inert. Everything else is REAL, spied
// where a skip has to be proven.
vi.mock("./scroll.js", () => import("./__test-helpers__/scroll-mock.js").then((m) => m.scrollMock));
vi.mock("./turns.js", { spy: true });
vi.mock("./reconcile.js", { spy: true });
vi.mock("./fold-state.js", { spy: true });
vi.mock("./turn-rail.js", { spy: true });
vi.mock("./messages-blocks.js", { spy: true });

const store = await import("./store.js");
const turnsMod = await import("./turns.js");
const reconcileMod = await import("./reconcile.js");
const foldMod = await import("./fold-state.js");
const railMod = await import("./turn-rail.js");
const blocksMod = await import("./messages-blocks.js");
const messages = await import("./messages.js");

messages.mountChatView();
const messagesEl = document.getElementById("messages")!;

/** The active view's element — the paint root under the multiplexer. */
function viewRoot(): HTMLElement {
  return messages.activeTranscriptView() ?? (messagesEl as HTMLElement);
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

/** An assistant message longer than the block budget, so a paint mounts a WINDOW of
 *  it and its head is outside the DOM. */
function hugeAssistant(id: string, blocks: number): Message {
  return {
    id,
    role: "assistant",
    ts: 2,
    content: "",
    blocks: Array.from({ length: blocks }, (_, i) => ({
      type: "text",
      text: `chunk ${String(i)}`,
    })),
  } as unknown as Message;
}

function assistantWithTool(id: string, tc: ToolCall): Message {
  return {
    id,
    role: "assistant",
    ts: 2,
    content: "",
    blocks: [{ type: "tool_use", tool_call_id: tc.id }],
    tool_calls: [tc],
  } as unknown as Message;
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

/** Call counts on every seam a skipped pass must not touch. */
function seamCounts(): Record<string, number> {
  return {
    projectTurns: vi.mocked(turnsMod.projectTurns).mock.calls.length,
    reconcile: vi.mocked(reconcileMod.reconcile).mock.calls.length,
    isTurnOpen: vi.mocked(foldMod.isTurnOpen).mock.calls.length,
    observeTurns: vi.mocked(railMod.observeTurns).mock.calls.length,
    buildAssistantBody: vi.mocked(blocksMod.buildAssistantBody).mock.calls.length,
    updateAssistantBody: vi.mocked(blocksMod.updateAssistantBody).mock.calls.length,
  };
}

/** Mount `msgs` as a fresh chat and return the painted turn cards. */
function mount(chatID: string, msgs: Message[], thinking: boolean): HTMLElement[] {
  store.setSessions([session(chatID, msgs, thinking)]);
  store.setActive(chatID);
  return [...viewRoot().children] as HTMLElement[];
}

/** One microtask: the per-chat coalescer flushes, and the flush paints. */
async function flushed(): Promise<void> {
  await Promise.resolve();
}

describe("paint branches on the flushed render cause", () => {
  // Message ids are chat-prefixed THROUGHOUT: the reconcile is keyed by message
  // id, so reusing ids across this file's chats would make a chat switch UPDATE
  // a previous test's cards instead of mounting fresh ones.
  it(
    "a chunk flush runs zero projection, reconcile, fold or per-turn work",
    { timeout: 15_000 },
    async () => {
      const chat = "cause-chunk";
      const kids = mount(chat, [user("ck-u1", "hi"), assistant("ck-a1", "hello")], true);
      expect(kids.length).toBe(1);
      const before = seamCounts();

      store.appendChunk(chat, "ck-a1", " world", false, 0, "");
      await flushed();

      expect(store.renderCauseOf(chat).cause).toBe("chunk");
      expect(seamCounts()).toEqual(before);
      const after = [...viewRoot().children];
      expect(after.length).toBe(kids.length);
      expect(after.every((el, i) => el === kids[i])).toBe(true);
      // The skip lost nothing: the mounted block's own signal carries the text.
      // The reveal holds the live edge's tail back while the turn is thinking,
      // so END the turn — the fact flush finalizes and the reveal drains.
      store.setThinking(chat, false);
      await flushed();
      await vi.waitFor(
        () => {
          expect(viewRoot().querySelector(".message.assistant")?.textContent).toContain("world");
        },
        { timeout: 10_000 },
      );
    },
  );

  it("a delta to an UNMOUNTED block schedules nothing at all", async () => {
    // The signal-absent fallback exists for a MOUNTED block whose liveness was
    // misjudged: the full pass re-reads it. For a block outside the window the pass
    // paints nothing either, so a parked reader would pay one full pass per delta —
    // which on a streaming run is a pass per frame over the whole transcript.
    const chat = "cause-unmounted";
    const kids = mount(chat, [user("cu-u1", "hi"), hugeAssistant("cu-a1", 700)], false);
    expect(kids.length).toBe(1);
    await vi.waitFor(() => {
      expect(viewRoot().querySelectorAll("[data-block-index]").length).toBeGreaterThan(48);
    });
    // The case's premise: block 0 is outside the mounted window.
    expect(viewRoot().querySelector('[data-block-index="0"]')).toBeNull();
    const before = seamCounts();

    store.appendChunk(chat, "cu-a1", " more", false, 0, "");
    await flushed();

    expect(seamCounts()).toEqual(before);
    // And nothing is lost: the text is in the store, and a scroll back mounts it from
    // there (`block-virtualization.test.ts` pins that half).
    const blocks = store.get(chat)?.messages.find((m) => m.id === "cu-a1")?.blocks ?? [];
    expect(blocks[0]?.text).toBe("chunk 0 more");
  });

  it("a delta to a MOUNTED block with no signal still runs the full pass", async () => {
    // The other side of the same condition, and the behaviour the fallback is FOR: a
    // settled message's blocks carry no per-block signal, so the pass is what puts the
    // text on screen through `syncMountedText`.
    const chat = "cause-mounted";
    mount(chat, [user("cm-u1", "hi"), assistant("cm-a1", "hello")], false);
    await vi.waitFor(() => {
      expect(viewRoot().querySelector('[data-block-index="0"]')).not.toBeNull();
    });
    const before = seamCounts();

    // The delta runs past the assertion below because the bubble's reveal holds its
    // last characters back behind the caret.
    store.appendChunk(chat, "cm-a1", " world and then some", false, 0, "");
    await flushed();

    expect(store.renderCauseOf(chat).cause).toBe("shape");
    expect(vi.mocked(turnsMod.projectTurns).mock.calls.length).toBeGreaterThan(
      before["projectTurns"]!,
    );
    await vi.waitFor(() => {
      expect(viewRoot().querySelector('[data-block-index="0"]')?.textContent).toContain("world");
    });
  });

  it("a tool flush refreshes only the owning message's card", async () => {
    const chat = "cause-tool";
    const tc = {
      id: "tc-1",
      title: "Run command",
      kind: "execute",
      status: "in_progress",
    } as unknown as ToolCall;
    // The tool-bearing message is in the NEWEST turn: the fold policy wants that
    // turn open, so its card is mounted and its per-tool signal exists — which is
    // what makes the cause `tool` rather than the signal-absent `shape` fallback.
    const kids = mount(
      chat,
      [
        user("ct-u1", "one"),
        assistant("ct-a1", "done"),
        user("ct-u2", "two"),
        assistantWithTool("ct-a2", tc),
      ],
      false,
    );
    expect(kids.length).toBe(2);
    const before = seamCounts();
    const refreshBefore = vi.mocked(blocksMod.refreshMessageCard).mock.calls.length;

    store.upsertToolCall(chat, "ct-a2", { ...tc, status: "completed" } as ToolCall, 0);
    await flushed();

    expect(store.renderCauseOf(chat)).toEqual({ cause: "tool", msgID: "ct-a2" });
    // The keyed update ran for the one owning message, and it found its render.
    const refreshCalls = vi.mocked(blocksMod.refreshMessageCard).mock;
    expect(refreshCalls.calls.length).toBe(refreshBefore + 1);
    expect(refreshCalls.calls.at(-1)?.[0]).toBe("ct-a2");
    expect(refreshCalls.results.at(-1)?.value).toBe(true);
    // No projection, no reconcile, no fold pass — and zero FULL-PATH body
    // updates: the spy sees only cross-module calls, so the keyed update's own
    // internal path is exactly the one that does not show here.
    expect(seamCounts()).toEqual(before);
    // Sibling turn DOM identity preserved (nothing remounted anywhere).
    const after = [...viewRoot().children];
    expect(after.every((el, i) => el === kids[i])).toBe(true);
  });

  it("an old-turn same-length message_updated classifies shape and repaints that turn", async () => {
    const chat = "cause-shape";
    const kids = mount(
      chat,
      [
        user("cs-u1", "first ask"),
        assistant("cs-a1", "old answer"),
        user("cs-u2", "next"),
        assistant("cs-a2", "ok"),
      ],
      false,
    );
    const oldTurn = kids[0]!;
    expect(oldTurn.querySelector(".turn-req-text")?.textContent).toBe("first ask");
    const before = seamCounts();

    // Same LENGTH, different text: invisible to any identity- or length-based
    // change inference — only the store's declared cause can repaint it.
    store.upsertMessage(chat, user("cs-u1", "FIRST TASK"));
    await flushed();

    expect(store.renderCauseOf(chat).cause).toBe("shape");
    // The full pass ran…
    expect(vi.mocked(turnsMod.projectTurns).mock.calls.length).toBe(before["projectTurns"]! + 1);
    // …and repainted that turn IN PLACE with the new text.
    expect(viewRoot().children[0]).toBe(oldTurn);
    expect(oldTurn.querySelector(".turn-req-text")?.textContent).toBe("FIRST TASK");
  });

  it("a chunk flush over 500 mounted turns is bookkeeping-only", { timeout: 15_000 }, async () => {
    const chat = "cause-500";
    const msgs: Message[] = [];
    for (let i = 0; i < 500; i++) {
      msgs.push(
        user(`c5-u${String(i)}`, `ask ${String(i)}`),
        assistant(`c5-a${String(i)}`, `answer ${String(i)}`),
      );
    }
    const kids = mount(chat, msgs, true);
    expect(kids.length).toBe(500);
    const before = seamCounts();

    store.appendChunk(chat, "c5-a499", "!", false, 0, "");
    await flushed();

    // Bookkeeping only: zero projection, zero reconcile, zero fold-pass reads,
    // zero rail re-observation, zero per-turn updates…
    expect(seamCounts()).toEqual(before);
    // …and the 500 cards are the SAME 500 nodes (a rebuild would mint new ones).
    const after = [...viewRoot().children];
    expect(after.length).toBe(500);
    expect(after.every((el, i) => el === kids[i])).toBe(true);
  });
});

// ---------------------------------------------------------------------------
// `.is-bodyless` on the turn card mirrors "the card ends with an empty body"
// for CSS (29-turns.css keys the header's bottom edge on it). buildTurn and
// updateTurn stamp it after every pass, so it must track both facts it
// encodes: the body's children AND whether a footer follows.
// ---------------------------------------------------------------------------

describe("the bodyless turn card is marked .is-bodyless", () => {
  it("marks a prompt-only turn and clears it when the reply lands", async () => {
    const chat = "bodyless-clear";
    const kids = mount(chat, [user("bl-u1", "hi")], true);
    expect(kids.length).toBe(1);
    expect(kids[0]?.classList.contains("is-bodyless")).toBe(true);

    store.upsertMessage(chat, assistant("bl-a1", "hello"));
    await flushed();
    const card = viewRoot().firstElementChild;
    expect(card?.querySelector(":scope > .turn-body")?.childElementCount).toBe(1);
    expect(card?.classList.contains("is-bodyless")).toBe(false);
  });

  it("paints a reserved slot as a marked row, end to end", async () => {
    // The msg-row half of the same CSS contract, through the REAL block
    // callbacks (initBlockCallbacks): an assistant message whose text block is
    // still empty paints a `.msg-row.is-empty` the stylesheet can hide.
    const chat = "bodyless-row";
    mount(chat, [user("br-u1", "go"), assistant("br-a1", "")], false);
    const row = viewRoot().querySelector(".msg-row");
    expect(row).not.toBeNull();
    expect(row?.classList.contains("is-empty")).toBe(true);

    store.appendChunk(chat, "br-a1", "landed", false, 0, "");
    await flushed();
    expect(viewRoot().querySelector(".msg-row")?.classList.contains("is-empty")).toBe(false);
  });

  it("clears it when a FOOTER arrives under a still-empty body", async () => {
    // A second prompt gives the first turn a rewind footer while its body stays
    // empty. The body is no longer the card's last child, so the header keeps
    // its border and `.turn-body:empty + .turn-footer` drops the footer's
    // instead — marking here would erase the only line between the two bands.
    const chat = "bodyless-footer";
    const kids = mount(chat, [user("bf-u1", "one")], false);
    expect(kids[0]?.classList.contains("is-bodyless")).toBe(true);

    store.upsertMessage(chat, user("bf-u2", "two"));
    await flushed();
    const cards = [...viewRoot().children] as HTMLElement[];
    expect(cards.length).toBe(2);
    expect(cards[0]?.querySelector(":scope > .turn-footer")).not.toBeNull();
    expect(cards[0]?.querySelector(":scope > .turn-body")?.childElementCount).toBe(0);
    expect(cards[0]?.classList.contains("is-bodyless")).toBe(false);
    // The new prompt-only turn takes the mark instead.
    expect(cards[1]?.classList.contains("is-bodyless")).toBe(true);
  });
});

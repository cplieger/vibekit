// ---------------------------------------------------------------------------
// WHAT A CAPPED SNAPSHOT PAINTS.
//
// `capBlocks` keeps the TAIL of a turn's block array and re-indexes it from zero while
// `message_chunk.block_index` stays the buffer's ABSOLUTE index, so the client holds the
// base the snapshot reported and subtracts it. Without that subtraction a live chunk at
// absolute 130 reserved ~117 placeholder blocks in front of a 13-block window, and this
// file is what those reservations DID on screen: each one mounted an empty bubble whose
// row `13-messages.css` hides, and in a workflow-step turn the untagged reservations
// defeated `isStepMessage`, which opened a headerless "Agent-initiated turn" card over
// output belonging to the turn above it.
//
// A WIRED PAINT HARNESS, and NOT `messages-blocks.test.ts`, where a `.msg-row.is-empty`
// assertion is VACUOUS. That file builds BODIES, and the class has exactly ONE producer
// in the tree (`messages.ts` `makeRow`), which reaches the mounter only through
// `initBlockRenderer`: that harness leaves the injector at its until-init stubs
// everywhere but one describe, and the factory that describe DOES install returns a
// class-less div — so `.msg-row` matches nothing there in any state, and a turn CARD is
// not built there at all. Both halves need `mountChatView()` over the real store, which
// is what this file is.
//
// THE FIRST CASE IS THE CONTROL AND IT IS PART OF THE ARGUMENT: it paints a REAL pad
// (`padBlock`, the production object rather than an imitation of one) and asserts its row
// IS marked, so the zero assertion below it is known to be over a selector this harness
// can produce.
//
// The SETTLED transcript throughout, stated in the fixture rather than assumed: the class
// has one writer (`messages-blocks.ts` `mountText`'s `onBlankChange`) and
// `buildAssistantBubble` initialises `blank = !live && root.firstChild === null`, so a
// LIVE bubble reports not-blank however empty it is — `css/13-messages.css:274` says the
// same thing from the stylesheet's side ("`.is-empty` is absent while streaming, so a
// live bubble keeps its caret"). Settled is also a state a reader of a capped window is
// really in: `thinking` is cleared at `turn_ended` while the adopted record survives
// until the persist echo (`appendMessage`) spends it, and a workflow-step turn's window
// deliberately sets no `thinking` at all.
//
// EVERY WAIT POLLS AN OBSERVABLE, following messages-turn-number.test.ts: a fixed sleep
// would have to cover a real paint on a box CI packs onto four cores. Each poll settles
// in BOTH states, so a regression lands on the assertion that names it rather than on a
// bare deadline.
// ---------------------------------------------------------------------------
import { describe, it, expect, vi, beforeEach } from "vitest";
import { FRAME_BUDGET_MS } from "./__test-helpers__/frame-budget.js";
import { padBlock } from "./block-pad.js";
import type { Block, Message, Session } from "./types.js";

// The DOM the renderer's import graph resolves at load, nested the way the page nests it.
const outer = document.createElement("div");
outer.id = "messages-wrap-outer";
outer.style.cssText = "position:relative;";
const wrap = document.createElement("div");
wrap.id = "messages-wrap";
wrap.style.cssText = "height:300px;overflow-y:auto;overflow-anchor:none;position:relative;";
const messagesEl = document.createElement("div");
messagesEl.id = "messages";
wrap.appendChild(messagesEl);
outer.appendChild(wrap);
document.body.appendChild(outer);
for (const [id, tag] of [
  ["chat-view", "div"],
  ["scroll-bottom", "button"],
  ["send-btn", "button"],
  ["prompt-input", "textarea"],
] as const) {
  const e = document.createElement(tag);
  e.id = id;
  if (id === "scroll-bottom") {
    e.appendChild(document.createElement("span"));
  }
  document.body.appendChild(e);
}

// The rail's session-wide index is its own fetch and the pagination door is a network
// read; neither is what these cases are about.
vi.mock("./api-client.js", { spy: true });
vi.mock("./store-load.js", () => ({ loadMessages: vi.fn(), loadList: vi.fn() }));

const store = await import("./store.js");
const messages = await import("./messages.js");
const { apiGet } = await import("./api-client.js");

messages.mountChatView();

/** Where the capped window SITS: the capped `live_turn` dropped this many blocks off the
 *  front, so the delivered array covers absolute 117..129 inclusive. */
const BASE = 117;
/** Blocks the snapshot delivered. */
const WINDOW = 13;
/** The step id every block of the workflow-step window carries. */
const STEP = "wf:w1:root";

function user(id: string, content: string): Message {
  return { id, role: "user", ts: 1, content } as Message;
}

/** A settled reply, so the turn it closes makes a headerless one REACHABLE for the
 *  message after it — `opensHeaderlessTurn` needs `prevClosed`. */
function settledReply(id: string, text: string): Message {
  return {
    id,
    role: "assistant",
    ts: 2,
    content: text,
    turn_outcome: "completed",
    blocks: [{ type: "text", text }],
  } as unknown as Message;
}

/** The TAIL a capped snapshot delivered, re-indexed from zero. `subtaskID` is what makes
 *  it a workflow step's window rather than the chat's own. */
function cappedReply(id: string, subtaskID?: string): Message {
  const blocks: Block[] = Array.from({ length: WINDOW }, (_, i) => ({
    type: "text",
    text: `tail block ${String(i)}`,
    ...(subtaskID === undefined ? {} : { agent_subtask_id: subtaskID }),
  }));
  return { id, role: "assistant", ts: 3, content: "the tail", blocks } as unknown as Message;
}

/** A hydrated chat holding `msgs` and NOT streaming. */
function session(id: string, msgs: Message[]): Session {
  return {
    id,
    name: id,
    messages: msgs,
    message_count: msgs.length,
    has_more: false,
    residency: "loaded",
    thinking: false,
    working_label: "",
  } as unknown as Session;
}

let seq = 0;
/** A chat id no earlier case has used: `setActive` is a no-op for the id already active,
 *  so a reused id paints nothing and the case runs against an empty view. */
function nextChat(): string {
  seq += 1;
  return `cap${String(seq)}`;
}

async function until(pred: () => boolean, what: string): Promise<void> {
  const deadline = Date.now() + FRAME_BUDGET_MS;
  while (!pred()) {
    if (Date.now() > deadline) {
      throw new Error(`timed out after ${String(FRAME_BUDGET_MS)}ms waiting for ${what}`);
    }
    await new Promise((r) => setTimeout(r, 8));
  }
}

function viewRoot(): HTMLElement {
  return messages.activeTranscriptView() ?? (messagesEl as HTMLElement);
}

function cards(): HTMLElement[] {
  return [...viewRoot().querySelectorAll<HTMLElement>(":scope > .turn")];
}

/** What each card's header says its trigger IS (`turn-header.ts` stamps it), so the
 *  phantom is named structurally rather than only by the words it renders. */
function triggers(): string[] {
  return cards().map((c) => c.querySelector<HTMLElement>(".turn-header")?.dataset["trigger"] ?? "");
}

/** The request each card shows, in document order. */
function requests(): string[] {
  return cards().map((c) => c.querySelector(".turn-req-text")?.textContent ?? "");
}

/** Every mounted block row of the transcript. */
function rows(): HTMLElement[] {
  return [...viewRoot().querySelectorAll<HTMLElement>(".msg-row")];
}

/** The rows the stylesheet hides — one per block the client has nothing to show for. */
function hiddenRows(): HTMLElement[] {
  return rows().filter((r) => r.classList.contains("is-empty"));
}

/** The message's own wrap inside whichever card holds it, which is how `find-in-chat.ts`
 *  addresses one: it exists whether the message JOINED the turn above it or opened one of
 *  its own, so a poll on it settles in both states. */
function wrapFor(msgID: string): HTMLElement | null {
  return viewRoot().querySelector<HTMLElement>(
    `.turn-body > [data-reconcile-key="${CSS.escape(msgID)}"]`,
  );
}

/** Paint `msgs` as chat `id`'s window and wait for its cards to mount. */
async function paint(id: string, msgs: Message[]): Promise<void> {
  const expected = msgs.filter((m) => m.role === "user").length;
  store.setSessions([session(id, msgs)]);
  store.setActive(id);
  await until(() => cards().length === expected, `${String(expected)} cards to mount for ${id}`);
}

beforeEach(() => {
  vi.mocked(apiGet).mockResolvedValue({ turns: [] });
});

describe("the render of a capped live_turn snapshot", () => {
  it("hides the row of a block it has nothing to show for", async () => {
    // THE CONTROL for the two cases below, and the mechanism the defect was made of: a
    // pad mounts a bubble with no content, whose row the shipped stylesheet removes. The
    // pad is `padBlock`'s own object rather than a hand-written `{type:"text"}`, so the
    // control cannot drift from what `padBlocks` writes.
    const chat = nextChat();
    const msg = `${chat}-m`;
    await paint(chat, [
      user("u1", "what did that leave"),
      {
        id: msg,
        role: "assistant",
        ts: 2,
        content: "two real blocks",
        blocks: [
          { type: "text", text: "before" },
          padBlock(undefined),
          { type: "text", text: "after" },
        ],
      } as unknown as Message,
    ]);
    await until(() => rows().length === 3, "three block rows to mount");

    // The reservation's row is marked and the two real blocks' rows are not, so the
    // selector below is live in this harness and discriminating within it.
    expect(hiddenRows().length).toBe(1);
    expect(rows().map((r) => r.classList.contains("is-empty"))).toEqual([false, true, false]);
  });

  it("renders the tail of a capped ordinary turn with no hidden rows", async () => {
    // The ORDINARY chat, which is the blast radius: the same shift with NO
    // `agent_subtask_id` on any block, so every large ordinary turn.
    const chat = nextChat();
    const msg = `${chat}-m`;
    await paint(chat, [user("u1", "summarise the release")]);

    // `adoptLiveTurn`'s own order: the record of WHERE the delivered array sits, then the
    // array itself.
    store.noteAdoptedSnapshot(chat, msg, { blockBase: BASE, truncated: true });
    store.upsertMessage(chat, cappedReply(msg));
    await until(() => rows().length === WINDOW, `the window's ${String(WINDOW)} rows to mount`);

    // ABSOLUTE 130, the first index past the window: it maps to local 13, so `padBlocks`
    // is a no-op and the push appends. 129 would map to the window's LAST resident block
    // and exercise the extend arm instead, which reserves nothing either way — so it
    // cannot tell a correct base from an off-by-one.
    store.appendChunk(chat, msg, "and the live tail", false, BASE + WINDOW, "", 1);
    await until(
      () => (wrapFor(msg)?.textContent ?? "").includes("and the live tail"),
      "the live chunk to paint",
    );

    // The SETTLED transcript, pinned twice rather than assumed — it is the only state
    // that writes `.is-empty` at all (see the header comment).
    expect(store.get(chat)?.thinking).toBe(false);
    expect(viewRoot().querySelector(".message.assistant.streaming")).toBeNull();

    // The snapshot's text is on screen, from its first delivered block to its last…
    const body = wrapFor(msg)?.textContent ?? "";
    expect(body).toContain("tail block 0");
    expect(body).toContain(`tail block ${String(WINDOW - 1)}`);
    // …and nothing is hidden in front of it: one row per delivered block plus the live
    // one, and not a single reserved slot.
    expect(hiddenRows().length).toBe(0);
    expect(rows().length).toBe(WINDOW + 1);
  });

  it("mounts no agent-initiated card for a capped step snapshot", async () => {
    // The phantom's render half. A pad carries no content, so counting it as
    // parent-agent work makes a step message look like the chat's own: one untagged
    // reservation flips `isStepMessage`, which flips `opensHeaderlessTurn`, and the
    // step's output gets a headerless card of its own — empty, because `placeBlock` drops
    // a `wf:`-tagged block by design.
    //
    // NO body-content assertion here, and that asymmetry is deliberate: this turn
    // renders none even when perfectly aligned, so asserting content would be asserting
    // against that ratified drop. The body-content half belongs to the ordinary case
    // above.
    //
    // The whole state is built BEFORE the chat is activated, which is the COLD CONNECT
    // this defect was reported from and is required rather than tidy: a `wf:`-tagged
    // chunk is classified `chunk` by `store.ts` `droppedFrameCause`, so it runs no
    // projection and no reconcile — measured, a phantom the projection already produces
    // reaches the DOM only at the next FULL pass, and for a cold connect that is the
    // first paint.
    const chat = nextChat();
    const msg = `${chat}-m`;
    // A settled turn precedes it, so `prevClosed` holds and the phantom is REACHABLE.
    store.setSessions([
      session(chat, [user("u1", "run the release workflow"), settledReply("a1", "starting")]),
    ]);
    store.noteAdoptedSnapshot(chat, msg, { blockBase: BASE, truncated: true });
    store.upsertMessage(chat, cappedReply(msg, STEP));
    store.appendChunk(chat, msg, "more step output", false, BASE + WINDOW, STEP, 1);
    store.setActive(chat);
    // Polls something TRUE IN BOTH STATES: the step message's own wrap is in the turn
    // above it when the window is aligned and in a headerless card of its own when it is
    // not, so a regression lands on the assertions below rather than on a deadline.
    await until(() => wrapFor(msg) !== null, "the step message to reach a card");

    // ONE turn, and it is the reader's own: the count, the header's structural verdict on
    // its trigger, and the words it renders — three channels, because a rename of the
    // label alone would make a `not.toContain` pass vacuously.
    expect(cards().length).toBe(1);
    expect(triggers()).toEqual(["user"]);
    expect(requests()).toEqual(["run the release workflow"]);
    expect(viewRoot().textContent ?? "").not.toContain("Agent-initiated turn");
    expect(viewRoot().querySelectorAll('[data-trigger="system"]').length).toBe(0);
  });
});

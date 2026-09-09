// ---------------------------------------------------------------------------
// A steer note's POSITION is a projection of the current window.
//
// TWO FIELDS, TWO LIFETIMES (store.ts): `session.steers` is the dock's waiting
// rows, whose lifetime is the turn, and `session.steer_marks` is what has LEFT
// the dock, rendered inside the turn at the block it landed on, whose lifetime is
// the loaded transcript. Neither is re-derivable from anything durable — the
// server replays no steering buffer and no verb reads one back — so the mark is
// the only record a reader can be shown, and the reader's own report was that it
// vanishes after leaving the tab for a while.
//
// What this file pins is the RENDER half. A mark survives every rebuild path
// (store-load.ts carries both fields over), and what leaves is the MESSAGE its
// anchor names: the in-flight assistant message is server-side buffer state
// absent from `GET /api/chats/{id}`, so a refetch drops it (store-load.test.ts
// "drops the local copy…" pins that), and an evicted-then-refetched window does
// the same. So the anchor recorded at arrival is where the steer WAS read, and
// where it can be DRAWN is a question about the window that exists NOW.
//
// REAL store, REAL renderer, REAL layout (Browser Mode). The harness is
// messages-parked-views.test.ts's: DOM hosts before the imports, the shipped
// transcript stylesheet, `messages.teardownAll()` per case.
// ---------------------------------------------------------------------------

import { describe, it, expect, beforeEach } from "vitest";
import type { Block, Message, Session, SteerMark } from "./types.js";

// messages.ts's graph reads the shared DOM registry at module scope, and `byId`
// throws on a missing element, so every host exists before an import resolves.
for (const id of [
  "chat-view",
  "messages-wrap-outer",
  "messages-wrap",
  "messages",
  "scroll-bottom",
  "send-btn",
  "prompt-input",
]) {
  const d = document.createElement(id === "prompt-input" ? "textarea" : "div");
  d.id = id;
  if (id === "scroll-bottom") {
    d.appendChild(document.createElement("span"));
  }
  if (id === "messages-wrap") {
    document.getElementById("messages-wrap-outer")?.appendChild(d);
  } else if (id === "messages") {
    document.getElementById("messages-wrap")?.appendChild(d);
  } else {
    document.body.appendChild(d);
  }
}

import { loadCSS } from "./__test-helpers__/css-rules.js";

const style = document.createElement("style");
style.textContent =
  loadCSS("13-messages.css") +
  `
  #messages-wrap-outer { height: 400px; }
`;
document.head.appendChild(style);

const store = await import("./store.js");
const messages = await import("./messages.js");
const { clampObservationCount } = await import("./clamp-text.js");

messages.mountChatView();

let seq = 0;
function freshID(prefix: string): string {
  return `${prefix}-${String(++seq)}`;
}

function session(id: string, over: Partial<Session> = {}): Session {
  return {
    id,
    name: id,
    messages: [],
    message_count: 0,
    has_more: false,
    thinking: false,
    working_label: "",
    usage: { context_size: 0 },
    ...over,
  } as unknown as Session;
}

function user(id: string, content: string): Message {
  return { id, role: "user", ts: 1, content } as Message;
}

function assistant(id: string, blocks: Block[]): Message {
  return { id, role: "assistant", ts: 2, content: "", blocks } as unknown as Message;
}

function textBlock(text: string): Block {
  return { type: "text", text } as Block;
}

/** One microtask: the store's per-chat coalescer flushes, and the flush paints. */
async function flushed(): Promise<void> {
  await Promise.resolve();
}

function viewOf(chatID: string): HTMLElement {
  const el = messages.transcriptViewFor(chatID);
  if (el === null) {
    throw new Error(`no resident view for ${chatID}`);
  }
  return el;
}

/** Every `.steer-note` the chat's own view holds. */
function notes(chatID: string): HTMLElement[] {
  return [...viewOf(chatID).querySelectorAll<HTMLElement>(".steer-note")];
}

/** A live turn on `id`: the user's prompt plus the assistant message a steer's
 *  anchor is measured against. Mirrors what the store holds mid-turn — the
 *  assistant message is the one the SERVER has not persisted yet. */
function liveTurn(id: string, msgID: string, blockCount = 2): Session {
  return session(id, {
    thinking: true,
    messages: [
      user(`${id}-u`, "go"),
      assistant(
        msgID,
        Array.from({ length: blockCount }, (_, i) => textBlock(`chunk ${String(i)}`)),
      ),
    ],
    message_count: 2,
  });
}

/** The id of the assistant row a mid-turn refetch KEEPS: the agent persists rows
 *  DURING a turn (a plan update, a compaction watermark, a safety block), and
 *  those are in the page while the streaming reply is not. */
const PERSISTED = "a-persisted";

/** Replace the window the way a mid-turn refetch does: the newest PERSISTED page,
 *  so the in-flight assistant message the anchor names is gone while a row
 *  persisted during the same turn stays — with `steer_marks` carried over exactly
 *  as `loadMessages` carries it.
 *
 *  This is the gap path reduced to its shared mechanism, and the reduction is
 *  honest: `store-load.test.ts` "drops the local copy…" already pins that a
 *  refetch drops that message once `liveTurnMessage` is clear, and
 *  `handlers/system.test.ts` pins that the gap door clears it. */
function refetchDroppingLiveMessage(id: string, marks: readonly SteerMark[]): void {
  store.setSessions([
    session(id, {
      messages: [user(`${id}-u`, "go"), assistant(PERSISTED, [textBlock("the plan")])],
      message_count: 2,
      steer_marks: [...marks],
    }),
  ]);
  store.setActive(id);
  store.bumpMessages(id, "load");
}

beforeEach(() => {
  // The multiplexer's registry persists at module scope, so an earlier case's
  // parked view would otherwise count against this one's LRU budget.
  messages.teardownAll();
  store.setSessions([]);
  store.setActive("");
  window.scrollTo(0, 0);
});

// ---------------------------------------------------------------------------
// Controls. These pass on the pre-fix tree; a red one here means the harness is
// wrong rather than that the defect is absent.
// ---------------------------------------------------------------------------

describe("a promoted steer renders in the turn it landed in", () => {
  it("renders one note when the steer is read while the chat is active", async () => {
    const a = freshID("c-live");
    const m = freshID("m");
    store.setSessions([liveTurn(a, m)]);
    store.setActive(a);
    store.bumpMessages(a);
    await flushed();

    store.recordSteerQueued(a, { id: "steer-1", text: "use tabs", origin: "user" });
    store.promoteSteer(a, "steer-1", "use tabs", "user");
    await flushed();

    expect(notes(a)).toHaveLength(1);
    expect(notes(a)[0]?.dataset["state"]).toBe("read");
    expect(notes(a)[0]?.dataset["origin"]).toBe("user");
    expect(notes(a)[0]?.textContent).toContain("use tabs");
  });

  it("renders one note when the steer is read while the view is parked", async () => {
    const a = freshID("c-park");
    const b = freshID("c-park");
    const m = freshID("m");
    store.setSessions([liveTurn(a, m), session(b, { messages: [user(`${b}-u`, "other")] })]);
    store.setActive(a);
    store.bumpMessages(a);
    await flushed();

    store.setActive(b);
    await flushed();

    store.recordSteerQueued(a, { id: "steer-1", text: "read while away", origin: "user" });
    store.promoteSteer(a, "steer-1", "read while away", "user");
    await flushed();

    store.setActive(a);
    await flushed();

    expect(notes(a)).toHaveLength(1);
  });

  // Probe A's asymmetry: `flushSteerNotes` only ever MOUNTS, and nothing removes
  // a note when its mark goes. That is why a lost mark is invisible while the tab
  // stays open and fatal the moment the transcript is drawn again.
  it("keeps a mounted note after its mark leaves the store", async () => {
    const a = freshID("c-asym");
    const m = freshID("m");
    store.setSessions([liveTurn(a, m)]);
    store.setActive(a);
    store.bumpMessages(a);
    await flushed();
    store.promoteSteer(a, "steer-1", "still on screen", "user");
    await flushed();
    expect(notes(a)).toHaveLength(1);

    store.setSessions([liveTurn(a, m)]);
    store.setActive(a);
    store.bumpMessages(a);
    await flushed();

    expect(store.steerMarks(a)).toHaveLength(0);
    expect(notes(a)).toHaveLength(1);
  });
});

// ---------------------------------------------------------------------------
// SYMPTOM A, the reported one: a steer that LANDED in the turn, and whose note
// is missing from the transcript after leaving the tab for a while.
//
// Both cases below fail on the pre-fix tree with `expected 0 to be 1`.
// ---------------------------------------------------------------------------

describe("the transcript note survives losing the message its anchor names", () => {
  it("renders the note after a refetch dropped the anchored message", async () => {
    const a = freshID("c-orphan");
    const m = freshID("m");
    store.setSessions([liveTurn(a, m)]);
    store.setActive(a);
    store.bumpMessages(a);
    await flushed();

    store.recordSteerQueued(a, { id: "steer-1", text: "the correction", origin: "user" });
    store.promoteSteer(a, "steer-1", "the correction", "user");
    await flushed();
    const mark = store.steerMarks(a)[0];
    expect(mark?.anchor.msgID).toBe(m);

    // The window the reader comes back to: the in-flight message the anchor named
    // is server-side buffer state, so the newest persisted page does not hold it.
    refetchDroppingLiveMessage(a, store.steerMarks(a));
    await flushed();

    expect(store.steerMarks(a)).toHaveLength(1);
    expect(notes(a)).toHaveLength(1);
    expect(notes(a)[0]?.textContent).toContain("the correction");
  });

  it("renders the note for a steer read before the turn produced anything", async () => {
    const a = freshID("c-empty-anchor");
    // No assistant message yet, so `anchorFor` records the degenerate empty anchor.
    store.setSessions([session(a, { thinking: true, messages: [], message_count: 0 })]);
    store.setActive(a);
    store.bumpMessages(a);
    await flushed();

    store.promoteSteer(a, "steer-1", "read before any output", "user");
    await flushed();
    expect(store.steerMarks(a)[0]?.anchor.msgID).toBe("");

    // The turn then produces its reply, exactly as it would live.
    const m = freshID("m");
    store.setSessions([
      session(a, {
        thinking: true,
        messages: [user(`${a}-u`, "go"), assistant(m, [textBlock("reply")])],
        message_count: 2,
        steer_marks: [...store.steerMarks(a)],
      }),
    ]);
    store.setActive(a);
    store.bumpMessages(a, "load");
    await flushed();

    expect(notes(a)).toHaveLength(1);
    expect(notes(a)[0]?.textContent).toContain("read before any output");
  });
});

// ---------------------------------------------------------------------------
// SYMPTOM B: a steer still WAITING in the dock vanishes after leaving the tab.
//
// The dock is RIGHT to empty — KAS clears its steering buffer at every turn
// boundary, so a row still waiting was never read and can never post, and
// `dropSteers` (handlers/turn.ts's `turn_ended`, characterized in
// handlers/turn.test.ts) promotes each one as a `dropped: true` mark rather than
// deleting it silently. What the reader loses is the RECORD: that mark is
// anchored at the newest assistant message, which the refetch then drops, so the
// "not delivered" note renders nowhere and a correction the agent never read
// leaves no trace at all.
// ---------------------------------------------------------------------------

describe("the not-delivered record survives the turn boundary and the refetch", () => {
  it("renders a dropped steer's note after the anchored message left the window", async () => {
    const a = freshID("c-dropped");
    const b = freshID("c-dropped");
    const m = freshID("m");
    store.setSessions([liveTurn(a, m), session(b, { messages: [user(`${b}-u`, "other")] })]);
    store.setActive(a);
    store.bumpMessages(a);
    await flushed();

    store.recordSteerSent(a, "msg-1", "never read this");
    store.recordSteerQueued(a, {
      id: store.steerIDFor("msg-1"),
      text: "never read this",
      origin: "user",
    });
    await flushed();
    expect(store.steerCount(a)).toBe(1);

    // The reader leaves.
    store.setActive(b);
    await flushed();

    // The turn ends while they are away: KAS's buffer is cleared server-side, so
    // the dock is emptied and each waiting row becomes a `dropped: true` mark.
    store.dropSteers(a);
    await flushed();
    expect(store.steerCount(a)).toBe(0);
    expect(store.steerMarks(a)[0]?.dropped).toBe(true);

    refetchDroppingLiveMessage(a, store.steerMarks(a));
    await flushed();

    const rendered = notes(a);
    expect(rendered).toHaveLength(1);
    expect(rendered[0]?.dataset["state"]).toBe("dropped");
    expect(rendered[0]?.textContent).toContain("never read this");
    // The one control the wire can honour on an undelivered steer.
    expect(rendered[0]?.querySelector(".steer-note-restore")).not.toBeNull();
  });

  // THE RESIDUAL, pinned so it is a known state rather than a surprise: only an
  // ASSISTANT body renders steer notes, so a window holding no assistant message
  // at all has nowhere to draw one. Resolving to the user row instead would move
  // the anchor somewhere the renderer never visits — a silent loss wearing a
  // resolved anchor — so the mark keeps what it was written with and stays in the
  // store, ready for the next window that holds a reply. Closing this needs the
  // steer PERSISTED into the turn (the plan's phase 3), which is a wire change.
  it("keeps the mark but draws nothing when the window holds no assistant message", async () => {
    const a = freshID("c-noassistant");
    const m = freshID("m");
    store.setSessions([liveTurn(a, m)]);
    store.setActive(a);
    store.bumpMessages(a);
    await flushed();
    store.dropSteers(a, undefined);
    store.recordSteerQueued(a, { id: "steer-1", text: "never read this", origin: "user" });
    store.dropSteers(a);
    await flushed();
    expect(store.steerMarks(a)).toHaveLength(1);

    store.setSessions([
      session(a, {
        messages: [user(`${a}-u`, "go")],
        message_count: 1,
        steer_marks: [...store.steerMarks(a)],
      }),
    ]);
    store.setActive(a);
    store.bumpMessages(a, "load");
    await flushed();

    expect(store.steerMarks(a), "the record survives").toHaveLength(1);
    expect(notes(a), "and has nowhere to render").toHaveLength(0);
  });
});

// ---------------------------------------------------------------------------
// A CLAMP IS OBSERVED, so whoever discards its element owes the release, and
// `rebuildMessageBody` is the one teardown that keeps its row and replaces the
// row's children. Every park/unpark of a STREAMING message runs it, re-attaching
// a clamp through `flushSteerNotes` — and a steer note is the one thing in an
// assistant body that clamps, so without the release the observer's target set
// grows by one per tab switch for the life of the page.
// ---------------------------------------------------------------------------

describe("rebuilding a message body releases the clamps it discards", () => {
  /** One tab switch away and back: parks `a`'s view and unparks it, which is what
   *  routes its streaming message through `rebuildMessageBody`. */
  async function switchAwayAndBack(away: string, back: string): Promise<void> {
    store.setActive(away);
    await flushed();
    store.setActive(back);
    await flushed();
  }

  it("holds the observed-clamp count steady across repeated rebuilds", async () => {
    const a = freshID("c-clamp");
    const b = freshID("c-clamp");
    const m = freshID("m");
    store.setSessions([liveTurn(a, m), session(b, { messages: [user(`${b}-u`, "other")] })]);
    store.setActive(a);
    store.bumpMessages(a);
    await flushed();

    store.recordSteerQueued(a, { id: "steer-1", text: "use tabs", origin: "user" });
    store.promoteSteer(a, "steer-1", "use tabs", "user");
    await flushed();
    expect(notes(a), "the note the clamp belongs to").toHaveLength(1);

    // The baseline is taken AFTER one switch, because the first one also builds
    // `b`'s own view and its turn header clamps once — a one-off (that view is
    // PARKED, not disposed) which a baseline taken before it would read as growth.
    // The subject is per-rebuild growth, so measure between rebuilds.
    await switchAwayAndBack(b, a);
    const observed = clampObservationCount();

    await switchAwayAndBack(b, a);
    await switchAwayAndBack(b, a);
    await switchAwayAndBack(b, a);

    expect(notes(a), "one note, rebuilt three more times").toHaveLength(1);
    expect(clampObservationCount(), "one clamp per note, not one per rebuild").toBe(observed);
  });
});

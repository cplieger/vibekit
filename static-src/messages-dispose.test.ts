// ---------------------------------------------------------------------------
// The per-message half of the block-signal leak fix, at the renderer.
//
// A LIVE block mount mints a per-(message, block-index) signal
// (`ensureBlockTextSig` / `ensureBlockThinkingSig` in messages-blocks.ts), and
// until this fix nothing but the LAST chat closing ever removed the entry, so
// a long-lived page kept one signal per streamed block forever.
// `disposeMessage` runs for every row that leaves the renderer for good
// (message removal, view disposal, turn-card discard), so it is where the
// signals die with their message. A chat SWITCH is no longer such a moment:
// the multiplexer parks the view whole.
//
// The harness is messages-resume-reach.test.ts's: real store, real renderer,
// only the scroll subsystem mocked.
// ---------------------------------------------------------------------------

import { describe, it, expect } from "vitest";
import type { Block, Message, Session } from "./types.js";

// messages.ts's graph reads the shared DOM registry at module scope / mount,
// and `byId` throws on a missing element — so the hosts exist before any import
// resolves.
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

import { vi } from "vitest";
vi.mock("./scroll.js", () => import("./__test-helpers__/scroll-mock.js").then((m) => m.scrollMock));

// Spy-wrapped rather than replaced: the height cache is what a departing row hands
// over, so the ARGUMENT is the contract, and the real recording still has to run for
// the spacers that stand in for the row.
vi.mock("./block-heights.js", { spy: true });

const store = await import("./store.js");
const sigs = await import("./store-signals.js");
const messages = await import("./messages.js");
const heights = await import("./block-heights.js");
const { mountedWindow } = await import("./messages-blocks.js");
const { KEY_ATTR } = await import("./reconcile.js");

messages.mountChatView();

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

let seq = 0;

/** Mount one chat whose LAST assistant message is live-streaming — the state
 *  that makes the block mounts mint their signals. Returns (chat, message). */
function mountStreaming(blocks: Block[]): { chat: string; msgID: string } {
  const chat = `c-${String(++seq)}`;
  const msgID = `m-${String(seq)}`;
  store.setSessions([{ ...session(chat), thinking: true } as Session]);
  store.setActive(chat);
  store.appendMessage(chat, {
    id: msgID,
    role: "assistant",
    ts: 1,
    content: "",
    blocks,
  } as Message);
  return { chat, msgID };
}

/** Mount one chat over exactly `msgs`, the shape a page load hands the renderer. */
function mountChat(msgs: Message[]): string {
  const chat = `c-${String(++seq)}`;
  store.setSessions([{ ...session(chat), messages: msgs, message_count: msgs.length } as Session]);
  store.setActive(chat);
  store.bumpMessages(chat);
  return chat;
}

function asst(id: string, texts: string[]): Message {
  return {
    id,
    role: "assistant",
    ts: 2,
    content: "",
    blocks: texts.map((text) => ({ type: "text", text })),
  } as unknown as Message;
}

function turnCard(turnID: string): HTMLElement {
  const card = (messages.activeTranscriptView() ?? document.body).querySelector<HTMLElement>(
    `:scope > [${KEY_ATTR}="${turnID}"]`,
  );
  if (card === null) {
    throw new Error(`no card for turn ${turnID}`);
  }
  return card;
}

/** How many times `run()` reads a message row's height. `offsetHeight` is a
 *  configurable accessor on the prototype, so the count comes from wrapping it —
 *  calling THROUGH, so the measurement pass still records real heights, and
 *  restoring the captured descriptor in a `finally`, because a patch left installed
 *  would change what every later case in this file measures. */
function countRowGeometryReads(run: () => void): number {
  const desc = Object.getOwnPropertyDescriptor(HTMLElement.prototype, "offsetHeight");
  const real = desc?.get;
  if (desc === undefined || real === undefined) {
    throw new Error("offsetHeight is not an accessor on HTMLElement.prototype");
  }
  let reads = 0;
  Object.defineProperty(HTMLElement.prototype, "offsetHeight", {
    ...desc,
    get(this: HTMLElement): number {
      if (this.classList.contains("msg-wrap")) {
        reads++;
      }
      return real.call(this) as number;
    },
  });
  try {
    run();
  } finally {
    Object.defineProperty(HTMLElement.prototype, "offsetHeight", desc);
  }
  return reads;
}

function row(messageID: string): HTMLElement {
  const el = document.querySelector<HTMLElement>(`.msg-wrap[${KEY_ATTR}="${messageID}"]`);
  if (el === null) {
    throw new Error(`no row for message ${messageID}`);
  }
  return el;
}

describe("disposeMessage clears the row's block signals", () => {
  it("a removed message's signals die with its row", () => {
    // Only the TRAILING block of a live message streams, so the tail is what
    // mints a signal (renderRange: `live && i === lastIdx`).
    const { chat, msgID } = mountStreaming([
      { type: "thinking", thinking: "mulling" } as Block,
      { type: "text", text: "hello" } as Block,
    ]);
    expect(sigs.blockTextSigs.get(sigs.blockKey(msgID, 1))).toBeDefined();

    // The message leaves the transcript (a rewind's shape): the reconcile
    // removes its row, and disposeMessage runs for it.
    const s = store.get(chat)!;
    s.messages = [];
    store.bumpMessages(chat, "shape");

    expect(sigs.blockTextSigs.get(sigs.blockKey(msgID, 1))).toBeUndefined();
  });

  it("a chat switch PARKS the rows — signals survive until the view is disposed", () => {
    // A thinking tail, so the OTHER signal map is covered too.
    const { chat, msgID } = mountStreaming([{ type: "thinking", thinking: "mulling" } as Block]);
    expect(sigs.blockThinkingSigs.get(sigs.blockKey(msgID, 0))).toBeDefined();
    // A signal belonging to a message this renderer never mounted must survive
    // the disposal — per-message cleanup, not the wholesale teardown wipe.
    const foreign = sigs.ensureBlockTextSig("m-foreign", 0, "kept");

    // Switch to a second chat: the old chat's rows PARK with their view, and
    // their signals park with them — a parked transcript is resident state,
    // which is the whole point of the multiplexer (and why the store's
    // eviction exempts it).
    const other = `c-${String(++seq)}`;
    store.setSessions([store.get(store.getActiveId())!, session(other)] as Session[]);
    store.setActive(other);
    expect(sigs.blockThinkingSigs.get(sigs.blockKey(msgID, 0))).toBeDefined();

    // The view's REAL dispose (chat close, delete, LRU eviction) is where the
    // rows die now, and the signals die with them.
    messages.disposeChatView(chat);
    expect(sigs.blockThinkingSigs.get(sigs.blockKey(msgID, 0))).toBeUndefined();
    expect(sigs.blockTextSigs.get(sigs.blockKey("m-foreign", 0))).toBe(foreign);
    sigs.blockTextSigs.clear(sigs.blockKey("m-foreign", 0));
  });
});

// ---------------------------------------------------------------------------
// The height a departing row leaves behind, and where it is read.
//
// `reconcile` runs `onRemove` in place, between `el.remove()` calls, so the row
// measurement moved AHEAD of the mutation: one read pass per reconcile instead of
// one forced reflow per removed row. Two rules come out of that, and these cases
// pin both — the number a live row leaves is unchanged, and a row whose subtree the
// page is not rendering is measured not at all.
// ---------------------------------------------------------------------------

describe("what a dropped row records", () => {
  it("records the height the row measured while it was still mounted", () => {
    const chat = mountChat([
      { id: "u-live", role: "user", ts: 1, content: "prompt" } as Message,
      asst("a-live-1", ["first"]),
      asst("a-live-2", ["second"]),
    ]);
    const px = row("a-live-2").offsetHeight;
    expect(px).toBeGreaterThan(0);

    // The tail row leaves the body (a rewind's shape): the body reconciles, and
    // `onRemove` disposes the row it dropped.
    const s = store.get(chat)!;
    s.messages = s.messages.slice(0, 2);
    store.bumpMessages(chat, "shape");

    expect(vi.mocked(heights.recordRowHeight).mock.calls).toEqual([
      ["a-live-2", { from: 0, to: 1 }, px],
    ]);
  });

  it("disposes a FOLDED card's rows with no height recorded for them", async () => {
    // TWO prose blocks, so folding this turn hides something (turns.ts
    // `turnFoldHides`) and the policy folds it the moment a newer turn exists. A
    // folded card holds a body only while something has ASKED for one, which is the
    // search reveal's walk grant — and that body is `content-visibility: hidden`
    // and `block-size: 0`, so its rows have no height to hand over.
    const chat = mountChat([
      { id: "u-fold", role: "user", ts: 1, content: "older" } as Message,
      asst("a-fold", ["one", "two"]),
      { id: "u-new", role: "user", ts: 3, content: "newer" } as Message,
      asst("a-new", ["newest"]),
    ]);
    expect(turnCard("u-fold").hasAttribute("data-folded")).toBe(true);

    await messages.mountTurnBodyForWalk(chat, "u-fold");
    expect(turnCard("u-fold").hasAttribute("data-folded")).toBe(true);
    expect(mountedWindow("a-fold")).toEqual({ from: 0, to: 2 });
    vi.mocked(heights.recordRowHeight).mockClear();

    // The reveal ends, so the grant lapses and the fold pass takes the body back.
    messages.endWalkReveal(chat);
    store.bumpMessages(chat, "shape");

    // Disposed: its render state is gone with its row.
    expect(mountedWindow("a-fold")).toBeUndefined();
    expect(vi.mocked(heights.recordRowHeight)).not.toHaveBeenCalled();
  });

  it("reads no row geometry at all when the reconcile drops nothing", () => {
    const chat = mountChat([
      { id: "u-stream", role: "user", ts: 1, content: "prompt" } as Message,
      asst("a-stream-1", ["first"]),
      asst("a-stream-2", ["second"]),
    ]);

    // A streaming tail append: the turn keeps every row it holds and gains one, so
    // the reconcile has nothing departing and no `onRemove` to feed. A measurement
    // pass here would force a reflow on every frame of a running workflow for a
    // cache nothing reads back.
    const reads = countRowGeometryReads(() => {
      store.appendMessage(chat, asst("a-stream-3", ["third"]));
    });

    expect(mountedWindow("a-stream-3")).toEqual({ from: 0, to: 1 });
    expect(reads).toBe(0);
  });

  it("reads the departing row's geometry once, not once per mounted row", () => {
    const chat = mountChat([
      { id: "u-drop", role: "user", ts: 1, content: "prompt" } as Message,
      asst("a-drop-1", ["first"]),
      asst("a-drop-2", ["second"]),
      asst("a-drop-3", ["third"]),
    ]);

    // The tail row leaves the body, so the pass HAS a height to hand over — pinning
    // one read per drop rather than "some read happened", which the case above
    // would otherwise be satisfiable by never measuring at all.
    const s = store.get(chat)!;
    const reads = countRowGeometryReads(() => {
      s.messages = s.messages.slice(0, 3);
      store.bumpMessages(chat, "shape");
    });

    expect(mountedWindow("a-drop-3")).toBeUndefined();
    expect(reads).toBe(1);
  });

  it("disposes a PARKED view's rows with no height recorded for them", () => {
    const chat = mountChat([
      { id: "u-park", role: "user", ts: 1, content: "prompt" } as Message,
      asst("a-park", ["parked prose"]),
    ]);
    expect(mountedWindow("a-park")).toEqual({ from: 0, to: 1 });

    // Switching chats PARKS the view: `content-visibility: hidden`, so its rows
    // hold no geometry either, and the LRU dispose that eventually claims them
    // must not ask for any.
    const other = `c-${String(++seq)}`;
    store.setSessions([store.get(chat)!, session(other)] as Session[]);
    store.setActive(other);
    vi.mocked(heights.recordRowHeight).mockClear();

    messages.disposeChatView(chat);

    expect(mountedWindow("a-park")).toBeUndefined();
    expect(vi.mocked(heights.recordRowHeight)).not.toHaveBeenCalled();
  });
});

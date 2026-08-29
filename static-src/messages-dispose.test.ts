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

const store = await import("./store.js");
const sigs = await import("./store-signals.js");
const messages = await import("./messages.js");

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

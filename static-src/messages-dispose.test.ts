// ---------------------------------------------------------------------------
// The per-message half of the block-signal leak fix, at the renderer.
//
// A LIVE block mount mints a per-(message, block-index) signal
// (`ensureBlockTextSig` / `ensureBlockThinkingSig` in messages-blocks.ts), and
// until this fix nothing but `teardownAll` — the LAST chat closing — ever
// removed the entry, so a long-lived page kept one signal per streamed block
// forever. `disposeMessage` runs for every row the reconcile removes (message
// removal, chat switch, turn-card discard), so it is where the signals die
// with their message.
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

  it("a chat switch clears the previous chat's signals, not the whole map", () => {
    // A thinking tail, so the OTHER signal map is covered too.
    const { msgID } = mountStreaming([{ type: "thinking", thinking: "mulling" } as Block]);
    expect(sigs.blockThinkingSigs.get(sigs.blockKey(msgID, 0))).toBeDefined();
    // A signal belonging to a message this renderer never mounted must survive
    // the switch — per-message disposal, not the wholesale teardown wipe.
    const foreign = sigs.ensureBlockTextSig("m-foreign", 0, "kept");

    // Switch to a second chat: the old chat's rows unmount.
    const other = `c-${String(++seq)}`;
    store.setSessions([store.get(store.getActiveId())!, session(other)] as Session[]);
    store.setActive(other);

    expect(sigs.blockThinkingSigs.get(sigs.blockKey(msgID, 0))).toBeUndefined();
    expect(sigs.blockTextSigs.get(sigs.blockKey("m-foreign", 0))).toBe(foreign);
    sigs.blockTextSigs.clear(sigs.blockKey("m-foreign", 0));
  });
});

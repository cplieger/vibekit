// ---------------------------------------------------------------------------
// The transcript multiplexer: parked chat views with a pause/resume lifecycle.
//
// One `.transcript-view` per resident chat under `#messages`; exactly one is
// `.is-active`. A parked view keeps its DOM and render state with every writer
// paused — that is the contract this suite pins, from both sides: nothing may
// move a parked view (the freeze), and unparking must be equivalent to a cold
// rebuild (nothing missed while frozen).
//
// REAL store, REAL renderer, REAL scroll controller — the observers detaching
// at park is half the freeze claim, so the scroll module is deliberately not
// mocked here. Layout is real too (Browser Mode): the bottom-alignment and
// reading-state cases assert against actual boxes.
// ---------------------------------------------------------------------------

import { describe, it, expect, vi, beforeEach } from "vitest";
import type { Block, Message, Session, ToolCall } from "./types.js";

// messages.ts's graph reads the shared DOM registry at module scope / mount,
// and `byId` throws on a missing element — so the hosts exist before any import
// resolves. The transcript chain gets REAL geometry: the outer wrapper is given
// a fixed height, the scroller is absolutely positioned inside it (the shipped
// rule), so percentage chains and overflow behave as in production.
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

// The shipped transcript stylesheet, so the geometry under test is the
// production geometry (the multiplexer's height chain, the view's flex-end
// column, the parked view's zeroed box). Design tokens are absent, which only
// costs the token-driven paddings — every load-bearing declaration here is a
// literal. The harness block pins the wrapper's height, which production gets
// from the app shell's flex column.
const style = document.createElement("style");
style.textContent =
  loadCSS("13-messages.css") +
  `
  #messages-wrap-outer { height: 400px; }
`;
document.head.appendChild(style);

const store = await import("./store.js");
const sigs = await import("./store-signals.js");
const scroll = await import("./scroll.js");
const messages = await import("./messages.js");
const { appendTerminalChunk } = await import("./messages-tools.js");

messages.mountChatView();

const messagesEl = document.getElementById("messages") as HTMLElement;
const promptInput = document.getElementById("prompt-input") as HTMLTextAreaElement;

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

function assistant(id: string, blocks: Block[], toolCalls: ToolCall[] = []): Message {
  return {
    id,
    role: "assistant",
    ts: 2,
    content: "",
    blocks,
    tool_calls: toolCalls,
  } as unknown as Message;
}

function textBlock(text: string): Block {
  return { type: "text", text } as Block;
}

function call(id: string, over: Record<string, unknown> = {}): ToolCall {
  return {
    id,
    title: `run ${id}`,
    kind: "execute",
    status: "in_progress",
    ts: 0,
    ...over,
  } as unknown as ToolCall;
}

/** Mount a set of chats and activate the first. Each test owns its chats. */
function seed(...sessions: Session[]): void {
  store.setSessions(sessions);
  const first = sessions[0];
  if (first !== undefined) {
    store.setActive(first.id);
    store.bumpMessages(first.id);
  }
}

function switchTo(chatID: string): void {
  store.setActive(chatID);
}

function viewOf(chatID: string): HTMLElement {
  const el = messages.transcriptViewFor(chatID);
  if (el === null) {
    throw new Error(`no resident view for ${chatID}`);
  }
  return el;
}

/** One microtask: the store's per-chat coalescer flushes, and the flush paints. */
async function flushed(): Promise<void> {
  await Promise.resolve();
}

/** Two frames: enough for the mount's rAF-paced scrollToBottom to land, so a
 *  test gesture that follows cannot be undone by it. */
async function settledFrames(): Promise<void> {
  await new Promise((r) => requestAnimationFrame(() => r(undefined)));
  await new Promise((r) => requestAnimationFrame(() => r(undefined)));
}

beforeEach(() => {
  // The real teardown between cases: the multiplexer's registry persists at
  // module scope, and earlier tests' parked views would otherwise count
  // against the LRU budget of later ones.
  messages.teardownAll();
  store.setSessions([]);
  store.setActive("");
});

// ---------------------------------------------------------------------------
// The freeze: a parked view receives NOTHING.
// ---------------------------------------------------------------------------

describe("a parked view is frozen", () => {
  it("takes zero DOM writes, rAF and observer callbacks under appends, tool updates and terminal output", async () => {
    const a = freshID("c-frz");
    const b = freshID("c-frz");
    const msgID = freshID("m");
    const tc = call("t-frz", { terminal_id: "term-frz" });
    seed(
      session(a, {
        thinking: true,
        messages: [
          user(freshID("u"), "go"),
          // The text block LAST: only the trailing block of a live message
          // streams, so this is what arms a live binding effect — the thing
          // the park must disarm for the freeze to hold.
          assistant(
            msgID,
            [{ type: "tool_use", tool_call_id: tc.id } as Block, textBlock("hello")],
            [tc],
          ),
        ],
        message_count: 2,
      }),
      session(b, { messages: [user(freshID("u"), "other")], message_count: 1 }),
    );
    await flushed();

    switchTo(b);
    await flushed();
    await settledFrames();
    const parked = viewOf(a);
    expect(parked.classList.contains("is-active")).toBe(false);

    // Spies armed AFTER the park settled, so the switch's own work is not
    // counted against the freeze.
    const watcher = new MutationObserver(() => undefined);
    watcher.observe(parked, {
      childList: true,
      subtree: true,
      characterData: true,
      attributes: true,
    });
    const rafSpy = vi.spyOn(window, "requestAnimationFrame");
    const mutateCb = vi.fn();
    const offMutate = scroll.onTranscriptMutate(mutateCb);

    // Every writer that could reach the parked DOM fires once: a store append,
    // a streamed chunk into the live tail block, a tool_call_update, terminal
    // output.
    store.appendMessage(a, assistant(freshID("m"), [textBlock("appended while parked")]));
    store.appendChunk(a, msgID, " world", false, 1, "");
    store.upsertToolCall(a, msgID, call(tc.id, { status: "completed", title: "run t-frz" }), 0);
    appendTerminalChunk("term-frz", "chunk while parked\n", [], 0);
    await flushed();
    await flushed();

    expect(watcher.takeRecords()).toHaveLength(0);
    expect(rafSpy).not.toHaveBeenCalled();
    expect(mutateCb).not.toHaveBeenCalled();
    watcher.disconnect();
    rafSpy.mockRestore();
    offMutate();
  });
});

// ---------------------------------------------------------------------------
// Unpark correctness: nothing missed while frozen, nothing doubled after.
// ---------------------------------------------------------------------------

describe("park → grow → unpark", () => {
  it("shows the exact text after park-after-chunk → append-many-parked → unpark → append-again", async () => {
    const a = freshID("c-txt");
    const b = freshID("c-txt");
    const msgID = freshID("m");
    seed(
      session(a, {
        thinking: true,
        messages: [user(freshID("u"), "go"), assistant(msgID, [textBlock("Hel")])],
        message_count: 2,
      }),
      session(b, { messages: [user(freshID("u"), "other")], message_count: 1 }),
    );
    await flushed();
    // A chunk lands while the chat is live (through the block signal).
    store.appendChunk(a, msgID, "lo", false, 0, "");
    await flushed();

    switchTo(b);
    await flushed();

    // Many appends while parked: no subscriber sees them (the freeze case
    // above), the store accumulates them.
    for (const delta of [" wor", "ld", ", from", " the", " parked", " chat"]) {
      store.appendChunk(a, msgID, delta, false, 0, "");
    }
    await flushed();

    switchTo(a);
    await flushed();
    const bubble = (): string => viewOf(a).querySelector(".message.assistant")?.textContent ?? "";
    // The rebuilt live bubble holds the store's full text (the incremental
    // markdown parser may withhold a trailing character until the stream moves
    // or ends, so the EXACT assertion waits for the finalize below).
    await vi.waitFor(() => {
      expect(bubble()).toContain("Hello world, from the parked");
    });

    // Append again on the live view, then end the turn: exactly one binding
    // effect exists per block, so the delta lands exactly once and the
    // finalize drains the parser. A duplicated effect would append the delta
    // twice — the exact text IS the effect count.
    store.appendChunk(a, msgID, " — and more", false, 0, "");
    await flushed();
    store.setThinking(a, false);
    store.bumpMessages(a);
    await flushed();
    await vi.waitFor(() => {
      expect(bubble()).toBe("Hello world, from the parked chat — and more");
    });
    // One bubble, one row: the rebuild replaced the old body rather than
    // stacking a second copy beside it.
    expect(viewOf(a).querySelectorAll(".message.assistant")).toHaveLength(1);
  });

  it("unpark is equivalent to a cold rebuild, one copy of every block type", async () => {
    const a = freshID("c-eq");
    const b = freshID("c-eq");
    const msgID = freshID("m");
    const tc = call("t-eq");
    seed(
      session(a, {
        thinking: true,
        messages: [
          user(freshID("u"), "do the thing"),
          assistant(
            msgID,
            [
              { type: "thinking", thinking: "pondering" } as Block,
              textBlock("partial"),
              { type: "tool_use", tool_call_id: tc.id } as Block,
            ],
            [tc],
          ),
        ],
        message_count: 2,
      }),
      session(b, { messages: [user(freshID("u"), "other")], message_count: 1 }),
    );
    await flushed();

    switchTo(b);
    await flushed();

    // While parked: the text grows, the tool call completes, the turn ends.
    store.appendChunk(a, msgID, " answer", false, 1, "");
    store.upsertToolCall(a, msgID, call(tc.id, { status: "completed" }), 2);
    store.setThinking(a, false);
    store.bumpMessages(a);
    await flushed();

    switchTo(a);
    await flushed();
    const unparked = snapshot(viewOf(a));

    // The cold control: dispose the view outright and repaint from the same
    // store state.
    messages.disposeChatView(a);
    store.bumpMessages(a);
    await flushed();
    await vi.waitFor(() => {
      expect(snapshot(viewOf(a))).toEqual(unparked);
    });

    function snapshot(root: HTMLElement): Record<string, unknown> {
      return {
        text: [...root.querySelectorAll(".message.assistant")].map((n) => n.textContent),
        reasoning: [...root.querySelectorAll(".reasoning-block .reasoning-body")].map(
          (n) => n.textContent,
        ),
        cards: [...root.querySelectorAll<HTMLElement>(".tool-call")].map(
          (c) => c.dataset["outcome"] ?? "",
        ),
        turns: root.querySelectorAll(".turn").length,
      };
    }
  });

  it("paint-on-empty hides the view without disposing it", async () => {
    const a = freshID("c-empty");
    const msgID = freshID("m");
    seed(
      session(a, {
        messages: [user(freshID("u"), "hi"), assistant(msgID, [textBlock("kept whole")])],
        message_count: 2,
      }),
    );
    await flushed();
    const view = viewOf(a);
    const row = view.querySelector(".message.assistant");
    expect(row).not.toBeNull();

    // The last-tab window: no active chat, while the session stays in the
    // store. The paint must hide, never dispose.
    store.setActive("");
    await flushed();
    expect(view.isConnected).toBe(true);
    expect(view.classList.contains("is-active")).toBe(false);
    expect(view.inert).toBe(true);
    expect(sigs.blockTextSigs.get(sigs.blockKey(msgID, 0))).toBeUndefined(); // settled replay minted none

    // Reopening restores the SAME nodes: identity is the proof nothing was
    // disposed and rebuilt.
    switchTo(a);
    await flushed();
    expect(viewOf(a)).toBe(view);
    expect(view.querySelector(".message.assistant")).toBe(row);
    expect(view.classList.contains("is-active")).toBe(true);
    expect(view.inert).toBe(false);
  });
});

// ---------------------------------------------------------------------------
// The saved handle: reading state, scroll position, bottom alignment.
// ---------------------------------------------------------------------------

describe("the view handle", () => {
  /** Enough turns to overflow a 400px scroller even once older turns fold to
   *  header stubs: thirty headers alone overrun it. */
  function longChat(id: string): Session {
    const msgs: Message[] = [];
    for (let i = 0; i < 30; i++) {
      msgs.push(user(freshID("u"), `prompt ${String(i)}`));
      msgs.push(assistant(freshID("m"), [textBlock(`reply ${String(i)}`)]));
    }
    return session(id, { messages: msgs, message_count: msgs.length });
  }

  it("keeps two parked chats' reading states apart and restores each", async () => {
    const a = freshID("c-read");
    const b = freshID("c-read");
    const c = freshID("c-read");
    seed(
      longChat(a),
      longChat(b),
      session(c, { messages: [user(freshID("u"), "x")], message_count: 1 }),
    );
    await flushed();
    await settledFrames();

    // Chat A: the reader scrolls UP — Reading. `scroll-behavior: smooth` is on
    // the scroller, so a bare scrollTop assignment would only START an
    // animation; the instant scrollTo is the synchronous gesture.
    const scroller = scroll.getScrollEl();
    expect(scroller.scrollHeight).toBeGreaterThan(scroller.clientHeight + 150);
    scroller.scrollTo({ top: 5, behavior: "instant" });
    scroller.dispatchEvent(new Event("scroll"));
    expect(scroll.readingState()).toBe("reading");

    switchTo(b);
    await flushed();
    await settledFrames();
    // Chat B stays at the live edge — Following.
    expect(scroll.readingState()).toBe("following");

    switchTo(c);
    await flushed();
    await settledFrames();

    // Unpark A: Reading and the parked scroll offset come back.
    switchTo(a);
    await flushed();
    expect(scroll.readingState()).toBe("reading");
    expect(scroller.scrollTop).toBe(5);

    // Unpark B: Following.
    switchTo(b);
    await flushed();
    expect(scroll.readingState()).toBe("following");
  });

  it("keeps a short transcript bottom-aligned through a park/unpark cycle", async () => {
    const a = freshID("c-align");
    const b = freshID("c-align");
    seed(
      session(a, {
        messages: [user(freshID("u"), "hi"), assistant(freshID("m"), [textBlock("short")])],
        message_count: 2,
      }),
      session(b, { messages: [user(freshID("u"), "x")], message_count: 1 }),
    );
    await flushed();

    const wrap = scroll.getScrollEl();
    const bottomGap = (): number => {
      const card = viewOf(a).querySelector<HTMLElement>(".turn:last-of-type");
      if (card === null) {
        throw new Error("no card");
      }
      return wrap.getBoundingClientRect().bottom - card.getBoundingClientRect().bottom;
    };
    const before = bottomGap();
    // flex-end on a min-height:100% column: the card hugs the viewport bottom.
    // Tokens are absent here so the padding is 0; the gap is the alignment.
    expect(before).toBeLessThan(50);

    switchTo(b);
    await flushed();
    switchTo(a);
    await flushed();
    expect(bottomGap()).toBe(before);
  });
});

// ---------------------------------------------------------------------------
// Focus and reachability.
// ---------------------------------------------------------------------------

describe("focus and reachability", () => {
  it("relocates focus to the composer when the focused element parks, and marks the view inert", async () => {
    const a = freshID("c-foc");
    const b = freshID("c-foc");
    const tc = call("t-foc", { status: "completed" });
    seed(
      session(a, {
        messages: [
          user(freshID("u"), "hi"),
          assistant(freshID("m"), [{ type: "tool_use", tool_call_id: tc.id } as Block], [tc]),
        ],
        message_count: 2,
      }),
      session(b, { messages: [user(freshID("u"), "x")], message_count: 1 }),
    );
    await flushed();

    // A real focusable inside the view: the turn header's fold toggle.
    const toggle = viewOf(a).querySelector<HTMLElement>(".turn-fold-toggle, [tabindex], button");
    expect(toggle).not.toBeNull();
    toggle?.focus();
    expect(viewOf(a).contains(document.activeElement)).toBe(true);

    switchTo(b);
    await flushed();
    expect(document.activeElement).toBe(promptInput);

    // The parked view is inert: focus cannot land inside it (real Chromium
    // focus semantics — an inert subtree refuses programmatic focus too).
    const parked = viewOf(a);
    expect(parked.inert).toBe(true);
    toggle?.focus();
    expect(parked.contains(document.activeElement)).toBe(false);
  });

  it("keeps parked text out of the transcript find's walk", async () => {
    const a = freshID("c-find");
    const b = freshID("c-find");
    seed(
      session(a, {
        messages: [user(freshID("u"), "hi"), assistant(freshID("m"), [textBlock("needle in A")])],
        message_count: 2,
      }),
      session(b, {
        messages: [user(freshID("u"), "hi"), assistant(freshID("m"), [textBlock("plain B")])],
        message_count: 2,
      }),
    );
    await flushed();
    switchTo(b);
    await flushed();
    // `.msg-row` runs content-visibility:auto, and rows start SKIPPED until
    // the browser's first rendering pass resolves their relevance — the find
    // walker prunes skipped subtrees, so give it that frame (production find
    // opens on a user gesture, long after paint).
    await settledFrames();

    // The find walker roots at the ACTIVE view (find-in-chat resolves
    // `.transcript-view.is-active`), so A's text is unreachable while parked.
    // normalize() first: the streaming markdown renderer leaves a word split
    // across text nodes, and the engine matches per node — the subject here is
    // the WALK SCOPE, not tokenization.
    const { FindEngine } = await import("./find-engine.js");
    const active = messagesEl.querySelector<HTMLElement>(":scope > .transcript-view.is-active");
    expect(active).toBe(viewOf(b));
    active?.normalize();
    const engine = new FindEngine(active ?? messagesEl);
    engine.search("needle", false);
    expect(engine.total).toBe(0);
    engine.search("plain", false);
    expect(engine.total).toBe(1);
    engine.clear();
  });
});

// ---------------------------------------------------------------------------
// Disposal: LRU, close, teardown.
// ---------------------------------------------------------------------------

describe("disposal", () => {
  it("evicts the least-recently-used parked view past the budget and leaks nothing", async () => {
    const ids: string[] = [];
    const sessions: Session[] = [];
    for (let i = 0; i < messages.PARKED_VIEWS + 2; i++) {
      const id = freshID("c-lru");
      ids.push(id);
      const msgID = `m-of-${id}`;
      const tc = call(`t-of-${id}`, { status: "completed" });
      sessions.push(
        session(id, {
          messages: [
            user(`u-of-${id}`, "hi"),
            assistant(
              msgID,
              [textBlock(`text of ${id}`), { type: "tool_use", tool_call_id: tc.id } as Block],
              [tc],
            ),
          ],
          message_count: 2,
        }),
      );
    }
    seed(...sessions);
    await flushed();
    const first = ids[0] ?? "";
    // The first chat's card minted its composite-keyed signal at mount.
    expect(sigs.toolCallSigs.get(sigs.toolCallSigKey(first, `t-of-${first}`))).toBeDefined();
    for (const id of ids.slice(1)) {
      switchTo(id);
      await flushed();
    }

    // Five chats visited: the first is past the parked budget of three and ran
    // the real dispose — its container is gone, not hidden.
    expect(messages.transcriptViewFor(first)).toBeNull();
    expect(messagesEl.querySelectorAll(":scope > .transcript-view")).toHaveLength(
      messages.PARKED_VIEWS + 1,
    );
    // Its tool signal space went with it (composite keys, per-view dispose).
    expect(sigs.toolCallSigs.get(sigs.toolCallSigKey(first, `t-of-${first}`))).toBeUndefined();

    // The surviving parked views are intact.
    for (const id of ids.slice(2, -1)) {
      expect(viewOf(id).isConnected).toBe(true);
    }
  });

  it("teardownAll with parked views leaves no DOM and no state", async () => {
    const a = freshID("c-td");
    const b = freshID("c-td");
    const msgA = freshID("m");
    const tcA = call("t-td", { status: "in_progress" });
    seed(
      session(a, {
        thinking: true,
        messages: [
          user(freshID("u"), "hi"),
          // The text block LAST: only the trailing block of a live message
          // streams, and the streaming mount is what mints the block signal.
          assistant(
            msgA,
            [{ type: "tool_use", tool_call_id: tcA.id } as Block, textBlock("live")],
            [tcA],
          ),
        ],
        message_count: 2,
      }),
      session(b, { messages: [user(freshID("u"), "x")], message_count: 1 }),
    );
    await flushed();
    // A live block signal exists while the tail streams.
    expect(sigs.blockTextSigs.get(sigs.blockKey(msgA, 1))).toBeDefined();

    switchTo(b);
    await flushed();
    expect(messagesEl.querySelectorAll(":scope > .transcript-view")).toHaveLength(2);

    messages.teardownAll();

    // The op-set, observed: container removal (no view DOM at all)...
    expect(messagesEl.querySelectorAll(":scope > .transcript-view")).toHaveLength(0);
    expect(messagesEl.childElementCount).toBe(0);
    // ...per-message signal clears (the parked streaming tail's included)...
    expect(sigs.blockTextSigs.get(sigs.blockKey(msgA, 1))).toBeUndefined();
    // ...tool effect disposal through the composite keys, signal cleared...
    expect(sigs.toolCallSigs.get(sigs.toolCallSigKey(a, tcA.id))).toBeUndefined();
    // ...scroll reset (Following, no stale reading state)...
    expect(scroll.readingState()).toBe("following");
    // ...and a repaint after teardown starts from scratch rather than finding
    // stale registries (messageStates pruning, block-render resets): the next
    // bump re-creates the active chat's view whole.
    store.bumpMessages(b);
    await flushed();
    expect(viewOf(b).querySelectorAll(".turn").length).toBeGreaterThan(0);
  });
});

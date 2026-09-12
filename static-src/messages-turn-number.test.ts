// ---------------------------------------------------------------------------
// A PAGED transcript numbers its turns SESSION-ABSOLUTELY.
//
// The store is a newest-first window, so `projectTurns`' own scan starts at the
// window and cannot know what precedes it. Told nothing, it numbered turn 1 of the
// PAGE as turn 1 of the session: a card reading `#1` beside a rail marker reading
// `#14`, and — because the offset moves every time an older page lands — a number
// that CHANGED under the reader mid-scroll. The window response now carries the
// left edge (`turn_offset` / `turn_segment_closed`) and `store.ts turnBaseOf` feeds
// it to the paint pass.
//
// REAL PAINT over REAL layout, following messages-send-pin.test.ts's harness: what
// is under test is what the renderer WRITES (`.turn-n` text, the card's anchor id,
// the folded row's search-hit count), and none of those is reachable from the
// projection alone. The second case is the one that matters most and the one a
// pure-projection test cannot express at all: it repaints over an older page and
// asserts the cards already on screen keep their numbers.
//
// EVERY WAIT POLLS AN OBSERVABLE, for that file's reason: a fixed sleep here would
// have to cover a real paint and real layout on a box CI packs onto four cores.
// ---------------------------------------------------------------------------
import { describe, it, expect, vi, beforeEach } from "vitest";
import { FRAME_BUDGET_MS } from "./__test-helpers__/frame-budget.js";
import type { Message, Session } from "./types.js";

// The DOM the renderer's import graph resolves at load, nested the way the page
// nests it.
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
// Deterministic card boxes: production gets them from a stylesheet.
const style = document.createElement("style");
style.textContent = `.turn{block-size:200px}`;
document.head.appendChild(style);

// The rail's session-wide index is its own fetch and the pagination door is a
// network read; neither is what these cases are about.
vi.mock("./api-client.js", { spy: true });
vi.mock("./store-load.js", () => ({ loadMessages: vi.fn(), loadList: vi.fn() }));

const store = await import("./store.js");
const { noteLoaded, syncEpoch } = await import("./tab-freshness.js");
const messages = await import("./messages.js");
const search = await import("./chat-search.js");
const { apiGet } = await import("./api-client.js");

messages.mountChatView();

function user(id: string): Message {
  return { id, role: "user", ts: 1, content: `prompt ${id}` } as Message;
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

/** Turn `n` as the pair of messages that make one: the user message that OPENS it,
 *  whose id is the reconcile key, and its reply. */
function turnPair(n: number): Message[] {
  return [user(`u${String(n)}`), assistant(`a${String(n)}`, `reply ${String(n)}`)];
}

function pairs(from: number, to: number): Message[] {
  const out: Message[] = [];
  for (let n = from; n <= to; n++) {
    out.push(...turnPair(n));
  }
  return out;
}

/** A session holding a WINDOW: `turn_offset` is what the server answered for its
 *  oldest message, and `message_count` is the whole chat's, so `has_more` is honest.
 *  `residency` is `loaded`, or an activation refetches and the mocked loader
 *  answers nothing. */
function windowed(id: string, msgs: Message[], offset: number, total: number): Session {
  noteLoaded("chat", id, syncEpoch());
  return {
    id,
    name: id,
    messages: msgs,
    message_count: total,
    has_more: msgs.length < total,
    turn_offset: offset,
    turn_segment_closed: false,
    residency: "loaded",
    thinking: false,
    working_label: "",
  } as unknown as Session;
}

let seq = 0;
/** A chat id no earlier case has used: `setActive` is a no-op for the id already
 *  active, so a reused id paints nothing and the case runs against an empty view. */
function nextChat(): string {
  seq += 1;
  return `t${String(seq)}`;
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

function cards(): HTMLElement[] {
  const root = messages.activeTranscriptView();
  return root === null ? [] : [...root.querySelectorAll<HTMLElement>(":scope > .turn")];
}

/** What the reader SEES: the `#N` each card's header renders, in document order. */
function renderedNumbers(): string[] {
  return cards().map((c) => c.querySelector(".turn-n")?.textContent ?? "");
}

/** The anchor id each card carries (`turnAnchorID`), in document order. */
function anchorIDs(): string[] {
  return cards().map((c) => c.id);
}

/** The search-hit badge each card advertises, keyed by reconcile key. Empty string
 *  is what `setHitCount` writes for a turn with no hits. */
function hitBadges(): Record<string, string> {
  const out: Record<string, string> = {};
  for (const c of cards()) {
    const key = c.getAttribute("data-reconcile-key");
    if (key !== null) {
      out[key] =
        c.querySelector<HTMLElement>(":scope > .turn-header > .turn-head-row > .turn-hit-count")
          ?.textContent ?? "MISSING";
    }
  }
  return out;
}

/** Every card's number, keyed by its reconcile key, so two paints can be compared
 *  across a prepend that changed which cards exist. */
function numbersByKey(): Map<string, string> {
  const out = new Map<string, string>();
  for (const c of cards()) {
    const key = c.getAttribute("data-reconcile-key");
    if (key !== null) {
      out.set(key, c.querySelector(".turn-n")?.textContent ?? "");
    }
  }
  return out;
}

/** Paint `msgs` as chat `id`'s window and wait for the cards to mount. */
async function paint(id: string, msgs: Message[], offset: number, total: number): Promise<void> {
  const expected = msgs.filter((m) => m.role === "user").length;
  store.setSessions([windowed(id, msgs, offset, total)]);
  store.setActive(id);
  await until(() => cards().length === expected, `${String(expected)} cards to mount for ${id}`);
}

/** What `loadMessages(id, oldest.id)` leaves behind, applied the way that function
 *  applies it: the older page in FRONT of the window, the base replaced by the
 *  server's answer for the NEW oldest message, then the `load` bump the loader ends
 *  on. Not a second `paint`, deliberately — `setActive` is a no-op for the chat
 *  already active, so a repaint has to come from the store mutation, which is also
 *  what production does. */
async function prependPage(id: string, older: Message[], offset: number): Promise<void> {
  const s = store.get(id);
  if (s === undefined) {
    throw new Error(`no session for ${id}`);
  }
  const expected = [...older, ...s.messages].filter((m) => m.role === "user").length;
  s.messages = [...older, ...s.messages];
  s.turn_offset = offset;
  store.bumpMessages(id, "load");
  await until(() => cards().length === expected, `${String(expected)} cards after the prepend`);
}

beforeEach(() => {
  vi.mocked(apiGet).mockResolvedValue({ turns: [] });
  search.resetServerSearch();
});

describe("a paged transcript's rendered turn numbers", () => {
  it("numbers the window from its absolute ordinal, not from 1", async () => {
    // Turns 12-14 of a 14-turn chat: 11 precede the window, so the oldest card the
    // reader can see is #12. Base-less, all three read #1..#3.
    const id = nextChat();
    await paint(id, pairs(12, 14), 11, 28);

    expect(renderedNumbers()).toEqual(["#12", "#13", "#14"]);
    // The anchor moves with the number, so a `#turn-{n}` fragment names the turn the
    // rail's own session-wide index calls by that number.
    expect(anchorIDs()).toEqual(["turn-12", "turn-13", "turn-14"]);
  });

  it("keeps every visible turn's number when an older page is prepended", async () => {
    // The property the whole base mechanism exists for. A window-local scan renumbers
    // on every prepend, so the card the reader was looking at silently became a
    // different turn — and the rail marker it was clicked from stopped matching it.
    const id = nextChat();
    await paint(id, pairs(12, 14), 11, 28);
    const before = numbersByKey();
    expect([...before.keys()]).toEqual(["u12", "u13", "u14"]);

    // Turns 9-11 arrive in front, and the server's answer for the new oldest message
    // (u9) is that 8 turns precede it.
    await prependPage(id, pairs(9, 11), 8);

    expect(renderedNumbers()).toEqual(["#9", "#10", "#11", "#12", "#13", "#14"]);
    const after = numbersByKey();
    for (const [key, n] of before) {
      expect(`${key} was ${n} and is ${after.get(key) ?? "gone"}`).toBe(
        `${key} was ${n} and is ${n}`,
      );
    }
  });

  it("resolves a folded row's search-hit count against the server's absolute turn", async () => {
    // `chat-search.ts` keys `countsByTurn` by `SearchHit.turn`, which the server
    // computes over the WHOLE message array. A window-local `n` looked that absolute
    // key up, so a folded row on any paged chat advertised the wrong count — 0 for a
    // turn holding matches, and another turn's total for one that did not.
    const id = nextChat();
    // Two matches inside turn 13, which is the SECOND card of the window — so a
    // window-local scan would put this count on the card the server called turn 12.
    const hit = (offset: number): Record<string, unknown> => ({
      turn: 13,
      turn_message_id: "u13",
      message_id: "a13",
      excerpt: "reply 13",
      role: "assistant",
      segment_kind: "content",
      offset,
      segment_len: 8,
    });
    vi.mocked(apiGet).mockResolvedValue({ hits: [hit(0), hit(6)] });
    await search.runServerSearch(id, "reply");
    expect(search.searchHitCount(13)).toBe(2);
    // The rail's own index fetch runs during the paint below and must not answer
    // with hits.
    vi.mocked(apiGet).mockResolvedValue({ turns: [] });

    await paint(id, pairs(12, 14), 11, 28);

    // The count lands on the turn the SERVER said matched, and on no other.
    expect(hitBadges()).toEqual({ u12: "", u13: "2", u14: "" });
  });
});

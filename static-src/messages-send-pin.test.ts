// ---------------------------------------------------------------------------
// WHICH turn mounts ask for the live edge.
//
// `buildTurn` pins to the live edge for a turn whose trigger is a user message,
// and that pin publishes a READER GESTURE (scroll.ts `onReaderGesture`), which is
// what revokes the timeline rail's pick. So the question the gate has to answer is
// not "does this turn have a user trigger" — a chat-switch replay, a refetched
// window and a pagination prepend all mount user-triggered cards too — but "did
// the reader just send it".
//
// The paged rail jump is where that meets a live defect: `jumpToTurn` pages history
// in to reach a non-resident turn, those cards mount DURING the jump, and an
// ungated pin revoked the pick that same click had just set.
//
// REAL scroll module over REAL layout. `scroll.test.ts`'s `fakeScroller` shadows
// `scrollTo` with an assignment to its own number and emits no scroll event, so
// under it a pin publishes nothing through the listener and every case here would
// pass with the gate deleted. The cases live in their own file rather than in that
// one because driving a paint re-roots the controller's observers onto a
// `.transcript-view` (`activateView` → `attach`), which is not the fixture
// scroll.test.ts's own real-layout block appends its blocks into.
//
// EVERY WAIT HERE POLLS AN OBSERVABLE. A fixed sleep in this file would have to
// cover a real paint, real layout, a 700ms pin-settle window and scroll events the
// browser coalesces, on a box CI packs onto four cores — which is where this
// fleet's "passes locally, fails in CI" reports come from. A negative assertion
// waits for the POSITIVE precondition that would have produced the publish (the
// cards mounted, the page fetched, the scroller settled) and asserts afterwards;
// the publish is synchronous inside the paint that mounts the card, so a mutant's
// gesture is already recorded by the time the mount is observable.
// ---------------------------------------------------------------------------
import { describe, it, expect, vi, beforeEach } from "vitest";
import type { Message, Session } from "./types.js";
import type { TurnSummary } from "./turn-rail.js";

// The DOM the renderer's import graph resolves at load, nested the way the page
// nests it: the rail mounts in the positioned OUTER wrapper, the scroller is the
// wrapper, and `#messages` holds one `.transcript-view` per resident chat.
const outer = document.createElement("div");
outer.id = "messages-wrap-outer";
outer.style.cssText = "position:relative;";
const wrap = document.createElement("div");
wrap.id = "messages-wrap";
// `overflow-anchor: none` is production's (css/13-messages.css) and it is
// load-bearing here: without it Chromium's own scroll anchoring adjusts scrollTop
// when a page of older turns lands above the reader, which is the number these
// cases read to decide whether a pin happened.
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
// Turn cards get their height from a stylesheet in production; these cases need
// deterministic boxes, so the scene declares them. `u3` is deliberately SHORT: the
// paged case asserts that the mark lands on the turn that was CLICKED rather than
// on the taller neighbour the dominance rule would otherwise pick.
const style = document.createElement("style");
style.textContent =
  `.turn{block-size:200px}[data-reconcile-key="u3"]{block-size:40px}` +
  // The rail's marker capacity is computed from its own measured height
  // (`railRows`), so a rail with no box clusters every turn into one row and no
  // per-turn marker exists to click.
  `.turn-rail{position:absolute;inset-block-start:0;block-size:400px}`;
document.head.appendChild(style);

// The rail's session-wide index is its own fetch (`GET /api/chats/{id}/turns`), and
// the pagination door is a network read. Both are staged: what is under test is the
// sequencing around them.
const { served } = vi.hoisted(() => ({ served: { turns: [] as unknown[] } }));
vi.mock("./api-client.js", { spy: true });
vi.mock("./store-load.js", () => ({ loadMessages: vi.fn(), loadList: vi.fn() }));

const store = await import("./store.js");
const scroll = await import("./scroll.js");
const messages = await import("./messages.js");
const rail = await import("./turn-rail.js");
const { apiGet } = await import("./api-client.js");
const { loadMessages } = await import("./store-load.js");

messages.mountChatView();

const MINUTE = 60_000;

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

/** Turn `n` as the pair of messages that make one: the user message that OPENS it
 *  (whose id is both the reconcile key and `TurnSummary.id`) and its reply. */
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

/** How many cards a window projects to: a user message opens a turn, and every
 *  fixture here is user-first, so the count is the number of them. */
function turnCount(msgs: readonly Message[]): number {
  return msgs.filter((m) => m.role === "user").length;
}

function summary(n: number): TurnSummary {
  return { id: `u${String(n)}`, n, outcome: "completed", ts: n * MINUTE };
}

function session(id: string, msgs: Message[], hasMore = false): Session {
  return {
    id,
    name: id,
    messages: msgs,
    message_count: msgs.length,
    has_more: hasMore,
    thinking: false,
    working_label: "",
  } as unknown as Session;
}

/** Poll `pred` until it holds. `what` is the sentence the failure reads as, so a
 *  timeout names the thing that never happened rather than a bare deadline.
 *
 *  The budget is PER WAIT, and the longest case here chains three of them, so it
 *  is sized against that chain rather than against one wait: 3 x 1200ms leaves
 *  1.4s under the 5s `testTimeout`, which is what keeps an expiry this helper's
 *  message and not the runner's. At 2000ms the chain reached 6s and a
 *  slow-but-not-hung run lost the diagnosis the helper exists to give. Every
 *  predicate here settles within a few frames, so 1200ms is still ~15x headroom.
 *  The interval yields to the task queue, which is what lets a scroll event, a
 *  rAF callback and an observer delivery all land. */
async function until(pred: () => boolean, what: string, budget = 1200): Promise<void> {
  const deadline = Date.now() + budget;
  while (!pred()) {
    if (Date.now() > deadline) {
      throw new Error(`timed out after ${String(budget)}ms waiting for ${what}`);
    }
    await new Promise((r) => setTimeout(r, 8));
  }
}

/** A chat id no earlier case has used. Two module states make this necessary
 *  rather than tidy: `setActive` is a no-op for the id already active (so a reused
 *  id paints nothing and the case runs against an empty view), and the rail's own
 *  per-chat index records are keyed by it. */
let seq = 0;
function nextChat(): string {
  seq += 1;
  return `c${String(seq)}`;
}

/** The active view's cards, in document order. */
function cards(): HTMLElement[] {
  const root = messages.activeTranscriptView();
  return root === null ? [] : [...root.querySelectorAll<HTMLElement>(":scope > .turn")];
}

function cardFor(turnID: string): HTMLElement | null {
  return (
    messages
      .activeTranscriptView()
      ?.querySelector<HTMLElement>(`:scope > [data-reconcile-key="${turnID}"]`) ?? null
  );
}

function markers(): HTMLElement[] {
  return [...document.querySelectorAll<HTMLElement>(".turn-rail > .rail-marker")];
}

function markerFor(n: number): HTMLElement {
  const hit = markers().find((m) => m.textContent === String(n));
  if (hit === undefined) {
    throw new Error(`no rail marker for turn ${String(n)}`);
  }
  return hit;
}

function atLiveEdge(): boolean {
  return wrap.scrollTop === wrap.scrollHeight - wrap.clientHeight;
}

/** Wait until the scroller has stopped moving on its own.
 *
 *  Required before a gesture, not tidiness: a paint's Following pin reaches the
 *  live edge through a rAF, so a scroll written while that frame is still queued
 *  is silently overwritten by it — measured, a park at 120 came back as the live
 *  edge. Three identical reads across the poll interval is at least one frame with
 *  no write in it, which is all a queued callback needs to have run. */
async function quiet(): Promise<void> {
  let last = -1;
  let stable = 0;
  await until(() => {
    if (wrap.scrollTop === last) {
      stable += 1;
    } else {
      stable = 0;
      last = wrap.scrollTop;
    }
    return stable >= 3;
  }, "the scroller to stop moving");
}

/** Park the reader at `top` with a real gesture, so the state is the listener's
 *  own verdict rather than a seeded field — and CONFIRMED, both halves: the
 *  verdict the listener wrote on the event, and the position surviving it. */
async function park(top: number): Promise<void> {
  await quiet();
  wrap.scrollTop = top;
  await until(
    () => {
      // RE-ASSERT until it sticks. `quiet()` cannot prove no pin frame is queued:
      // the pin pass re-writes the SAME position every rAF for PIN_SETTLE_MS, so
      // positional stability reads identically to nothing writing at all. A frame
      // queued before this write lands after it and takes the scroller back to the
      // live edge, and with a one-shot write nothing ever puts it back — which is
      // this wait expiring under load. A reader holding a drag writes every frame
      // too, so re-asserting is the honest shape rather than a workaround.
      if (wrap.scrollTop !== top) {
        wrap.scrollTop = top;
        return false;
      }
      return scroll.readingState() === "reading";
    },
    `the reader parked at ${String(top)}`,
  );
}

/** Mount `chatID` as the active chat and wait for its first paint to have produced
 *  a card per turn. `setActive` paints synchronously; what the wait covers is the
 *  observers and the rAF the paint hands work to. */
async function mount(chatID: string, msgs: Message[], hasMore = false): Promise<void> {
  store.setSessions([session(chatID, msgs, hasMore)]);
  store.setActive(chatID);
  const want = turnCount(msgs);
  await until(
    () => messages.activeTranscriptView() !== null && cards().length === want,
    `${chatID}'s first paint to mount ${String(want)} card(s)`,
  );
}

function requireSession(chatID: string): Session {
  const s = store.get(chatID);
  if (s === undefined) {
    throw new Error(`no session ${chatID}`);
  }
  return s;
}

/** The older-page prepend, exactly as `loadMessages`' own older-page branch
 *  applies it: the fetched page in front of the resident window, the index
 *  rebuilt, one `load` bump. */
function prepend(chatID: string, older: Message[], hasMore = false): void {
  const s = requireSession(chatID);
  s.messages = [...older, ...s.messages];
  s.message_count = s.messages.length;
  s.has_more = hasMore;
  store.rebuildMsgIndex(chatID, s.messages);
  store.bumpMessages(chatID, "load");
}

/** The newest-page fetch, as `loadMessages`' own newest-page branch applies it: the
 *  server's window in place of the resident one, the index rebuilt, one `load`
 *  bump.
 *
 *  The CAUSE is the one thing this helper restates rather than drives, because
 *  `store-load.js` is mocked out of this file's graph; that `loadMessages` really
 *  announces a fetched window as `load` is pinned at the production call site, in
 *  store-load.test.ts. */
function loadWindow(chatID: string, fetched: Message[], hasMore = false): void {
  const s = requireSession(chatID);
  s.messages = fetched;
  s.message_count = fetched.length;
  s.has_more = hasMore;
  store.rebuildMsgIndex(chatID, s.messages);
  store.bumpMessages(chatID, "load");
}

beforeEach(() => {
  messages.teardownAll();
  store.setActive("");
  store.setSessions([]);
  served.turns = [];
  vi.mocked(apiGet).mockImplementation((path: string) =>
    Promise.resolve(path.endsWith("/turns") ? ({ turns: served.turns } as never) : null),
  );
  vi.mocked(loadMessages).mockReset();
});

describe("the live-edge pin a turn mount asks for", () => {
  it("publishes a reader gesture for a turn the reader just sent", async () => {
    // The genuine case, and the one the gate must not cost: the reader asked for
    // this turn, so the pin takes them to it even though they were parked further
    // up, and anything holding a position they have now abandoned has to hear it.
    const chat = nextChat();
    await mount(chat, pairs(1, 3));
    await park(120);
    expect(scroll.readingState()).toBe("reading");

    const seen = vi.fn();
    const off = scroll.onReaderGesture(seen);
    store.appendMessage(chat, user("u4"));
    // The card first, then the landing: `scrollToBottom` runs inside `buildTurn`,
    // BEFORE reconcile has inserted the card, so the pin's own re-assert frames are
    // what carry the scroller to the height the new card added.
    await until(() => cardFor("u4") !== null, "the sent turn's card to mount");
    await until(atLiveEdge, "the pin to settle at the live edge");
    off();

    expect(seen).toHaveBeenCalledTimes(1);
  });

  it("publishes for the first turn of a chat that had nothing in it", async () => {
    // The paint before this one had no TAIL to append past, which is not the same
    // as nothing having arrived: this is the reader's first prompt in a fresh chat,
    // the one appended-tail paint with no tail behind it. A one-turn transcript
    // cannot scroll, so the gesture is the observable — and with no scroll event
    // possible here, the pin is the only thing that can publish one.
    const chat = nextChat();
    await mount(chat, []);
    expect(cards()).toHaveLength(0);

    const seen = vi.fn();
    const off = scroll.onReaderGesture(seen);
    store.appendMessage(chat, user("u1"));
    await until(() => seen.mock.calls.length > 0, "the pin to publish its gesture");
    off();

    expect(cardFor("u1")).not.toBeNull();
    expect(seen).toHaveBeenCalledTimes(1);
  });

  it("stays silent for a turn card a pagination prepend built", async () => {
    // The reader is reading old history; the cards a page of it mounts are not
    // theirs. The scrollTop assertion is the control that stops this passing
    // because nothing mounted: a pin would take them to the live edge.
    const chat = nextChat();
    await mount(chat, pairs(4, 6), true);
    await park(120);

    const seen = vi.fn();
    const off = scroll.onReaderGesture(seen);
    prepend(chat, pairs(1, 3));
    await until(() => cards().length === 6, "the prepended page's cards to mount");
    off();

    expect(cardFor("u1")).not.toBeNull();
    expect(wrap.scrollTop).toBe(120);
    expect(seen).toHaveBeenCalledTimes(0);
  });

  it("stays silent for a turn card a chat-switch replay built", async () => {
    // Every turn of the incoming chat mounts at once, and none of them is a turn
    // the reader just sent — they are a conversation being replayed.
    const first = nextChat();
    const second = nextChat();
    await mount(first, pairs(1, 2));
    store.setSessions([session(first, pairs(1, 2)), session(second, pairs(1, 3))]);

    const seen = vi.fn();
    const off = scroll.onReaderGesture(seen);
    store.setActive(second);
    await until(() => cards().length === 3, "the switched-to chat's cards to mount");
    // The control that stops the gate reading as a regression: an opened chat
    // still lands at the live edge, because a Following reader is pinned there by
    // the streaming auto-scroll rather than by this mount.
    await until(atLiveEdge, "the opened chat to land at the live edge");
    off();

    expect(seen).toHaveBeenCalledTimes(0);
  });

  it("stays silent for the window a cold open's own fetch filled", async () => {
    // The case the arrivals rule cannot answer from the array. `activateChatView`
    // paints on `setActive` and only then awaits `loadMessages`, so a chat whose
    // transcript is not resident paints EMPTY first: the fetched window that follows
    // is not a chat switch, and its predecessor recorded no tail to append past —
    // which is the same state the fresh-chat case above is in, and the opposite
    // answer. So the paint's CAUSE is what separates them, and both consumers of
    // the arrivals set read it: the pin, and the entry animation.
    const chat = nextChat();
    await mount(chat, []);

    const seen = vi.fn();
    const off = scroll.onReaderGesture(seen);
    loadWindow(chat, pairs(1, 3));
    await until(() => cards().length === 3, "the fetched window's cards to mount");
    await until(atLiveEdge, "the opened chat to land at the live edge");
    off();

    expect(seen).toHaveBeenCalledTimes(0);
    // The other consumer, and the reason this window is not merely unpinned: a
    // replay animates nothing, or every row of a reopened conversation fades in
    // together — the contract the paint states three lines above the arrivals scan.
    expect(cards().filter((c) => c.hasAttribute("data-chat-entry"))).toHaveLength(0);
  });
});

describe("a rail jump onto a non-resident turn", () => {
  it("keeps the pick the click set, and marks the clicked turn rather than its neighbour", async () => {
    // The end-to-end path: the click sets the pick, the jump pages history in, and
    // every prepended user turn used to fire the pin — which published a reader
    // gesture and revoked the pick before the jump had even resolved its target.
    //
    // `u3` is the SHORT card, so once the jump lands the dominance rule names `u4`.
    // That is what makes this case fail for the right reason rather than because
    // the scroll-derived mark happens to agree with the pick.
    const chat = nextChat();
    await mount(chat, pairs(4, 6), true);
    served.turns = [1, 2, 3, 4, 5, 6].map(summary);
    await rail.loadTurnRail(chat);
    await until(() => markers().length === 6, "the rail's own index to render its markers");
    vi.mocked(loadMessages).mockImplementation((chatID: string) => {
      prepend(chatID, pairs(1, 3));
      return Promise.resolve(true);
    });

    markerFor(3).click();
    // The whole path, read from its two ends: the target card is resident, and the
    // jump's own pending state has been cleared by the `finally` that renders the
    // rail one last time.
    await until(
      () => cardFor("u3") !== null && markers().every((m) => m.dataset["pending"] === undefined),
      "the paged jump to resolve",
    );
    // AFTER the jump's own scroll has settled, which is the second half of what
    // "survives the jump" means: the write is `scrollIntoView`'s, and the event it
    // produces arrives a frame later — so a pick revoked by an unrecorded landing
    // (`jumpTo`'s own marker) is revoked after the jump has otherwise resolved.
    await quiet();

    expect(vi.mocked(loadMessages)).toHaveBeenCalledTimes(1);
    const marked = markers().filter(
      (m) => m.dataset["selected"] !== undefined || m.dataset["current"] !== undefined,
    );
    expect(marked.map((m) => m.textContent)).toEqual(["3"]);
    expect(markerFor(3).getAttribute("aria-current")).toBe("true");
  });
});

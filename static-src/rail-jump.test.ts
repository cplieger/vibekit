// The rail's jump against REAL layout: one real scroller, real card boxes, the
// platform's own scroll animation and its own `scrollend`. turn-rail.test.ts drives
// the same pipeline over a scroller fake, which can assert only the calls the rail
// makes; what needs a real engine is the landing itself, the correction that follows
// a target moving under the animation, and the release when `scrollend` is taken
// away — Chromium fires one for every programmatic scroll, so the timeout path is
// unreachable until the event is suppressed.
import { describe, it, expect, vi, beforeEach } from "vitest";
import { KEY_ATTR } from "@cplieger/reactive";
import { FRAME_BUDGET_MS, testTimeoutFor } from "./__test-helpers__/frame-budget.js";
import type * as ScrollModule from "./scroll.js";
import type { Message, Session } from "./types.js";
import type { TurnSummary } from "./turn-rail.js";

/** How long one scroll is given to settle before the landing is re-measured
 *  (`turn-rail.ts` PICK_SETTLE_MS), and the correction loop's landing tolerance.
 *  Hardcoded: deriving either from the module would make these assertions agree
 *  with whatever the module believes. */
const SETTLE_MS = 1200;
const TOLERANCE_PX = 8;

// The two seams worth watching, wrapped so each call carries a TIME. The real
// module is spread in and both wrappers call through, so the behaviour under test
// stays the platform's — this is an instrument, not a fake.
const { probe } = vi.hoisted(() => ({
  probe: {
    /** Every absolute landing the jump asked for, with the behavior and the clock. */
    landings: [] as { px: number; behavior: string; at: number }[],
    /** When the epoch was released, which is the operation's own last act. */
    releases: [] as number[],
    reset(): void {
      this.landings.length = 0;
      this.releases.length = 0;
    },
  },
}));

vi.mock("./scroll.js", async (importOriginal) => {
  const real = await importOriginal<typeof ScrollModule>();
  return {
    ...real,
    scrollToOffset: (px: number, behavior: ScrollBehavior): void => {
      probe.landings.push({ px, behavior, at: Date.now() });
      real.scrollToOffset(px, behavior);
    },
    endSelfScroll: (): void => {
      probe.releases.push(Date.now());
      real.endSelfScroll();
    },
  };
});
// The session-wide index is the rail's own fetch, and the pagination door is a
// network read. Both are staged; the sequencing around them is what is under test.
vi.mock("./api-client.js", () => ({ apiGet: vi.fn() }));
vi.mock("./store-load.js", () => ({ loadMessages: vi.fn(), loadList: vi.fn() }));

// The DOM the scroll controller resolves at import, nested the way the page nests
// it: the rail mounts in the positioned OUTER wrapper, `#messages-wrap` is the
// scroller, and `#messages` holds the turn cards.
const outer = document.createElement("div");
outer.id = "messages-wrap-outer";
outer.style.cssText = "position:relative";
const wrap = document.createElement("div");
wrap.id = "messages-wrap";
// `overflow-anchor: none` is production's (css/13-messages.css) and it is
// load-bearing: without it Chromium's own scroll anchoring moves `scrollTop` when a
// card above the reader changes height, which is the number every case here reads.
wrap.style.cssText = "height:300px;overflow-y:auto;overflow-anchor:none;position:relative";
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
// Browser Mode serves no CSS, so the scene declares the boxes the stylesheet would:
// `.turn-rail` takes its height from `position: absolute; inset-block` in
// production, and a track with no box holds one marker.
const style = document.createElement("style");
style.textContent = ".turn-rail{position:absolute;inset-block-start:0;block-size:400px}";
document.head.appendChild(style);

const store = await import("./store.js");
const scroll = await import("./scroll.js");
const rail = await import("./turn-rail.js");
const { apiGet } = await import("./api-client.js");
const { loadMessages } = await import("./store-load.js");

rail.mountTurnRail(outer);

const MINUTE = 60_000;
/** The reading line, which the SCROLLER owns (`clientHeight / 3`). Read through it
 *  rather than written down, because a landing measured against any other line
 *  would agree with no other consumer of the rail. */
const readingLine = (): number => scroll.readingLineOffset();

function summary(n: number): TurnSummary {
  return { id: `u${String(n)}`, n, outcome: "completed", ts: n * MINUTE };
}

function message(id: string): Message {
  return { id, role: "user", ts: 1, content: `prompt ${id}` } as Message;
}

function session(id: string, msgs: Message[], hasMore: boolean, turnOffset: number): Session {
  return {
    id,
    name: id,
    messages: msgs,
    message_count: msgs.length,
    has_more: hasMore,
    turn_offset: turnOffset,
    thinking: false,
    working_label: "",
  } as unknown as Session;
}

/** A resident turn card, keyed the way the transcript keys one: the id of the turn's
 *  OPENING message, which is what the rail joins on. */
function card(n: number, px: number): HTMLElement {
  const e = document.createElement("div");
  e.className = "turn";
  e.setAttribute(KEY_ATTR, `u${String(n)}`);
  e.style.blockSize = `${String(px)}px`;
  messagesEl.appendChild(e);
  return e;
}

function markerFor(n: number): HTMLButtonElement {
  const hit = [...document.querySelectorAll<HTMLButtonElement>(".turn-rail > .rail-marker")].find(
    (b) => b.firstChild?.textContent === String(n),
  );
  if (hit === undefined) {
    throw new Error(`no rail marker for turn ${String(n)}`);
  }
  return hit;
}

/** A card's top measured from the scrollport's own top edge, so it compares directly
 *  against the reading line. Rects rather than `offsetTop`, for `scroll.ts`'s reason:
 *  a turn card's offsetParent is not the scroller. */
function topOnScreen(key: string): number {
  const el = messagesEl.querySelector<HTMLElement>(`[${KEY_ATTR}="${key}"]`);
  if (el === null) {
    throw new Error(`no card for ${key}`);
  }
  return el.getBoundingClientRect().top - wrap.getBoundingClientRect().top;
}

/** Poll `pred` until it holds. `what` is the sentence a failure reads as, so a
 *  timeout names the thing that never happened rather than a bare deadline. Bounded
 *  by the suite's shared frame budget, never a wall-clock guess: this browser
 *  throttles rAF to 1Hz partway through a full run. */
async function until(pred: () => boolean, what: string): Promise<void> {
  const deadline = Date.now() + FRAME_BUDGET_MS;
  while (!pred()) {
    if (Date.now() > deadline) {
      throw new Error(`timed out after ${String(FRAME_BUDGET_MS)}ms waiting for ${what}`);
    }
    await new Promise((r) => setTimeout(r, 8));
  }
}

/** Wait out the whole operation. The release is the one observable every exit
 *  reaches, including the exits that scroll nowhere. */
function released(): Promise<void> {
  return until(() => probe.releases.length > 0, "the jump to release its epoch");
}

/** Wait until the scroller has stopped moving on its own, so a position read is the
 *  landing rather than a frame the animation is passing through. Three identical
 *  reads is at least one frame with no write in it. */
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

/** Run `fn` once, on the first scroll event the jump's own animation produces —
 *  which is the moment the target can still be moved under it. */
function onFirstScroll(fn: () => void): void {
  wrap.addEventListener(
    "scroll",
    () => {
      fn();
    },
    { once: true, passive: true },
  );
}

/** Make the platform report a reduced-motion preference, which is what takes the
 *  jump's own behavior to `auto`. Chromium cannot be asked to prefer it, and
 *  `matchMedia` is an ordinary property, so the query is stubbed and every other
 *  query still answers for real. `unstubGlobals` restores it. */
function preferReducedMotion(): void {
  const real = window.matchMedia.bind(window);
  vi.stubGlobal("matchMedia", (q: string) =>
    q.includes("prefers-reduced-motion") ? ({ matches: true } as MediaQueryList) : real(q),
  );
}

/** Take `scrollend` away from the rail's own settle. The controller registered its
 *  own listener at import, so the epoch still closes on the event — what this
 *  suppresses is the rail hearing it, which leaves `PICK_SETTLE_MS` as the only
 *  release path. */
function suppressScrollend(): void {
  const real = wrap.addEventListener.bind(wrap);
  vi.spyOn(wrap, "addEventListener").mockImplementation((type, fn, opts) => {
    if (type === "scrollend") {
      return;
    }
    real(type, fn, opts);
  });
}

/** The rail pointed at a chat holding `n` turns, every one of them resident. */
async function residentChat(n: number): Promise<void> {
  vi.mocked(apiGet).mockResolvedValue({
    turns: Array.from({ length: n }, (_, i) => summary(i + 1)),
  } as never);
  await rail.loadTurnRail(`c-resident-${String(n)}`);
  for (let i = 1; i <= n; i++) {
    card(i, 200);
  }
  rail.setResidentTurns([...messagesEl.children] as HTMLElement[]);
  await until(() => document.querySelectorAll(".rail-marker").length === n, "the rail's markers");
}

beforeEach(async () => {
  rail.resetTurnRail();
  store.setActive("");
  store.setSessions([]);
  messagesEl.replaceChildren();
  wrap.scrollTop = 0;
  scroll.resetScrollState();
  probe.reset();
  vi.mocked(loadMessages).mockReset();
  await until(() => wrap.scrollTop === 0, "the scroller to be back at the top");
});

describe("the jump's own scroll", { timeout: testTimeoutFor(FRAME_BUDGET_MS) }, () => {
  it("uses the platform's animation and leaves the turn on the reading line", async () => {
    await residentChat(6);

    markerFor(4).click();
    await released();
    await quiet();

    // ONE animation, and the landing is the reader's own line rather than the top of
    // the scrollport: the same line activation reads, so the turn the rail marks and
    // the turn the reader is in cannot disagree.
    expect(probe.landings[0]?.behavior).toBe("smooth");
    expect(Math.abs(topOnScreen("u4") - readingLine())).toBeLessThanOrEqual(TOLERANCE_PX);
  });

  it("scrolls instantly when the reader prefers reduced motion", async () => {
    preferReducedMotion();
    await residentChat(6);

    markerFor(4).click();
    await released();
    await quiet();

    expect(probe.landings.map((l) => l.behavior)).toEqual(["auto"]);
    expect(Math.abs(topOnScreen("u4") - readingLine())).toBeLessThanOrEqual(TOLERANCE_PX);
  });

  it("corrects a target that moved under the animation, and starts no second one", async () => {
    // A smooth flight's target is frozen when it starts, so height arriving above
    // the target while it runs lands the reader short of the reading line — which is
    // what the correction closes, with `auto` so exactly one animation is ever in
    // flight.
    await residentChat(6);
    onFirstScroll(() => {
      const first = messagesEl.firstElementChild as HTMLElement | null;
      if (first !== null) {
        first.style.blockSize = "400px";
      }
    });

    markerFor(4).click();
    await released();
    await quiet();

    expect(probe.landings.length).toBeGreaterThan(1);
    expect(probe.landings[0]?.behavior).toBe("smooth");
    expect(probe.landings.slice(1).every((l) => l.behavior === "auto")).toBe(true);
    expect(Math.abs(topOnScreen("u4") - readingLine())).toBeLessThanOrEqual(TOLERANCE_PX);
  });

  it("corrects the landing after a page of history lands in front of the target", async () => {
    // The paged path, and the one that made the correction necessary: the cards a
    // page mounts carry an ESTIMATED height until the rows are rendered, so the
    // target's real position arrives after the scroll was aimed at it.
    const chat = "c-paged";
    vi.mocked(apiGet).mockResolvedValue({
      turns: [1, 2, 3, 4, 5, 6].map(summary),
    } as never);
    await rail.loadTurnRail(chat);
    store.setSessions([
      session(
        chat,
        [4, 5, 6].map((n) => message(`u${String(n)}`)),
        true,
        3,
      ),
    ]);
    store.setActive(chat);
    for (const n of [4, 5, 6]) {
      card(n, 200);
    }
    await until(() => document.querySelectorAll(".rail-marker").length === 6, "six markers");
    vi.mocked(loadMessages).mockImplementation((chatID: string) => {
      const s = store.get(chatID);
      if (s !== undefined) {
        s.messages = [1, 2, 3].map((n) => message(`u${String(n)}`)).concat(s.messages);
        s.message_count = s.messages.length;
        s.turn_offset = 0;
      }
      const grown: HTMLElement[] = [];
      for (const n of [3, 2, 1]) {
        const e = card(n, 100);
        messagesEl.prepend(e);
        grown.push(e);
      }
      onFirstScroll(() => {
        for (const e of grown) {
          e.style.blockSize = "200px";
        }
      });
      return Promise.resolve(true);
    });

    markerFor(3).click();
    await released();
    await quiet();

    expect(vi.mocked(loadMessages)).toHaveBeenCalledTimes(1);
    expect(probe.landings.length).toBeGreaterThan(1);
    expect(probe.landings.slice(1).every((l) => l.behavior === "auto")).toBe(true);
    expect(Math.abs(topOnScreen("u3") - readingLine())).toBeLessThanOrEqual(TOLERANCE_PX);
    // The pending state means a fetch is in flight, so the operation has to clear it.
    expect(markerFor(3).dataset["pending"]).toBeUndefined();
  });
});

describe("what releases the jump", { timeout: testTimeoutFor(FRAME_BUDGET_MS) }, () => {
  /** The window the release is measured in: from the SCROLL the settle belongs to,
   *  stamped as the rail asks for it and therefore strictly before its own timer
   *  starts. Measured from the click instead, one throttled frame (~1s) would sit
   *  inside the threshold and make the two paths indistinguishable; measured from
   *  the scroll EVENT, its delivery lag would put a real timeout release a few ms
   *  under the bound. */
  function releaseAfterScroll(): number {
    const scrolled = probe.landings[0]?.at;
    const at = probe.releases[0];
    if (scrolled === undefined || at === undefined) {
      throw new Error("the jump never scrolled, or never released its epoch");
    }
    return at - scrolled;
  }

  it("releases on the scroll's own end, without waiting the settle out", async () => {
    // Reduced motion, so the scroll is INSTANT: Chromium fires `scrollend` for one
    // of those too, which is what makes this window the event's rather than the
    // timer's whatever the frame rate is doing.
    preferReducedMotion();
    await residentChat(6);

    markerFor(4).click();
    await released();

    expect(releaseAfterScroll()).toBeLessThan(SETTLE_MS);
  });

  it("releases on the timeout when scrollend never arrives", async () => {
    // `scrollend` is not universally implemented, so the timeout is the only release
    // path on an engine without it rather than belt-and-braces. Chromium has it and
    // would satisfy the case above for free, so the event is taken away.
    preferReducedMotion();
    await residentChat(6);
    suppressScrollend();

    markerFor(4).click();
    await released();

    expect(releaseAfterScroll()).toBeGreaterThanOrEqual(SETTLE_MS);
    // Released rather than merely late: the landing still stands.
    expect(Math.abs(topOnScreen("u4") - readingLine())).toBeLessThanOrEqual(TOLERANCE_PX);
  });

  it("hands the position back when the reader wheels out of the jump", async () => {
    // The reader states a position while the jump holds one, so the pick goes: the
    // wheel ends the epoch inside scroll.ts and the scroll event that follows
    // publishes the gesture the rail cancels on.
    await residentChat(6);
    markerFor(4).click();
    await until(() => probe.landings.length > 0, "the jump's own scroll to start");

    wrap.dispatchEvent(new WheelEvent("wheel", { deltaY: -1 }));
    wrap.scrollTop = 40;

    await until(
      () => markerFor(4).dataset["selected"] === undefined,
      "the reader's gesture to revoke the pick",
    );
    expect(
      [...document.querySelectorAll<HTMLElement>(".rail-marker")].every(
        (m) => m.dataset["selected"] === undefined,
      ),
    ).toBe(true);
  });
});

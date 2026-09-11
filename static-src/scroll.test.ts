// The follow model's two compensation MODES.
//
// These are worth pinning because measuring the wrong one compensates by ZERO
// rather than by the wrong amount, which is a silent failure: a growing composer
// dock treated as content-growth reads a scrollHeight delta of 0 and moves
// nothing, which is exactly the bug the helper exists to prevent.
import { describe, it, expect, beforeEach, vi } from "vitest";

// Read for the premise test below: the regression cases only reproduce
// production while `.msg-row` really is a containment box, so the declaration is
// asserted out of the shipped stylesheet rather than copied into a fixture.
import messagesCss from "./css/13-messages.css?raw";

// The module builds its singleton against $.messages / $.messagesWrap at import,
// and reads $.scrollBottom in init.
vi.mock("./dom.js", () => ({
  $: new Proxy(
    {},
    {
      get: (_t, prop: string) => {
        const id = String(prop);
        let e = document.getElementById(id);
        if (e === null) {
          e = document.createElement(id === "scrollBottom" ? "button" : "div");
          e.id = id;
          if (id === "scrollBottom") {
            e.appendChild(document.createElement("span"));
          }
          document.body.appendChild(e);
        }
        return e;
      },
    },
  ),
}));
vi.mock("./skeleton.js", () => ({ loadMoreSkeleton: () => document.createElement("div") }));

const scroll = await import("./scroll.js");

/** The scroller is faked with writable properties. That is the right level here:
 *  the helper's contract is arithmetic over three numbers, not real layout. */
function fakeScroller(init: { scrollHeight: number; clientHeight: number; scrollTop: number }) {
  const el = scroll.getScrollEl();
  const state = { ...init };
  for (const key of ["scrollHeight", "clientHeight"] as const) {
    Object.defineProperty(el, key, {
      configurable: true,
      get: () => state[key],
    });
  }
  Object.defineProperty(el, "scrollTop", {
    configurable: true,
    get: () => state.scrollTop,
    set: (v: number) => {
      state.scrollTop = v;
    },
  });
  // `scrollTo` has to be shadowed alongside the metrics, or the faked scrollTop
  // is bypassed entirely: production reaches the live edge through
  // `scrollEl.scrollTo({top, behavior})`, and in a real browser that is the
  // platform's own method writing a real scroll position — which stays 0 on an
  // element with no overflow, so every pin assertion read 0. An instance
  // assignment shadows the prototype method (a `delete` would not); the helper
  // keeps one source of truth for the number under test.
  el.scrollTo = ((arg?: number | ScrollToOptions, y?: number): void => {
    const top = typeof arg === "number" ? y : arg?.top;
    if (top !== undefined) {
      state.scrollTop = top;
    }
  }) as typeof el.scrollTo;
  return state;
}

beforeEach(() => {
  scroll.resetScrollState();
});

describe("preserveReadingPosition", () => {
  it("runs the mutation bare while Following", () => {
    const s = fakeScroller({ scrollHeight: 1000, clientHeight: 500, scrollTop: 500 });
    scroll.setUserScrolledUp(false);
    scroll.preserveReadingPosition(() => {
      s.scrollHeight = 1400;
    }, "content-growth");
    // Following is pinned to the live edge and the auto-scroll re-pins, so there
    // is nothing to preserve and scrollTop must not be nudged.
    expect(s.scrollTop).toBe(500);
  });

  it("runs the mutation itself on that bare path", () => {
    // The assertion above holds whether or not the mutation ran at all, and the
    // bare path is the one every append while Following takes.
    fakeScroller({ scrollHeight: 1000, clientHeight: 500, scrollTop: 500 });
    scroll.setUserScrolledUp(false);
    const mutate = vi.fn();
    scroll.preserveReadingPosition(mutate, "content-growth");
    expect(mutate).toHaveBeenCalledTimes(1);
  });

  it("restores the reader by the scrollHeight delta on content growth", () => {
    const s = fakeScroller({ scrollHeight: 1000, clientHeight: 500, scrollTop: 200 });
    scroll.setUserScrolledUp(true);
    scroll.preserveReadingPosition(() => {
      s.scrollHeight = 1400;
    }, "content-growth");
    expect(s.scrollTop).toBe(600);
  });

  it("restores the reader when content SHRINKS above them", () => {
    // A fold is the shrink case, and it is the one that motivated the helper:
    // hundreds of pixels leave from above the reading position.
    const s = fakeScroller({ scrollHeight: 2000, clientHeight: 500, scrollTop: 900 });
    scroll.setUserScrolledUp(true);
    scroll.preserveReadingPosition(() => {
      s.scrollHeight = 1200;
    }, "content-growth");
    expect(s.scrollTop).toBe(100);
  });

  it("restores the reader by the clientHeight delta on viewport shrink", () => {
    const s = fakeScroller({ scrollHeight: 1000, clientHeight: 500, scrollTop: 200 });
    scroll.setUserScrolledUp(true);
    scroll.preserveReadingPosition(() => {
      s.clientHeight = 400;
    }, "viewport-shrink");
    expect(s.scrollTop).toBe(300);
  });

  // The distinction that makes two modes necessary rather than one.
  it("compensates ZERO if a viewport shrink is measured as content growth", () => {
    const s = fakeScroller({ scrollHeight: 1000, clientHeight: 500, scrollTop: 200 });
    scroll.setUserScrolledUp(true);
    scroll.preserveReadingPosition(() => {
      s.clientHeight = 400; // scrollHeight untouched
    }, "content-growth");
    expect(s.scrollTop).toBe(200);
  });

  it("leaves scrollTop alone when nothing moved", () => {
    const s = fakeScroller({ scrollHeight: 1000, clientHeight: 500, scrollTop: 200 });
    scroll.setUserScrolledUp(true);
    scroll.preserveReadingPosition(() => {
      /* no geometry change */
    }, "content-growth");
    expect(s.scrollTop).toBe(200);
  });
});

describe("deferWhileReading", () => {
  it("applies immediately while Following", () => {
    fakeScroller({ scrollHeight: 1000, clientHeight: 500, scrollTop: 500 });
    scroll.setUserScrolledUp(false);
    const fn = vi.fn();
    scroll.deferWhileReading(fn);
    expect(fn).toHaveBeenCalledTimes(1);
  });

  // Content must never disappear from above the reader through no action of
  // their own, which is exactly what a turn folding mid-read would do.
  it("queues while Reading and applies on the return to Following", () => {
    fakeScroller({ scrollHeight: 1000, clientHeight: 500, scrollTop: 200 });
    scroll.setUserScrolledUp(true);
    const fn = vi.fn();
    scroll.deferWhileReading(fn);
    expect(fn).not.toHaveBeenCalled();

    scroll.setUserScrolledUp(false);
    expect(fn).toHaveBeenCalledTimes(1);
  });

  it("preserves arrival order", () => {
    fakeScroller({ scrollHeight: 1000, clientHeight: 500, scrollTop: 200 });
    scroll.setUserScrolledUp(true);
    const order: number[] = [];
    scroll.deferWhileReading(() => order.push(1));
    scroll.deferWhileReading(() => order.push(2));
    scroll.deferWhileReading(() => order.push(3));
    scroll.setUserScrolledUp(false);
    expect(order).toEqual([1, 2, 3]);
  });

  it("does not replay a flushed queue on the next return", () => {
    fakeScroller({ scrollHeight: 1000, clientHeight: 500, scrollTop: 200 });
    scroll.setUserScrolledUp(true);
    const fn = vi.fn();
    scroll.deferWhileReading(fn);
    scroll.setUserScrolledUp(false);
    scroll.setUserScrolledUp(true);
    scroll.setUserScrolledUp(false);
    expect(fn).toHaveBeenCalledTimes(1);
  });

  it("drops the queue on a chat switch", () => {
    fakeScroller({ scrollHeight: 1000, clientHeight: 500, scrollTop: 200 });
    scroll.setUserScrolledUp(true);
    const fn = vi.fn();
    scroll.deferWhileReading(fn);
    scroll.resetScrollState();
    scroll.setUserScrolledUp(false);
    expect(fn).not.toHaveBeenCalled();
  });
});

// The reset is what a chat switch runs, so anything it leaves behind belongs to
// the previous chat. Both cases below shipped: it assigned the state field
// directly rather than going through the only writer of the control's class, and
// it nulled the pagination fields without removing the button they render.
describe("resetScrollState", () => {
  beforeEach(resetBetween);

  it("hides the resume control the reader left behind", () => {
    fakeScroller({ scrollHeight: 2000, clientHeight: 500, scrollTop: 0 });
    readerScroll();
    expect(scrollBtn.classList.contains("hidden")).toBe(false);

    scroll.resetScrollState();
    expect(scroll.readingState()).toBe("following");
    expect(scrollBtn.classList.contains("hidden")).toBe(true);
  });

  it("leaves no control that a return to the live edge cannot clear", () => {
    // The field-assignment version wrote Following into the field and left the
    // control on screen, which setState's unchanged-state guard then made
    // permanent: scrolling back to the bottom is a Following-to-Following
    // no-op, so the reader could not dismiss it by any gesture.
    const s = fakeScroller({ scrollHeight: 2000, clientHeight: 500, scrollTop: 0 });
    readerScroll();
    scroll.resetScrollState();

    s.scrollTop = 1500;
    readerScroll();
    expect(scrollBtn.classList.contains("hidden")).toBe(true);
  });

  it("removes the previous chat's Load-older-messages button", () => {
    // An unkeyed child of #messages, so the transcript's keyed reconcile never
    // touches it. Left in place it sat over the next chat still holding the
    // previous chat's callback, so pressing it fetched the wrong conversation.
    scroll.setLoadMore(() => undefined, true);
    expect(document.getElementById("load-more-indicator")).not.toBeNull();

    scroll.resetScrollState();
    expect(document.getElementById("load-more-indicator")).toBeNull();
  });
});

// A jump is the one way into Reading that can have nowhere to go. Pinned
// because the failure was silent and sticky: the resume control appeared over a
// transcript that had not moved, and since nothing scrolled, no scroll event
// arrived to put the state back.
describe("jumpTo", () => {
  // The helper below appends to the shared transcript, which the fix requires:
  // `scrollFrameRect` answers null for a disconnected element, so a target that
  // is not in the DOM takes the no-landing branch instead of the arithmetic.
  beforeEach(resetBetween);

  /** A target `top` px from the scroller's own top edge, `height` tall.
   *
   *  The rect is offset by the scroller's live rect so the frame conversion
   *  cancels it and `top` means exactly what it says, whatever the harness's own
   *  layout puts the scroller at. Appended, and answering `getClientRects`,
   *  because the landing arithmetic reads rects and treats an element with no box
   *  as one a jump cannot move the reader to. */
  function target(top: number, height: number): HTMLElement {
    const e = document.createElement("div");
    const rectAt = (): DOMRect => {
      const wrapTop = scroll.getScrollEl().getBoundingClientRect().top;
      return new DOMRect(0, wrapTop + top, 100, height);
    };
    e.getBoundingClientRect = rectAt;
    e.getClientRects = (() => [rectAt()] as unknown as DOMRectList) as typeof e.getClientRects;
    e.scrollIntoView = () => {
      /* no layout is driven here; the state decision is what is under test */
    };
    messagesEl.appendChild(e);
    return e;
  }

  it("stays Following when the transcript cannot scroll", () => {
    fakeScroller({ scrollHeight: 800, clientHeight: 800, scrollTop: 0 });
    scroll.jumpTo(target(100, 400));
    expect(scroll.readingState()).toBe("following");
  });

  it("parks the reader when the jump leaves the live edge", () => {
    fakeScroller({ scrollHeight: 4000, clientHeight: 800, scrollTop: 3200 });
    scroll.jumpTo(target(-3000, 400));
    expect(scroll.readingState()).toBe("reading");
    expect(document.getElementById("scrollBottom")?.classList.contains("hidden")).toBe(false);
  });

  // The no-landing branch, on the same geometry as the case above — which parks
  // the reader — so this passes because the target has no box and not because
  // the jump lands at the live edge. An element with no box has no landing, so
  // the jump moves the reader nowhere, and a transcript that did not move must
  // not raise a resume control over itself.
  it("keeps the reader Following when the jump target has left the DOM", () => {
    fakeScroller({ scrollHeight: 4000, clientHeight: 800, scrollTop: 3200 });
    const gone = target(-3000, 400);
    gone.remove();
    scroll.jumpTo(gone);
    expect(scroll.readingState()).toBe("following");
  });

  it("returns to Following, and hides the resume control, when the jump lands at the bottom", () => {
    fakeScroller({ scrollHeight: 4000, clientHeight: 800, scrollTop: 3200 });
    scroll.jumpTo(target(-3000, 400));
    expect(scroll.readingState()).toBe("reading");

    // The last turn: its top is past the scroller's own maximum, so the landing
    // clamps to the bottom and there is nothing to resume from.
    scroll.jumpTo(target(3100, 400));
    expect(scroll.readingState()).toBe("following");
    expect(document.getElementById("scrollBottom")?.classList.contains("hidden")).toBe(true);
  });

  // find-in-chat centres its hit, which lands the reader half a viewport higher
  // than a `start` jump would. Measuring that as `start` would call a centred hit
  // near the bottom "at the live edge" and unfreeze a reader who is not.
  it("accounts for the requested block when deciding", () => {
    fakeScroller({ scrollHeight: 4000, clientHeight: 800, scrollTop: 0 });
    const nearBottom = target(3300, 20);
    scroll.jumpTo(nearBottom, { block: "start" });
    expect(scroll.readingState()).toBe("following");

    scroll.resetScrollState();
    scroll.jumpTo(nearBottom, { block: "center" });
    expect(scroll.readingState()).toBe("reading");
  });
});

describe("readingState", () => {
  it("starts Following", () => {
    fakeScroller({ scrollHeight: 1000, clientHeight: 500, scrollTop: 500 });
    expect(scroll.readingState()).toBe("following");
  });
  it("names the state rather than exposing a boolean", () => {
    fakeScroller({ scrollHeight: 1000, clientHeight: 500, scrollTop: 200 });
    scroll.setUserScrolledUp(true);
    expect(scroll.readingState()).toBe("reading");
    scroll.setUserScrolledUp(false);
    expect(scroll.readingState()).toBe("following");
  });

  it("notifies listeners on a transition, and only on a transition", () => {
    fakeScroller({ scrollHeight: 1000, clientHeight: 500, scrollTop: 200 });
    const seen: string[] = [];
    scroll.onReadingStateChange((s) => seen.push(s));
    scroll.setUserScrolledUp(true);
    scroll.setUserScrolledUp(true);
    scroll.setUserScrolledUp(false);
    expect(seen).toEqual(["reading", "following"]);
  });
});

// ---------------------------------------------------------------------------
// The rest of the module: the event paths `init()` wires up, the streaming
// anchor, and pagination. The fake stays at the level the file already chose —
// A harness-built scroller has no real overflow, so its three numbers
// are faked and the assertions are on what the module DERIVES from them (a named
// state, a scrollTop, a fetch, a control's visibility), never on layout itself.
// ---------------------------------------------------------------------------

const messagesEl = document.getElementById("messages")!;
const scrollBtn = document.getElementById("scrollBottom")!;

/** The reader's own scroll, in the two events a device produces: the INPUT that
 *  says WHOSE scroll it is, then the `scroll` the browser delivers.
 *
 *  Both halves are load-bearing. The controller decides intent from the input, so
 *  a bare `scroll` event is the PLATFORM's shape — a `content-visibility`
 *  re-measure clamping the position — and a fixture that omits the wheel is
 *  asking for the clamp's behaviour, not the reader's. */
function readerScroll(): void {
  readerArrivedAt(scroll.getScrollEl());
}

/** Dispatch the wheel whose DIRECTION would have brought the reader to where `el`
 *  already sits, then the `scroll` event. The controller enters Reading from the aim
 *  of the input, so a fixture that seeds a position off the live edge and then fires a
 *  DOWNWARD wheel is describing a reader travelling the wrong way, and gets the
 *  platform's answer (Following) rather than the reader's. */
function readerArrivedAt(el: HTMLElement): void {
  const atEdge = el.scrollTop + el.clientHeight >= el.scrollHeight - 100;
  el.dispatchEvent(new WheelEvent("wheel", { deltaY: atEdge ? 1 : -1 }));
  el.dispatchEvent(new Event("scroll"));
}

/** The legacy touch factories, absent from the DOM lib because they are not
 *  standard. `createTouchList` is typed as the array `TouchEventInit` declares,
 *  which is the init that consumes it — an engine answering these refuses a real
 *  array in its place. */
interface LegacyTouchDoc {
  createTouch: (
    view: Window,
    target: EventTarget,
    identifier: number,
    pageX: number,
    pageY: number,
    screenX: number,
    screenY: number,
  ) => Touch;
  createTouchList: (...touches: Touch[]) => Touch[];
}

/** A touch event built through whichever construction this engine allows: the
 *  standard constructors, the legacy factories, or — where no touch API is compiled
 *  in at all — an ordinary event carrying `touches`, the one field these listeners
 *  read. Delivering touch events and letting a script BUILD one are separate
 *  capabilities, so the tier is read off the platform rather than assumed. */
function touchEvent(
  type: "touchstart" | "touchmove" | "touchend",
  target: HTMLElement,
  clientY?: number,
): Event {
  const engine = globalThis as {
    readonly TouchEvent?: typeof TouchEvent;
    readonly Touch?: typeof Touch;
  };
  const doc = document as Document & Partial<LegacyTouchDoc>;
  const init = { bubbles: true, cancelable: true };
  const ys = clientY === undefined ? [] : [clientY];

  if (
    engine.TouchEvent !== undefined &&
    doc.createTouch !== undefined &&
    doc.createTouchList !== undefined
  ) {
    const make = doc.createTouch.bind(doc);
    const touches = doc.createTouchList(...ys.map((y) => make(window, target, 1, 10, y, 10, y)));
    return new engine.TouchEvent(type, { ...init, touches });
  }
  if (engine.TouchEvent !== undefined && engine.Touch !== undefined) {
    const Point = engine.Touch;
    const touches = ys.map((y) => new Point({ identifier: 1, target, clientX: 10, clientY: y }));
    return new engine.TouchEvent(type, { ...init, touches });
  }
  const ev = new Event(type, init);
  Object.defineProperty(ev, "touches", {
    value: ys.map((y) => ({ identifier: 1, target, clientX: 10, clientY: y })),
  });
  return ev;
}

/** Drain the MutationObserver callback and the queued animation frame. Every pin
 *  writes synchronously now, so this covers the observer-driven state revalidation
 *  and the bottom pin's re-assert frames, not a deferred scroll write. */
async function settle(): Promise<void> {
  await new Promise((r) => setTimeout(r, 25));
}

/** The singleton outlives every test and only some of its state is in
 *  resetScrollState's remit: the anchor provider, the transcript's children and
 *  the pagination furniture leak into the next test otherwise. Settling last
 *  keeps a frame queued by this cleanup out of the test that follows. */
async function resetBetween(): Promise<void> {
  scroll.setAnchorProvider(null);
  messagesEl.replaceChildren();
  document.getElementById("load-more-indicator")?.remove();
  document.getElementById("load-more-skeleton")?.remove();
  scrollBtn.classList.remove("hidden");
  await settle();
}

describe("the scroll listener's reading model", () => {
  beforeEach(resetBetween);

  it("parks the reader in Reading when they scroll away from the bottom", () => {
    fakeScroller({ scrollHeight: 2000, clientHeight: 500, scrollTop: 0 });
    readerScroll();
    expect(scroll.readingState()).toBe("reading");
  });

  // The defect this file's real-layout section reproduces, in its cheapest form:
  // the SAME position, arriving with no input behind it, is the platform's own
  // clamp and may not park anyone.
  it("ignores a scroll away from the bottom that no reader input produced", () => {
    fakeScroller({ scrollHeight: 2000, clientHeight: 500, scrollTop: 0 });
    scroll.getScrollEl().dispatchEvent(new Event("scroll"));
    expect(scroll.readingState()).toBe("following");
  });

  it("returns to Following when the scroll reaches the bottom again", () => {
    const s = fakeScroller({ scrollHeight: 2000, clientHeight: 500, scrollTop: 0 });
    readerScroll();
    s.scrollTop = 1500;
    readerScroll();
    expect(scroll.readingState()).toBe("following");
  });

  // BOTTOM_TOLERANCE_PX is 100, so the last 100px still count as the live edge:
  // 1400 + 500 === 2000 - 100 exactly.
  it("counts the tolerance band as the bottom", () => {
    fakeScroller({ scrollHeight: 2000, clientHeight: 500, scrollTop: 1400 });
    readerScroll();
    expect(scroll.readingState()).toBe("following");
  });

  it("counts one pixel above the tolerance band as Reading", () => {
    fakeScroller({ scrollHeight: 2000, clientHeight: 500, scrollTop: 1399 });
    readerScroll();
    expect(scroll.readingState()).toBe("reading");
  });

  // The control window is what stops the auto-scroll fighting a scroll still in
  // flight: a wheel gesture that ends inside the tolerance band leaves the reader
  // Following, and a chunk arriving inside READER_CONTROL_MS must not yank.
  it("suppresses the auto-scroll for the debounce window after a user scroll", async () => {
    const s = fakeScroller({ scrollHeight: 2000, clientHeight: 500, scrollTop: 1500 });
    readerScroll();
    expect(scroll.readingState()).toBe("following");
    // The document GROWS with the chunk, or the pin would write 1500 either way
    // and the case could not fail: at the live edge the suppressed write and the
    // one that lands are the same number.
    s.scrollHeight = 3000;
    messagesEl.appendChild(document.createElement("div"));
    await settle();
    expect(s.scrollTop).toBe(1500);
  });
});

// ---------------------------------------------------------------------------
// WHICH INPUTS COUNT AS THE READER. Intent is decided from input rather than
// position, so every device that scrolls this box needs a listener of its own,
// and a device left out parks the reader by their own gesture. The observable
// here is the suppression: while the reader owns the scroller, a transcript that
// grows may not move it.
// ---------------------------------------------------------------------------
describe("the reader's input surfaces", () => {
  beforeEach(resetBetween);

  /** A touch at one vertical position. `touchmove` carries a position rather than a
   *  delta, so a drag is two of these and the controller keeps the previous one. */
  function touchAt(el: HTMLElement, type: "touchstart" | "touchmove", clientY: number): void {
    el.dispatchEvent(touchEvent(type, el, clientY));
  }

  /** Grow the transcript by a chunk and report where the scroller ended up. 1500 is
   *  the write suppressed, 2500 is the pin running. */
  async function chunkLands(s: { scrollHeight: number; scrollTop: number }): Promise<number> {
    s.scrollHeight = 3000;
    messagesEl.appendChild(document.createElement("div"));
    await settle();
    return s.scrollTop;
  }

  it("hands the scroller to a scrolling key", async () => {
    const s = fakeScroller({ scrollHeight: 2000, clientHeight: 500, scrollTop: 1500 });
    document.dispatchEvent(new KeyboardEvent("keydown", { key: "PageDown" }));
    expect(await chunkLands(s)).toBe(1500);
  });

  it("hands the scroller to a scrolling key under a modifier", async () => {
    // Ctrl+Home scrolls this box to the top. Dropped for its modifier, the reader
    // ends at a position carrying no fingerprint, which reads as Following and
    // gets pinned straight back down.
    const s = fakeScroller({ scrollHeight: 2000, clientHeight: 500, scrollTop: 1500 });
    document.dispatchEvent(new KeyboardEvent("keydown", { key: "Home", ctrlKey: true }));
    expect(await chunkLands(s)).toBe(1500);
  });

  it("takes no licence from a pointer moving with no thumb held", async () => {
    // Without the held-thumb guard every mouse movement over the page refreshes the
    // quiet period, and the auto-scroll is suppressed for as long as the reader's hand
    // is on the mouse.
    const s = fakeScroller({ scrollHeight: 2000, clientHeight: 500, scrollTop: 1500 });
    document.dispatchEvent(new PointerEvent("pointermove", { clientX: 10, clientY: 100 }));
    expect(await chunkLands(s)).toBe(2500);
  });

  it("parks the reader on a key that scrolls UP", () => {
    const s = fakeScroller({ scrollHeight: 2000, clientHeight: 500, scrollTop: 1500 });
    document.dispatchEvent(new KeyboardEvent("keydown", { key: "PageUp" }));
    s.scrollTop = 0;
    scroll.getScrollEl().dispatchEvent(new Event("scroll"));
    expect(scroll.readingState()).toBe("reading");
  });

  it("leaves the reader Following on a key that scrolls DOWN", () => {
    // Same displacement, opposite aim: a reader heading for the live edge is not
    // parked by whatever the layout does to them on the way.
    const s = fakeScroller({ scrollHeight: 2000, clientHeight: 500, scrollTop: 1500 });
    document.dispatchEvent(new KeyboardEvent("keydown", { key: "PageDown" }));
    s.scrollTop = 0;
    scroll.getScrollEl().dispatchEvent(new Event("scroll"));
    expect(scroll.readingState()).toBe("following");
  });

  it("reads Shift+Space as a scroll UP", () => {
    // The one direction no key spelling distinguishes: Space pages down, Shift+Space
    // pages up, and both arrive as `key: " "`.
    const s = fakeScroller({ scrollHeight: 2000, clientHeight: 500, scrollTop: 1500 });
    document.dispatchEvent(new KeyboardEvent("keydown", { key: " ", shiftKey: true }));
    s.scrollTop = 0;
    scroll.getScrollEl().dispatchEvent(new Event("scroll"));
    expect(scroll.readingState()).toBe("reading");
  });

  it("spends the reader's aim when they reach the live edge", () => {
    const s = fakeScroller({ scrollHeight: 2000, clientHeight: 500, scrollTop: 1500 });
    const el = scroll.getScrollEl();
    el.dispatchEvent(new WheelEvent("wheel", { deltaY: -1 }));
    s.scrollTop = 0;
    el.dispatchEvent(new Event("scroll"));
    expect(scroll.readingState()).toBe("reading");

    s.scrollTop = 1500;
    el.dispatchEvent(new Event("scroll"));
    expect(scroll.readingState()).toBe("following");

    // The layout now moves them off the edge with no input at all. An aim left
    // standing from the gesture they have already satisfied would re-park them here.
    s.scrollTop = 0;
    el.dispatchEvent(new Event("scroll"));
    expect(scroll.readingState()).toBe("following");
  });

  it("leaves the scroller alone for a key that does not scroll", async () => {
    const s = fakeScroller({ scrollHeight: 2000, clientHeight: 500, scrollTop: 1500 });
    document.dispatchEvent(new KeyboardEvent("keydown", { key: "k" }));
    expect(await chunkLands(s)).toBe(2500);
  });

  it("leaves the scroller alone for a scrolling key typed into a field", async () => {
    const s = fakeScroller({ scrollHeight: 2000, clientHeight: 500, scrollTop: 1500 });
    const field = document.createElement("input");
    document.body.appendChild(field);
    field.dispatchEvent(new KeyboardEvent("keydown", { key: "PageDown", bubbles: true }));
    field.remove();
    expect(await chunkLands(s)).toBe(2500);
  });

  it("parks the reader when a touch drag aims UP", () => {
    // A finger moving DOWN the screen scrolls the content UP, so the sign inverts —
    // and the aim is what parks them, which is why the momentum that follows needs no
    // direction of its own.
    const s = fakeScroller({ scrollHeight: 2000, clientHeight: 500, scrollTop: 1500 });
    const el = scroll.getScrollEl();
    touchAt(el, "touchstart", 100);
    touchAt(el, "touchmove", 260);
    s.scrollTop = 0;
    el.dispatchEvent(new Event("scroll"));
    expect(scroll.readingState()).toBe("reading");
  });

  it("leaves the reader Following when a touch drag aims DOWN", () => {
    // The same displacement, aimed the other way: everything below the reader is the
    // layout's business, and a fling toward the live edge must not park them.
    const s = fakeScroller({ scrollHeight: 2000, clientHeight: 500, scrollTop: 1500 });
    const el = scroll.getScrollEl();
    touchAt(el, "touchstart", 260);
    touchAt(el, "touchmove", 100);
    s.scrollTop = 0;
    el.dispatchEvent(new Event("scroll"));
    expect(scroll.readingState()).toBe("following");
  });

  it("keeps the momentum after a lifted finger inside the reader's window", async () => {
    // `touchend` marks but cannot aim. It is in the set for the SUPPRESSION only:
    // iOS momentum outlives the finger, and a chunk arriving under it must not yank.
    const s = fakeScroller({ scrollHeight: 2000, clientHeight: 500, scrollTop: 1500 });
    const el = scroll.getScrollEl();
    el.dispatchEvent(touchEvent("touchend", el));
    expect(await chunkLands(s)).toBe(1500);
  });

  // The premise the touch cases rest on, in the two halves they cannot check for
  // themselves: the event carries the position, and where the engine can build a
  // real one the builder did not quietly fall through to the shaped tier.
  it("builds a touch event carrying the position the listeners read", () => {
    const el = scroll.getScrollEl();
    const { TouchEvent: Ctor } = globalThis as { readonly TouchEvent?: typeof TouchEvent };
    const ev = touchEvent("touchmove", el, 42) as Event & {
      readonly touches: { readonly clientY: number }[];
    };
    expect({
      type: ev.type,
      count: ev.touches.length,
      y: ev.touches[0]?.clientY,
      real: Ctor === undefined || ev instanceof Ctor,
    }).toEqual({ type: "touchmove", count: 1, y: 42, real: true });
  });
});

describe("the End key", () => {
  beforeEach(resetBetween);

  function pressEnd(target: HTMLElement, init: KeyboardEventInit = {}): void {
    target.dispatchEvent(new KeyboardEvent("keydown", { key: "End", bubbles: true, ...init }));
  }

  // The live edge is `scrollHeight - clientHeight` — the largest scrollTop the
  // box actually has. Every landing is clamped to it, because the marker that
  // tells the scroll listener a scroll was the controller's own has to be the
  // position the browser will really reach.
  it("resumes Following and pins to the live edge", async () => {
    const s = fakeScroller({ scrollHeight: 2000, clientHeight: 500, scrollTop: 0 });
    scroll.setUserScrolledUp(true);
    pressEnd(document.body);
    expect(scroll.readingState()).toBe("following");
    await settle();
    expect(s.scrollTop).toBe(1500);
  });

  it("leaves the reader alone when they are already Following", async () => {
    const s = fakeScroller({ scrollHeight: 2000, clientHeight: 500, scrollTop: 1500 });
    scroll.setUserScrolledUp(false);
    pressEnd(document.body);
    await settle();
    expect(s.scrollTop).toBe(1500);
  });

  it("ignores End while the caret is in a text field", () => {
    fakeScroller({ scrollHeight: 2000, clientHeight: 500, scrollTop: 0 });
    const input = document.createElement("input");
    document.body.appendChild(input);
    scroll.setUserScrolledUp(true);
    pressEnd(input);
    input.remove();
    expect(scroll.readingState()).toBe("reading");
  });

  it("ignores End while the caret is in a contenteditable", () => {
    fakeScroller({ scrollHeight: 2000, clientHeight: 500, scrollTop: 0 });
    const box = document.createElement("div");
    box.contentEditable = "true";
    document.body.appendChild(box);
    scroll.setUserScrolledUp(true);
    pressEnd(box);
    box.remove();
    expect(scroll.readingState()).toBe("reading");
  });

  // The tag test is anchored at both ends: a custom element whose name merely
  // contains a field tag is not a field.
  it("treats a custom element named around a field tag as ordinary", () => {
    fakeScroller({ scrollHeight: 2000, clientHeight: 500, scrollTop: 0 });
    const before = document.createElement("my-input");
    const after = document.createElement("input-box");
    document.body.append(before, after);
    scroll.setUserScrolledUp(true);
    pressEnd(before);
    const resumedForSuffix = scroll.readingState();
    scroll.setUserScrolledUp(true);
    pressEnd(after);
    const resumedForPrefix = scroll.readingState();
    before.remove();
    after.remove();
    expect([resumedForSuffix, resumedForPrefix]).toEqual(["following", "following"]);
  });

  it("ignores End with Ctrl held", () => {
    fakeScroller({ scrollHeight: 2000, clientHeight: 500, scrollTop: 0 });
    scroll.setUserScrolledUp(true);
    pressEnd(document.body, { ctrlKey: true });
    expect(scroll.readingState()).toBe("reading");
  });

  it("ignores End with Meta held", () => {
    fakeScroller({ scrollHeight: 2000, clientHeight: 500, scrollTop: 0 });
    scroll.setUserScrolledUp(true);
    pressEnd(document.body, { metaKey: true });
    expect(scroll.readingState()).toBe("reading");
  });

  it("ignores End with Alt held", () => {
    fakeScroller({ scrollHeight: 2000, clientHeight: 500, scrollTop: 0 });
    scroll.setUserScrolledUp(true);
    pressEnd(document.body, { altKey: true });
    expect(scroll.readingState()).toBe("reading");
  });

  it("ignores a key that is not End", () => {
    fakeScroller({ scrollHeight: 2000, clientHeight: 500, scrollTop: 0 });
    scroll.setUserScrolledUp(true);
    document.body.dispatchEvent(new KeyboardEvent("keydown", { key: "Home", bubbles: true }));
    expect(scroll.readingState()).toBe("reading");
  });
});

describe("the resume control", () => {
  beforeEach(resetBetween);

  it("is shown while Reading and hidden on the return to Following", () => {
    fakeScroller({ scrollHeight: 2000, clientHeight: 500, scrollTop: 0 });
    scroll.setUserScrolledUp(true);
    const shownWhileReading = scrollBtn.classList.contains("hidden");
    scroll.setUserScrolledUp(false);
    expect([shownWhileReading, scrollBtn.classList.contains("hidden")]).toEqual([false, true]);
  });

  it("returns the reader to the live edge when clicked", async () => {
    const s = fakeScroller({ scrollHeight: 2000, clientHeight: 500, scrollTop: 0 });
    scroll.setUserScrolledUp(true);
    scrollBtn.click();
    expect(scroll.readingState()).toBe("following");
    await settle();
    expect(s.scrollTop).toBe(1500);
  });

  it("carries the label the caller sets", () => {
    scroll.setResumeLabel("3 new blocks");
    expect(scrollBtn.querySelector("span")?.textContent).toBe("3 new blocks");
  });
});

describe("scrollToBottom", () => {
  beforeEach(resetBetween);

  it("returns to Following and jumps to the live edge on the next frame", async () => {
    const s = fakeScroller({ scrollHeight: 2000, clientHeight: 500, scrollTop: 0 });
    scroll.setUserScrolledUp(true);
    scroll.scrollToBottom();
    expect(scroll.readingState()).toBe("following");
    await settle();
    expect(s.scrollTop).toBe(1500);
  });

  it("hides the resume control once the jump has landed", async () => {
    fakeScroller({ scrollHeight: 2000, clientHeight: 500, scrollTop: 0 });
    scroll.setUserScrolledUp(true);
    scroll.scrollToBottom();
    await settle();
    expect(scrollBtn.classList.contains("hidden")).toBe(true);
  });
});

describe("the streaming auto-scroll", () => {
  beforeEach(resetBetween);

  // The anchored pin is measured under REAL LAYOUT below ("the streaming pin
  // through a containment-bounded row" and "the anchor's coordinate space"): it
  // reads the anchor's rect against the scroller's, and a fake whose `scrollTop`
  // moves no box double-counts the second pin of a burst.

  it("pins to the document bottom when no anchor is offered", async () => {
    const s = fakeScroller({ scrollHeight: 2000, clientHeight: 500, scrollTop: 100 });
    messagesEl.appendChild(document.createElement("div"));
    await settle();
    expect(s.scrollTop).toBe(1500);
  });

  it("does not move the reader while they are Reading", async () => {
    const s = fakeScroller({ scrollHeight: 2000, clientHeight: 500, scrollTop: 100 });
    scroll.setUserScrolledUp(true);
    messagesEl.appendChild(document.createElement("div"));
    await settle();
    expect(s.scrollTop).toBe(100);
  });

  // The resume control is deliberately not asserted here: setState is its only
  // owner, so hidden ⇔ Following, and the auto-scroll only ever runs while
  // already Following. "the resume control > is shown while Reading and hidden
  // on the return to Following" pins that invariant by driving the real
  // transition instead.

  // One frame per burst of mutations, but the guard has to re-arm or the second
  // chunk of a stream never scrolls.
  it("re-arms the frame guard so the next chunk scrolls too", async () => {
    const s = fakeScroller({ scrollHeight: 2000, clientHeight: 500, scrollTop: 0 });
    messagesEl.appendChild(document.createElement("div"));
    await settle();
    const afterFirst = s.scrollTop;
    s.scrollHeight = 3000;
    messagesEl.appendChild(document.createElement("div"));
    await settle();
    expect([afterFirst, s.scrollTop]).toEqual([1500, 2500]);
  });
});

describe("pagination", () => {
  beforeEach(resetBetween);

  it("fetches older messages when the reader scrolls near the top", () => {
    const load = vi.fn();
    fakeScroller({ scrollHeight: 2000, clientHeight: 500, scrollTop: 0 });
    scroll.setLoadMore(load, true);
    readerScroll();
    expect(load).toHaveBeenCalledTimes(1);
  });

  // LOAD_MORE_THRESHOLD_PX is 100 and the scroll listener does not force.
  it("does not fetch while the reader is below the threshold", () => {
    const load = vi.fn();
    fakeScroller({ scrollHeight: 2000, clientHeight: 500, scrollTop: 100 });
    scroll.setLoadMore(load, true);
    readerScroll();
    expect(load).not.toHaveBeenCalled();
  });

  it("does not fetch when the server said there is nothing older", () => {
    const load = vi.fn();
    fakeScroller({ scrollHeight: 2000, clientHeight: 500, scrollTop: 0 });
    scroll.setLoadMore(load, false);
    readerScroll();
    expect(load).not.toHaveBeenCalled();
  });

  it("does not start a second fetch while one is in flight", () => {
    const load = vi.fn();
    fakeScroller({ scrollHeight: 2000, clientHeight: 500, scrollTop: 0 });
    scroll.setLoadMore(load, true);
    readerScroll();
    readerScroll();
    expect(load).toHaveBeenCalledTimes(1);
  });

  it("swaps the load-more button for the skeleton while fetching", () => {
    fakeScroller({ scrollHeight: 2000, clientHeight: 500, scrollTop: 0 });
    scroll.setLoadMore(() => undefined, true);
    expect(document.getElementById("load-more-indicator")).not.toBeNull();
    readerScroll();
    expect(document.getElementById("load-more-indicator")).toBeNull();
    expect(document.getElementById("load-more-skeleton")).not.toBeNull();
  });

  // The whole point of the module: the older page lands ABOVE the reader, so the
  // scroller has to give back exactly the height it gained.
  it("keeps the reader's position when the older page lands above them", async () => {
    const s = fakeScroller({ scrollHeight: 1000, clientHeight: 500, scrollTop: 0 });
    scroll.setLoadMore(() => {
      // The real caller fetches, prepends the page, and drops the skeleton — all
      // after maybeLoadMore has returned and its observer is watching.
      setTimeout(() => {
        messagesEl.prepend(document.createElement("div"));
        document.getElementById("load-more-skeleton")?.remove();
        s.scrollHeight = 1400;
      }, 0);
    }, true);
    readerScroll();
    await settle();
    expect(s.scrollTop).toBe(400);
  });

  it("clears the in-flight flag once the page has landed", async () => {
    const s = fakeScroller({ scrollHeight: 1000, clientHeight: 500, scrollTop: 0 });
    const load = vi.fn(() => {
      setTimeout(() => {
        document.getElementById("load-more-skeleton")?.remove();
        s.scrollHeight = 1400;
      }, 0);
    });
    scroll.setLoadMore(load, true);
    readerScroll();
    await settle();
    s.scrollTop = 0;
    readerScroll();
    expect(load).toHaveBeenCalledTimes(2);
  });

  it("offers a button that fetches whatever the reader's position", () => {
    const load = vi.fn();
    fakeScroller({ scrollHeight: 2000, clientHeight: 500, scrollTop: 900 });
    scroll.setLoadMore(load, true);
    document.getElementById("load-more-indicator")!.click();
    expect(load).toHaveBeenCalledTimes(1);
  });

  it("drops the button when there is nothing older left", () => {
    fakeScroller({ scrollHeight: 2000, clientHeight: 500, scrollTop: 0 });
    scroll.setLoadMore(() => undefined, true);
    scroll.setLoadMore(() => undefined, false);
    expect(document.getElementById("load-more-indicator")).toBeNull();
  });

  it("drops the button when the caller withdraws the fetcher", () => {
    fakeScroller({ scrollHeight: 2000, clientHeight: 500, scrollTop: 0 });
    scroll.setLoadMore(() => undefined, true);
    scroll.setLoadMore(null, true);
    expect(document.getElementById("load-more-indicator")).toBeNull();
  });

  it("keeps one button across repeated wiring", () => {
    fakeScroller({ scrollHeight: 2000, clientHeight: 500, scrollTop: 0 });
    scroll.setLoadMore(() => undefined, true);
    scroll.setLoadMore(() => undefined, true);
    expect(document.querySelectorAll("#load-more-indicator")).toHaveLength(1);
  });

  // A pagination pass belongs to the chat that started it. Its completion signal
  // does not: the skeleton is a global element id, and the caller drops it the
  // moment ITS fetch resolves, whichever chat the reader has moved to by then.
  // So the pass has to be disarmed when the scroller changes hands, or the
  // compensation lands on a transcript whose height it was never measured
  // against.
  it("takes the previous chat's loading skeleton down with it", () => {
    fakeScroller({ scrollHeight: 5000, clientHeight: 500, scrollTop: 50 });
    scroll.setLoadMore(() => undefined, true);
    readerScroll();
    expect(document.getElementById("load-more-skeleton")).not.toBeNull();

    scroll.resetScrollState();
    expect(document.getElementById("load-more-skeleton")).toBeNull();
  });

  it("does not compensate the next chat for the previous chat's page", async () => {
    const s = fakeScroller({ scrollHeight: 5000, clientHeight: 500, scrollTop: 50 });
    scroll.setLoadMore(() => undefined, true);
    readerScroll();

    // The reader switches chats with the fetch still in flight.
    scroll.resetScrollState();

    // The incoming chat is shorter, and the reader scrolls up into it. Reading is
    // the state that makes the damage stick: the streaming auto-scroll re-pins a
    // Following reader on the very next mutation, and returns early for this one.
    s.scrollHeight = 1000;
    s.scrollTop = 300;
    readerScroll();
    expect(scroll.readingState()).toBe("reading");

    // Now the OUTGOING chat's fetch resolves and drops the skeleton without
    // asking which chat is on screen. Read as this pass completing, it charged
    // the reader 300 + (1000 - 5000) and threw them to the top of a conversation
    // they had deliberately parked in.
    document.getElementById("load-more-skeleton")?.remove();
    await settle();
    expect(s.scrollTop).toBe(300);
  });
});

describe("fillViewport", () => {
  beforeEach(resetBetween);

  // Folding can starve pagination: a transcript shorter than its viewport fires
  // no scroll event, so nothing ever asks for the next page. The fetch is forced
  // here, which is why a reader parked at the threshold still gets one.
  it("fetches while the transcript is not taller than the viewport plus the tolerance", () => {
    const load = vi.fn();
    fakeScroller({ scrollHeight: 600, clientHeight: 500, scrollTop: 100 });
    scroll.setLoadMore(load, true);
    scroll.fillViewport();
    expect(load).toHaveBeenCalledTimes(1);
  });

  it("does nothing once the transcript overflows", () => {
    const load = vi.fn();
    fakeScroller({ scrollHeight: 2000, clientHeight: 500, scrollTop: 0 });
    scroll.setLoadMore(load, true);
    scroll.fillViewport();
    expect(load).not.toHaveBeenCalled();
  });

  it("does nothing when there is nothing older", () => {
    const load = vi.fn();
    fakeScroller({ scrollHeight: 400, clientHeight: 500, scrollTop: 0 });
    scroll.setLoadMore(load, false);
    scroll.fillViewport();
    expect(load).not.toHaveBeenCalled();
  });

  it("does nothing while a fetch is in flight", () => {
    const load = vi.fn();
    fakeScroller({ scrollHeight: 400, clientHeight: 500, scrollTop: 0 });
    scroll.setLoadMore(load, true);
    scroll.fillViewport();
    scroll.fillViewport();
    expect(load).toHaveBeenCalledTimes(1);
  });
});

// ---------------------------------------------------------------------------
// REAL LAYOUT. This section is the one that must not use `fakeScroller`, and
// the reason is mechanical rather than stylistic: the fake shadows `scrollTo`
// with a plain assignment to its own `scrollTop` number, which fires no `scroll`
// event. The defect below lives entirely in that event — the controller's own
// pin re-entering its own listener — so a faked scroller reports every one of
// these as passing while the feature is broken in the browser.
//
// So the scroller here is a real overflowing box, the writes are the platform's
// own, and the events are the ones a reader would produce.
// ---------------------------------------------------------------------------

/** Undo the fake and give the singleton's element real overflow. */
function realScroller(): HTMLElement {
  const wrap = scroll.getScrollEl();
  // `Reflect.deleteProperty` rather than `delete`: the keys are computed, and
  // the point is to drop `fakeScroller`'s redefinitions so the platform's own
  // metrics and `scrollTo` come back.
  for (const key of ["scrollHeight", "clientHeight", "scrollTop", "scrollTo"]) {
    Reflect.deleteProperty(wrap, key);
  }
  // `scrollbar-gutter: stable` and `overflow-anchor: none` are both shipped
  // declarations (css/13-messages.css) that this section MEASURES rather than
  // decorates: the gutter is what a scrollbar press aims at, and without the
  // anchoring off Chromium restores a clamped position itself, which is the
  // platform doing the controller's job and hiding whether it works.
  wrap.style.cssText =
    "height:400px;overflow-y:auto;position:relative;scrollbar-gutter:stable;overflow-anchor:none;";
  if (messagesEl.parentElement !== wrap) {
    wrap.appendChild(messagesEl);
  }
  messagesEl.replaceChildren();
  wrap.scrollTop = 0;
  return wrap;
}

/** A block with real height, appended to the transcript. */
function block(px: number, className = ""): HTMLElement {
  const d = document.createElement("div");
  if (className !== "") {
    d.className = className;
  }
  d.style.cssText = `height:${String(px)}px;`;
  messagesEl.appendChild(d);
  return d;
}

/** The production shape of a live top-level bubble: a `.msg-row` wrapper holding
 *  the `.message.assistant.streaming` element the block dispatcher registers as
 *  the anchor. Returns the CHILD, which is what `getLiveAnchor` hands over.
 *
 *  The row's declarations are `.msg-row`'s own (css/13-messages.css), inline so
 *  the scene needs no stylesheet, and `content-visibility` is the load-bearing
 *  one: it implies `contain: layout paint style`, and `contain: paint` makes the
 *  row a containing block — which is where an offsetParent walk stops. */
function containedRow(px: number): HTMLElement {
  const row = document.createElement("div");
  row.className = "msg-row";
  row.style.cssText =
    "display:flex;align-items:flex-end;flex-shrink:0;" +
    "content-visibility:auto;contain-intrinsic-size:auto 3rem;";
  const child = document.createElement("div");
  child.className = "message assistant streaming";
  child.style.cssText = `height:${String(px)}px;width:100%;`;
  row.appendChild(child);
  messagesEl.appendChild(row);
  return child;
}

/** Every scrollTop this controller WRITES, in order, still performing the write.
 *
 *  A landing assertion cannot see an INTERMEDIATE position, and the defect below
 *  is intermediate by construction: it writes 0, and the next frame — or the next
 *  block seal, which empties the anchor slot and falls the target through to the
 *  document bottom — writes the right number again. The reader sees a flicker
 *  that the final scrollTop agrees with.
 *
 *  Delegates to the platform's own method, captured by `bind` before the instance
 *  property shadows it, so the scroller really moves and the events the listener
 *  reads are really fired. `realScroller`'s delete loop already drops `scrollTo`,
 *  so this comes off with the rest of the shadowing. */
function recordWrites(wrap: HTMLElement): number[] {
  const writes: number[] = [];
  const platform = wrap.scrollTo.bind(wrap);
  wrap.scrollTo = ((arg?: number | ScrollToOptions, y?: number): void => {
    if (typeof arg === "number") {
      writes.push(y ?? 0);
      platform(arg, y ?? 0);
      return;
    }
    if (arg?.top !== undefined) {
      writes.push(arg.top);
    }
    platform(arg ?? {});
  }) as typeof wrap.scrollTo;
  return writes;
}

/** How many of those writes were the top of the transcript. Zero is the
 *  assertion in every case below: none of these scenes has a legitimate follow
 *  target of 0, so a single one is the defect. */
function zeroWrites(writes: readonly number[]): number {
  return writes.filter((top) => top === 0).length;
}

/** Longer than `settle()`: a real scroll event is delivered on its own turn,
 *  after the frame the write happened in. The argument is for a wait that has to
 *  outlast a named piece of choreography (a transition, a settle window). */
async function land(ms = 120): Promise<void> {
  await new Promise((r) => setTimeout(r, ms));
}

/** Move the scroller AS THE READER: the input event that says whose scroll it is,
 *  then the position they reach. Assigning `scrollTop` alone is the shape the
 *  PLATFORM produces (a `content-visibility` clamp), which the controller
 *  deliberately refuses to read as intent. */
function readerScrollTo(wrap: HTMLElement, top: number): void {
  wrap.dispatchEvent(new WheelEvent("wheel", { deltaY: top < wrap.scrollTop ? -1 : 1 }));
  wrap.scrollTop = top;
}

/** Both real-layout blocks start the same way: no anchor, a real overflowing
 *  box, an empty transcript, and the reader Following at the top. */
async function realLayoutReset(): Promise<void> {
  scroll.setAnchorProvider(null);
  realScroller();
  await land();
  scroll.resetScrollState();
}

/** Park the reader with a real gesture pair: down to the bottom, then back to
 *  the top. Two writes rather than one, so the state passes through Following
 *  and the park is the listener's own verdict rather than a seeded field. */
async function park(wrap: HTMLElement): Promise<void> {
  readerScrollTo(wrap, wrap.scrollHeight - wrap.clientHeight);
  await land();
  readerScrollTo(wrap, 0);
  await land();
}

/** The three facts one failure of a resume has to name: where the click landed,
 *  whether the reader is following again, and whether the control took itself
 *  down. */
function landing(wrap: HTMLElement): {
  scrollTop: number;
  state: string;
  hidden: boolean;
} {
  return {
    scrollTop: wrap.scrollTop,
    state: scroll.readingState(),
    hidden: scrollBtn.classList.contains("hidden"),
  };
}

describe("a large tool card below the streaming block", () => {
  beforeEach(realLayoutReset);

  it("keeps Following when the pin lands far from the document bottom", async () => {
    // The reported failure: the agent streams a sentence, a 900px tool card
    // renders below it, and the anchored pin lands 850px above the bottom. The
    // controller used to read its own landing through `isAtBottom` and declare
    // the reader Reading, which is the state that switches the auto-scroll off.
    block(1500);
    const streaming = block(200, "message assistant streaming");
    block(900);
    scroll.setAnchorProvider(() => streaming);
    await land();

    streaming.appendChild(document.createTextNode("a streamed chunk"));
    await land();

    const wrap = scroll.getScrollEl();
    expect(scroll.readingState()).toBe("following");
    expect(wrap.scrollTop).toBe(1350);
    expect(wrap.scrollHeight - wrap.clientHeight).toBe(2200);
  });

  it("keeps Following when the anchor sits in a collapsed disclosure", async () => {
    // A COLLAPSED disclosure holding a live text block, built here by hand: no
    // transcript surface streams into one any more (a delegate's own blocks are
    // dropped, and a folded box is one the store already holds something after), so
    // this is the geometry rule rather than a live shape. `height: 0` + `overflow:
    // hidden` clips the block without removing it from layout, so it still reports the
    // offsets the pin arithmetic reads — offsets that overflow a document the box
    // contributes no height to.
    block(1500);
    const box = block(0);
    box.style.cssText = "height:0;overflow:hidden;";
    const streaming = document.createElement("div");
    streaming.className = "message assistant streaming";
    streaming.style.cssText = "height:300px;";
    box.appendChild(streaming);
    block(900);
    scroll.setAnchorProvider(() => streaming);
    await land();

    streaming.appendChild(document.createTextNode("a streamed chunk"));
    await land();

    expect(scroll.readingState()).toBe("following");
  });

  it("still parks the reader in Reading when THEY scroll up", async () => {
    // The controller ignores a scroll no input produced, so the gesture the state
    // exists for has to keep working over the same DOM.
    block(1500);
    const streaming = block(200, "message assistant streaming");
    block(900);
    scroll.setAnchorProvider(() => streaming);
    await land();

    readerScrollTo(scroll.getScrollEl(), 200);
    await land();
    expect(scroll.readingState()).toBe("reading");
  });

  it("keeps Following when a pagination pass compensates its own height", async () => {
    // The completion observer gives back the height the older page added above
    // the reader. Written straight to `scrollTop` it recorded no marker, so the
    // listener read the controller's own compensation as a reader gesture — and
    // at an anchored pin `isAtBottom` is false, so it parked the reader, put the
    // resume control back on screen and switched the anchor re-pin off. The
    // damage is the chunk AFTER it: the transcript stops following.
    const wrap = realScroller();
    block(1500);
    const streaming = block(200, "message assistant streaming");
    block(900);
    scroll.setAnchorProvider(() => streaming);
    await land();

    scroll.setLoadMore(() => {
      // What the real caller does: fetch, prepend the page, drop the skeleton —
      // all after maybeLoadMore has returned and its observer is watching.
      setTimeout(() => {
        const page = document.createElement("div");
        page.style.cssText = "height:600px;";
        messagesEl.prepend(page);
        document.getElementById("load-more-skeleton")?.remove();
      }, 0);
    }, true);
    await land();

    // The button forces the fetch whatever the reader's position, which is what
    // keeps the anchored pin — the geometry the defect needs — in place.
    document.getElementById("load-more-indicator")!.click();
    await land();

    streaming.style.height = "400px";
    streaming.appendChild(document.createTextNode("the chunk after the page landed"));
    await land();

    // 600 + 1500 + 400 - 400 + 50, short of a 3000 maximum. Parked, the reader
    // stays where the compensation left them at 1950 and the growth passes them by.
    expect({ scrollTop: wrap.scrollTop, state: scroll.readingState() }).toEqual({
      scrollTop: 2150,
      state: "following",
    });
  });

  it("re-pins on every chunk rather than once per debounce window", async () => {
    // The self-scroll also used to arm the user-scroll debounce, throttling the
    // pin to one landing every 150ms for as long as a turn streamed.
    block(1500);
    const streaming = block(200, "message assistant streaming");
    block(900);
    scroll.setAnchorProvider(() => streaming);
    await land();

    const wrap = scroll.getScrollEl();
    streaming.style.height = "400px";
    streaming.appendChild(document.createTextNode("chunk one"));
    await land();
    const first = wrap.scrollTop;
    streaming.style.height = "600px";
    streaming.appendChild(document.createTextNode("chunk two"));
    await land();
    // Two distinct pin positions, not two landings on a clamped maximum: the
    // 900px card below keeps both pins short of the document's end.
    expect([first, wrap.scrollTop]).toEqual([1550, 1750]);
  });
});

// A gesture landing inside BOTTOM_TOLERANCE_PX parks the reader, and content
// arriving afterwards leaves them where they asked to be.
//
// Real layout, and this section cannot be written any other way: `fakeScroller`
// shadows `scrollTo` with an assignment to its own number and fires no scroll
// event, so the derivation under test never runs at all.
describe("a deliberate upward gesture inside the bottom tolerance", () => {
  beforeEach(realLayoutReset);

  /** Following at the live edge of a real overflowing box, put there by the
   *  auto-scroll rather than by any input — so the wheel below is the only
   *  gesture in the scene. 3000px of content in a 400px viewport, so the maximum
   *  is 2600 and a 100px gesture lands exactly ON the tolerance. */
  async function atTheLiveEdge(): Promise<HTMLElement> {
    const wrap = realScroller();
    block(3000);
    await land();
    expect({ at: wrap.scrollTop, state: scroll.readingState() }).toEqual({
      at: 2600,
      state: "following",
    });
    return wrap;
  }

  /** Wheel up `px` in `notches`, as a device delivers it: the input event that
   *  carries the aim, then the position it reaches. `scrollTo` with an explicit
   *  `instant` rather than an assignment, because the shipped scroller declares
   *  `scroll-behavior: smooth` and an assignment there only starts an animation. */
  async function wheelUp(wrap: HTMLElement, px: number, notches: number): Promise<void> {
    const step = px / notches;
    for (let n = 0; n < notches; n++) {
      wrap.dispatchEvent(new WheelEvent("wheel", { deltaY: -step }));
      wrap.scrollTo({ top: wrap.scrollTop - step, behavior: "instant" });
      await land();
    }
  }

  it("parks the reader on a 100px gesture and shows the resume control", async () => {
    const wrap = await atTheLiveEdge();

    await wheelUp(wrap, 100, 2);

    // 2500 against a 2600 maximum: `isAtBottom` still answers true here, which is
    // correct for auto-follow and must not decide this.
    expect(landing(wrap)).toEqual({ scrollTop: 2500, state: "reading", hidden: false });
  });

  it("holds that position across content mutations", async () => {
    const wrap = await atTheLiveEdge();
    await wheelUp(wrap, 100, 2);
    // Past READER_CONTROL_MS, so the debounce is not what holds the reader here —
    // the state is. Inside it every mutation is suppressed anyway and the case
    // would pass with the fix absent.
    await land(400);

    const positions: number[] = [];
    for (let m = 0; m < 6; m++) {
      block(200);
      await land();
      positions.push(wrap.scrollTop);
    }

    // Every mutation grows the document BELOW the reader, so a reader who owns the
    // scroller does not move at all. Parked in Following instead, the pin walks
    // them down to each new maximum and ends pinned at the bottom.
    expect({ positions, ...landing(wrap) }).toEqual({
      positions: [2500, 2500, 2500, 2500, 2500, 2500],
      scrollTop: 2500,
      state: "reading",
      hidden: false,
    });
  });

  it("keeps Following when a positional write with no input lands inside the band", async () => {
    // The other side of the same branch, and the defect this fix must not
    // reintroduce: a `content-visibility` re-measure clamps `scrollTop` with
    // nothing behind it, and reading THAT as a park latched the auto-scroll off
    // for a whole session. The park is gated on the reader's own input, never on
    // the position — so the same landing with no wheel in front of it stays
    // Following. `land(400)` first, or the licence would be missing for the
    // uninteresting reason.
    const wrap = await atTheLiveEdge();
    await land(400);

    wrap.scrollTo({ top: 2500, behavior: "instant" });
    await land();

    expect({ at: wrap.scrollTop, state: scroll.readingState() }).toEqual({
      at: 2500,
      state: "following",
    });
  });

  it("releases a parked reader once their gesture window has expired", async () => {
    // The park needs a LIVE gesture, not a remembered one: past READER_CONTROL_MS
    // the reader is no longer working the scroller, so a bare positional write
    // inside the band promotes them and spends the aim. Without that conjunct the
    // aim would park every later in-band event for the rest of the session.
    const wrap = await atTheLiveEdge();
    await wheelUp(wrap, 100, 2);
    expect(scroll.readingState()).toBe("reading");
    await land(400);

    wrap.scrollTo({ top: 2540, behavior: "instant" });
    await land();

    expect({ at: wrap.scrollTop, state: scroll.readingState() }).toEqual({
      at: 2540,
      state: "following",
    });
  });
});

// The reported failure: while the reader sat at the bottom of a streaming reply,
// the transcript snapped to the very top of turn 1 for one or more frames and
// then snapped back, over and over.
//
// The follow target was measured with `offsetTop`, which is relative to the
// anchor's `offsetParent` — and a top-level bubble's offsetParent is its own
// `.msg-row`, not the scroller, because `content-visibility: auto` on that row
// implies `contain: paint` and a paint-containing box is a containing block. So
// `offsetTop` read 0 however far down the transcript the live block sat, and the
// target resolved to scrollTop 0. The snap BACK is the same bug's other half: a
// block seal empties the anchor slot (`clearLiveAnchor`), the target falls
// through to `scrollHeight`, and a turn shaped prose → tool → prose alternates
// between the two several times. Both writes go through `scrollSelfTo`, so the
// listener excused each one and the reader was never parked — there was no state
// change to interrupt the flicker.
//
// Every case here asserts the WRITE SEQUENCE as well as the landing, because the
// defect is an intermediate position the final scrollTop agrees with.
describe("the streaming pin through a containment-bounded row", () => {
  beforeEach(realLayoutReset);

  it("still declares content-visibility on .msg-row", () => {
    // Green before and after the fix, on purpose: it is what stops the three
    // cases below from silently ceasing to reproduce production if the
    // declaration ever moves. Read out of the stylesheet, never copied here.
    const rule = /^\.msg-row\s*\{[^}]*\}/m.exec(messagesCss);
    expect(rule, "the .msg-row rule is missing from css/13-messages.css").not.toBeNull();
    expect(rule?.[0]).toContain("content-visibility");
  });

  it("follows the live block through a containment-bounded row", async () => {
    // 1350 is the number the uncontained sibling case already asserts ("keeps
    // Following when the pin lands far from the document bottom"), and that is
    // the point: wrapping the anchor in its production row must not move the pin.
    const wrap = realScroller();
    block(1500);
    const streaming = containedRow(200);
    block(900);
    scroll.setAnchorProvider(() => streaming);
    const writes = recordWrites(wrap);
    await land();

    streaming.appendChild(document.createTextNode("a streamed chunk"));
    await land();

    expect({
      scrollTop: wrap.scrollTop,
      state: scroll.readingState(),
      zeros: zeroWrites(writes),
      max: wrap.scrollHeight - wrap.clientHeight,
    }).toEqual({ scrollTop: 1350, state: "following", zeros: 0, max: 2200 });
  });

  it("never writes an intermediate 0 while a turn streams through several blocks", async () => {
    // The seal/re-register alternation the reader actually saw: anchor
    // registered, then the slot emptied at a block boundary, then a second
    // bubble takes it. Each transition is a chance to write the top of the
    // transcript, and the bottom-anchored position has to survive all of them.
    const wrap = realScroller();
    block(1500);
    const first = containedRow(200);
    const tail = block(900);
    let live: HTMLElement | null = first;
    scroll.setAnchorProvider(() => live);
    const writes = recordWrites(wrap);
    await land();

    first.appendChild(document.createTextNode("prose"));
    await land();
    const onFirst = wrap.scrollTop;

    // The seal: no top-level bubble is live, so the target is the document
    // bottom for as long as the slot stays empty. 1500 + 200 + 1000 - 400.
    live = null;
    tail.style.height = "1000px";
    await land();
    const sealed = wrap.scrollTop;

    // The next block registers its own bubble, with tall evidence below it so
    // the new pin is short of the maximum rather than coincidentally equal to it.
    const second = containedRow(300);
    block(900);
    live = second;
    await land();
    second.appendChild(document.createTextNode("more prose"));
    await land();

    expect({
      onFirst,
      sealed,
      onSecond: wrap.scrollTop,
      state: scroll.readingState(),
      zeros: zeroWrites(writes),
    }).toEqual({
      onFirst: 1350,
      sealed: 2300,
      onSecond: 2650,
      state: "following",
      zeros: 0,
    });
  });

  it("falls back to the document bottom when the anchor has left the DOM", async () => {
    // The seventh producer of the same write: a detached anchor reports
    // offsetTop 0, offsetHeight 0 and offsetParent null, so the old arithmetic
    // resolved to the top of the transcript here too. An element with no box has
    // no position to follow, which is the same answer as having no anchor.
    const wrap = realScroller();
    block(1500);
    const streaming = containedRow(200);
    block(900);
    scroll.setAnchorProvider(() => streaming);
    const writes = recordWrites(wrap);
    await land();

    streaming.remove();
    block(100);
    await land();

    expect({
      scrollTop: wrap.scrollTop,
      max: wrap.scrollHeight - wrap.clientHeight,
      state: scroll.readingState(),
      zeros: zeroWrites(writes),
    }).toEqual({
      scrollTop: wrap.scrollHeight - wrap.clientHeight,
      max: wrap.scrollHeight - wrap.clientHeight,
      state: "following",
      zeros: 0,
    });
  });
});

// The anchor in its PRODUCTION wrapper. Every fixture above appends the bubble
// straight into the transcript column, where the scroller is the offsetParent and an
// offsetTop walk is right by accident; production seats it in a `.msg-row`, whose
// `content-visibility: auto` (13-messages.css) makes the row that offsetParent.
describe("the anchor's coordinate space", () => {
  beforeEach(realLayoutReset);

  /** The shipped `.msg-row` declarations that decide this geometry; the
   *  containment is what moves the offsetParent onto the row. */
  function msgRow(): HTMLElement {
    const row = block(0);
    row.style.cssText =
      "display:flex;align-items:flex-end;flex-shrink:0;content-visibility:auto;contain-intrinsic-size:auto 3rem;";
    return row;
  }

  /** The live bubble as `mountText` seats it: inside the row, and positioned,
   *  which is the shape the shipped `.message` rule gives it. */
  function bubbleIn(row: HTMLElement, px: number): HTMLElement {
    const streaming = document.createElement("div");
    streaming.className = "message assistant streaming";
    streaming.style.cssText = `position:relative;height:${String(px)}px;width:100%;`;
    row.appendChild(streaming);
    return streaming;
  }

  // The fixture's own premise, so a layout change that hands the bubble a
  // different offsetParent turns the case below into one that cannot fail rather
  // than leaving it green for the wrong reason.
  it("seats the bubble in a row that is its offsetParent", () => {
    const row = msgRow();
    expect(bubbleIn(row, 200).offsetParent).toBe(row);
  });

  it("keeps Following when the anchor above the fold asks for a negative scrollTop", async () => {
    // The other edge of the same arithmetic, and the one no case reached: an anchor within
    // one viewport of the document's top makes the pin's target NEGATIVE. Three things the
    // fixture has to carry or it cannot fail. The anchor's bottom must sit above
    // `clientHeight − BOTTOM_TOLERANCE_PX / 2`, or the target is positive. The write has
    // to come from a MUTATION rather than a resume, because `pinLiveEdgeNow` clamps before
    // it calls through while `autoScrollIfAnchored` does not. And the reader must be at a
    // NON-ZERO scrollTop: at 0 the clamped write moves nothing, so no scroll event is
    // delivered and there is no marker comparison left to get wrong.
    const wrap = scroll.getScrollEl();
    const streaming = bubbleIn(msgRow(), 200);
    block(3000);
    scroll.setAnchorProvider(() => streaming);
    await land();

    // A gesture landing inside the tolerance band keeps Following, which is how a reader
    // gets to the live edge while the anchor still owes a negative pin. Long enough for
    // the reader's own control window (READER_CONTROL_MS, 300) to expire, or the
    // mutation's write is suppressed and the case never reaches the arithmetic.
    readerScrollTo(wrap, wrap.scrollHeight - wrap.clientHeight);
    await land(400);
    expect({ at: wrap.scrollTop, state: scroll.readingState() }).toEqual({
      at: 2800,
      state: "following",
    });

    streaming.appendChild(document.createTextNode("a streamed chunk"));
    await land();

    // −150 asked for (a 200px anchor bottom, a 400px viewport, half a 100px band), so 0
    // is the reachable landing. Both halves are the assertion: an unclamped landing gives
    // the repair a target it can never reach, and a 2800px drop the reader did not ask
    // for must still leave them Following, because no input produced it.
    expect({ scrollTop: wrap.scrollTop, state: scroll.readingState() }).toEqual({
      scrollTop: 0,
      state: "following",
    });
  });

  it("pins the anchor's own bottom, not its offset inside its row", async () => {
    block(1500);
    const streaming = bubbleIn(msgRow(), 200);
    block(900);
    scroll.setAnchorProvider(() => streaming);
    await land();

    streaming.appendChild(document.createTextNode("a streamed chunk"));
    await land();

    // The contract in the reader's own units. A gap rather than a scrollTop, so
    // the c-v render the pin itself triggers cannot make the expected number a
    // function of which frame settled last.
    const wrap = scroll.getScrollEl();
    const gap = wrap.getBoundingClientRect().bottom - streaming.getBoundingClientRect().bottom;
    expect({ gap, state: scroll.readingState() }).toEqual({ gap: 50, state: "following" });
  });
});

// The reported failure: "the scroll to bottom button showed '108 new blocks', I
// clicked it, and instead of scrolling to the bottom it scrolled to the start of
// the last output message." The click's landing is short by exactly the height
// that arrives AFTER the click — the fold batch the resume itself flushes, or the
// turn still streaming — and the reader is left parked with the control back on
// screen, which is why the wrong position sticks.
//
// The control's own handler is what these drive (`scrollBtn.click()`, never
// `scroll.resume()`), because the defect is composed of two mechanisms and one of
// them lives in the scroll listener the click's write feeds.
describe("the resume control's landing", () => {
  beforeEach(realLayoutReset);

  it("lands at the true bottom when the resume's own flush grows the transcript", async () => {
    // `applyFoldPass` wraps every fold, unfold and body mount in
    // `deferWhileReading`, so a reader who parked mid-turn has the whole batch
    // waiting on their return — and `setState("following")` flushes it on the
    // line before the bottom is measured. Those transitions are ANIMATED
    // (`--fold-slide`), so the height the flush adds arrives over the next
    // 300-420ms, after any target computed at click time.
    const wrap = realScroller();
    block(3000);
    const late = block(0);
    late.style.cssText = "block-size:0;overflow:hidden;transition:block-size 300ms linear;";
    await land();

    await park(wrap);
    expect(scroll.readingState()).toBe("reading");

    scroll.deferWhileReading(() => {
      late.style.blockSize = "2000px";
    });
    scrollBtn.click();
    await land(900);

    expect(landing(wrap)).toEqual({
      scrollTop: wrap.scrollHeight - wrap.clientHeight,
      state: "following",
      hidden: true,
    });
  });

  it("lands at the true bottom when the turn keeps streaming through the click", async () => {
    const wrap = realScroller();
    block(3000);
    const tail = block(200);
    await land();

    await park(wrap);
    expect(scroll.readingState()).toBe("reading");

    scrollBtn.click();
    await land(60);
    tail.style.height = "2200px";
    await land(900);

    expect(landing(wrap)).toEqual({
      scrollTop: wrap.scrollHeight - wrap.clientHeight,
      state: "following",
      hidden: true,
    });
  });

  it("hands the scroller back when the reader scrolls during the settle window", async () => {
    // The pin holds the bottom for a bounded window, and a gesture inside it is
    // the reader overruling their own click. It must stop the pass rather than
    // drag them back.
    const wrap = realScroller();
    block(3000);
    await land();

    await park(wrap);
    scrollBtn.click();
    await land(60);
    readerScrollTo(wrap, 200);
    await land(400);

    expect({ state: scroll.readingState(), scrollTop: wrap.scrollTop }).toEqual({
      state: "reading",
      scrollTop: 200,
    });
  });

  it("does not drag an incoming view to the bottom with the outgoing chat's pass", async () => {
    // `attach` restores the incoming view's own saved position, and a pass still
    // running for the chat being parked would overwrite it on the next frame.
    // `readingState: "following"` is load-bearing rather than incidental: under
    // "reading" the pass's own state guard would stop it, so the case would pass
    // with the cancellation deleted.
    const wrap = realScroller();
    block(3000);
    await land();

    await park(wrap);
    scrollBtn.click();
    await land(60);
    scroll.attach({ el: messagesEl, scrollTop: 300, readingState: "following" });
    await land(400);

    expect(wrap.scrollTop).toBe(300);
  });
});

// The other side of that window: while it runs it must hold the position
// Following MEANS, not a different one. A pass re-asserting the document maximum
// switched the anchor re-pin off for its whole duration, so for up to 700ms after
// every sent turn (`buildTurn` calls scrollToBottom for a turn the user sent) the
// reader was parked BELOW the sentence being written whenever a plan card, an
// event row or a second message sat under it. Nothing re-asked the question at
// the deadline either — `autoScrollIfAnchored` runs only from the two observers —
// so the correction waited for the next mutation and arrived as a jump.
//
// Every case here asserts a LANDING POSITION against a measured maximum, so a
// pass writing the maximum and a pass writing the follow target are
// distinguishable rather than coincidentally equal.
describe("the bottom pin's settle window", () => {
  beforeEach(realLayoutReset);

  /** The reachable shape: a live text block with tall evidence BELOW it, so the
   *  anchor's pin sits far above the document bottom. Same geometry as "keeps
   *  Following when the pin lands far from the document bottom" — anchorTop is
   *  1500 + 200 - 400 + 50 = 1350 against a maximum of 2200. */
  function pinScene(): { wrap: HTMLElement; streaming: HTMLElement } {
    const wrap = realScroller();
    block(1500);
    const streaming = block(200, "message assistant streaming");
    block(900);
    scroll.setAnchorProvider(() => streaming);
    return { wrap, streaming };
  }

  it("pins the live text block, not the document bottom, while the window runs", async () => {
    const { wrap, streaming } = pinScene();
    await land();

    // What `buildTurn` does for a turn the user just sent.
    scroll.scrollToBottom();
    await land(60);
    const opened = wrap.scrollTop;

    streaming.appendChild(document.createTextNode("a streamed chunk"));
    await land(60);
    const afterChunk = wrap.scrollTop;

    await land(300);
    const held = wrap.scrollTop;

    expect({ opened, afterChunk, held, max: wrap.scrollHeight - wrap.clientHeight }).toEqual({
      opened: 1350,
      afterChunk: 1350,
      held: 1350,
      max: 2200,
    });
  });

  it("lands the resume click on the live text block too", async () => {
    // The other producer of a pin pass, and the one whose landing this changed:
    // `resume` is reachable only from Reading, and a reader resuming into a live
    // turn has an anchor registered, so the click now lands where Following means
    // rather than at the document maximum. That is the same position
    // `autoScrollIfAnchored` would write on the next chunk, and it still puts the
    // live block's BOTTOM at the viewport bottom rather than its start — so it is
    // not a return of the short landing the window was added for.
    const { wrap } = pinScene();
    await land();

    await park(wrap);
    expect(scroll.readingState()).toBe("reading");

    scrollBtn.click();
    await land(300);

    expect(landing(wrap)).toEqual({ scrollTop: 1350, state: "following", hidden: true });
  });

  it("does not move the reader when the window closes", async () => {
    const { wrap, streaming } = pinScene();
    await land();

    scroll.scrollToBottom();
    await land(300);
    const inside = wrap.scrollTop;
    // Past PIN_SETTLE_MS, so the pass is dead and the anchor re-pin owns the next
    // frame. The chunk is what makes there BE a next frame: autoScrollIfAnchored
    // runs from the two observers and nothing else, so with no mutation after the
    // deadline nothing re-asks the question and the hand-off never happens.
    await land(500);
    streaming.appendChild(document.createTextNode("a chunk past the deadline"));
    await land(60);
    const handedOff = wrap.scrollTop;

    // Equal is the assertion: the hand-off between the two writers has to move
    // the reader nowhere. A window holding the maximum reads 2200 then 1350 —
    // the 850px jump the reader saw as a snap.
    expect({ inside, handedOff }).toEqual({ inside: 1350, handedOff: 1350 });
  });

  it("keeps following the anchor for growth arriving inside the window", async () => {
    const { wrap, streaming } = pinScene();
    await land();

    scroll.scrollToBottom();

    streaming.style.height = "400px";
    await land(60);
    const grown = wrap.scrollTop;

    streaming.style.height = "600px";
    await land(60);
    const grownAgain = wrap.scrollTop;

    // Both landings are short of their own maximum (2400, then 2600), which is
    // what the window exists to cover — the growth arrives after the click.
    expect({ grown, grownAgain }).toEqual({ grown: 1550, grownAgain: 1750 });
  });

  it("yields to an aimless reader move that lands inside the bottom tolerance", async () => {
    // The one input that marks the reader's window without aiming it: `touchend`
    // does not end a touch scroll, and the iOS momentum that outlives the finger
    // carries a position with no direction attached. That keeps the state Following
    // inside BOTTOM_TOLERANCE_PX, so the debounce is the only thing that can hand
    // the pass back — with an AIMED gesture the state guard stops the pass instead
    // and the debounce could be deleted with this still green.
    const wrap = realScroller();
    block(3000);
    await land();

    await park(wrap);
    scrollBtn.click();
    await land(60);

    wrap.dispatchEvent(touchEvent("touchend", wrap));
    wrap.scrollTo({ top: 2560, behavior: "instant" });
    await land(400);

    expect({ scrollTop: wrap.scrollTop, state: scroll.readingState() }).toEqual({
      scrollTop: 2560,
      state: "following",
    });
  });

  it("parks the reader on an upward gesture that lands inside the tolerance", async () => {
    // 40px up off the resume's own landing: a deliberate gesture of any size is a
    // gesture, so the reader is parked here rather than promoted back to Following
    // for being inside the band — and the pass has to leave them where they asked
    // either way.
    const wrap = realScroller();
    block(3000);
    await land();

    await park(wrap);
    scrollBtn.click();
    await land(60);

    readerScrollTo(wrap, 2560);
    await land(400);

    expect({ scrollTop: wrap.scrollTop, state: scroll.readingState() }).toEqual({
      scrollTop: 2560,
      state: "reading",
    });
  });

  it("leaves a jump landing at the live edge where it landed", async () => {
    // The landing is within BOTTOM_TOLERANCE_PX of the maximum, so the jump keeps
    // the reader Following and the pass's state check cannot stop it. The next
    // frame would overwrite the landing and abort the smooth scroll with it.
    const wrap = realScroller();
    block(3000);
    const target = block(200);
    block(250);
    await land();

    await park(wrap);
    scrollBtn.click();
    await land(60);

    scroll.jumpTo(target, { block: "start" });
    await land(500);

    expect({ scrollTop: wrap.scrollTop, state: scroll.readingState() }).toEqual({
      scrollTop: target.offsetTop,
      state: "following",
    });
  });

  it("keeps a jump's in-band landing Following when the reader's aim still stands", async () => {
    // The jump is the reader's NEW stated position, so it spends the aim that took
    // them up: no resume click intervenes here, and a jump arms the gesture window
    // itself, so an aim left standing would make the jump's own scroll event park
    // the reader and raise the control over a landing 50px from the maximum.
    //
    // `instant` is the rail's own spelling (turn-rail.ts) and the reason this case
    // is deterministic: a smooth flight is still travelling when the assertion
    // reads it, so waiting one out would pin the animation rather than the state.
    const wrap = realScroller();
    block(3000);
    const target = block(200);
    block(250);
    await land();

    await park(wrap);
    expect(scroll.readingState()).toBe("reading");

    scroll.jumpTo(target, { block: "start", behavior: "instant" });
    await land();

    expect(landing(wrap)).toEqual({
      scrollTop: target.offsetTop,
      state: "following",
      hidden: true,
    });
  });
});

// ---------------------------------------------------------------------------
// The STREAMING follow write's licence, and that it is re-read where it is spent.
//
// `autoScrollIfAnchored` decides on the frame it RUNS in and writes in the next
// one. `queuePinFrame` re-reads its conditions in that next frame and this one did
// not, so a revocation arriving inside the gap was ignored: the reader's own
// wheel, or the transcript being handed to another chat.
//
// Real layout, because the whole subject is a write landing a frame after the
// gesture that should have stopped it: `fakeScroller` shadows `scrollTo` with an
// assignment to its own number and fires no scroll event, so under it the
// reader's gesture never reaches the listener and both cases pass either way.
// ---------------------------------------------------------------------------

describe("the streaming follow write's licence", () => {
  beforeEach(realLayoutReset);

  /** Following at the live edge with the pin pass DEAD, which is the state that
   *  licenses a follow write and nothing else. `land(800)` outlasts
   *  PIN_SETTLE_MS, so a write observed afterwards is this pass's own rather than
   *  the pin's re-assert. */
  async function followingAtEdge(): Promise<HTMLElement> {
    const wrap = realScroller();
    block(3000);
    await land();
    scroll.scrollToBottom();
    await land(800);
    return wrap;
  }

  it("does not take the reader back when they scroll up inside its frame", async () => {
    const wrap = await followingAtEdge();
    expect(scroll.readingState()).toBe("following");

    // The mutation licenses ONE write, and awaiting a microtask is the
    // MutationObserver's own delivery — so the frame is queued and has not run.
    block(400);
    await Promise.resolve();
    readerScrollTo(wrap, 0);
    await land(300);

    expect({ scrollTop: wrap.scrollTop, state: scroll.readingState() }).toEqual({
      scrollTop: 0,
      state: "reading",
    });
  });

  it("does not land on a view handed over while it was queued", async () => {
    // The parked-chat case, and the one that lets `messages-parked-views.test.ts`
    // stop waiting out frames before a gesture: `attach` restores the incoming
    // view's own Reading, so the re-read refuses a write the outgoing view
    // licensed. `readingState: "reading"` is the subject rather than the setup —
    // an incoming FOLLOWING view legitimately belongs at its live edge, so there
    // is nothing to protect there and no cancellation to add.
    const wrap = await followingAtEdge();

    block(400);
    await Promise.resolve();
    scroll.detach();
    scroll.attach({ el: messagesEl, scrollTop: 250, readingState: "reading" });
    await land(300);

    expect({ scrollTop: wrap.scrollTop, state: scroll.readingState() }).toEqual({
      scrollTop: 250,
      state: "reading",
    });
  });
});

// ---------------------------------------------------------------------------
// `onReaderGesture`: the seam for "the reader said where they want to be".
//
// TWO publishers, and the second is the one this block exists to pin: a scroll,
// and a request for the LIVE EDGE. The resume control, End, and a turn the reader
// just sent all reach the scroller through `scrollSelfTo`, whose marker the scroll
// listener consumes before it decides whether to publish — so a seam published
// from the scroll branch alone is silent for every one of them, which is what let
// the timeline rail keep its accent fill on the turn the reader had just left.
//
// Real-layout, and for a stronger reason than the block above: half the contract is
// that a scroll the CONTROLLER performed does NOT fire, and `fakeScroller` shadows
// `scrollTo` with an assignment to its own number, so it emits no scroll event at
// all — under it every case here would pass with the callbacks wired to the wrong
// branch, or to none.
// ---------------------------------------------------------------------------

describe("onReaderGesture", () => {
  beforeEach(realLayoutReset);

  it("fires for a scroll the reader performed", async () => {
    const wrap = realScroller();
    block(3000);
    await land();
    const seen = vi.fn();
    const off = scroll.onReaderGesture(seen);

    wrap.scrollTop = 1200;
    await land();
    off();

    expect(seen).toHaveBeenCalledTimes(1);
  });

  it("fires when the reader asks for the live edge", async () => {
    // The resume control. It is a gesture whose whole meaning is "take me to the
    // live edge", so a consumer holding a position the reader has now abandoned has
    // to hear it — and the scroll listener cannot say so, because this landing is
    // written through `scrollSelfTo` and excused.
    const wrap = realScroller();
    block(3000);
    await land();
    await park(wrap);
    expect(scroll.readingState()).toBe("reading");

    const seen = vi.fn();
    const off = scroll.onReaderGesture(seen);
    scrollBtn.click();
    await land(300);
    off();

    expect(wrap.scrollTop).toBe(2600);
    expect(seen).toHaveBeenCalledTimes(1);
  });

  it("fires for a turn the reader just sent", async () => {
    // `buildTurn`'s `scrollToBottom()` for a triggered turn, and the app's own
    // comment at that call site says why it counts: the reader asked for the turn,
    // so the pin takes them to it even if they were parked further up. The
    // transcript moves; anything claiming they are still where they were is wrong.
    const wrap = realScroller();
    block(3000);
    await land();
    await park(wrap);

    const seen = vi.fn();
    const off = scroll.onReaderGesture(seen);
    scroll.scrollToBottom();
    await land(300);
    off();

    expect(wrap.scrollTop).toBe(2600);
    expect(seen).toHaveBeenCalledTimes(1);
  });

  it("publishes a live-edge request once, not once per re-assert frame", async () => {
    // The pin holds its landing for PIN_SETTLE_MS by re-asserting it every frame
    // (~42 of them). The GESTURE happened once, so publishing from the frame loop
    // would hand a consumer dozens of identical revocations and make the seam
    // unusable for anything that repaints on one.
    const wrap = realScroller();
    block(3000);
    const streaming = block(200, "message assistant streaming");
    block(900);
    scroll.setAnchorProvider(() => streaming);
    await land();
    await park(wrap);

    const seen = vi.fn();
    const off = scroll.onReaderGesture(seen);
    scrollBtn.click();
    // Well past the settle window, with growth arriving inside it so the frames
    // have something to re-assert.
    await land(200);
    streaming.appendChild(document.createTextNode("a streamed chunk"));
    await land(800);
    off();

    expect(seen).toHaveBeenCalledTimes(1);
  });

  it("does not fire for an instant jump", async () => {
    // A jump is a gesture whose whole meaning is "this turn", so publishing it
    // revokes the very pick that produced it — the timeline rail sets its pick and
    // then jumps to that turn. The write is the platform's own `scrollIntoView`,
    // so `jumpTo` records where it LANDED for the listener to excuse; only an
    // instant scroll has landed by the time it returns, which is the asymmetry the
    // rail asks for by name and find-in-chat's smooth jump keeps.
    const wrap = realScroller();
    block(3000);
    const target = block(200);
    block(2000);
    await land();

    const seen = vi.fn();
    const off = scroll.onReaderGesture(seen);
    scroll.jumpTo(target, { block: "start", behavior: "instant" });
    await land(300);
    off();

    expect(wrap.scrollTop).toBe(target.offsetTop);
    expect(scroll.readingState()).toBe("reading");
    expect(seen).not.toHaveBeenCalled();
  });

  it("does not fire for the controller's own streaming re-pin", async () => {
    // The other half of the contract, and the case the seam was built for: a turn
    // streaming under a reader who has not moved writes a scroll position several
    // times a second, and none of those is the reader changing their mind. The
    // scrollTop assertion is what stops this passing because nothing scrolled.
    const wrap = realScroller();
    block(1500);
    const streaming = block(200, "message assistant streaming");
    block(900);
    scroll.setAnchorProvider(() => streaming);
    await land();
    expect(scroll.readingState()).toBe("following");

    const seen = vi.fn();
    const off = scroll.onReaderGesture(seen);
    streaming.appendChild(document.createTextNode("a streamed chunk"));
    await land(300);
    off();

    expect(wrap.scrollTop).toBe(1350);
    expect(seen).not.toHaveBeenCalled();
  });

  it("fires for a gesture that keeps the reader Following", async () => {
    // Not `onReadingStateChange`: a scroll landing inside BOTTOM_TOLERANCE_PX
    // stays Following, so a state listener hears nothing while the reader has
    // plainly acted.
    const wrap = realScroller();
    block(3000);
    await land();
    wrap.scrollTop = 2600;
    await land();
    expect(scroll.readingState()).toBe("following");

    const seen = vi.fn();
    const stateSeen = vi.fn();
    scroll.onReadingStateChange(stateSeen);
    const off = scroll.onReaderGesture(seen);

    wrap.scrollTop = 2560;
    await land();
    off();

    expect(seen).toHaveBeenCalledTimes(1);
    expect(stateSeen).not.toHaveBeenCalled();
  });

  it("stops firing once unregistered", async () => {
    const wrap = realScroller();
    block(3000);
    await land();
    const seen = vi.fn();
    const off = scroll.onReaderGesture(seen);

    wrap.scrollTop = 500;
    await land();
    off();
    wrap.scrollTop = 900;
    await land();

    expect(seen).toHaveBeenCalledTimes(1);
  });
});

// ---------------------------------------------------------------------------
// THE SCROLLBAR, which is the one input surface with no event of its own: a
// thumb drag produces no wheel and no touch, so the PRESS is the input and its
// position is the only thing separating it from a click in the transcript. Real
// layout, because the whole discrimination is a measured gutter width. The
// surface exists only where the platform RESERVES that width, so this block is
// skipped where nothing is and the one after it takes the other class.
// ---------------------------------------------------------------------------

/** Does this platform reserve a strip for the scrollbar? `scrollbar-gutter: stable`
 *  is the shipped declaration (css/13-messages.css) and it reserves nothing where
 *  the bar is an overlay, so the answer is measured on a throwaway box carrying
 *  that declaration rather than assumed from the engine. */
function reservesScrollbarGutter(): boolean {
  const probe = document.createElement("div");
  probe.style.cssText =
    "position:absolute;visibility:hidden;height:100px;width:100px;" +
    "overflow-y:auto;scrollbar-gutter:stable;";
  const tall = document.createElement("div");
  tall.style.cssText = "height:1000px;";
  probe.appendChild(tall);
  document.body.appendChild(probe);
  const gutter = probe.offsetWidth - probe.clientWidth;
  probe.remove();
  return gutter > 0;
}

describe.skipIf(!reservesScrollbarGutter())("the scrollbar as an input surface", () => {
  beforeEach(realLayoutReset);

  /** A press on the thumb: the reserved gutter's width, and the press that lands in
   *  it. `scrollbar-gutter: stable` reserves the strip whether or not a bar is
   *  drawn, which is what makes the arithmetic answerable in a test. */
  function gutterPress(wrap: HTMLElement): { gutter: number; press: () => void } {
    const gutter = wrap.offsetWidth - wrap.clientWidth;
    const x = wrap.getBoundingClientRect().right - gutter / 2;
    return {
      gutter,
      press: () => {
        // `clientY` matters: it is the origin `dragThumb` measures its travel from.
        wrap.dispatchEvent(
          new PointerEvent("pointerdown", { clientX: x, clientY: 200, bubbles: true }),
        );
      },
    };
  }

  /** A transcript that overflows, with the reader moved off the live edge by the
   *  PLATFORM rather than by any input — the auto-scroll pins to the bottom on the
   *  append, and a case asserting the bottom asserts nothing about a press.
   *
   *  The returned state is half the assertion: a positionless move must leave the
   *  reader Following, so a demotion in any case below came from the press. */
  async function driftedOffTheEdge(): Promise<HTMLElement> {
    block(1500);
    const wrap = scroll.getScrollEl();
    await land();
    wrap.scrollTop = 0;
    await land();
    expect(scroll.readingState()).toBe("following");
    return wrap;
  }

  /** Move the held thumb by `dy`, watched on the DOCUMENT because a drag that leaves
   *  the scroller still owns the bar. The thumb travels WITH the content, so this sign
   *  does not invert the way a finger's does. */
  function dragThumb(wrap: HTMLElement, dy: number): void {
    const gutter = wrap.offsetWidth - wrap.clientWidth;
    const x = wrap.getBoundingClientRect().right - gutter / 2;
    document.dispatchEvent(new PointerEvent("pointermove", { clientX: x, clientY: 200 + dy }));
  }

  it("parks the reader when the thumb is dragged UP", async () => {
    const wrap = await driftedOffTheEdge();
    const { gutter, press } = gutterPress(wrap);
    // The premise this surface needs, pinned rather than assumed: this platform
    // reserves a strip to aim at. An overlay scrollbar measures 0, and there the
    // surface is deliberately absent because a touch drag is how you scroll.
    expect(gutter).toBeGreaterThan(0);

    press();
    dragThumb(wrap, -40);
    wrap.dispatchEvent(new Event("scroll"));
    expect(scroll.readingState()).toBe("reading");
  });

  it("leaves the reader Following when the thumb is dragged DOWN", async () => {
    const wrap = await driftedOffTheEdge();
    gutterPress(wrap).press();
    dragThumb(wrap, 40);
    wrap.dispatchEvent(new Event("scroll"));
    expect(scroll.readingState()).toBe("following");
  });

  it("takes no aim from a drag that started inside the transcript", async () => {
    // A copy button, a fold header. Tracked as a thumb, the pointer's own travel
    // would park a reader who never touched the scrollbar.
    const wrap = await driftedOffTheEdge();
    wrap.dispatchEvent(
      new PointerEvent("pointerdown", {
        clientX: wrap.getBoundingClientRect().left + 10,
        bubbles: true,
      }),
    );
    dragThumb(wrap, -40);
    wrap.dispatchEvent(new Event("scroll"));
    expect(scroll.readingState()).toBe("following");
  });

  it("suppresses the auto-scroll for as long as the thumb is held", async () => {
    // Untimed, because a held thumb produces no repeat input to refresh a deadline:
    // 400ms is past READER_CONTROL_MS with the press still down. This is the half of
    // the press that survives — the aim comes from the drag, the licence from the hold.
    await driftedOffTheEdge();
    const wrap = scroll.getScrollEl();
    gutterPress(wrap).press();
    await land(400);
    const was = wrap.scrollTop;
    block(1500);
    await land();
    expect(wrap.scrollTop).toBe(was);
  });

  it("gives the scroller back when the thumb is released", async () => {
    // A release that goes unseen latches the licence on for the session.
    await driftedOffTheEdge();
    const wrap = scroll.getScrollEl();
    gutterPress(wrap).press();
    document.dispatchEvent(new PointerEvent("pointerup", { bubbles: true }));
    await land(400);
    block(1500);
    await land();
    expect(wrap.scrollTop).toBe(wrap.scrollHeight - wrap.clientHeight);
  });
});

// ---------------------------------------------------------------------------
// THE OTHER PLATFORM CLASS. A scroller reserving no strip has no thumb to aim at,
// so a press at its right edge must take no aim — `inScrollbarGutter`'s width test,
// the one clause the block above cannot reach. Portable rather than platform-gated:
// `scrollbar-width: none` reserves nothing in any engine, so the class is a property
// of the FIXTURE.
// ---------------------------------------------------------------------------
describe("the scrollbar surface a platform does not reserve", () => {
  beforeEach(realLayoutReset);

  it("takes no aim from a press at the scroller's right edge", async () => {
    const wrap = realScroller();
    wrap.style.setProperty("scrollbar-width", "none");
    block(1500);
    await land();
    wrap.scrollTop = 0;
    await land();
    expect({ gutter: wrap.offsetWidth - wrap.clientWidth, state: scroll.readingState() }).toEqual({
      gutter: 0,
      state: "following",
    });

    // The tightest witness the guard has: with the width test dropped, a press AT
    // the right edge satisfies the remaining comparison and the drag parks them.
    const { right } = wrap.getBoundingClientRect();
    wrap.dispatchEvent(
      new PointerEvent("pointerdown", { clientX: right, clientY: 200, bubbles: true }),
    );
    document.dispatchEvent(new PointerEvent("pointermove", { clientX: right, clientY: 160 }));
    wrap.dispatchEvent(new Event("scroll"));
    expect(scroll.readingState()).toBe("following");
  });
});

// ---------------------------------------------------------------------------
// EVERY PAGINATION LOOKUP IS SCOPED TO THE ATTACHED VIEW.
//
// The multiplexer keeps one `.transcript-view` per resident chat and hands the
// scroller between them (`attach`/`detach`), so a PARKED view's own pagination
// furniture — its "Load older messages" button, and the skeleton of a fetch that
// was in flight when it was parked — is DOM this controller does not own.
// `maybeLoadMore` and `updateLoadMoreIndicator` were already `viewEl`-scoped;
// `abandonLoadPass` reached for the skeleton by document id, which contradicted
// them two functions apart and took the parked view's skeleton down with it.
//
// Real elements rather than the shared transcript, because the property IS which
// element a lookup reaches: two views under `#messages`, exactly as the
// multiplexer nests them.
// ---------------------------------------------------------------------------
// ---------------------------------------------------------------------------
// THE SELF-SCROLL EPOCH: an interval in which every scroll event is the
// controller's own animation. Real layout throughout, and this section cannot be
// written any other way — `fakeScroller` shadows `scrollTo` with an assignment to
// its own number, so it produces neither an animation nor a scroll event, which
// is the whole subject.
// ---------------------------------------------------------------------------
describe("the self-scroll epoch", () => {
  /** The section's reset plus an explicit re-root, so these cases do not depend on
   *  which view an earlier block left the scroller attached to. */
  async function epochReset(): Promise<void> {
    scroll.attach({ el: messagesEl, scrollTop: 0, readingState: "following" });
    await realLayoutReset();
  }

  /** 3000px of content in the 400px scrollport and an epoch open over a landing at
   *  400. Nothing is awaited after the landing, because Chromium answers even an
   *  instant `scrollTo` with `scrollend` — which is a close, so a wait here would
   *  measure the closer rather than the epoch. */
  async function openEpochAt400(): Promise<HTMLElement> {
    const wrap = realScroller();
    block(1500);
    block(200);
    block(1300);
    await land();
    scroll.beginSelfScroll();
    scroll.scrollToOffset(400, "instant");
    return wrap;
  }

  /** Does an ordinary scroll publish a reader gesture? False while an epoch is
   *  open, so it is the observable a closer has to move. */
  async function gesturePublishes(wrap: HTMLElement): Promise<boolean> {
    const seen = vi.fn();
    const off = scroll.onReaderGesture(seen);
    wrap.scrollTop = 1200;
    await land();
    off();
    return seen.mock.calls.length > 0;
  }

  /** A page of content prepended above the reader — the shift every
   *  `preserveReadingPosition` caller declares as `content-growth`. */
  function foldIn(px: number): void {
    scroll.preserveReadingPosition(() => {
      const page = document.createElement("div");
      page.style.cssText = `height:${String(px)}px;`;
      messagesEl.prepend(page);
    }, "content-growth");
  }

  beforeEach(epochReset);

  it("publishes no gesture across the animation's own events", async () => {
    const wrap = realScroller();
    block(3000);
    await land();
    const seen = vi.fn();
    const off = scroll.onReaderGesture(seen);

    scroll.beginSelfScroll();
    scroll.scrollToOffset(400, "smooth");
    await land(1000);
    off();

    expect(wrap.scrollTop).toBe(400);
    expect(seen).not.toHaveBeenCalled();
  });

  it("hands the events back the moment the reader touches the scroller", async () => {
    const wrap = realScroller();
    block(3000);
    await land();

    scroll.beginSelfScroll();
    scroll.scrollToOffset(400, "smooth");
    const seen = vi.fn();
    const off = scroll.onReaderGesture(seen);
    await land(60);
    readerScrollTo(wrap, 1800);
    await land();
    off();

    expect(seen).toHaveBeenCalled();
  });

  it("keeps the streaming follow write out of the animation", async () => {
    const wrap = realScroller();
    block(1500);
    const streaming = block(200, "message assistant streaming");
    block(900);
    scroll.setAnchorProvider(() => streaming);
    await land();
    expect(scroll.readingState()).toBe("following");

    await park(wrap);
    // Out of the park's own READER_CONTROL_MS window, or that is what holds the
    // follow write back and the epoch's guard is never the thing under test.
    await land(350);

    // Back to the live edge, so the state stays Following and the epoch is the only
    // thing left that can stop a follow write. SMOOTH, because the flight is what
    // keeps the epoch open: Chromium answers an instant `scrollTo` with `scrollend`.
    scroll.beginSelfScroll();
    scroll.scrollToOffset(wrap.scrollHeight, "smooth");
    expect(scroll.readingState()).toBe("following");
    const writes = recordWrites(wrap);
    streaming.appendChild(document.createTextNode("a streamed chunk"));
    await land(60);
    expect(writes).toEqual([]);

    scroll.endSelfScroll();
    streaming.appendChild(document.createTextNode("the chunk after it closed"));
    await land();
    expect(writes.length).toBeGreaterThan(0);
  });

  it("stays open with nothing to close it", async () => {
    const wrap = await openEpochAt400();
    expect(await gesturePublishes(wrap)).toBe(false);
  });

  it("closes on attach", async () => {
    const wrap = await openEpochAt400();
    scroll.attach({ el: messagesEl, scrollTop: 400, readingState: "reading" });
    expect(await gesturePublishes(wrap)).toBe(true);
  });

  it("closes on detach", async () => {
    const wrap = await openEpochAt400();
    scroll.detach();
    expect(await gesturePublishes(wrap)).toBe(true);
  });

  it("closes on resetScrollState", async () => {
    const wrap = await openEpochAt400();
    scroll.resetScrollState();
    expect(await gesturePublishes(wrap)).toBe(true);
  });

  it("closes on jumpTo", async () => {
    const wrap = realScroller();
    block(1500);
    const target = block(200);
    block(1300);
    await land();
    scroll.beginSelfScroll();
    scroll.scrollToOffset(400, "instant");

    scroll.jumpTo(target, { behavior: "instant" });

    expect(await gesturePublishes(wrap)).toBe(true);
  });

  it("parks the reader, and a mutation after the epoch cannot re-pin them", async () => {
    const wrap = realScroller();
    block(1500);
    const streaming = block(200, "message assistant streaming");
    block(900);
    scroll.setAnchorProvider(() => streaming);
    await land();
    expect(scroll.readingState()).toBe("following");

    scroll.beginSelfScroll();
    scroll.scrollToOffset(500, "smooth");
    expect(scroll.readingState()).toBe("reading");

    // Past `scrollend` AND past SELF_SCROLL_MAX_MS, so the chunk below is delivered
    // with the epoch provably closed and the park is the only thing still holding
    // the reader.
    await land(1700);
    streaming.appendChild(document.createTextNode("a streamed chunk"));
    await land();

    expect({ scrollTop: wrap.scrollTop, state: scroll.readingState() }).toEqual({
      scrollTop: 500,
      state: "reading",
    });
  });

  it("holds the park when a card resizes the reader onto the live edge", async () => {
    const wrap = realScroller();
    block(500);
    const tail = block(2500);
    await land();
    readerScrollTo(wrap, 2600);
    await land();
    readerScrollTo(wrap, 500);
    await land();
    expect(scroll.readingState()).toBe("reading");
    // Out of the gesture's own READER_CONTROL_MS window, or that is what refuses the
    // release and the epoch's clause is never the thing under test.
    await land(350);

    scroll.beginSelfScroll();
    // The tail collapsing to exactly the scrollport leaves the reader at the live
    // edge by arithmetic and does NOT move `scrollTop`, so the resize seam is the
    // only path that can re-derive the state.
    tail.style.height = "400px";
    await land();
    expect(scroll.readingState()).toBe("reading");

    // The control: with the epoch closed, the same seam releases them — so the
    // assertion above is about the epoch rather than about a resize that reaches
    // nothing.
    scroll.endSelfScroll();
    tail.style.height = "401px";
    await land();
    expect(scroll.readingState()).toBe("following");
  });

  it("sets Following when the landing is the live edge", async () => {
    const wrap = realScroller();
    block(3000);
    await land();
    await park(wrap);
    expect(scroll.readingState()).toBe("reading");

    scroll.beginSelfScroll();
    scroll.scrollToOffset(2600, "instant");

    expect(landing(wrap)).toEqual({ scrollTop: 2600, state: "following", hidden: true });
  });

  it("suspends the pagination pass while it is open", async () => {
    realScroller();
    block(3000);
    const load = vi.fn();
    scroll.setLoadMore(load, true);
    await land();

    scroll.beginSelfScroll();
    scroll.scrollToOffset(0, "instant");
    await land();

    expect(load).not.toHaveBeenCalled();
  });

  it("runs the pagination pass again once it closes", async () => {
    const wrap = realScroller();
    block(3000);
    const load = vi.fn();
    scroll.setLoadMore(load, true);
    await land();
    scroll.beginSelfScroll();
    scroll.scrollToOffset(0, "instant");
    await land();

    scroll.endSelfScroll();
    wrap.dispatchEvent(new Event("scroll"));
    await land();

    expect(load).toHaveBeenCalledTimes(1);
  });

  it("still runs the forced pagination call inside it", async () => {
    realScroller();
    block(3000);
    const load = vi.fn();
    scroll.setLoadMore(load, true);
    await land();
    scroll.beginSelfScroll();
    scroll.scrollToOffset(0, "instant");
    await land();

    document.getElementById("load-more-indicator")!.click();

    expect(load).toHaveBeenCalledTimes(1);
  });

  it("compensates its TARGET when a fold lands mid-animation", async () => {
    const wrap = realScroller();
    block(3000);
    await land();

    scroll.beginSelfScroll();
    scroll.scrollToOffset(500, "smooth");
    // Mid-flight, so the live position and the target are hundreds of pixels apart
    // and the two candidate compensations cannot agree by accident.
    await land(60);
    expect(wrap.scrollTop).toBeGreaterThan(900);

    // The WRITE rather than the settled position: an interrupted smooth animation
    // gets one more frame in before it aborts, so the position lands a few px short
    // of the value the compensation asked for (measured: 668 against 700).
    const writes = recordWrites(wrap);
    foldIn(200);

    expect(writes).toEqual([700]);
  });

  it("compensates the live position when no epoch is open", async () => {
    const wrap = realScroller();
    block(3000);
    await land();
    await park(wrap);
    readerScrollTo(wrap, 500);
    await land();
    expect(scroll.readingState()).toBe("reading");

    foldIn(200);

    expect(wrap.scrollTop).toBe(700);
  });

  it("compensates the landing when a fold arrives after the animation settled", async () => {
    // The fold that lands between `scrollend` and the correction loop's first pass:
    // the epoch has closed, so the live position IS the target, and the
    // compensation has to still run rather than have been suspended for the jump.
    const wrap = realScroller();
    block(3000);
    await land();
    scroll.beginSelfScroll();
    scroll.scrollToOffset(500, "smooth");
    await land(1000);
    expect(wrap.scrollTop).toBe(500);

    foldIn(200);

    expect(wrap.scrollTop).toBe(700);
  });
});

describe("onContentResize", () => {
  beforeEach(realLayoutReset);

  it("fires from the per-child ResizeObserver and returns a working unregister", async () => {
    realScroller();
    const card = block(200);
    await land();
    const seen = vi.fn();
    const off = scroll.onContentResize(seen);

    card.style.height = "600px";
    await land();
    const fired = seen.mock.calls.length;
    expect(fired).toBeGreaterThan(0);

    off();
    card.style.height = "300px";
    await land();

    expect(seen.mock.calls.length).toBe(fired);
  });
});

describe("onAttach", () => {
  beforeEach(realLayoutReset);

  it("fires when a view takes the scroller, after the position is restored", () => {
    const wrap = realScroller();
    wrap.style.cssText = "height:400px;overflow-y:auto;position:relative;";
    const view = document.createElement("div");
    view.className = "transcript-view";
    view.style.cssText = "height:3000px;";
    messagesEl.replaceChildren(view);
    // Read INSIDE the callback: a listener re-measuring the incoming view has to see
    // the restored offset, so firing before the write would hand it the outgoing
    // view's position.
    const seen = vi.fn(() => wrap.scrollTop);
    const off = scroll.onAttach(seen);

    scroll.attach({ el: view, scrollTop: 250, readingState: "reading" });
    expect(seen).toHaveBeenCalledTimes(1);
    expect(seen.mock.results[0]?.value).toBe(250);

    off();
    scroll.attach({ el: view, scrollTop: 100, readingState: "reading" });

    expect(seen).toHaveBeenCalledTimes(1);
  });
});

describe("readingLineOffset", () => {
  it("is a third of the scrollport, from its top", () => {
    const s = fakeScroller({ scrollHeight: 3000, clientHeight: 900, scrollTop: 0 });
    expect(scroll.readingLineOffset()).toBe(300);
    s.clientHeight = 600;
    expect(scroll.readingLineOffset()).toBe(200);
  });
});

describe("pagination furniture belongs to its own view", () => {
  /** Two sibling transcript views under the scroller, and the scroller attached to
   *  the first — the shape after one chat switch. */
  function twoViews(): { parked: HTMLElement; active: HTMLElement } {
    const wrap = realScroller();
    wrap.style.cssText = "height:400px;overflow-y:auto;position:relative;";
    const parked = document.createElement("div");
    parked.className = "transcript-view";
    const active = document.createElement("div");
    active.className = "transcript-view";
    messagesEl.replaceChildren(parked, active);
    return { parked, active };
  }

  /** The skeleton a load pass in flight leaves in a view. Built by hand because
   *  what is under test is which element a REMOVAL reaches, not how one is mounted. */
  function plantSkeleton(view: HTMLElement): HTMLElement {
    const skel = document.createElement("div");
    skel.id = "load-more-skeleton";
    view.prepend(skel);
    return skel;
  }

  it("abandons only the attached view's load pass, leaving a parked view's skeleton", () => {
    const { parked, active } = twoViews();
    const parkedSkeleton = plantSkeleton(parked);
    scroll.attach({ el: active, scrollTop: 0, readingState: "following" });
    const activeSkeleton = plantSkeleton(active);

    // `resetScrollState` ends in `abandonLoadPass`, and it is the reachable door:
    // a chat switch runs it for the INCOMING view.
    scroll.resetScrollState();

    expect({
      parkedKept: parked.contains(parkedSkeleton),
      activeGone: !active.contains(activeSkeleton),
    }).toEqual({ parkedKept: true, activeGone: true });
  });

  it("leaves a parked view's Load-older-messages button alone", () => {
    // The other half of the same scoping, from the indicator's side: parking wires
    // the button through the view that owned it, and the incoming view's own reset
    // must not reach it.
    const { parked, active } = twoViews();
    scroll.attach({ el: parked, scrollTop: 0, readingState: "following" });
    scroll.setLoadMore(() => undefined, true);
    const button = parked.querySelector(`[id="load-more-indicator"]`);
    expect(button).not.toBeNull();

    scroll.attach({ el: active, scrollTop: 0, readingState: "following" });
    scroll.resetScrollState();

    expect({
      parkedKept: button !== null && parked.contains(button),
      activeHasNone: active.querySelector(`[id="load-more-indicator"]`) === null,
    }).toEqual({ parkedKept: true, activeHasNone: true });
  });
});

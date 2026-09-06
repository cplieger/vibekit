// The follow model's two compensation MODES.
//
// These are worth pinning because measuring the wrong one compensates by ZERO
// rather than by the wrong amount, which is a silent failure: a growing composer
// dock treated as content-growth reads a scrollHeight delta of 0 and moves
// nothing, which is exactly the bug the helper exists to prevent.
import { describe, it, expect, beforeEach, vi } from "vitest";

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
  /** A target at `top` relative to the scroller's own top. jsdom gives every
   *  rect zeros, so the scroller's own rect needs no faking. */
  function target(top: number, height: number): HTMLElement {
    const e = document.createElement("div");
    e.getBoundingClientRect = (() => ({ top, height })) as never;
    Object.defineProperty(e, "offsetHeight", { configurable: true, get: () => height });
    e.scrollIntoView = () => {
      /* jsdom has no layout; the state decision is what is under test */
    };
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
  const el = scroll.getScrollEl();
  el.dispatchEvent(new WheelEvent("wheel", { deltaY: 1 }));
  el.dispatchEvent(new Event("scroll"));
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

  it("keeps the scroller with the reader after the finger lifts", async () => {
    const s = fakeScroller({ scrollHeight: 2000, clientHeight: 500, scrollTop: 1500 });
    scroll.getScrollEl().dispatchEvent(new TouchEvent("touchend", { touches: [] }));
    expect(await chunkLands(s)).toBe(1500);
  });

  it("reads the momentum after a lifted finger as the reader's own scroll", () => {
    // The event that carries the reader out of the tolerance band arrives AFTER
    // `touchend` — iOS momentum outlives the finger — so without that listener this
    // is a bare scroll and the reader stays Following at a position they have left.
    const s = fakeScroller({ scrollHeight: 2000, clientHeight: 500, scrollTop: 1500 });
    const el = scroll.getScrollEl();
    el.dispatchEvent(new TouchEvent("touchend", { touches: [] }));
    s.scrollTop = 0;
    el.dispatchEvent(new Event("scroll"));
    expect(scroll.readingState()).toBe("reading");
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

  // The anchored pin is measured under REAL LAYOUT below ("the anchor's coordinate
  // space"): it reads the anchor's rect against the scroller's, and a fake whose
  // `scrollTop` moves no box double-counts the second pin of a burst.

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
  wrap.dispatchEvent(new WheelEvent("wheel", { deltaY: 1 }));
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
    // A subagent's live text block streams inside a box that is collapsed by
    // default. `height: 0` + `overflow: hidden` clips it without removing it
    // from layout, so it still reports the offsets the pin arithmetic reads —
    // offsets that overflow a document the box contributes no height to.
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

  it("yields to a reader scroll that lands inside the bottom tolerance", async () => {
    // A gesture landing within BOTTOM_TOLERANCE_PX keeps the state Following, so
    // the state alone cannot see it: the scroll debounce is what can.
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
      state: "following",
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
});

// ---------------------------------------------------------------------------
// THE SCROLLBAR, which is the one input surface with no event of its own: a
// thumb drag produces no wheel and no touch, so the PRESS is the input and its
// position is the only thing separating it from a click in the transcript. Real
// layout, because the whole discrimination is a measured gutter width.
// ---------------------------------------------------------------------------
describe("the scrollbar as an input surface", () => {
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
        wrap.dispatchEvent(new PointerEvent("pointerdown", { clientX: x, bubbles: true }));
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

  it("reads a scroll after a press on the scrollbar as the reader's own", async () => {
    const wrap = await driftedOffTheEdge();
    const { gutter, press } = gutterPress(wrap);
    // The premise this surface needs, pinned rather than assumed: this platform
    // reserves a strip to aim at. An overlay scrollbar measures 0, and there the
    // surface is deliberately absent because a touch drag is how you scroll.
    expect(gutter).toBeGreaterThan(0);

    press();
    wrap.dispatchEvent(new Event("scroll"));
    expect(scroll.readingState()).toBe("reading");
  });

  it("ignores a press inside the transcript", async () => {
    // A copy button, a fold header. Counted as the reader's, the next chunk's pin
    // arrives holding their licence and parks them.
    const wrap = await driftedOffTheEdge();
    wrap.dispatchEvent(
      new PointerEvent("pointerdown", {
        clientX: wrap.getBoundingClientRect().left + 10,
        bubbles: true,
      }),
    );
    wrap.dispatchEvent(new Event("scroll"));
    expect(scroll.readingState()).toBe("following");
  });

  it("keeps the scroller for as long as the thumb is held", async () => {
    // Untimed, because a held thumb produces no repeat input to refresh a deadline.
    // 400ms is past READER_CONTROL_MS with the press still down.
    const wrap = await driftedOffTheEdge();
    gutterPress(wrap).press();
    await land(400);
    wrap.dispatchEvent(new Event("scroll"));
    expect(scroll.readingState()).toBe("reading");
  });

  it("gives the scroller back when the thumb is released", async () => {
    // Watched on the DOCUMENT: a drag that leaves the scroller still owns the bar,
    // and a release that goes unseen latches the licence on for the session.
    const wrap = await driftedOffTheEdge();
    gutterPress(wrap).press();
    document.dispatchEvent(new PointerEvent("pointerup", { bubbles: true }));
    await land(400);
    wrap.dispatchEvent(new Event("scroll"));
    expect(scroll.readingState()).toBe("following");
  });
});

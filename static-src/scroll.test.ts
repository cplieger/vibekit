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
    scroll.getScrollEl().dispatchEvent(new Event("scroll"));
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
    scroll.getScrollEl().dispatchEvent(new Event("scroll"));
    scroll.resetScrollState();

    s.scrollTop = 1500;
    scroll.getScrollEl().dispatchEvent(new Event("scroll"));
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

/** Drain the MutationObserver callback, the queued animation frame, and a smooth
 *  scrollTo's deferred write. `behavior: "smooth"` lands on a later frame,
 *  so an assertion on scrollTop after `resume()` needs this; `behavior: "instant"`
 *  lands inside the frame. */
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
    scroll.getScrollEl().dispatchEvent(new Event("scroll"));
    expect(scroll.readingState()).toBe("reading");
  });

  it("returns to Following when the scroll reaches the bottom again", () => {
    const s = fakeScroller({ scrollHeight: 2000, clientHeight: 500, scrollTop: 0 });
    scroll.getScrollEl().dispatchEvent(new Event("scroll"));
    s.scrollTop = 1500;
    scroll.getScrollEl().dispatchEvent(new Event("scroll"));
    expect(scroll.readingState()).toBe("following");
  });

  // BOTTOM_TOLERANCE_PX is 100, so the last 100px still count as the live edge:
  // 1400 + 500 === 2000 - 100 exactly.
  it("counts the tolerance band as the bottom", () => {
    fakeScroller({ scrollHeight: 2000, clientHeight: 500, scrollTop: 1400 });
    scroll.getScrollEl().dispatchEvent(new Event("scroll"));
    expect(scroll.readingState()).toBe("following");
  });

  it("counts one pixel above the tolerance band as Reading", () => {
    fakeScroller({ scrollHeight: 2000, clientHeight: 500, scrollTop: 1399 });
    scroll.getScrollEl().dispatchEvent(new Event("scroll"));
    expect(scroll.readingState()).toBe("reading");
  });

  // The debounce is what stops the auto-scroll fighting a scroll still in
  // flight: a wheel gesture that ends inside the tolerance band leaves the
  // reader Following, and a chunk arriving in the same 150ms must not yank.
  it("suppresses the auto-scroll for the debounce window after a user scroll", async () => {
    const s = fakeScroller({ scrollHeight: 2000, clientHeight: 500, scrollTop: 1500 });
    scroll.getScrollEl().dispatchEvent(new Event("scroll"));
    expect(scroll.readingState()).toBe("following");
    messagesEl.appendChild(document.createElement("div"));
    await settle();
    expect(s.scrollTop).toBe(1500);
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

  /** An element with the layout the anchor arithmetic reads. */
  function fakeAnchor(offsetTop: number, offsetHeight: number): HTMLElement {
    const anchor = document.createElement("div");
    Object.defineProperty(anchor, "offsetTop", { configurable: true, get: () => offsetTop });
    Object.defineProperty(anchor, "offsetHeight", { configurable: true, get: () => offsetHeight });
    return anchor;
  }

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

  // The bug this exists for: a tall diff card renders BELOW the sentence being
  // streamed, and pinning to scrollHeight scrolls that sentence off the top.
  // 1000 + 200 - 500 + 100/2 puts the anchor's bottom at the viewport's bottom.
  it("puts the anchor's bottom at the viewport's bottom", async () => {
    const s = fakeScroller({ scrollHeight: 4000, clientHeight: 500, scrollTop: 0 });
    const anchor = fakeAnchor(1000, 200);
    scroll.setAnchorProvider(() => anchor);
    messagesEl.appendChild(anchor);
    await settle();
    expect(s.scrollTop).toBe(750);
  });

  it("never scrolls to a negative offset for an anchor above the fold", async () => {
    // 0 + 100 - 500 + 50 is -350, which is not a scroll position.
    const s = fakeScroller({ scrollHeight: 4000, clientHeight: 500, scrollTop: 0 });
    const anchor = fakeAnchor(0, 100);
    scroll.setAnchorProvider(() => anchor);
    messagesEl.appendChild(anchor);
    await settle();
    expect(s.scrollTop).toBe(0);
  });

  it("never scrolls past the end of the content", async () => {
    // 9000 + 100 - 500 + 50 is past a 1000px transcript, so the landing is the
    // maximum scrollTop a 500px viewport over 1000px of content has.
    const s = fakeScroller({ scrollHeight: 1000, clientHeight: 500, scrollTop: 0 });
    const anchor = fakeAnchor(9000, 100);
    scroll.setAnchorProvider(() => anchor);
    messagesEl.appendChild(anchor);
    await settle();
    expect(s.scrollTop).toBe(500);
  });

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
    scroll.getScrollEl().dispatchEvent(new Event("scroll"));
    expect(load).toHaveBeenCalledTimes(1);
  });

  // LOAD_MORE_THRESHOLD_PX is 100 and the scroll listener does not force.
  it("does not fetch while the reader is below the threshold", () => {
    const load = vi.fn();
    fakeScroller({ scrollHeight: 2000, clientHeight: 500, scrollTop: 100 });
    scroll.setLoadMore(load, true);
    scroll.getScrollEl().dispatchEvent(new Event("scroll"));
    expect(load).not.toHaveBeenCalled();
  });

  it("does not fetch when the server said there is nothing older", () => {
    const load = vi.fn();
    fakeScroller({ scrollHeight: 2000, clientHeight: 500, scrollTop: 0 });
    scroll.setLoadMore(load, false);
    scroll.getScrollEl().dispatchEvent(new Event("scroll"));
    expect(load).not.toHaveBeenCalled();
  });

  it("does not start a second fetch while one is in flight", () => {
    const load = vi.fn();
    fakeScroller({ scrollHeight: 2000, clientHeight: 500, scrollTop: 0 });
    scroll.setLoadMore(load, true);
    scroll.getScrollEl().dispatchEvent(new Event("scroll"));
    scroll.getScrollEl().dispatchEvent(new Event("scroll"));
    expect(load).toHaveBeenCalledTimes(1);
  });

  it("swaps the load-more button for the skeleton while fetching", () => {
    fakeScroller({ scrollHeight: 2000, clientHeight: 500, scrollTop: 0 });
    scroll.setLoadMore(() => undefined, true);
    expect(document.getElementById("load-more-indicator")).not.toBeNull();
    scroll.getScrollEl().dispatchEvent(new Event("scroll"));
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
    scroll.getScrollEl().dispatchEvent(new Event("scroll"));
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
    scroll.getScrollEl().dispatchEvent(new Event("scroll"));
    await settle();
    s.scrollTop = 0;
    scroll.getScrollEl().dispatchEvent(new Event("scroll"));
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
    scroll.getScrollEl().dispatchEvent(new Event("scroll"));
    expect(document.getElementById("load-more-skeleton")).not.toBeNull();

    scroll.resetScrollState();
    expect(document.getElementById("load-more-skeleton")).toBeNull();
  });

  it("does not compensate the next chat for the previous chat's page", async () => {
    const s = fakeScroller({ scrollHeight: 5000, clientHeight: 500, scrollTop: 50 });
    scroll.setLoadMore(() => undefined, true);
    scroll.getScrollEl().dispatchEvent(new Event("scroll"));

    // The reader switches chats with the fetch still in flight.
    scroll.resetScrollState();

    // The incoming chat is shorter, and the reader scrolls up into it. Reading is
    // the state that makes the damage stick: the streaming auto-scroll re-pins a
    // Following reader on the very next mutation, and returns early for this one.
    s.scrollHeight = 1000;
    s.scrollTop = 300;
    scroll.getScrollEl().dispatchEvent(new Event("scroll"));
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
describe("a large tool card below the streaming block", () => {
  /** Undo the fake and give the singleton's element real overflow. */
  function realScroller(): HTMLElement {
    const wrap = scroll.getScrollEl();
    // `Reflect.deleteProperty` rather than `delete`: the keys are computed, and
    // the point is to drop `fakeScroller`'s redefinitions so the platform's own
    // metrics and `scrollTo` come back.
    for (const key of ["scrollHeight", "clientHeight", "scrollTop", "scrollTo"]) {
      Reflect.deleteProperty(wrap, key);
    }
    wrap.style.cssText = "height:400px;overflow-y:auto;position:relative;";
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
   *  after the frame the write happened in. */
  async function land(): Promise<void> {
    await new Promise((r) => setTimeout(r, 120));
  }

  beforeEach(async () => {
    scroll.setAnchorProvider(null);
    realScroller();
    await land();
    scroll.resetScrollState();
  });

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
    // The guard excuses the controller's own landing and nothing else, so the
    // gesture the state exists for has to keep working over the same DOM.
    block(1500);
    const streaming = block(200, "message assistant streaming");
    block(900);
    scroll.setAnchorProvider(() => streaming);
    await land();

    scroll.getScrollEl().scrollTop = 200;
    await land();
    expect(scroll.readingState()).toBe("reading");
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

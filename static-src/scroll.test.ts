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

  /** An element reporting a position `frameTop` px down the scroller's own scroll
   *  frame.
   *
   *  Faked through RECTS rather than `offsetTop`, because rects are the
   *  coordinate space the anchor arithmetic reads: `offsetTop` is measured
   *  against `offsetParent`, which for a real transcript bubble is its own
   *  containment-bounded `.msg-row` and not the scroller. The rect is computed
   *  per call against the scroller's live rect and scrollTop, which is what the
   *  frame conversion undoes — so the four numbers below still mean a position in
   *  the transcript, and they are unchanged from the offsetTop era on purpose. If
   *  one of them moves, the fake is wrong. */
  function fakeAnchor(frameTop: number, height: number): HTMLElement {
    const anchor = document.createElement("div");
    const rectAt = (): DOMRect => {
      const wrap = scroll.getScrollEl();
      const top = wrap.getBoundingClientRect().top + wrap.clientTop + frameTop - wrap.scrollTop;
      return new DOMRect(0, top, 100, height);
    };
    anchor.getBoundingClientRect = rectAt;
    anchor.getClientRects = (() =>
      [rectAt()] as unknown as DOMRectList) as typeof anchor.getClientRects;
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
  wrap.scrollTop = wrap.scrollHeight - wrap.clientHeight;
  await land();
  wrap.scrollTop = 0;
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
    wrap.scrollTop = 200;
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

    wrap.scrollTop = 2560;
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
// `onReaderGesture`: the seam for "the reader said where they want to be".
//
// TWO publishers, and the second is the one this block exists to pin: a scroll,
// and a request for the LIVE EDGE. The resume control, End, and a turn the reader
// just sent all reach the scroller through `scrollSelfTo`, whose marker the scroll
// listener consumes on its early-return branch — so a seam published from the
// scroll branch alone is silent for every one of them, which is what let the
// timeline rail keep its accent fill on the turn the reader had just left.
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

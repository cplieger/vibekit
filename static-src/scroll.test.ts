// @vitest-environment happy-dom
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

/** happy-dom reports 0 for every layout metric, so the scroller is faked with
 *  writable properties. That is the right level here: the helper's contract is
 *  arithmetic over three numbers, not real layout. */
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

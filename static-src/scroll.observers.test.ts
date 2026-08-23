// @vitest-environment happy-dom
// The observers `init()` wires, and the module-scope boot.
//
// A separate file from scroll.test.ts because these paths can only be reached
// by replacing a global BEFORE the module is imported: the singleton is built at
// import and captures `ResizeObserver` and `document.readyState` there. Every
// test here therefore re-imports the module against a fresh DOM
// (`vi.resetModules()` + `await import()`), which is the same discipline
// platform.pwa.test.ts needs for its module-scope constants.
//
// happy-dom's own ResizeObserver never fires and reports 0 for every box, so
// with the real one the resize half of this module is unobservable: nothing
// models a layout change. The fake below is a recorder plus a trigger — it is
// the layout engine's role in the contract, not the module's, so faking it does
// not hide the code under test.
import { describe, it, expect, afterEach, vi } from "vitest";
import type { MockInstance } from "vitest";
import type * as ScrollModule from "./scroll.js";

// The module builds its singleton against $.messages / $.messagesWrap at import
// and reads $.scrollBottom in init; ids are created on demand so a cleared body
// yields a fresh element set for the next import.
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

class FakeResizeObserver {
  static instances: FakeResizeObserver[] = [];
  readonly targets = new Set<Element>();
  readonly observeCalls: Element[] = [];
  readonly unobserveCalls: Element[] = [];
  private readonly cb: () => void;
  constructor(cb: () => void) {
    this.cb = cb;
    FakeResizeObserver.instances.push(this);
  }
  observe(el: Element): void {
    this.targets.add(el);
    this.observeCalls.push(el);
  }
  unobserve(el: Element): void {
    this.targets.delete(el);
    this.unobserveCalls.push(el);
  }
  disconnect(): void {
    this.targets.clear();
  }
  /** What happy-dom will not do: report that a box changed. */
  fire(): void {
    this.cb();
  }
  observedCount(el: Element): number {
    return this.observeCalls.filter((e) => e === el).length;
  }
}

/** Inside the test body, never at module scope: `unstubGlobals` is on, so a
 *  stub installed at collection time is restored before the first test runs. */
function stubResizeObserver(): void {
  FakeResizeObserver.instances.length = 0;
  vi.stubGlobal("ResizeObserver", FakeResizeObserver);
}

/** happy-dom reports 0 for every layout metric, so the scroller's boxes are
 *  faked with writable properties — the same level scroll.test.ts chose. */
function fakeGeometry(
  el: HTMLElement,
  init: {
    scrollHeight: number;
    clientHeight: number;
    scrollTop: number;
    offsetWidth?: number;
    clientWidth?: number;
  },
): {
  scrollHeight: number;
  clientHeight: number;
  scrollTop: number;
  offsetWidth: number;
  clientWidth: number;
} {
  const state = { offsetWidth: 0, clientWidth: 0, ...init };
  for (const key of ["scrollHeight", "clientHeight", "offsetWidth", "clientWidth"] as const) {
    Object.defineProperty(el, key, { configurable: true, get: () => state[key] });
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

/** Drain the MutationObserver microtask, the queued animation frame, and a
 *  smooth scroll's deferred write. */
async function settle(): Promise<void> {
  await new Promise((r) => setTimeout(r, 25));
}

/** Drain the same queues without depending on `setTimeout`, which a fake-timer
 *  test has replaced. happy-dom's animation frames run on a real `setImmediate`
 *  captured at its own import, so they still land. */
async function settleFrames(): Promise<void> {
  for (let i = 0; i < 3; i++) {
    await new Promise<void>((r) => {
      requestAnimationFrame(() => {
        r();
      });
    });
  }
}

interface Harness {
  scroll: typeof ScrollModule;
  ro: FakeResizeObserver;
  messagesEl: HTMLElement;
  scrollEl: HTMLElement;
  /** Every `addEventListener` on the scroller since just before the module was
   *  imported. */
  listeners: MockInstance<typeof EventTarget.prototype.addEventListener>;
  /** A row the transcript already held when the controller was built, when the
   *  test asked for one. */
  existingRow: HTMLElement | null;
}

/** A module instance whose observers are the fakes above, against a DOM no
 *  previous instance holds a reference to. */
async function freshModule(opts: { withExistingRow?: boolean } = {}): Promise<Harness> {
  stubResizeObserver();
  document.body.replaceChildren();
  document.documentElement.style.removeProperty("--scrollbar-w");
  // Built before the import so the scroller can be spied on: the mocked registry
  // hands out whatever is already in the document under that id.
  const messages = document.createElement("div");
  messages.id = "messages";
  const wrap = document.createElement("div");
  wrap.id = "messagesWrap";
  let existingRow: HTMLElement | null = null;
  if (opts.withExistingRow === true) {
    // A transcript that already has content, which is the ordinary case on a
    // reload of an open chat.
    existingRow = document.createElement("div");
    messages.appendChild(existingRow);
  }
  document.body.append(messages, wrap);
  const listeners = vi.spyOn(wrap, "addEventListener");
  vi.resetModules();
  const scroll = await import("./scroll.js");
  const scrollEl = scroll.getScrollEl();
  expect([FakeResizeObserver.instances.length, scrollEl]).toEqual([1, wrap]);
  return {
    scroll,
    ro: FakeResizeObserver.instances[0]!,
    messagesEl: messages,
    scrollEl,
    listeners,
    existingRow,
  };
}

afterEach(() => {
  vi.useRealTimers();
});

// ---------------------------------------------------------------------------
// The scroller's own resize observer: the reserved gutter and the re-pin.
// ---------------------------------------------------------------------------
describe("the scroller's resize observer", () => {
  it("watches the scroller itself", async () => {
    // The box whose reserved gutter and viewport height everything else is
    // measured against.
    const h = await freshModule();
    expect(h.ro.targets.has(h.scrollEl)).toBe(true);
  });

  it("re-measures the reserved gutter when the scroller's box changes", async () => {
    // The width the transcript's inline-END inset gives back. It is read off the
    // real element, so a box that changes between two frames has to be read
    // again — a window listener read the pre-relayout value, which is the defect
    // the observer replaced.
    const h = await freshModule();
    expect(document.documentElement.style.getPropertyValue("--scrollbar-w")).toBe("0px");
    fakeGeometry(h.scrollEl, {
      scrollHeight: 500,
      clientHeight: 500,
      scrollTop: 0,
      offsetWidth: 1000,
      clientWidth: 985,
    });
    h.ro.fire();
    expect(document.documentElement.style.getPropertyValue("--scrollbar-w")).toBe("15px");
  });

  it("writes the gutter once across a resize storm", async () => {
    // Same width twice must cost one style invalidation, not two: a browser-zoom
    // or overlay-scrollbar change moves this box repeatedly and every write
    // invalidates the whole document's style.
    const h = await freshModule();
    fakeGeometry(h.scrollEl, {
      scrollHeight: 500,
      clientHeight: 500,
      scrollTop: 0,
      offsetWidth: 1000,
      clientWidth: 985,
    });
    const setProperty = vi.spyOn(document.documentElement.style, "setProperty");
    h.ro.fire();
    h.ro.fire();
    h.ro.fire();
    expect(setProperty.mock.calls.filter((c) => c[0] === "--scrollbar-w")).toHaveLength(1);
  });

  it("re-pins the reader to the live edge when the box shrinks under them", async () => {
    // The composer growing or the shell panel opening leaves the reader Following
    // but no longer AT the edge, and no DOM mutation says so.
    const h = await freshModule();
    const g = fakeGeometry(h.scrollEl, { scrollHeight: 2000, clientHeight: 500, scrollTop: 0 });
    h.ro.fire();
    await settle();
    expect(g.scrollTop).toBe(2000);
  });
});

// ---------------------------------------------------------------------------
// What `init()` wires. These paths run once, at construction, and the assertions
// are about the registrations themselves because that is where their behaviour
// lives: a scroll listener that is not passive, an observer that ignores the
// inside of a row, or a transcript whose existing rows were never registered are
// all invisible to any assertion about a single mutation.
// ---------------------------------------------------------------------------
describe("what init wires", () => {
  it("registers the scroll listener as passive", async () => {
    // The transcript is the app's hottest scroller: a non-passive listener makes
    // the browser wait for this handler before it may scroll. Nothing in happy-dom
    // models that, so the registration is the observable.
    const h = await freshModule();
    const scrollRegistrations = h.listeners.mock.calls.filter((c) => c[0] === "scroll");
    expect(scrollRegistrations.map((c) => c[2])).toEqual([{ passive: true }]);
  });

  it("watches the rows the transcript already had", async () => {
    // A reload of an open chat renders the resident transcript before this module
    // is imported, so the rows that are already there have to be picked up at
    // construction rather than waiting for the next mutation.
    const h = await freshModule({ withExistingRow: true });
    expect(h.ro.targets.has(h.existingRow!)).toBe(true);
  });

  it("wires the End key on the document, not on the transcript", async () => {
    // The keyboard half of the resume control has to work while the focus is
    // anywhere that is not a text field, which is why it is registered on the
    // document.
    const h = await freshModule();
    fakeGeometry(h.scrollEl, { scrollHeight: 2000, clientHeight: 500, scrollTop: 0 });
    h.scroll.setUserScrolledUp(true);
    document.body.dispatchEvent(new KeyboardEvent("keydown", { key: "End", bubbles: true }));
    expect(h.scroll.readingState()).toBe("following");
  });
});

// ---------------------------------------------------------------------------
// The transcript's mutation observer. Streaming does not append rows — it grows
// the text inside the row that is already there — so the observer has to watch
// the whole subtree and the character data, not just the child list.
// ---------------------------------------------------------------------------
describe("the transcript's mutation observer", () => {
  it("scrolls for a change nested inside a row", async () => {
    const h = await freshModule();
    const row = document.createElement("div");
    h.messagesEl.appendChild(row);
    const g = fakeGeometry(h.scrollEl, { scrollHeight: 2000, clientHeight: 500, scrollTop: 0 });
    await settle();
    g.scrollTop = 0;
    row.appendChild(document.createElement("span"));
    await settle();
    expect(g.scrollTop).toBe(2000);
  });

  it("scrolls when a row's text grows", async () => {
    // The streaming case: a chunk extends an existing text node and no element
    // is added or removed anywhere.
    const h = await freshModule();
    const row = document.createElement("div");
    const text = document.createTextNode("half a sen");
    row.appendChild(text);
    h.messagesEl.appendChild(row);
    const g = fakeGeometry(h.scrollEl, { scrollHeight: 2000, clientHeight: 500, scrollTop: 0 });
    await settle();
    g.scrollTop = 0;
    text.data += "tence";
    await settle();
    expect(g.scrollTop).toBe(2000);
  });
});

// ---------------------------------------------------------------------------
// The per-child observers. A child is watched because a code block expanding or
// an image loading changes ITS box without changing the scroller's, and the
// bookkeeping is what stops the registration set drifting from the transcript.
// ---------------------------------------------------------------------------
describe("the per-child resize observers", () => {
  it("observes each child of the transcript as it arrives", async () => {
    const h = await freshModule();
    const a = document.createElement("div");
    h.messagesEl.appendChild(a);
    await settle();
    expect(h.ro.targets.has(a)).toBe(true);
  });

  it("stops observing a child that leaves", async () => {
    // Not tidiness: an observer holding a removed turn keeps it alive for as long
    // as the chat is open.
    const h = await freshModule();
    const a = document.createElement("div");
    h.messagesEl.appendChild(a);
    await settle();
    a.remove();
    await settle();
    expect([h.ro.targets.has(a), h.ro.unobserveCalls.includes(a)]).toEqual([false, true]);
  });

  it("keeps observing the children that stayed", async () => {
    const h = await freshModule();
    const a = document.createElement("div");
    const b = document.createElement("div");
    h.messagesEl.append(a, b);
    await settle();
    h.messagesEl.appendChild(document.createElement("div"));
    await settle();
    expect([h.ro.targets.has(a), h.ro.targets.has(b)]).toEqual([true, true]);
  });

  it("does not re-observe a child it is already watching", async () => {
    // `observe()` on a live target re-delivers an entry for it, so re-registering
    // every resident turn on every streamed chunk is a callback storm, not a
    // no-op.
    const h = await freshModule();
    const a = document.createElement("div");
    h.messagesEl.appendChild(a);
    await settle();
    h.messagesEl.appendChild(document.createElement("div"));
    await settle();
    h.messagesEl.appendChild(document.createElement("div"));
    await settle();
    expect(h.ro.observedCount(a)).toBe(1);
  });

  it("observes a child that comes back", async () => {
    // Reconciliation can move a row out and put the same node back; the
    // bookkeeping has to forget it in between or it is never watched again.
    const h = await freshModule();
    const a = document.createElement("div");
    h.messagesEl.appendChild(a);
    await settle();
    a.remove();
    await settle();
    h.messagesEl.appendChild(a);
    await settle();
    expect(h.ro.targets.has(a)).toBe(true);
  });
});

// ---------------------------------------------------------------------------
// One scroll per frame. Each chunk's small delta is what makes the motion read
// as continuous; two writes in one frame are the stutter the module's header
// comment is about.
// ---------------------------------------------------------------------------
describe("the auto-scroll frame guard", () => {
  it("coalesces two triggers in the same frame into one scroll", async () => {
    const h = await freshModule();
    fakeGeometry(h.scrollEl, { scrollHeight: 2000, clientHeight: 500, scrollTop: 0 });
    const scrollTo = vi.spyOn(h.scrollEl, "scrollTo");
    h.ro.fire();
    h.messagesEl.appendChild(document.createElement("div"));
    h.ro.fire();
    await settle();
    expect(scrollTo).toHaveBeenCalledTimes(1);
  });

  it("scrolls again for the next frame's trigger", async () => {
    const h = await freshModule();
    fakeGeometry(h.scrollEl, { scrollHeight: 2000, clientHeight: 500, scrollTop: 0 });
    const scrollTo = vi.spyOn(h.scrollEl, "scrollTo");
    h.ro.fire();
    await settle();
    h.ro.fire();
    await settle();
    expect(scrollTo).toHaveBeenCalledTimes(2);
  });
});

// ---------------------------------------------------------------------------
// The debounce window is what stops the auto-scroll fighting a gesture still in
// flight. Its end is EXCLUSIVE: a chunk arriving on the deadline is a chunk
// arriving after the gesture, and must move the transcript.
// ---------------------------------------------------------------------------
describe("the user-scroll debounce window", () => {
  it("scrolls a chunk that arrives exactly at the end of the window", async () => {
    const h = await freshModule();
    const g = fakeGeometry(h.scrollEl, { scrollHeight: 2000, clientHeight: 500, scrollTop: 1500 });
    const now = vi.spyOn(Date, "now").mockReturnValue(1000);
    h.scrollEl.dispatchEvent(new Event("scroll"));
    expect(h.scroll.readingState()).toBe("following");
    // USER_SCROLL_DEBOUNCE_MS is 150, so 1150 is the first instant no longer
    // inside the gesture.
    now.mockReturnValue(1150);
    h.messagesEl.appendChild(document.createElement("div"));
    await settle();
    expect(g.scrollTop).toBe(2000);
  });

  it("holds a chunk that arrives one millisecond earlier", async () => {
    const h = await freshModule();
    const g = fakeGeometry(h.scrollEl, { scrollHeight: 2000, clientHeight: 500, scrollTop: 1500 });
    const now = vi.spyOn(Date, "now").mockReturnValue(1000);
    h.scrollEl.dispatchEvent(new Event("scroll"));
    now.mockReturnValue(1149);
    h.messagesEl.appendChild(document.createElement("div"));
    await settle();
    expect(g.scrollTop).toBe(1500);
  });
});

// ---------------------------------------------------------------------------
// Pagination's furniture and its safety net. The load is asynchronous — the
// skeleton's disappearance is what marks it complete — so both the completion
// path and the timeout that has to exist because a fetch can simply never land
// are reachable only from here.
// ---------------------------------------------------------------------------
describe("pagination's skeleton", () => {
  /** Wire a fetcher and park the reader at the top so the scroll listener asks
   *  for a page. */
  function startLoad(h: Harness, load: () => void): void {
    h.scroll.setLoadMore(load, true);
    h.scrollEl.dispatchEvent(new Event("scroll"));
  }

  it("prepends the skeleton when there is no button left to replace", async () => {
    // The button is consumed by the first fetch and nothing puts it back, so the
    // second fetch of a session has nothing to swap and must place the skeleton
    // itself — otherwise a reader waiting for an older page sees no sign of it.
    const h = await freshModule();
    const g = fakeGeometry(h.scrollEl, { scrollHeight: 1000, clientHeight: 500, scrollTop: 0 });
    startLoad(h, () => undefined);
    document.getElementById("load-more-skeleton")?.remove();
    await settle();
    g.scrollTop = 0;
    h.scrollEl.dispatchEvent(new Event("scroll"));
    expect(h.messagesEl.firstElementChild?.id).toBe("load-more-skeleton");
  });

  it("waits for the skeleton to go before restoring the reader", async () => {
    // The page arrives in more than one mutation: the rows land, then the
    // skeleton goes. Compensating on the first mutation restores against a
    // height the page had not finished growing to.
    const h = await freshModule();
    const g = fakeGeometry(h.scrollEl, { scrollHeight: 1000, clientHeight: 500, scrollTop: 0 });
    startLoad(h, () => undefined);
    h.messagesEl.prepend(document.createElement("div"));
    g.scrollHeight = 1200;
    await settle();
    const beforeSkeletonWent = g.scrollTop;
    document.getElementById("load-more-skeleton")?.remove();
    g.scrollHeight = 1400;
    await settle();
    expect([beforeSkeletonWent, g.scrollTop]).toEqual([0, 400]);
  });
});

describe("pagination's safety timeout", () => {
  /** The load-more window is 15s; only the module's own timer is faked, so
   *  happy-dom's observers and frames keep running. */
  function useLoadTimeout(): void {
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
  }

  it("gives up on a fetch that never lands and lets the reader retry", async () => {
    const h = await freshModule();
    const g = fakeGeometry(h.scrollEl, { scrollHeight: 1000, clientHeight: 500, scrollTop: 0 });
    const load = vi.fn();
    useLoadTimeout();
    h.scroll.setLoadMore(load, true);
    h.scrollEl.dispatchEvent(new Event("scroll"));
    expect(document.getElementById("load-more-skeleton")).not.toBeNull();

    vi.advanceTimersByTime(15_000);
    await settleFrames();
    // The furniture goes, and the reader is not stuck: the next scroll to the
    // top asks again.
    expect(document.getElementById("load-more-skeleton")).toBeNull();
    g.scrollTop = 0;
    h.scrollEl.dispatchEvent(new Event("scroll"));
    expect(load).toHaveBeenCalledTimes(2);
  });

  it("does not move the reader when it gives up", async () => {
    // The transcript can have grown from streaming while the fetch hung, so the
    // height delta the completion path would apply is real — and applying it
    // here would jump a reader whose page never arrived.
    const h = await freshModule();
    const g = fakeGeometry(h.scrollEl, { scrollHeight: 1000, clientHeight: 500, scrollTop: 0 });
    useLoadTimeout();
    h.scroll.setLoadMore(() => undefined, true);
    h.scrollEl.dispatchEvent(new Event("scroll"));
    g.scrollHeight = 1500;

    vi.advanceTimersByTime(15_000);
    await settleFrames();
    expect(g.scrollTop).toBe(0);
  });

  it("does not fire after the page has landed", async () => {
    // The timer belongs to ONE load. Left armed, it expires in the middle of a
    // LATER fetch and clears its in-flight flag, which is how two fetches end up
    // in the air at once and a page lands twice.
    const h = await freshModule();
    const g = fakeGeometry(h.scrollEl, { scrollHeight: 1000, clientHeight: 500, scrollTop: 0 });
    const load = vi.fn();
    useLoadTimeout();
    h.scroll.setLoadMore(load, true);
    h.scrollEl.dispatchEvent(new Event("scroll"));
    document.getElementById("load-more-skeleton")?.remove();
    g.scrollHeight = 1400;
    await settleFrames();

    // One second short of the first load's window, then a second fetch — so the
    // first load's deadline falls INSIDE the second load's flight.
    vi.advanceTimersByTime(14_000);
    g.scrollTop = 0;
    h.scrollEl.dispatchEvent(new Event("scroll"));
    expect(load).toHaveBeenCalledTimes(2);
    expect(document.getElementById("load-more-skeleton")).not.toBeNull();

    vi.advanceTimersByTime(2_000);
    await settleFrames();
    // The second fetch is still the one in flight: its furniture is untouched and
    // nothing else may be started.
    g.scrollTop = 0;
    h.scrollEl.dispatchEvent(new Event("scroll"));
    expect([
      document.getElementById("load-more-skeleton") === null,
      load.mock.calls.length,
    ]).toEqual([false, 2]);
  });
});

// ---------------------------------------------------------------------------
// The module-scope boot. Importing before the document is ready must not build
// the controller against a transcript that is not in the DOM yet.
// ---------------------------------------------------------------------------
describe("the deferred boot", () => {
  afterEach(() => {
    Reflect.deleteProperty(document, "readyState");
  });

  /** Everything freshModule does except reaching into the module afterwards: the
   *  point of these two tests is what the IMPORT alone did. */
  function stageDocument(): void {
    stubResizeObserver();
    document.body.replaceChildren();
    document.documentElement.style.removeProperty("--scrollbar-w");
    const messages = document.createElement("div");
    messages.id = "messages";
    const wrap = document.createElement("div");
    wrap.id = "messagesWrap";
    document.body.append(messages, wrap);
    vi.resetModules();
  }

  it("wires the transcript on import when the document is already parsed", async () => {
    // The ordinary case: the bundle runs after the document has been parsed, and
    // nothing will fire DOMContentLoaded again — so deferring here would leave
    // the transcript unwired until some other module happened to call in.
    expect(document.readyState).toBe("complete");
    stageDocument();
    await import("./scroll.js");
    expect(FakeResizeObserver.instances).toHaveLength(1);
    expect(document.documentElement.style.getPropertyValue("--scrollbar-w")).toBe("0px");
  });

  it("waits for DOMContentLoaded when the document is still loading", async () => {
    Object.defineProperty(document, "readyState", {
      configurable: true,
      get: () => "loading",
    });
    stageDocument();
    await import("./scroll.js");
    // Nothing wired: no observers, and the gutter the layout reads is unwritten.
    expect(FakeResizeObserver.instances).toHaveLength(0);
    expect(document.documentElement.style.getPropertyValue("--scrollbar-w")).toBe("");

    document.dispatchEvent(new Event("DOMContentLoaded"));
    expect(FakeResizeObserver.instances).toHaveLength(1);
    expect(document.documentElement.style.getPropertyValue("--scrollbar-w")).toBe("0px");
  });
});

// The shared clamp: measure, decide, observe. Extracted from the turn header,
// so these tests pin the machinery once and the three consumers pin their own
// wiring.
//
// Real layout throughout, because "does this overflow N lines" has no honest
// answer without it: a detached element measures 0 on both sides and the module
// falls back to a character guess, which is exactly the case the observer
// exists to correct.
import { describe, it, expect, beforeAll, afterAll, afterEach } from "vitest";
import { attachClamp, releaseClamp, releaseClampsIn, clampObservationCount } from "./clamp-text.js";
import clampSource from "./clamp-text.ts?raw";
import type { Message, Session } from "./types.js";

// The transcript's own fixture, for the `disposeChatView` case below. Built at
// module scope and BEFORE `messages.js` is imported, because `scroll.ts`
// self-initialises at import and reads the scroller out of the DOM registry.
for (const id of [
  "messages-wrap-outer",
  "chat-view",
  "scroll-bottom",
  "send-btn",
  "prompt-input",
]) {
  const d = document.createElement(id === "prompt-input" ? "textarea" : "div");
  d.id = id;
  document.body.appendChild(d);
}
const scrollerEl = document.createElement("div");
scrollerEl.id = "messages-wrap";
document.getElementById("messages-wrap-outer")?.appendChild(scrollerEl);
const transcriptEl = document.createElement("div");
transcriptEl.id = "messages";
scrollerEl.appendChild(transcriptEl);

const { setSessions, setActive, bumpMessages } = await import("./store.js");
const { mountChatView, disposeChatView } = await import("./messages.js");

const CSS = `
  .ct-text { font: 16px/20px monospace; overflow-wrap: anywhere; }
  .ct-text[data-clamped] {
    display: -webkit-box;
    -webkit-line-clamp: 3;
    -webkit-box-orient: vertical;
    overflow: hidden;
  }
`;

let styleEl: HTMLStyleElement;
let host: HTMLElement;

beforeAll(() => {
  styleEl = document.createElement("style");
  styleEl.textContent = CSS;
  document.head.appendChild(styleEl);
  host = document.createElement("div");
  document.body.appendChild(host);
});

afterAll(() => {
  styleEl.remove();
  host.remove();
});

afterEach(() => {
  host.replaceChildren();
});

interface Pair {
  readonly text: HTMLElement;
  readonly more: HTMLButtonElement;
}

/** Build a clamped text plus its opener, mounted at a stated width. */
function mount(body: string, width: number, lines = 3): Pair {
  host.style.inlineSize = `${String(width)}px`;
  const text = document.createElement("div");
  text.className = "ct-text";
  text.textContent = body;
  const more = document.createElement("button");
  more.type = "button";
  host.replaceChildren(text, more);
  attachClamp(text, more, { lines });
  return { text, more };
}

/** Wait for the opener to reach `hidden`. Only for a verdict that must CHANGE:
 *  a poll whose condition already holds returns before the observer has run,
 *  which is how a test of an unchanged verdict passes vacuously. */
async function settles(p: Pair, hidden: boolean, why: string): Promise<void> {
  const deadline = Date.now() + 2000;
  while (p.more.hidden !== hidden && Date.now() < deadline) {
    await observerRuns();
  }
  expect(p.more.hidden, why).toBe(hidden);
}

/** Give the observer its chance, for a verdict that must NOT change. A resize
 *  callback is delivered after the frame's layout and a rAF callback runs before
 *  it, so two frames span one full delivery. */
async function observerRuns(): Promise<void> {
  await new Promise<void>((resolve) => {
    requestAnimationFrame(() => {
      requestAnimationFrame(() => {
        resolve();
      });
    });
  });
}

const ONE_LINE = "target main";
// Twelve monospace lines at 400px, whatever the exact metrics.
const LONG = "the quick brown fox jumps over the lazy dog ".repeat(12);

describe("deciding whether the opener is needed", () => {
  it("hides the opener for a text that fits", async () => {
    const p = mount(ONE_LINE, 400);
    await observerRuns();
    expect(p.more.hidden).toBe(true);
    expect(p.text.hasAttribute("data-clamped"), "clamped even so, harmlessly").toBe(true);
  });

  it("shows the opener for a text that overflows", async () => {
    const p = mount(LONG, 400);
    await settles(p, false, "offered for a text past its line cap");
    expect(p.text.scrollHeight).toBeGreaterThan(p.text.clientHeight);
  });

  it("re-decides at every width, so narrowing cannot hide text with no way to open it", async () => {
    // Three lines of a wide box become four of a narrow one, and a measure-once
    // clamp cut the difference away silently.
    const body = "widen the existing front-matter struct with the missing field instead";
    const p = mount(body, 900);
    await settles(p, true, "fits wide");

    host.style.inlineSize = "120px";
    await settles(p, false, "offered once it no longer fits");

    host.style.inlineSize = "900px";
    await settles(p, true, "withdrawn again when it fits");
  });

  it("corrects the no-layout guess once the element is laid out", async () => {
    // A detached element measures 0 on both sides, so the first verdict is the
    // 220-character guess — wrong for a long text that still fits.
    const body = "x".repeat(240);
    const text = document.createElement("div");
    text.className = "ct-text";
    text.textContent = body;
    const more = document.createElement("button");
    more.type = "button";
    attachClamp(text, more, { lines: 3 });
    expect(more.hidden, "the guess, while detached").toBe(false);

    host.style.inlineSize = "1200px";
    host.replaceChildren(text, more);
    await settles({ text, more }, true, "corrected once laid out");
  });
});

describe("opening and closing", () => {
  it("drops the clamp on the opener's click and restores it on the next", async () => {
    const p = mount(LONG, 400);
    await settles(p, false, "offered");
    expect(p.more.textContent).toBe("Show more");
    expect(p.more.getAttribute("aria-expanded")).toBe("false");

    p.more.click();
    expect(p.text.hasAttribute("data-clamped")).toBe(false);
    expect(p.more.textContent).toBe("Show less");
    expect(p.more.getAttribute("aria-expanded")).toBe("true");

    p.more.click();
    expect(p.text.hasAttribute("data-clamped")).toBe(true);
    expect(p.more.textContent).toBe("Show more");
    expect(p.more.getAttribute("aria-expanded")).toBe("false");
  });

  it("leaves an expansion alone when the box resizes under it", async () => {
    // Expanding changes the text's own box, so the observer fires on the
    // reader's own gesture and must not undo it.
    const p = mount(LONG, 400);
    await settles(p, false, "offered");
    p.more.click();

    host.style.inlineSize = "300px";
    await observerRuns();
    expect(p.text.hasAttribute("data-clamped"), "still open").toBe(false);
    expect(p.more.hidden, "and the opener stays reachable").toBe(false);
  });

  it("keeps the expanded flag where the caller stores it", async () => {
    host.style.inlineSize = "400px";
    const text = document.createElement("div");
    text.className = "ct-text";
    text.textContent = LONG;
    const more = document.createElement("button");
    more.type = "button";
    host.replaceChildren(text, more);
    const store = { open: false };
    const handle = attachClamp(text, more, {
      lines: 3,
      isExpanded: () => store.open,
      setExpanded: (on) => {
        store.open = on;
      },
    });
    await settles({ text, more }, false, "offered");

    more.click();
    expect(store.open, "written through to the caller").toBe(true);
    // A repaint re-syncs against the caller's flag rather than re-collapsing.
    handle.sync();
    expect(text.hasAttribute("data-clamped")).toBe(false);
    expect(more.hidden).toBe(false);
  });
});

describe("the handle", () => {
  it("collapse() forgets an expansion, for content that has changed", async () => {
    const p = mount(LONG, 400);
    await settles(p, false, "offered");
    const handle = attachClamp(p.text, p.more);
    p.more.click();
    expect(p.text.hasAttribute("data-clamped")).toBe(false);

    handle.collapse();
    expect(p.text.hasAttribute("data-clamped")).toBe(true);
    expect(p.more.textContent).toBe("Show more");
    expect(p.more.hidden, "and it is still offered, since the text still overflows").toBe(false);
  });

  it("disable() takes the clamp off and a resize cannot put it back", async () => {
    const p = mount(LONG, 400);
    await settles(p, false, "offered");
    attachClamp(p.text, p.more).disable();
    expect(p.text.hasAttribute("data-clamped")).toBe(false);
    expect(p.more.hidden).toBe(true);

    host.style.inlineSize = "120px";
    await observerRuns();
    expect(p.text.hasAttribute("data-clamped"), "still off").toBe(false);
    expect(p.more.hidden).toBe(true);
  });

  it("is idempotent: a repeat attach wires no second listener", async () => {
    const p = mount(LONG, 400);
    await settles(p, false, "offered");
    // A second attach returning a fresh state would re-register the click, so
    // one click would toggle twice and land back where it started.
    attachClamp(p.text, p.more, { lines: 3 });
    p.more.click();
    expect(p.text.hasAttribute("data-clamped")).toBe(false);
  });
});

describe("releasing", () => {
  it("unobserves an element that has left the document", async () => {
    const p = mount(LONG, 400);
    await settles(p, false, "offered");

    p.text.remove();
    p.more.remove();
    // The final zero-size change carries `isConnected === false`, which is the
    // release. A still-observed element would be re-decided here, and a detached
    // one measures 0, so the character guess would answer for it.
    await observerRuns();
    p.more.hidden = true;
    host.style.inlineSize = "120px";
    await observerRuns();
    expect(p.more.hidden, "nothing re-decided it").toBe(true);
  });

  it("takes the observation count back to zero when the host subtree is released", async () => {
    // The reason the explicit release exists: the callback-inferred one needs a
    // final zero-size entry, which WebKit may never deliver and which
    // `content-visibility: hidden` on a parked view DEFERS on every engine — so
    // for an element discarded while its view is parked nothing arrives at all,
    // and an evicted view is never un-parked.
    const before = clampObservationCount();
    host.style.inlineSize = "400px";
    const texts: HTMLElement[] = [];
    for (let i = 0; i < 5; i++) {
      const text = document.createElement("div");
      text.className = "ct-text";
      text.textContent = LONG;
      const more = document.createElement("button");
      more.type = "button";
      host.append(text, more);
      attachClamp(text, more, { lines: 3 });
      texts.push(text);
    }
    await observerRuns();
    expect(clampObservationCount() - before, "five more watched").toBe(5);

    // The subtree is DISCARDED, which is the precondition the export states.
    releaseClampsIn(host);
    expect(clampObservationCount() - before, "and none after the sweep").toBe(0);
    // Nothing re-decides a released element, so a width change moves no opener.
    for (const text of texts) {
      text.remove();
    }
    host.style.inlineSize = "120px";
    await observerRuns();
    expect(clampObservationCount() - before).toBe(0);
  });

  it("releases one element without touching its siblings", async () => {
    const before = clampObservationCount();
    const a = mount(LONG, 400);
    const b = document.createElement("div");
    b.className = "ct-text";
    b.textContent = LONG;
    const bMore = document.createElement("button");
    bMore.type = "button";
    host.append(b, bMore);
    attachClamp(b, bMore, { lines: 3 });
    await observerRuns();
    expect(clampObservationCount() - before).toBe(2);

    releaseClamp(a.text);
    expect(clampObservationCount() - before, "only the named one went").toBe(1);
    releaseClamp(b);
    expect(clampObservationCount() - before).toBe(0);
  });

  it("releases a mounted chat view's header clamps when the view is disposed", async () => {
    // The owner that matters: `disposeChatView` is the single per-view dispose
    // chat close, chat delete, LRU eviction and `teardownAll` all run, so one
    // sweep there covers every turn header of a whole chat.
    const before = clampObservationCount();
    mountChatView();
    const chat = "c-clamp-dispose";
    const messages: Message[] = [];
    for (let t = 0; t < 4; t++) {
      messages.push({
        id: `t${String(t)}`,
        role: "user",
        ts: 1,
        content: `a request long enough to be worth clamping, number ${String(t)}`,
      } as Message);
      messages.push({
        id: `t${String(t)}-a`,
        role: "assistant",
        ts: 2,
        content: "",
        blocks: [{ type: "text", text: "reply" }],
      } as unknown as Message);
    }
    setSessions([
      {
        id: chat,
        name: "c",
        messages,
        message_count: messages.length,
        has_more: false,
        thinking: false,
        working_label: "",
      },
    ] as unknown as Session[]);
    setActive(chat);
    bumpMessages(chat);
    // One clamp per turn header, or the assertion below cannot fail.
    expect(clampObservationCount() - before, "one per turn header").toBe(4);

    disposeChatView(chat);
    expect(clampObservationCount() - before).toBe(0);
  });

  it("keeps the callback-inferred sweep as well as the explicit release", () => {
    // A source guard, because the failure mode is a SIMPLIFICATION: whichever half
    // is deleted, the suite above still passes for the elements it does release,
    // and the leak is invisible. The `isConnected` branch is belt and braces for an
    // element discarded with no release; the export is the mechanism.
    expect([
      clampSource.includes("export function releaseClamp("),
      clampSource.includes("export function releaseClampsIn("),
      clampSource.includes("!entry.target.isConnected"),
    ]).toEqual([true, true, true]);
  });
});

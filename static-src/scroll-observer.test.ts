// ---------------------------------------------------------------------------
// The transcript's resize observers, over REAL layout: no undeliverable
// observation, whatever the scrollport is.
//
// `css/13-messages.css` reads `--scrollbar-w` in the scroller's own
// `padding-inline`, so a write from inside a resize delivery resizes an element
// this loop has already delivered — and, while ONE observer also carried every
// turn card, every card with it. Chromium reports that as "ResizeObserver loop
// completed with undelivered notifications" and defers the observations, having
// invalidated the whole document's style mid-frame.
//
// Real Chromium and the shipped stylesheet are the whole point: the error is the
// BROWSER's own verdict on a frame, so nothing short of a real engine laying out
// a real transcript can produce or refute it. `window.ResizeObserver` is wrapped
// to attribute callbacks and entries to their CONSTRUCTION SITE, so a regression
// names the observer it came from rather than reporting a count.
// ---------------------------------------------------------------------------

import { describe, it, expect, afterAll, beforeAll, beforeEach } from "vitest";
import { framesBudgetMs, testTimeoutFor } from "./__test-helpers__/frame-budget.js";
import type { Message, Session } from "./types.js";

// NESTED as the shipped page nests them (static/index.html): `#messages-wrap` is
// `position: absolute; inset: 0` inside the outer wrapper, so it is the
// `offsetParent` of the whole transcript AND the scroller. Flat siblings give the
// scroller no box to reserve a gutter in, which is the one measurement this file
// is about.
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
const messagesEl = document.createElement("div");
messagesEl.id = "messages";
scrollerEl.appendChild(messagesEl);

/** What one construction site's observer did. */
interface Tally {
  calls: number;
  entries: number;
}

const tallies = new Map<string, Tally>();

function tallyFor(site: string): Tally {
  let t = tallies.get(site);
  if (t === undefined) {
    t = { calls: 0, entries: 0 };
    tallies.set(site, t);
  }
  return t;
}

/** The first frame BELOW this file: the module and line that constructed the
 *  observer. A count says a loop happened; a site says whose. */
function constructionSite(): string {
  const stack = new Error("ro").stack ?? "";
  for (const line of stack.split("\n").slice(1)) {
    if (line.includes("scroll-observer.test.ts")) {
      continue;
    }
    const m = /([A-Za-z0-9._-]+\.ts)\??[^/]*?:(\d+):/.exec(line);
    if (m !== null) {
      return `${m[1] ?? "?"}:${m[2] ?? "?"}`;
    }
  }
  return "unknown";
}

const NativeResizeObserver = window.ResizeObserver;

/** A delegating wrapper rather than a subclass: the tally has to be reachable
 *  from the callback, and a derived constructor cannot touch anything of its own
 *  before `super()`. */
class ProbeResizeObserver implements ResizeObserver {
  private readonly inner: ResizeObserver;
  constructor(cb: ResizeObserverCallback) {
    const tally = tallyFor(constructionSite());
    this.inner = new NativeResizeObserver((entries, observer) => {
      tally.calls += 1;
      tally.entries += entries.length;
      cb(entries, observer);
    });
  }
  observe(target: Element, options?: ResizeObserverOptions): void {
    this.inner.observe(target, options);
  }
  unobserve(target: Element): void {
    this.inner.unobserve(target);
  }
  disconnect(): void {
    this.inner.disconnect();
  }
}

// Installed BEFORE `scroll.ts` is imported, because that module builds its
// observers at import. Assigned rather than `vi.stubGlobal`'d: `unstubGlobals` is
// on, so a stub installed at collection time is restored before the first test.
window.ResizeObserver = ProbeResizeObserver as unknown as typeof ResizeObserver;

/** Every `ResizeObserver loop` the engine reported, whichever pass produced it. */
const loopErrors: string[] = [];
window.addEventListener("error", (e) => {
  if (e.message.includes("ResizeObserver loop")) {
    loopErrors.push(e.message);
  }
});

/** How many times `--scrollbar-w` was actually written. Zero means the fixture
 *  never reached the write, and the loop assertions below would be vacuous. */
let gutterWrites = 0;
const rootStyle = document.documentElement.style;
const nativeSetProperty = rootStyle.setProperty.bind(rootStyle);
rootStyle.setProperty = (name: string, value: string | null, priority?: string): void => {
  if (name === "--scrollbar-w") {
    gutterWrites += 1;
  }
  nativeSetProperty(name, value, priority);
};

const { setSessions, setActive, bumpMessages } = await import("./store.js");
const { mountChatView } = await import("./messages.js");
const { mountAppCSS } = await import("./__test-helpers__/css-rules.js");

/** Turns and blocks per turn: the resting shape the loop reproduces on. Sixty
 *  cards is past the point where one observer carrying all of them makes the
 *  deferred observation set large. */
const TURNS = 60;
const BLOCKS = 12;

/** One callback per observed pass, with room for the settle: a BOUND, so a
 *  regression that re-couples the write to the delivery shows up as a callback
 *  storm even on a build where the engine declines to report the loop. */
const MAX_CALLS_PER_SITE = 8;

let seq = 0;

function transcript(): Message[] {
  const out: Message[] = [];
  for (let t = 0; t < TURNS; t++) {
    out.push({
      id: `t${String(t)}`,
      role: "user",
      ts: 1,
      content: `prompt ${String(t)}`,
    } as Message);
    out.push({
      id: `t${String(t)}-a`,
      role: "assistant",
      ts: 2,
      content: "",
      blocks: Array.from({ length: BLOCKS }, (_, i) => ({
        type: "text",
        text: `turn ${String(t)} block ${String(i)} of some prose long enough to wrap on a narrow measure`,
      })),
    } as unknown as Message);
  }
  return out;
}

function paint(): void {
  seq++;
  const id = `c-ro-${String(seq)}`;
  setSessions([
    {
      id,
      name: "c",
      messages: transcript(),
      message_count: TURNS * 2,
      has_more: false,
      thinking: false,
      working_label: "",
    },
  ] as unknown as Session[]);
  setActive(id);
  bumpMessages(id);
}

/** Frames the settle waits for: the deferred write lands on the next one, the
 *  resize it causes is delivered on the one after, and the rest are margin. */
const SETTLE_FRAMES = 6;

/** Let the engine finish delivering: several frames plus a macrotask, which is
 *  where a deferred observation lands and where the loop error would arrive. */
async function settle(): Promise<void> {
  for (let f = 0; f < SETTLE_FRAMES; f++) {
    await new Promise<void>((r) => {
      requestAnimationFrame(() => {
        r();
      });
    });
  }
  await new Promise<void>((r) => {
    setTimeout(r, 100);
  });
}

/** Per-site counts as they stood when the case started.
 *
 *  A DELTA rather than a reset, because `scroll.ts` builds its two observers once,
 *  at import — clearing the map would drop them for the rest of the file and leave
 *  the bound below checking only the observers a paint happens to construct. */
let baseline = new Map<string, Tally>();

function snapshot(): Map<string, Tally> {
  return new Map([...tallies].map(([site, t]) => [site, { ...t }]));
}

function since(): Map<string, Tally> {
  const out = new Map<string, Tally>();
  for (const [site, t] of tallies) {
    const was = baseline.get(site) ?? { calls: 0, entries: 0 };
    out.set(site, { calls: t.calls - was.calls, entries: t.entries - was.entries });
  }
  return out;
}

function report(): string {
  return [...since()]
    .map(([site, t]) => `${site} ${String(t.calls)} calls / ${String(t.entries)} entries`)
    .join("; ");
}

// The subject IS frames, and a browser running the whole suite delivers them at
// 1Hz partway through (`__test-helpers__/frame-budget.ts`), so the per-test
// timeout has to be sized in seconds per frame waited for. Cold and full is the
// only mode that reaches the throttle, so the default 5s reads as a bare timeout
// exactly where the case is working.
describe(
  "the transcript's resize observers over real layout",
  { timeout: testTimeoutFor(framesBudgetMs(SETTLE_FRAMES)) },
  () => {
    let style: HTMLStyleElement;

    beforeAll(() => {
      style = mountAppCSS();
    });

    afterAll(() => {
      style.remove();
      window.ResizeObserver = NativeResizeObserver;
      rootStyle.setProperty = nativeSetProperty;
    });

    beforeEach(() => {
      mountChatView();
      loopErrors.length = 0;
      baseline = snapshot();
    });

    // `#messages-wrap-outer` is `flex: 1` of a column this fixture does not build,
    // so without an explicit height the absolutely-positioned scroller inside it is
    // 0 tall and nothing overflows — which is the one state that cannot reserve a
    // gutter and therefore cannot reproduce the write.
    for (const viewport of [600, 720]) {
      it(`delivers every observation at a ${String(viewport)}px scrollport`, async () => {
        const outer = document.getElementById("messages-wrap-outer")!;
        outer.style.height = `${String(viewport)}px`;
        paint();
        await settle();

        expect(loopErrors, report()).toEqual([]);
        const observed = since();
        // TWO observers from `scroll.ts` — the content one and the gutter one — and
        // between them at least one delivered entry, or the bound below is a loop
        // over nothing.
        const fromScroll = [...observed].filter(([site]) => site.startsWith("scroll.ts"));
        expect(
          [fromScroll.length, fromScroll.reduce((n, [, t]) => n + t.entries, 0) > 0],
          report(),
        ).toEqual([2, true]);
        for (const [site, t] of observed) {
          expect(t.calls, `${site} — ${report()}`).toBeLessThanOrEqual(MAX_CALLS_PER_SITE);
        }
      });
    }

    it("still reserves the gutter, so the pass above is not vacuous", () => {
      // The error only ever occurred on a pass that WROTE `--scrollbar-w`. A run
      // that never wrote it would pass the two cases above having reproduced
      // nothing.
      expect([gutterWrites > 0, rootStyle.getPropertyValue("--scrollbar-w")]).toEqual([
        true,
        expect.stringMatching(/^\d+px$/),
      ]);
    });
  },
);

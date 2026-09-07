// `forceReflow` pins a browser behaviour no other test covered: four call sites
// remove a class or attribute and re-add it in one task to restart an animation,
// and that only works because a layout read between the two writes separates
// them. Red-checked — with the flush removed, both cases below report ONE
// animationstart and the second assertion fails.
//
// Real Chromium only. No DOM emulator models style-change coalescing, so this
// would pass vacuously anywhere else.
import { describe, it, expect, beforeAll, afterEach } from "vitest";
import { forceReflow } from "./dom.js";
import { framesBudgetMs, testTimeoutFor } from "./__test-helpers__/frame-budget.js";

const ANIM = "reflow-probe";

beforeAll(() => {
  const style = document.createElement("style");
  style.textContent = `
    @keyframes ${ANIM} { from { opacity: 0 } to { opacity: 1 } }
    .${ANIM}-on { animation: ${ANIM} 5s linear }
  `;
  document.head.append(style);
});

const hosts: HTMLElement[] = [];

afterEach(() => {
  for (const h of hosts.splice(0)) {
    h.remove();
  }
});

function mountProbe(): { el: HTMLElement; starts: () => number } {
  const el = document.createElement("div");
  // A real box, so getBoundingClientRect has something to measure.
  el.style.inlineSize = "40px";
  el.style.blockSize = "20px";
  document.body.append(el);
  hosts.push(el);
  let starts = 0;
  el.addEventListener("animationstart", () => {
    starts += 1;
  });
  return { el, starts: () => starts };
}

// One frame is enough for animationstart, which fires on the first sample.
function nextFrame(): Promise<void> {
  return new Promise((resolve) => {
    requestAnimationFrame(() => {
      requestAnimationFrame(() => {
        resolve();
      });
    });
  });
}

describe("forceReflow", { timeout: testTimeoutFor(framesBudgetMs(10)) }, () => {
  it("restarts an animation that a bare remove-then-add does not", async () => {
    const withoutFlush = mountProbe();
    withoutFlush.el.classList.add(`${ANIM}-on`);
    await nextFrame();
    expect(withoutFlush.starts()).toBe(1);

    // Coalesced: the browser never sees the element without the class.
    withoutFlush.el.classList.remove(`${ANIM}-on`);
    withoutFlush.el.classList.add(`${ANIM}-on`);
    await nextFrame();
    expect(withoutFlush.starts()).toBe(1);

    const withFlush = mountProbe();
    withFlush.el.classList.add(`${ANIM}-on`);
    await nextFrame();
    expect(withFlush.starts()).toBe(1);

    withFlush.el.classList.remove(`${ANIM}-on`);
    forceReflow(withFlush.el);
    withFlush.el.classList.add(`${ANIM}-on`);
    await nextFrame();
    expect(withFlush.starts()).toBe(2);
  });

  it("reads a real measurement, so the call cannot be elided", () => {
    const { el } = mountProbe();
    expect(forceReflow(el)).toBe(20);
  });
});

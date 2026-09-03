// The sidebar's connection dot is STILL when the connection is healthy.
//
// `#status-dot` reports the SSE transport and has never reported a turn, but it
// used to run a `vk-ping` ripple on a `::after` overlay every four seconds
// forever — a dot pulsing in the healthy steady state, in the same green the tab
// dot uses for a finished turn, next to a card carrying credits and account
// usage. That is what a work indicator looks like, so it read as one.
//
// The dot had no test of any kind, which is how an animation nobody wanted stayed
// for a year. These are the two halves worth pinning: that the settled state
// carries no motion, and that `connecting` keeps its breathe, because motion
// meaning an UNSETTLED state is the app's own motion axis and removing it would
// be the opposite mistake.
//
// Computed rather than read out of the source: the `::after` was removed
// wholesale rather than just its `animation` declaration (`vk-ping` was the only
// thing fading an otherwise opaque green disc at `inset: 0`), and "does the
// pseudo generate at all" is a question only a real cascade answers.
import { describe, it, expect, beforeAll, afterAll } from "vitest";

import { mountAppCSS } from "./__test-helpers__/css-rules.js";

let style: HTMLStyleElement;

beforeAll(() => {
  style = mountAppCSS();
});

afterAll(() => {
  style.remove();
});

function mountDot(cls: string): HTMLElement {
  const dot = document.createElement("button");
  dot.className = cls;
  document.body.replaceChildren(dot);
  return dot;
}

describe("the sidebar connection dot", () => {
  it("carries no animation once connected", () => {
    const dot = mountDot("status-dot connected");
    expect(getComputedStyle(dot).animationName).toBe("none");
    expect(dot.getAnimations({ subtree: true })).toHaveLength(0);
  });

  it("generates no ::after overlay to animate", () => {
    // The ripple lived on the pseudo, so a rule that survived with its animation
    // stripped would leave an opaque green disc covering the dot. No `content`
    // means no box at all.
    const dot = mountDot("status-dot connected");
    expect(getComputedStyle(dot, "::after").content).toBe("none");
  });

  it("still breathes while connecting", () => {
    const dot = mountDot("status-dot");
    expect(getComputedStyle(dot).animationName).toBe("vk-breathe");
  });

  it("is static when disconnected", () => {
    const dot = mountDot("status-dot error");
    expect(getComputedStyle(dot).animationName).toBe("none");
  });
});

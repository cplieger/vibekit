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

  it("paints and animates nothing on its ::after overlay", () => {
    // The ripple lived on the pseudo, so a rule that survived with its animation
    // stripped would leave an opaque green disc covering the dot.
    //
    // The pseudo EXISTS again, and this assertion is narrowed to the property that
    // was ever the defect rather than to the box. `all: unset` on this control
    // resets `min-width`/`min-height` at class specificity, which makes it
    // invisible to the app-wide zero-specificity hit-target floor rather than an
    // override of it — an 8px button at every tier. The mark's size is its
    // meaning, so the TARGET grows past the paint through an `::after` expander
    // (10-shell-app.css). Transparent and inert: no background, no animation.
    const dot = mountDot("status-dot connected");
    const after = getComputedStyle(dot, "::after");
    expect(after.backgroundColor).toBe("rgba(0, 0, 0, 0)");
    expect(after.animationName).toBe("none");
    expect(dot.getAnimations({ subtree: true })).toHaveLength(0);
  });

  it("grows its hit target past its 8px disc", () => {
    // The whole point of the expander: the painted mark stays 8px while the target
    // reaches the tier's floor. `inset` is negative by half the difference, so the
    // target follows `--hit-floor` with no second declaration to keep in step.
    const dot = mountDot("status-dot connected");
    const inset = getComputedStyle(dot, "::after").insetBlockStart;
    expect(parseFloat(inset)).toBeLessThan(0);
  });

  it("still breathes while connecting, by tracking the document clock", () => {
    // The dot owns no animation any more: it reads `--vk-beat` (03-base.css), so
    // the footer breathes in step with the tab strip instead of against it. The
    // clock is DRIVEN to two known phases here rather than sampled over time —
    // deterministic, and it proves the dot follows the clock rather than merely
    // naming it.
    const dot = mountDot("status-dot");
    const clock = document.documentElement.getAnimations()[0];
    expect(clock, "the document beat must be running on :root").toBeDefined();
    clock!.pause();

    clock!.currentTime = 0; // --vk-beat 0 -> resting, fully opaque
    expect(Number(getComputedStyle(dot).opacity)).toBeCloseTo(1, 2);

    clock!.currentTime = 1200; // half of --dot-beat-dur -> --vk-beat 1 -> trough
    expect(Number(getComputedStyle(dot).opacity)).toBeCloseTo(0.45, 2);

    clock!.play();
  });

  it.each(["connected", "error"])("is static when %s, at every phase of the clock", (state) => {
    // BOTH settled states, because the two are separate rules and only one of
    // them being wrong is the live shape of this bug. The settled states reset
    // OPACITY, not an animation: with the beat inherited there is no animation on
    // this element to cancel, so `animation: none` here cancels nothing and the
    // dot keeps breathing — which is the claim these states exist to deny.
    const dot = mountDot(`status-dot ${state}`);
    const clock = document.documentElement.getAnimations()[0];
    expect(clock, "the document beat must be running on :root").toBeDefined();
    clock!.pause();
    for (const t of [0, 600, 1200, 1800]) {
      clock!.currentTime = t;
      expect(Number(getComputedStyle(dot).opacity), `phase ${String(t)}ms`).toBeCloseTo(1, 3);
    }
    clock!.play();
  });
});
